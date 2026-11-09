package roles

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"sort"
	"strings"
	"time"

	"chat-agent/internal/storage"
)

const (
	proposalDirectory = ".proposals"
	maxProposals      = 64
	maxProposalBytes  = 256 << 10 // Includes JSON escaping of both 16 KiB documents.
)

var (
	ErrProposalConflict = errors.New("role proposal or profile changed")
	ErrProposalNotFound = errors.New("role proposal not found")
	ErrProposalInvalid  = errors.New("invalid role proposal")
)

type Proposal struct {
	ID              string    `json:"id"`
	RoleID          string    `json:"role_id"`
	File            string    `json:"file"`
	BaseHash        string    `json:"base_hash"`
	Before          string    `json:"before"`
	Content         string    `json:"content"`
	Reason          string    `json:"reason"`
	SourceSessionID string    `json:"source_session_id"`
	Status          string    `json:"status"`
	Revision        int       `json:"revision"`
	CreatedAt       time.Time `json:"created_at"`
	UpdatedAt       time.Time `json:"updated_at"`
}

func ProfileHash(content string) string {
	sum := sha256.Sum256([]byte(content))
	return hex.EncodeToString(sum[:])
}

// Metadata lives outside role document directories and is never prompt material.
// OpenRoot confines metadata reads/writes even if a directory is replaced.
func (m *Manager) proposalRoot(role string, create bool) (*os.Root, error) {
	if !storage.ValidID(role) {
		return nil, ErrProposalNotFound
	}
	root, err := os.OpenRoot(m.dir)
	if err != nil {
		return nil, err
	}
	defer root.Close()
	for _, path := range []string{proposalDirectory, proposalDirectory + "/" + role} {
		info, err := root.Lstat(path)
		if errors.Is(err, os.ErrNotExist) && create {
			if err = root.Mkdir(path, 0700); err != nil {
				return nil, err
			}
			info, err = root.Lstat(path)
		}
		if err != nil {
			return nil, err
		}
		if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return nil, ErrProposalInvalid
		}
	}
	return root.OpenRoot(proposalDirectory + "/" + role)
}

func readProposal(root *os.Root, role, id string) (Proposal, error) {
	if !storage.ValidID(id) {
		return Proposal{}, ErrProposalNotFound
	}
	f, err := root.Open(id + ".json")
	if errors.Is(err, os.ErrNotExist) {
		return Proposal{}, ErrProposalNotFound
	}
	if err != nil {
		return Proposal{}, err
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, maxProposalBytes+1))
	if err != nil {
		return Proposal{}, err
	}
	var p Proposal
	if len(data) > maxProposalBytes || json.Unmarshal(data, &p) != nil ||
		p.ID != id || p.RoleID != role || p.File != "PROFILE.md" ||
		p.BaseHash != ProfileHash(p.Before) || len(p.Before) > 16<<10 ||
		strings.TrimSpace(p.Content) == "" || len(p.Content) > 16<<10 ||
		strings.TrimSpace(p.Reason) == "" || len(p.Reason) > 1024 ||
		len(p.SourceSessionID) > 128 || p.Revision < 1 ||
		(p.Status != "pending" && p.Status != "applied" && p.Status != "rejected") {
		return Proposal{}, ErrProposalInvalid
	}
	return p, nil
}

func writeProposal(root *os.Root, p Proposal) error {
	data, err := json.Marshal(p)
	if err != nil || len(data) > maxProposalBytes {
		return ErrProposalInvalid
	}
	name := ".pending-" + randomProposalID()
	f, err := root.OpenFile(name, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	defer func() { _ = f.Close(); _ = root.Remove(name) }()
	n, err := f.Write(data)
	if err != nil {
		return err
	}
	if n != len(data) {
		return io.ErrShortWrite
	}
	if err = f.Sync(); err != nil {
		return err
	}
	if err = f.Close(); err != nil {
		return err
	}
	return root.Rename(name, p.ID+".json")
}

func randomProposalID() string {
	// crypto/rand.Read terminates the process on an irrecoverable RNG failure.
	var b [16]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}

func proposalList(root *os.Root, role string) ([]Proposal, error) {
	f, err := root.Open(".")
	if err != nil {
		return nil, err
	}
	defer f.Close()
	entries, err := f.ReadDir(maxProposals*2 + 1)
	if err != nil && !errors.Is(err, io.EOF) {
		return nil, err
	}
	if len(entries) > maxProposals*2 {
		return nil, ErrProposalInvalid
	}
	out := []Proposal{}
	for _, entry := range entries {
		if !strings.HasSuffix(entry.Name(), ".json") {
			continue // Interrupted atomic writes are not proposal records.
		}
		if entry.IsDir() || entry.Type()&os.ModeSymlink != 0 || len(out) >= maxProposals {
			return nil, ErrProposalInvalid
		}
		p, err := readProposal(root, role, strings.TrimSuffix(entry.Name(), ".json"))
		if err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.After(out[j].CreatedAt) })
	return out, nil
}

func (m *Manager) Proposals(role string) ([]Proposal, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, err := m.load(role); err != nil {
		return nil, ErrProposalNotFound
	}
	root, err := m.proposalRoot(role, false)
	if errors.Is(err, os.ErrNotExist) {
		return []Proposal{}, nil
	}
	if err != nil {
		return nil, err
	}
	defer root.Close()
	return proposalList(root, role)
}

func (m *Manager) ProposeProfile(role, baseHash, content, reason, sessionID string) (Proposal, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if strings.TrimSpace(content) == "" || len(content) > 16<<10 ||
		strings.TrimSpace(reason) == "" || len(reason) > 1024 ||
		(sessionID != "" && !storage.ValidID(sessionID)) {
		return Proposal{}, ErrProposalInvalid
	}
	r, err := m.load(role)
	if err != nil {
		return Proposal{}, ErrProposalNotFound
	}
	if ProfileHash(r.Profile) != baseHash {
		return Proposal{}, ErrProposalConflict
	}
	if r.Profile == content {
		return Proposal{}, ErrProposalInvalid
	}
	root, err := m.proposalRoot(role, true)
	if err != nil {
		return Proposal{}, err
	}
	defer root.Close()
	items, err := proposalList(root, role)
	if err != nil {
		return Proposal{}, err
	}
	for _, existing := range items {
		if existing.Status == "pending" && existing.BaseHash == baseHash && existing.Content == content {
			return existing, nil
		}
	}
	if len(items) >= maxProposals {
		return Proposal{}, ErrProposalInvalid
	}
	now := time.Now().UTC()
	p := Proposal{ID: randomProposalID(), RoleID: role, File: "PROFILE.md", BaseHash: baseHash, Before: r.Profile, Content: content, Reason: reason, SourceSessionID: sessionID, Status: "pending", Revision: 1, CreatedAt: now, UpdatedAt: now}
	if err := writeProposal(root, p); err != nil {
		return Proposal{}, err
	}
	return p, nil
}

func (m *Manager) ReviewProposal(role, id, action string, revision int) (Proposal, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if action != "approve" && action != "reject" {
		return Proposal{}, ErrProposalInvalid
	}
	r, err := m.load(role)
	if err != nil {
		return Proposal{}, ErrProposalNotFound
	}
	root, err := m.proposalRoot(role, false)
	if errors.Is(err, os.ErrNotExist) {
		return Proposal{}, ErrProposalNotFound
	}
	if err != nil {
		return Proposal{}, err
	}
	defer root.Close()
	p, err := readProposal(root, role, id)
	if err != nil {
		return Proposal{}, err
	}
	if p.Revision != revision || p.Status != "pending" {
		return Proposal{}, ErrProposalConflict
	}
	// If PROFILE was atomically replaced but saving the status failed/crashed,
	// settle the already-applied change on retry, without overwriting user edits.
	if r.Profile == p.Content {
		p.Status = "applied"
	} else {
		if action == "approve" && ProfileHash(r.Profile) != p.BaseHash {
			return Proposal{}, ErrProposalConflict
		}
		p.Status = "rejected"
		if action == "approve" {
			if _, err := m.update(role, "PROFILE.md", p.Content); err != nil {
				return Proposal{}, err
			}
			p.Status = "applied"
		}
	}
	p.Revision++
	p.UpdatedAt = time.Now().UTC()
	if err := writeProposal(root, p); err != nil {
		return Proposal{}, err
	}
	return p, nil
}

// DeleteProposal removes only the review record, never a role document.
func (m *Manager) DeleteProposal(role, id string, revision int) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, err := m.load(role); err != nil {
		return ErrProposalNotFound
	}
	root, err := m.proposalRoot(role, false)
	if errors.Is(err, os.ErrNotExist) {
		return ErrProposalNotFound
	}
	if err != nil {
		return err
	}
	defer root.Close()
	p, err := readProposal(root, role, id)
	if err != nil {
		return err
	}
	if p.Revision != revision {
		return ErrProposalConflict
	}
	return root.Remove(id + ".json")
}
