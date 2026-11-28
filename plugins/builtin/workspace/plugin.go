// Package workspace serves files and fixed actions in one host-approved directory.
package workspace

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"mime"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"chat-agent/internal/pluginapi"
	"chat-agent/internal/probe"
	"chat-agent/internal/webtools"
)

const ID = "builtin.workspace"
const Permission = "filesystem:workspace://server"
const limit = 12 << 10
const downloadLimit = 32 << 20

// widerModes is the escalation ladder. It is deliberately not a preference: a
// call may only reach a level strictly above the one it chose, so "widen" can
// never be used to narrow, and there is no path from the wider level back to a
// level the caller could have picked without asking. The executor asks the person
// before any of these apply, and the grant covers one call.
var widerModes = map[string][]string{
	"read-only":       {"workspace-write", "danger-full-access"},
	"workspace-write": {"danger-full-access"},
}

// writeLimit bounds a whole file; limit bounds one read response. They used to be
// the same number, which meant a file larger than one response could not be read
// at all and therefore could never be edited — the edit path requires having read
// the file. Reads now page through anything write would accept.
const writeLimit = 256 << 10

type Plugin struct {
	path    string
	root    *os.Root
	actions map[string][]string
	writeMu sync.Mutex
	// web is the host's egress client. Download needs it because the confined
	// shell has no sockets; without a client the tool stays absent rather than
	// falling back to an unchecked fetch.
	web *webtools.Client
}

// SetWeb lets the host hand over its egress client. It is optional: a deployment
// without web access simply does not offer workspace_download.
func (p *Plugin) SetWeb(client *webtools.Client) { p.web = client }

func New(path, dataDir, actionsJSON string) (*Plugin, error) {
	if !filepath.IsAbs(path) {
		return nil, errors.New("workspace root must be absolute")
	}
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil {
		return nil, err
	}
	data, err := filepath.Abs(dataDir)
	if err != nil {
		return nil, err
	}
	if real, err := filepath.EvalSymlinks(data); err == nil {
		data = real
	}
	if filepath.Dir(resolved) == resolved {
		return nil, errors.New("workspace root must be a dedicated directory")
	}
	for _, protected := range []string{data, "/etc", "/proc", "/sys", "/dev", "/run", "/root", "/opt/chat-agent"} {
		if under(resolved, protected) || under(protected, resolved) {
			return nil, errors.New("workspace root overlaps protected host data")
		}
	}
	actions := map[string][]string{}
	if actionsJSON != "" {
		if len(actionsJSON) > 16384 || json.Unmarshal([]byte(actionsJSON), &actions) != nil || len(actions) > 16 {
			return nil, errors.New("invalid workspace actions JSON")
		}
		for name, argv := range actions {
			if name == "" || len(name) > 48 || len(argv) == 0 || len(argv) > 24 || !filepath.IsAbs(argv[0]) {
				return nil, errors.New("workspace action requires a name and a fixed absolute executable")
			}
		}
	}
	return &Plugin{path: resolved, actions: actions}, nil
}

func under(base, path string) bool {
	relative, err := filepath.Rel(base, path)
	return err == nil && filepath.IsLocal(relative)
}

func (p *Plugin) Manifest() pluginapi.Manifest {
	tools := []string{"workspace_list", "workspace_read", "workspace_search", "workspace_write", "workspace_share"}
	if len(p.actions) > 0 && canExecute() {
		tools = append(tools, "workspace_exec")
	}
	if sandboxReady() {
		tools = append(tools, "workspace_shell")
	}
	return pluginapi.Manifest{
		APIVersion: pluginapi.APIVersion, Kind: pluginapi.Kind,
		Metadata: pluginapi.Metadata{ID: ID, Name: "工作区", Version: "0.3.0"},
		Spec: pluginapi.Spec{Runtime: pluginapi.RuntimeBuiltin, Capabilities: []string{"workspace.server"},
			Permissions: pluginapi.Permissions{Filesystem: []string{"workspace://server"}}, Tools: tools},
	}
}

func (p *Plugin) Start(_ context.Context, host pluginapi.Host) error {
	if !host.Allowed(Permission) {
		return errors.New("workspace permission denied")
	}
	root, err := os.OpenRoot(p.path)
	p.root = root
	return err
}
func (p *Plugin) Stop(context.Context) error {
	if p.root == nil {
		return nil
	}
	return p.root.Close()
}

func (p *Plugin) Register(registry *pluginapi.Registry) error {
	path := `{"type":"object","properties":{"workspace":{"type":"string","enum":["server"]},"path":{"type":"string","maxLength":256}},"required":["workspace","path"],"additionalProperties":false}`
	read := `{"type":"object","properties":{"workspace":{"type":"string","enum":["server"]},"path":{"type":"string","maxLength":256},"offset":{"type":"integer","minimum":0,"maximum":262144,"description":"Absolute byte offset. Start at zero. For another page use next_read.offset."},"expected_sha256":{"type":"string","minLength":64,"maxLength":64,"description":"Use next_read.expected_sha256 on subsequent pages to keep the same file version."}},"required":["workspace","path"],"additionalProperties":false}`
	search := `{"type":"object","properties":{"workspace":{"type":"string","enum":["server"]},"query":{"type":"string","minLength":1,"maxLength":128}},"required":["workspace","query"],"additionalProperties":false}`
	tools := []pluginapi.Tool{
		{Name: "workspace_list", Description: "List the approved server workspace. Use path . for its root.", Parameters: json.RawMessage(path), Handler: p.list},
		{Name: "workspace_read", Description: "Read a UTF-8 text file in the approved workspace. If truncated, call workspace_read with the JSON object after next_read. The sha256 covers the full file and is accepted by workspace_write.", Parameters: json.RawMessage(read), Handler: p.read},
		{Name: "workspace_search", Description: "Find literal text in small files in the approved server workspace.", Parameters: json.RawMessage(search), Handler: p.search},
		{Name: "workspace_write", Kind: "edit", Description: "Create or replace one bounded UTF-8 file in the approved workspace. The expected SHA-256 prevents overwriting a file that changed since it was read.", Usage: "read an existing file first and pass its exact SHA-256; use expected_sha256=missing only for a new path. Write only what the user requested and verify important changes by reading them back.", Parameters: json.RawMessage(`{"type":"object","properties":{"workspace":{"type":"string","enum":["server"]},"path":{"type":"string","maxLength":256},"content":{"type":"string","maxLength":262144},"expected_sha256":{"type":"string","maxLength":64}},"required":["workspace","path","content","expected_sha256"],"additionalProperties":false}`), Decision: string(probe.DecisionRequired), Risk: string(probe.RiskHigh), ApprovalRequired: true, Handler: p.write},
		{Name: "workspace_share", Kind: "read", Description: "Offer one regular file from the approved workspace to the user as an authenticated, same-session download. The receipt includes the exact size and SHA-256; never expose an absolute host path.", Usage: "use after creating or locating the exact file the user asked to receive; share the file itself instead of pasting binary data or a host path.", Parameters: json.RawMessage(path), Handler: p.share},
	}
	if len(p.actions) > 0 && canExecute() {
		names := make([]string, 0, len(p.actions))
		for name := range p.actions {
			names = append(names, name)
		}
		slices.Sort(names)
		enum, _ := json.Marshal(names)
		schema := `{"type":"object","properties":{"workspace":{"type":"string","enum":["server"]},"action":{"type":"string","enum":` + string(enum) + `},"require_sandbox":{"type":"boolean"}},"required":["workspace","action"],"additionalProperties":false}`
		tools = append(tools, pluginapi.Tool{Name: "workspace_exec", Description: "Run a fixed operator-configured action in the trusted workspace. Process restrictions are not a security sandbox.", Usage: "use only when the user explicitly requests the configured action.", Parameters: json.RawMessage(schema), Handler: p.execute})
	}
	if sandboxReady() {
		shell := `{"type":"object","properties":{"workspace":{"type":"string","enum":["server"]},"command":{"type":"string","minLength":1,"maxLength":4096},"cwd":{"type":"string","maxLength":256},` +
			`"mode":{"type":"string","enum":["read-only","workspace-write"],"description":"The confinement this command needs. Choose the narrowest level that suffices."},` +
			`"sandbox_permissions":{"type":"string","enum":["workspace-write","danger-full-access"],"description":"The narrowest wider level for a one-shot retry of the exact command the sandbox just denied. Repeating the current level needs no approval; asking for more always asks the person first."},` +
			`"justification":{"type":"string","minLength":1,"maxLength":500,"description":"One sentence, shown to the person, saying why this command cannot work at the current level. Required with sandbox_permissions and invalid without it."}},` +
			`"required":["workspace","command","mode"],"additionalProperties":false}`
		tools = append(tools, pluginapi.Tool{
			Name: "workspace_shell",
			Description: "Run a shell command on the server under a fail-closed Landlock and seccomp policy. Choose read-only for inspection and workspace-write only when the requested task needs to modify the approved workspace. " +
				"System program directories remain read-only, host configuration and process state are unreachable, and socket creation is denied under both levels. " +
				"Two levels exist only as escalation targets: workspace-write widens writes, and danger-full-access removes the confinement for one approved call, including its network ban. " +
				"Escalation is never the default and never applies to a second call; if the person rejects it, stop and explain instead of working around the boundary.",
			Usage: "plan and run ordinary shell commands as a careful human operator would: inspect before editing, use one coherent command at a time, select the narrowest mode, check the result, and never use sudo. " +
				"Ask for sandbox_permissions only after a real denial, with the narrowest level that would have sufficed and one sentence of justification. This is not a root shell.",
			Parameters: json.RawMessage(shell),
			Kind:       "execute",
			Risk:       string(probe.RiskHigh),
			// Repeating the current level is ordinary work; only reaching wider asks.
			ApprovalWhen: shellApprovalReason,
			Handler:      p.shell,
		})
	}
	if p.web != nil && p.web.FetchEnabled() {
		// The confined shell has no sockets, so a task that needs a file from the web
		// has no way to get one. This is that way: the host fetches under the same
		// public-HTTPS policy as any page read, then publishes the bytes into the
		// workspace where the shell can work on them.
		tools = append(tools, pluginapi.Tool{
			Name: "workspace_download",
			Description: "Download one public HTTPS file into the server workspace and return its path, size and SHA-256. " +
				"Use it because the shell has no network: fetch here first, then process the file with workspace_shell. " +
				"Private and reserved destinations, redirects and oversized bodies are refused, so some addresses will fail; report that plainly.",
			Usage:      "use when the task needs a file that already exists at a public address. Give the destination path inside the workspace, never an absolute host path, and pass expected_sha256 when the source publishes one.",
			Parameters: json.RawMessage(`{"type":"object","properties":{"workspace":{"type":"string","enum":["server"]},"url":{"type":"string","maxLength":2048},"path":{"type":"string","minLength":1,"maxLength":256},"expected_sha256":{"type":"string","pattern":"^[a-f0-9]{64}$"}},"required":["workspace","url","path"],"additionalProperties":false}`),
			Kind:       "edit",
			Risk:       string(probe.RiskMedium),
			Handler:    p.download,
		})
	}
	for i, tool := range tools {
		tool.Capability, tool.Order, tool.Timeout = "workspace.server", 710+i, 12*time.Second
		if err := registry.Tool(tool); err != nil {
			return err
		}
	}
	return nil
}

func allowedPath(path string) bool {
	if path == "." {
		return true
	}
	if !fs.ValidPath(path) || len(path) > 256 || strings.ContainsAny(path, "\\:\x00") {
		return false
	}
	for _, part := range strings.Split(path, "/") {
		if strings.HasPrefix(part, ".") {
			return false
		}
	}
	return true
}

func pathArg(raw json.RawMessage) (string, error) {
	var args struct{ Workspace, Path string }
	if json.Unmarshal(raw, &args) != nil || args.Workspace != "server" || !allowedPath(args.Path) {
		return "", errors.New("invalid workspace or relative path")
	}
	return args.Path, nil
}

func (p *Plugin) list(ctx context.Context, raw json.RawMessage) (pluginapi.Result, error) {
	path, err := pathArg(raw)
	if err != nil {
		return pluginapi.Result{}, err
	}
	root, err := p.root.OpenRoot(path)
	if err != nil {
		return pluginapi.Result{}, errors.New("workspace directory unavailable")
	}
	defer root.Close()
	f, err := root.Open(".")
	if err != nil {
		return pluginapi.Result{}, errors.New("workspace directory unavailable")
	}
	defer f.Close()
	items, err := f.ReadDir(100)
	if err != nil && !errors.Is(err, io.EOF) {
		return pluginapi.Result{}, errors.New("workspace directory unavailable")
	}
	names := []string{}
	for _, entry := range items {
		if !allowedPath(entry.Name()) || entry.Type()&os.ModeSymlink != 0 {
			continue
		}
		name := entry.Name()
		if entry.IsDir() {
			name += "/"
		}
		names = append(names, name)
	}
	if err := ctx.Err(); err != nil {
		return pluginapi.Result{}, err
	}
	return encoded(map[string]any{"entries": names, "limit": 100})
}

func (p *Plugin) text(path string) (string, error) {
	info, err := p.root.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Size() > limit {
		return "", errors.New("workspace file is missing or exceeds the text limit")
	}
	f, err := openText(p.root, path)
	if err != nil {
		return "", errors.New("workspace file unavailable")
	}
	defer f.Close()
	info, err = f.Stat()
	if err != nil || !info.Mode().IsRegular() {
		return "", errors.New("workspace file is not regular")
	}
	b, err := io.ReadAll(io.LimitReader(f, limit+1))
	if err != nil || len(b) > limit || !utf8.Valid(b) || strings.ContainsRune(string(b), 0) {
		return "", errors.New("workspace file is not bounded UTF-8 text")
	}
	return string(b), nil
}

// readWhole loads a workspace file up to what write accepts. Reads page their
// responses, but the digest workspace_write verifies is over the whole file, so
// paging must not change what is hashed.
func (p *Plugin) readWhole(path string) ([]byte, error) {
	info, err := p.root.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Size() > writeLimit {
		return nil, errors.New("workspace file is missing or exceeds the writable limit")
	}
	f, err := openText(p.root, path)
	if err != nil {
		return nil, errors.New("workspace file unavailable")
	}
	defer f.Close()
	if opened, err := f.Stat(); err != nil || !opened.Mode().IsRegular() {
		return nil, errors.New("workspace file is not regular")
	}
	b, err := io.ReadAll(io.LimitReader(f, writeLimit+1))
	if err != nil || len(b) > writeLimit || !utf8.Valid(b) || strings.ContainsRune(string(b), 0) {
		return nil, errors.New("workspace file is not bounded UTF-8 text")
	}
	return b, nil
}

func (p *Plugin) read(ctx context.Context, raw json.RawMessage) (pluginapi.Result, error) {
	var args struct {
		Offset         int    `json:"offset"`
		ExpectedSHA256 string `json:"expected_sha256"`
	}
	if err := json.Unmarshal(raw, &args); err != nil {
		return pluginapi.Result{}, errors.New("invalid workspace read arguments")
	}
	path, err := pathArg(raw)
	if err != nil {
		return pluginapi.Result{}, err
	}
	if err := ctx.Err(); err != nil {
		return pluginapi.Result{}, err
	}
	b, err := p.readWhole(path)
	if err != nil {
		return pluginapi.Result{}, err
	}
	total := len(b)
	if args.Offset < 0 || args.Offset > total || (args.Offset < total && !utf8.RuneStart(b[args.Offset])) {
		return pluginapi.Result{}, errors.New("workspace read offset is not a UTF-8 boundary inside the file")
	}
	end := min(args.Offset+limit, total)
	// Never cut a multi-byte character in half; the caller resumes from a rune
	// boundary or the excerpt would not be valid UTF-8 on its own.
	for end > args.Offset && end < total && !utf8.RuneStart(b[end]) {
		end--
	}
	digest := sha256.Sum256(b)
	if args.ExpectedSHA256 != "" && args.ExpectedSHA256 != hex.EncodeToString(digest[:]) {
		return pluginapi.Result{}, errors.New("workspace file changed; restart reading at offset 0")
	}
	if end < total {
		next, _ := json.Marshal(map[string]any{"workspace": "server", "path": path, "offset": end, "expected_sha256": fmt.Sprintf("%x", digest)})
		return pluginapi.Result{Output: fmt.Sprintf(
			"%s\n\n---\ntruncated: true\nnext_read: %s\noffset: %d\ntotal_bytes: %d\nnext_offset: %d\nsha256: %x",
			b[args.Offset:end], next, args.Offset, total, end, digest)}, nil
	}
	// The digest is over the whole file even when this is one page of several, so
	// workspace_write can verify what it replaces.
	return pluginapi.Result{Output: fmt.Sprintf(
		"%s\n\n---\noffset: %d\ntotal_bytes: %d\nsha256: %x", b[args.Offset:end], args.Offset, total, digest)}, nil
}

func (p *Plugin) write(ctx context.Context, raw json.RawMessage) (pluginapi.Result, error) {
	var args struct {
		Workspace      string `json:"workspace"`
		Path           string `json:"path"`
		Content        string `json:"content"`
		ExpectedSHA256 string `json:"expected_sha256"`
	}
	if json.Unmarshal(raw, &args) != nil || args.Workspace != "server" || args.Path == "." || !allowedPath(args.Path) || len(args.Content) > writeLimit || !utf8.ValidString(args.Content) {
		return pluginapi.Result{}, errors.New("invalid workspace write")
	}
	p.writeMu.Lock()
	defer p.writeMu.Unlock()
	if err := ctx.Err(); err != nil {
		return pluginapi.Result{}, err
	}
	info, statErr := p.root.Lstat(args.Path)
	if args.ExpectedSHA256 == "missing" {
		if statErr == nil || !errors.Is(statErr, os.ErrNotExist) {
			return pluginapi.Result{}, errors.New("workspace path already exists or cannot be verified")
		}
	} else {
		if len(args.ExpectedSHA256) != 64 || strings.ToLower(args.ExpectedSHA256) != args.ExpectedSHA256 || statErr != nil || !info.Mode().IsRegular() || info.Size() > writeLimit {
			return pluginapi.Result{}, errors.New("workspace file is missing or expected hash is invalid")
		}
		if _, err := hex.DecodeString(args.ExpectedSHA256); err != nil {
			return pluginapi.Result{}, errors.New("workspace expected hash is invalid")
		}
		current, readErr := p.readWhole(args.Path)
		if readErr != nil {
			return pluginapi.Result{}, errors.New("workspace file cannot be verified")
		}
		digest := sha256.Sum256(current)
		if fmt.Sprintf("%x", digest[:]) != args.ExpectedSHA256 {
			return pluginapi.Result{}, errors.New("workspace file changed; read it again before writing")
		}
	}
	parent := filepath.ToSlash(filepath.Dir(args.Path))
	if parent != "." {
		if err := p.root.MkdirAll(parent, 0700); err != nil {
			return pluginapi.Result{}, errors.New("workspace parent directory is unavailable")
		}
	}
	var random [8]byte
	if _, err := io.ReadFull(rand.Reader, random[:]); err != nil {
		return pluginapi.Result{}, err
	}
	temporary := filepath.ToSlash(filepath.Join(parent, ".ca-write-"+hex.EncodeToString(random[:])))
	file, err := p.root.OpenFile(temporary, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return pluginapi.Result{}, errors.New("workspace temporary file is unavailable")
	}
	defer p.root.Remove(temporary)
	if _, err = io.WriteString(file, args.Content); err == nil {
		err = file.Sync()
	}
	closeErr := file.Close()
	if err == nil {
		err = closeErr
	}
	if err != nil {
		return pluginapi.Result{}, errors.New("workspace write failed")
	}
	if err := ctx.Err(); err != nil {
		return pluginapi.Result{}, err
	}
	if args.ExpectedSHA256 == "missing" {
		if err = p.root.Link(temporary, args.Path); err != nil {
			return pluginapi.Result{}, errors.New("workspace exclusive create failed; verify the target path before retrying")
		}
	} else {
		current, readErr := p.readWhole(args.Path)
		if readErr != nil || fmt.Sprintf("%x", sha256.Sum256(current)) != args.ExpectedSHA256 {
			return pluginapi.Result{}, errors.New("workspace file changed; read it again before writing")
		}
		if err = p.root.Rename(temporary, args.Path); err != nil {
			return pluginapi.Result{}, errors.New("workspace atomic replace failed")
		}
	}
	digest := sha256.Sum256([]byte(args.Content))
	result, err := encoded(map[string]any{"path": args.Path, "bytes": len(args.Content), "sha256": fmt.Sprintf("%x", digest[:]), "status": "written"})
	if err != nil {
		return result, err
	}
	// A write is a business operation, not just a tool result: the receipt is what
	// lets the outcome be reconciled after a restart. The receipt contract requires
	// identifier-shaped entity ids and a revision of at least 1, and a workspace
	// path is neither — "note.txt" contains a dot — so the id is derived from the
	// path and the revision from the content digest. Both are deterministic, which
	// is what makes them checkable later: same path and same bytes reproduce the
	// same pair, different bytes do not.
	argumentsDigest := sha256.Sum256(raw)
	entityDigest := sha256.Sum256([]byte(args.Path))
	revision := int(digest[0])<<16 | int(digest[1])<<8 | int(digest[2])
	if revision == 0 {
		revision = 1
	}
	operationID := probe.ToolOperationID(ctx)
	if operationID == "" {
		operationID = probe.ToolCallID(ctx)
	}
	if operationID == "" {
		// Derive a stable id from the call itself so the receipt still names an
		// operation when the runtime supplied neither.
		operationID = fmt.Sprintf("workspace-%x", argumentsDigest[:16])
	}
	result.Receipt = &pluginapi.Receipt{
		Store:              "workspace",
		OperationID:        operationID,
		ArgumentsSHA256:    fmt.Sprintf("%x", argumentsDigest[:]),
		EntityIDs:          []string{fmt.Sprintf("%x", entityDigest[:16])},
		ResultingRevisions: []int{revision},
		OutcomeDigest:      fmt.Sprintf("%x", digest[:]),
	}
	return result, nil
}

func (p *Plugin) search(ctx context.Context, raw json.RawMessage) (pluginapi.Result, error) {
	var args struct{ Workspace, Query string }
	if json.Unmarshal(raw, &args) != nil || args.Workspace != "server" || strings.TrimSpace(args.Query) == "" || len(args.Query) > 512 {
		return pluginapi.Result{}, errors.New("invalid search query")
	}
	paths := []string{}
	visited := 0
	err := fs.WalkDir(p.root.FS(), ".", func(path string, entry fs.DirEntry, err error) error {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		visited++
		if visited > 200 || len(paths) >= 30 {
			return fs.SkipAll
		}
		if err != nil {
			return nil
		}
		if !allowedPath(path) {
			if entry.IsDir() {
				return fs.SkipDir
			}
			return nil
		}
		if entry.Type().IsRegular() {
			content, err := p.text(path)
			if err == nil && strings.Contains(content, args.Query) {
				paths = append(paths, path)
			}
		}
		return nil
	})
	if err != nil {
		return pluginapi.Result{}, err
	}
	return encoded(map[string]any{"matches": paths, "scan_limit": 200})
}

// share issues a content-bound URL rather than copying bytes into the model
// transcript. The download handler re-hashes the current file, so a file that
// changes between the tool call and the click is rejected.
func (p *Plugin) share(ctx context.Context, raw json.RawMessage) (pluginapi.Result, error) {
	path, err := pathArg(raw)
	if err != nil || path == "." {
		return pluginapi.Result{}, errors.New("a regular workspace file is required")
	}
	file, info, mediaType, digest, err := openDownload(p.path, path, "")
	if err != nil {
		return pluginapi.Result{}, err
	}
	_ = file.Close()
	session := probe.ToolSessionID(ctx)
	if session == "" {
		return pluginapi.Result{}, errors.New("workspace share requires a conversation scope")
	}
	downloadURL := "/api/sessions/" + url.PathEscape(session) + "/workspace-files/" + digest + "?path=" + url.QueryEscape(path)
	return encoded(map[string]any{
		"kind": "workspace_file", "name": filepath.Base(path), "bytes": info.Size(), "media_type": mediaType,
		"sha256": digest, "download_url": downloadURL,
	})
}

// OpenDownload is the shared enforcement seam between a tool receipt and the
// authenticated host download route.
func OpenDownload(root, path, digest string) (*os.File, os.FileInfo, string, string, error) {
	return openDownload(root, path, digest)
}

func openDownload(rootPath, path, expected string) (*os.File, os.FileInfo, string, string, error) {
	if !allowedPath(path) || path == "." || expected != "" && (len(expected) != 64 || strings.ToLower(expected) != expected) {
		return nil, nil, "", "", errors.New("invalid workspace download receipt")
	}
	root, err := os.OpenRoot(rootPath)
	if err != nil {
		return nil, nil, "", "", errors.New("workspace unavailable")
	}
	defer root.Close()
	file, err := root.Open(path)
	if err != nil {
		return nil, nil, "", "", errors.New("workspace file unavailable")
	}
	fail := func(err error) (*os.File, os.FileInfo, string, string, error) {
		_ = file.Close()
		return nil, nil, "", "", err
	}
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Size() < 0 || info.Size() > downloadLimit {
		return fail(errors.New("workspace file is missing, not regular, or exceeds 32 MiB"))
	}
	hash := sha256.New()
	if _, err = io.Copy(hash, io.LimitReader(file, downloadLimit+1)); err != nil {
		return fail(errors.New("workspace file could not be verified"))
	}
	digest := fmt.Sprintf("%x", hash.Sum(nil))
	if expected != "" && digest != expected {
		return fail(errors.New("workspace file changed after it was offered; ask the agent to share it again"))
	}
	if _, err = file.Seek(0, io.SeekStart); err != nil {
		return fail(errors.New("workspace file could not be reopened"))
	}
	mediaType := mime.TypeByExtension(strings.ToLower(filepath.Ext(path)))
	if mediaType == "" {
		mediaType = "application/octet-stream"
	}
	return file, info, mediaType, digest, nil
}

func (p *Plugin) execute(ctx context.Context, raw json.RawMessage) (pluginapi.Result, error) {
	var args struct {
		Workspace, Action string
		RequireSandbox    bool `json:"require_sandbox"`
	}
	if json.Unmarshal(raw, &args) != nil || args.Workspace != "server" {
		return pluginapi.Result{}, errors.New("invalid workspace")
	}
	if args.RequireSandbox {
		return pluginapi.Result{}, errors.New("SANDBOX_UNAVAILABLE: this host only offers the fixed process runner")
	}
	argv := p.actions[args.Action]
	operation := probe.ToolOperationID(ctx)
	if len(argv) == 0 || operation == "" {
		return pluginapi.Result{}, errors.New("workspace action requires a configured action and a recorded operation")
	}
	output, status := runAction(ctx, p.path, argv)
	return encoded(map[string]any{"operation_id": operation, "status": status, "output": output})
}

func encoded(value any) (pluginapi.Result, error) {
	data, err := json.Marshal(value)
	return pluginapi.Result{Output: string(data)}, err
}

// shellApprovalReason decides whether one shell call needs a person's approval.
// An empty result means no: choosing a level is ordinary work, and only reaching
// past it asks. The reason is what the person reads, so it names both levels and
// carries the model's own sentence.
func shellApprovalReason(raw json.RawMessage) string {
	var args struct {
		Mode               string `json:"mode"`
		SandboxPermissions string `json:"sandbox_permissions"`
		Justification      string `json:"justification"`
	}
	if json.Unmarshal(raw, &args) != nil || args.SandboxPermissions == "" || args.SandboxPermissions == args.Mode {
		return ""
	}
	return "把服务器 shell 从 " + args.Mode + " 升到 " + args.SandboxPermissions + "：" + strings.TrimSpace(args.Justification)
}

// resolveShellMode returns the level one call actually runs at.
//
// The two arguments travel together, and the target must be strictly wider than
// the chosen level. Both rules are enforced here, at execution, because the
// schema can only list the closed set of targets while "wider" depends on what
// this call asked for.
func resolveShellMode(mode, permissions, justification string) (string, error) {
	if mode != "read-only" && mode != "workspace-write" {
		return "", errors.New("sandbox mode must be read-only or workspace-write")
	}
	if (permissions == "") != (strings.TrimSpace(justification) == "") {
		return "", errors.New("sandbox_permissions requires a justification, and a justification is only valid with sandbox_permissions")
	}
	if permissions == "" {
		return mode, nil
	}
	if !slices.Contains(widerModes[mode], permissions) {
		return "", errors.New("sandbox_permissions must be strictly wider than mode")
	}
	return permissions, nil
}
//
// The point of the capability is a terminal on the server, so the command is
// free-form and reaches /bin/sh. What constrains it is the filesystem policy:
// read-only system directories, one writable workspace, no TCP. When that
// policy cannot be enforced the command does not run and the caller is told
// SANDBOX_UNAVAILABLE, because running unconfined would be a different promise.
// download fetches one public file and publishes it into the workspace.
//
// The bytes land through the host's egress client, never through the shell, so the
// same public-HTTPS policy applies as for reading a page. The destination is
// written through a private temporary file and then renamed into place, so a
// half-finished download can never become a file the shell picks up and treats as
// real input.
func (p *Plugin) download(ctx context.Context, raw json.RawMessage) (pluginapi.Result, error) {
	var args struct {
		Workspace      string `json:"workspace"`
		URL            string `json:"url"`
		Path           string `json:"path"`
		ExpectedSHA256 string `json:"expected_sha256"`
	}
	if json.Unmarshal(raw, &args) != nil || args.Workspace != "server" {
		return pluginapi.Result{}, errors.New("invalid workspace")
	}
	if !allowedPath(args.Path) || args.Path == "." {
		return pluginapi.Result{}, errors.New("destination must be a relative path inside the workspace")
	}
	data, mediaType, err := p.web.Download(ctx, args.URL, downloadLimit)
	if err != nil {
		return pluginapi.Result{}, err
	}
	if len(data) == 0 {
		return pluginapi.Result{}, errors.New("the address returned an empty body")
	}
	digest := sha256.Sum256(data)
	sum := fmt.Sprintf("%x", digest[:])
	if args.ExpectedSHA256 != "" && args.ExpectedSHA256 != sum {
		return pluginapi.Result{}, errors.New("downloaded bytes do not match expected_sha256")
	}
	p.writeMu.Lock()
	defer p.writeMu.Unlock()
	if err := ctx.Err(); err != nil {
		return pluginapi.Result{}, err
	}
	parent := filepath.ToSlash(filepath.Dir(args.Path))
	if parent != "." {
		if err := p.root.MkdirAll(parent, 0700); err != nil {
			return pluginapi.Result{}, errors.New("workspace parent directory is unavailable")
		}
	}
	var random [8]byte
	if _, err := io.ReadFull(rand.Reader, random[:]); err != nil {
		return pluginapi.Result{}, err
	}
	temporary := filepath.ToSlash(filepath.Join(parent, ".ca-download-"+hex.EncodeToString(random[:])))
	file, err := p.root.OpenFile(temporary, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return pluginapi.Result{}, errors.New("workspace temporary file is unavailable")
	}
	defer p.root.Remove(temporary)
	_, err = file.Write(data)
	if err == nil {
		err = file.Sync()
	}
	if closeErr := file.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return pluginapi.Result{}, errors.New("workspace download could not be written")
	}
	if err = p.root.Rename(temporary, args.Path); err != nil {
		return pluginapi.Result{}, errors.New("workspace publish failed")
	}
	return encoded(map[string]any{"path": args.Path, "bytes": len(data), "sha256": sum, "media_type": mediaType, "status": "downloaded"})
}

func (p *Plugin) shell(ctx context.Context, raw json.RawMessage) (pluginapi.Result, error) {
	var args struct {
		Workspace, Command string
		Cwd                string `json:"cwd"`
		Mode               string `json:"mode"`
		SandboxPermissions string `json:"sandbox_permissions"`
		Justification      string `json:"justification"`
	}
	if args.Workspace != "server" {
		return pluginapi.Result{}, errors.New("invalid workspace")
	}
	command := strings.TrimSpace(args.Command)
	if command == "" || len(args.Command) > 4096 {
		return pluginapi.Result{}, errors.New("command must be between 1 and 4096 bytes")
	}
	effective, err := resolveShellMode(args.Mode, args.SandboxPermissions, args.Justification)
	if err != nil {
		return pluginapi.Result{}, err
	}
	dir := p.path
	if cwd := strings.TrimSpace(args.Cwd); cwd != "" && cwd != "." {
		if !allowedPath(cwd) {
			return pluginapi.Result{}, errors.New("cwd must be a relative path inside the workspace")
		}
		if _, err := p.root.Stat(cwd); err != nil {
			return pluginapi.Result{}, errors.New("cwd is not a directory in the workspace")
		}
		dir = filepath.Join(p.path, filepath.Clean(cwd))
		info, statErr := os.Stat(dir)
		if statErr != nil || !info.IsDir() {
			return pluginapi.Result{}, errors.New("workspace cwd must be an existing directory")
		}
	}
	output, status, err := runShell(ctx, p.path, dir, command, effective)
	if err != nil {
		return pluginapi.Result{}, err
	}
	// The result states the confinement that actually applied, not the one asked
	// for: a caller reading "network: sockets denied" must be able to trust it.
	network := "sockets denied"
	if effective == "danger-full-access" {
		network = "unrestricted (approved for this call only)"
	}
	return encoded(map[string]any{
		"status": status, "output": output, "cwd": strings.TrimPrefix(dir, p.path),
		"mode": effective, "writable": map[bool]string{true: "workspace only", false: "temporary scratch only"}[effective == "workspace-write"], "network": network, "sandbox": sandboxStatus(),
	})
}
