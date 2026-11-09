// Package pluginapi defines the contract between the chat-agent core and the
// capabilities that plug into it.
//
// The shape follows "plugin-oriented core": plugins declare what they can do,
// while the core keeps the authority over identity, conversation, the tool
// execution loop, permissions and the run record. A plugin can register tools,
// services and storage scopes; it cannot create its own chat history, its own
// model loop, or its own approval path.
//
// The manifest is JSON rather than YAML because this project stays on the Go
// standard library and ships no YAML decoder. The structure is deliberately the
// same one a plugin.yaml would carry, so a converter is trivial if that decision
// ever changes.
package pluginapi

import (
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
)

// APIVersion is the only manifest version this core understands. A plugin that
// declares anything else is rejected at load time rather than at first call.
const APIVersion = "chat-agent.dev/v1"

// Kind is the only manifest kind accepted today. It exists so the format can grow
// other documents (bundles, presets) without breaking the field.
const Kind = "Plugin"

// Runtime names how a plugin is executed. Only RuntimeBuiltin is implemented; the
// other two are declared so manifests are forward-compatible and so the loader can
// reject them with a clear message instead of pretending to support them.
const (
	RuntimeBuiltin = "builtin" // in-process Go package, static registration
	RuntimeService = "service" // separate process over a socket (not implemented)
	RuntimeSandbox = "sandbox" // WASM or container isolation (not implemented)
)

// Manifest is the static description the host reads before a plugin runs.
type Manifest struct {
	APIVersion string   `json:"apiVersion"`
	Kind       string   `json:"kind"`
	Metadata   Metadata `json:"metadata"`
	Spec       Spec     `json:"spec"`
}

type Metadata struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Version     string `json:"version"`
	Description string `json:"description,omitempty"`
	Author      string `json:"author,omitempty"`
}

type Spec struct {
	Runtime      string      `json:"runtime"`
	Capabilities []string    `json:"capabilities"`
	Permissions  Permissions `json:"permissions"`
	// Tools lists the tool names this plugin registers. It must match what the
	// plugin actually registers; a mismatch is a load error, because a manifest
	// that overstates its tools is how a model ends up offered a phantom tool.
	Tools []string `json:"tools"`
	// Provides names the services this plugin publishes for other plugins.
	Provides []string `json:"provides,omitempty"`
	// Dependencies names services this plugin needs before it may load. A missing
	// dependency makes the plugin unavailable rather than failing startup: the
	// rest of the system keeps working without it.
	Dependencies []string `json:"dependencies,omitempty"`
	// Config is an optional JSON Schema for the plugin's configuration block.
	Config    json.RawMessage `json:"config,omitempty"`
	Resources Resources       `json:"resources,omitempty"`
}

// Permissions are declarations, not grants. The host re-checks every one of them
// against user configuration and the plugin's trust level before anything opens.
type Permissions struct {
	// Filesystem entries are logical namespaces such as "memory://role", never
	// absolute paths. The host resolves them to real storage.
	Filesystem []string `json:"filesystem,omitempty"`
	// Network lists allowed hosts, or exactly ["denied"] for no outbound access.
	Network []string `json:"network,omitempty"`
	// Model lists model roles the plugin may request, such as "utility". Empty
	// means the plugin never calls a model.
	Model []string `json:"model,omitempty"`
	// Sessions is "denied" or "read"; no plugin gets conversation write access.
	Sessions string `json:"sessions,omitempty"`
}

type Resources struct {
	Memory  string `json:"memory,omitempty"`
	Timeout string `json:"timeout,omitempty"`
}

var (
	idPattern         = regexp.MustCompile(`^[a-z][a-z0-9-]{0,62}(\.[a-z][a-z0-9-]{0,62})*$`)
	versionPattern    = regexp.MustCompile(`^[0-9]+\.[0-9]+\.[0-9]+([-+][0-9A-Za-z.-]+)?$`)
	capabilityPattern = regexp.MustCompile(`^[a-z][a-z0-9-]*(\.[a-z][a-z0-9-]*)+$`)
	toolNamePattern   = regexp.MustCompile(`^[a-z][a-z0-9_]{1,63}$`)
	namespacePattern  = regexp.MustCompile(`^[a-z][a-z0-9-]*://[A-Za-z0-9._/-]*$`)
)

// Parse decodes and validates a manifest document.
func Parse(data []byte) (Manifest, error) {
	var manifest Manifest
	decoder := json.NewDecoder(strings.NewReader(string(data)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&manifest); err != nil {
		return Manifest{}, fmt.Errorf("manifest 不是合法的 JSON：%w", err)
	}
	if err := manifest.Validate(); err != nil {
		return Manifest{}, err
	}
	return manifest, nil
}

// Validate enforces the parts of the contract the host relies on.
func (m Manifest) Validate() error {
	if m.APIVersion != APIVersion {
		return fmt.Errorf("不支持的 apiVersion %q，需要 %q", m.APIVersion, APIVersion)
	}
	if m.Kind != Kind {
		return fmt.Errorf("不支持的 kind %q，需要 %q", m.Kind, Kind)
	}
	if !idPattern.MatchString(m.Metadata.ID) {
		return errors.New("metadata.id 必须是点分的小写标识，例如 builtin.search")
	}
	if strings.TrimSpace(m.Metadata.Name) == "" {
		return errors.New("metadata.name 不能为空")
	}
	if !versionPattern.MatchString(m.Metadata.Version) {
		return errors.New("metadata.version 必须是 x.y.z 形式")
	}
	switch m.Spec.Runtime {
	case RuntimeBuiltin:
	case RuntimeService, RuntimeSandbox:
		return fmt.Errorf("runtime %q 尚未实现，当前只支持 builtin", m.Spec.Runtime)
	default:
		return fmt.Errorf("未知的 runtime %q", m.Spec.Runtime)
	}
	if len(m.Spec.Capabilities) == 0 {
		return errors.New("spec.capabilities 至少要声明一项能力")
	}
	seen := map[string]bool{}
	for _, capability := range m.Spec.Capabilities {
		if !capabilityPattern.MatchString(capability) {
			return fmt.Errorf("能力名 %q 必须是点分形式，例如 search.web", capability)
		}
		if seen[capability] {
			return fmt.Errorf("能力名 %q 重复声明", capability)
		}
		seen[capability] = true
	}
	tools := map[string]bool{}
	for _, name := range m.Spec.Tools {
		if !toolNamePattern.MatchString(name) {
			return fmt.Errorf("工具名 %q 不合法", name)
		}
		if tools[name] {
			return fmt.Errorf("工具名 %q 重复声明", name)
		}
		tools[name] = true
	}
	for _, namespace := range m.Spec.Permissions.Filesystem {
		if !namespacePattern.MatchString(namespace) || strings.Contains(namespace, "..") {
			return fmt.Errorf("文件系统权限 %q 必须是逻辑命名空间，例如 memory://role", namespace)
		}
	}
	services := map[string]bool{}
	for _, name := range m.Spec.Provides {
		if !capabilityPattern.MatchString(name) {
			return fmt.Errorf("服务名 %q 必须是点分形式，例如 search.client", name)
		}
		if services[name] {
			return fmt.Errorf("服务名 %q 重复声明", name)
		}
		services[name] = true
	}
	for _, name := range m.Spec.Dependencies {
		if !capabilityPattern.MatchString(name) {
			return fmt.Errorf("依赖名 %q 必须是点分形式，例如 search.client", name)
		}
		if services[name] {
			return fmt.Errorf("插件不能依赖自己提供的服务 %q", name)
		}
	}
	if len(m.Spec.Config) > 0 && !json.Valid(m.Spec.Config) {
		return errors.New("spec.config 必须是合法的 JSON Schema")
	}
	switch m.Spec.Permissions.Sessions {
	case "", "denied", "read":
	default:
		return fmt.Errorf("spec.permissions.sessions 只能是 denied 或 read，收到 %q", m.Spec.Permissions.Sessions)
	}
	for _, host := range m.Spec.Permissions.Network {
		if strings.TrimSpace(host) == "" {
			return errors.New("网络权限不能包含空条目")
		}
	}
	return nil
}

// Capability reports whether the manifest declares a capability.
func (m Manifest) Capability(name string) bool {
	for _, capability := range m.Spec.Capabilities {
		if capability == name {
			return true
		}
	}
	return false
}

// DeclaresTool reports whether the manifest promised this tool.
func (m Manifest) DeclaresTool(name string) bool {
	for _, tool := range m.Spec.Tools {
		if tool == name {
			return true
		}
	}
	return false
}
