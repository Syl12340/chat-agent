package api

import (
	"encoding/json"
	"errors"
	"os"
	"sort"
	"sync"

	"chat-agent/internal/storage"
)

// maxHostGrantSessions bounds the runtime allow-list. Host tooling is a machine
// capability, not a feature flag: a deployment that ends up with dozens of
// granted conversations has lost track of what it exposed.
const maxHostGrantSessions = 8

// hostGrants is the runtime half of the host-tool allow-list.
//
// The environment list is the operator's deployment-wide statement of intent, but
// it needs a restart to change and cannot answer "let this conversation look at
// the server" at the moment the user asks. These grants do, are per conversation,
// and are revocable without a restart. The two are unioned, so deleting the file
// can only narrow access back to the deployment default.
type hostGrants struct {
	path     string
	mu       sync.Mutex
	sessions map[string]bool
}

type hostGrantsFile struct {
	Version  int      `json:"version"`
	Sessions []string `json:"sessions"`
}

// openHostGrants reads the grants file. A missing file is the normal state and
// means "no runtime grants", not an error; a malformed one is an error, because
// silently starting from empty would quietly revoke an operator's decision.
func openHostGrants(path string) (*hostGrants, error) {
	grants := &hostGrants{path: path, sessions: map[string]bool{}}
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return grants, nil
	}
	if err != nil {
		return nil, err
	}
	var file hostGrantsFile
	if err := json.Unmarshal(data, &file); err != nil || file.Version != 1 || len(file.Sessions) > maxHostGrantSessions {
		return nil, errors.New("invalid host grant file")
	}
	for _, id := range file.Sessions {
		if !validID(id) {
			return nil, errors.New("invalid host grant session id")
		}
		grants.sessions[id] = true
	}
	return grants, nil
}

func (g *hostGrants) Allows(session string) bool {
	if g == nil || session == "" {
		return false
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.sessions[session]
}

func (g *hostGrants) List() []string {
	if g == nil {
		return []string{}
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.sortedLocked()
}

func (g *hostGrants) sortedLocked() []string {
	out := make([]string, 0, len(g.sessions))
	for id := range g.sessions {
		out = append(out, id)
	}
	sort.Strings(out)
	return out
}

// Set adds or removes one conversation and persists before returning, so a grant
// the page already shows cannot be lost by a crash a second later.
func (g *hostGrants) Set(session string, allow bool) error {
	if g == nil {
		return errors.New("host grants unavailable")
	}
	if !validID(session) {
		return errors.New("invalid host grant session id")
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	next := make(map[string]bool, len(g.sessions)+1)
	for id := range g.sessions {
		next[id] = true
	}
	if allow {
		if !next[session] && len(next) >= maxHostGrantSessions {
			return errors.New("too many granted sessions")
		}
		next[session] = true
	} else {
		delete(next, session)
	}
	g.sessions = next
	encoded, err := json.MarshalIndent(hostGrantsFile{Version: 1, Sessions: g.sortedLocked()}, "", "  ")
	if err != nil {
		return err
	}
	return storage.Replace(g.path, append(encoded, '\n'))
}
