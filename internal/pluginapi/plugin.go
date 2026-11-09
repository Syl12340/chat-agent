package pluginapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"reflect"
	"sort"
	"strings"
	"time"
)

// Plugin is one unit of capability.
//
// The lifecycle mirrors Cordis (DeepSeek Harness, MIT): a plugin is declared, its
// manifest is validated, and it only loads while its declared services exist.
// Register is pure declaration — no I/O, no goroutines — so a rejected plugin
// costs nothing; Start is where real work begins; Stop must undo exactly what
// Start did.
type Plugin interface {
	Manifest() Manifest
	Register(*Registry) error
	Start(context.Context, Host) error
	Stop(context.Context) error
}

// Host is the narrow surface a plugin is given. It deliberately does not expose
// the data directory, session writes, the model API key or the raw store: those
// stay behind core decisions.
type Host interface {
	Logger() *log.Logger
	// DataDir returns the plugin's own storage root, already isolated per plugin.
	// Empty means the host granted no storage.
	DataDir() string
	// Allowed reports whether the host actually granted a declared permission.
	// A plugin must not treat its manifest as authorization.
	Allowed(permission string) bool
}

// Tool is one callable capability offered to the model.
type Tool struct {
	Name        string
	Description string
	// Usage is the short guidance shown in the tool catalog, not the schema.
	Usage string
	// Order controls catalog position: lower first.
	Order      int
	Parameters json.RawMessage
	Timeout    time.Duration
	// Capability is the manifest capability this tool needs. The core exposes the
	// tool only when that capability is enabled for the current turn.
	Capability string
	Handler    Handler
}

// Handler is the plugin side of a tool call. Authorization already happened
// before it is reached, so a handler validates arguments, not permissions.
type Handler func(ctx context.Context, args json.RawMessage) (Result, error)

// Result is what a tool returns. Output is what the model reads; Data is for the
// host and never crosses the model boundary unless the handler folds it in.
type Result struct {
	Output string
	Data   json.RawMessage
}

// Service is a synchronous contract another plugin may depend on.
type Service struct {
	Name string
	Impl any
}

// Registry collects declarations during Register. It is single-use per plugin and
// is not safe for concurrent use.
type Registry struct {
	manifest Manifest
	tools    []Tool
	services map[string]any
	seen     map[string]bool
}

func newRegistry(manifest Manifest) *Registry {
	return &Registry{manifest: manifest, services: map[string]any{}, seen: map[string]bool{}}
}

// Tool records a tool declaration, checking it against the manifest so a plugin
// cannot quietly register something it never announced.
func (r *Registry) Tool(tool Tool) error {
	if r == nil {
		return errors.New("插件注册表不可用")
	}
	if strings.TrimSpace(tool.Name) == "" || tool.Handler == nil {
		return errors.New("工具必须有名称和处理函数")
	}
	if r.seen[tool.Name] {
		return fmt.Errorf("工具 %s 重复注册", tool.Name)
	}
	if !r.manifest.DeclaresTool(tool.Name) {
		return fmt.Errorf("工具 %s 没有在 manifest 的 spec.tools 中声明", tool.Name)
	}
	if tool.Capability != "" && !r.manifest.Capability(tool.Capability) {
		return fmt.Errorf("工具 %s 引用了未声明的能力 %s", tool.Name, tool.Capability)
	}
	if len(tool.Parameters) > 0 && !json.Valid(tool.Parameters) {
		return fmt.Errorf("工具 %s 的参数结构不是合法 JSON", tool.Name)
	}
	if tool.Timeout < 0 {
		return fmt.Errorf("工具 %s 的超时不能为负", tool.Name)
	}
	r.seen[tool.Name] = true
	r.tools = append(r.tools, tool)
	return nil
}

// Service publishes an implementation for other plugins to consume.
func (r *Registry) Service(service Service) error {
	if r == nil {
		return errors.New("插件注册表不可用")
	}
	if strings.TrimSpace(service.Name) == "" || service.Impl == nil {
		return errors.New("服务必须有名称和实现")
	}
	if _, exists := r.services[service.Name]; exists {
		return fmt.Errorf("服务 %s 重复注册", service.Name)
	}
	if !declares(r.manifest.Spec.Provides, service.Name) {
		return fmt.Errorf("服务 %s 没有在 manifest 的 spec.provides 中声明", service.Name)
	}
	r.services[service.Name] = service.Impl
	return nil
}

func declares(list []string, name string) bool {
	for _, item := range list {
		if item == name {
			return true
		}
	}
	return false
}

// Catalog is the immutable result of loading every plugin: the full set of
// installed capabilities. It is not the same as what any single turn may use.
type Catalog struct {
	entries     []entry
	tools       map[string]LoadedTool
	unavailable []Unavailable
	services    map[string]string
}

type entry struct {
	manifest Manifest
	plugin   Plugin
}

// Unavailable records a plugin that was installed but not loaded, and why. This
// replaces "startup failure": a missing dependency disables one plugin, not the
// process, which is how Cordis behaves (DeepSeek Harness, MIT).
type Unavailable struct {
	PluginID string
	Reason   string
}

type LoadedTool struct {
	Tool     Tool
	PluginID string
	Manifest Manifest
}

// Tools returns every installed tool in catalog order.
func (c *Catalog) Tools() []LoadedTool {
	out := make([]LoadedTool, 0, len(c.tools))
	for _, tool := range c.tools {
		out = append(out, tool)
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Tool.Order != out[j].Tool.Order {
			return out[i].Tool.Order < out[j].Tool.Order
		}
		return out[i].Tool.Name < out[j].Tool.Name
	})
	return out
}

// Plugins returns loaded plugin manifests in id order.
func (c *Catalog) Plugins() []Manifest {
	out := make([]Manifest, 0, len(c.entries))
	for _, item := range c.entries {
		out = append(out, item.manifest)
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Metadata.ID < out[j].Metadata.ID })
	return out
}

// Unavailable explains every plugin that did not load.
func (c *Catalog) Unavailable() []Unavailable {
	out := make([]Unavailable, len(c.unavailable))
	copy(out, c.unavailable)
	return out
}

// Service resolves a published service implementation.
func (c *Catalog) Service(name string) (any, bool) {
	if c == nil {
		return nil, false
	}
	for _, item := range c.entries {
		if declares(item.manifest.Spec.Provides, name) {
			return item.plugin, true
		}
	}
	return nil, false
}

// Tool looks up one installed tool.
func (c *Catalog) Tool(name string) (LoadedTool, bool) {
	tool, ok := c.tools[name]
	return tool, ok
}

// Loader assembles plugins into a catalog. Registration is static: plugins are Go
// values compiled into the binary, not shared objects. Go's plugin package cannot
// carry this safely — version skew and unload behaviour make it a poor base.
type Loader struct {
	plugins []Plugin
}

func NewLoader() *Loader { return &Loader{} }

// Add registers a plugin for the next Load.
//
// A nil interface check is not enough: a typed nil pointer stored in Plugin is not
// nil, and calling Manifest on it would panic at load time. Configurations
// routinely produce exactly that (a constructor that returns its zero value when
// the feature is unconfigured), so reject it here.
func (l *Loader) Add(plugin Plugin) {
	if plugin == nil {
		return
	}
	value := reflect.ValueOf(plugin)
	switch value.Kind() {
	case reflect.Ptr, reflect.Map, reflect.Slice, reflect.Func, reflect.Chan, reflect.Interface:
		if value.IsNil() {
			return
		}
	}
	l.plugins = append(l.plugins, plugin)
}

// Load validates every manifest, resolves service dependencies, runs Register for
// the plugins whose dependencies are satisfied, and builds the catalog.
func (l *Loader) Load() (*Catalog, error) {
	catalog := &Catalog{tools: map[string]LoadedTool{}, services: map[string]string{}}

	// First pass: validate manifests and collect the service namespace. A plugin
	// whose dependencies are unmet is recorded and skipped instead of aborting.
	manifests := make([]Manifest, 0, len(l.plugins))
	available := map[string]bool{}
	owner := map[string]string{}
	for _, plugin := range l.plugins {
		manifest := plugin.Manifest()
		if err := manifest.Validate(); err != nil {
			return nil, fmt.Errorf("插件 %s 的 manifest 无效：%w", manifest.Metadata.ID, err)
		}
		if _, clash := owner[manifest.Metadata.ID]; clash {
			return nil, fmt.Errorf("插件 %s 重复加载", manifest.Metadata.ID)
		}
		owner[manifest.Metadata.ID] = manifest.Metadata.ID
		for _, service := range manifest.Spec.Provides {
			if existing, taken := catalog.services[service]; taken && existing != manifest.Metadata.ID {
				return nil, fmt.Errorf("服务 %s 同时由 %s 和 %s 提供", service, existing, manifest.Metadata.ID)
			}
			catalog.services[service] = manifest.Metadata.ID
		}
		manifests = append(manifests, manifest)
	}

	provides := map[string]bool{}
	for _, manifest := range manifests {
		for _, service := range manifest.Spec.Provides {
			provides[service] = true
		}
	}
	for _, manifest := range manifests {
		missing := make([]string, 0, len(manifest.Spec.Dependencies))
		for _, service := range manifest.Spec.Dependencies {
			if !provides[service] {
				missing = append(missing, service)
			}
		}
		if len(missing) > 0 {
			catalog.unavailable = append(catalog.unavailable, Unavailable{
				PluginID: manifest.Metadata.ID,
				Reason:   "缺少依赖服务：" + strings.Join(missing, ", "),
			})
			continue
		}
		available[manifest.Metadata.ID] = true
	}

	// Second pass: register only the loadable plugins.
	for i, plugin := range l.plugins {
		manifest := manifests[i]
		if !available[manifest.Metadata.ID] {
			continue
		}
		registry := newRegistry(manifest)
		if err := plugin.Register(registry); err != nil {
			return nil, fmt.Errorf("插件 %s 注册失败：%w", manifest.Metadata.ID, err)
		}
		for _, tool := range registry.tools {
			if existing, clash := catalog.tools[tool.Name]; clash {
				return nil, fmt.Errorf("工具 %s 同时由 %s 和 %s 注册", tool.Name, existing.PluginID, manifest.Metadata.ID)
			}
			catalog.tools[tool.Name] = LoadedTool{Tool: tool, PluginID: manifest.Metadata.ID, Manifest: manifest}
		}
		catalog.entries = append(catalog.entries, entry{manifest: manifest, plugin: plugin})
	}
	return catalog, nil
}

// StartAll starts every loaded plugin, stopping the ones already started if any
// later plugin fails, so a partial load never leaves the core half-wired.
func (c *Catalog) StartAll(ctx context.Context, hostFor func(Manifest) Host) error {
	started := make([]Plugin, 0, len(c.entries))
	for _, item := range c.entries {
		if err := item.plugin.Start(ctx, hostFor(item.manifest)); err != nil {
			for i := len(started) - 1; i >= 0; i-- {
				_ = started[i].Stop(context.Background())
			}
			return fmt.Errorf("插件 %s 启动失败：%w", item.manifest.Metadata.ID, err)
		}
		started = append(started, item.plugin)
	}
	return nil
}

// StopAll stops plugins in reverse load order.
func (c *Catalog) StopAll(ctx context.Context) {
	for i := len(c.entries) - 1; i >= 0; i-- {
		if err := c.entries[i].plugin.Stop(ctx); err != nil {
			log.Printf("plugin %s stop: %v", c.entries[i].manifest.Metadata.ID, err)
		}
	}
}

// Enablement decides which capabilities a single turn may see. Installing a
// plugin never implies exposing its tools: the core asks this per turn.
type Enablement interface {
	Enabled(capability string) bool
}

// EnablementFunc adapts a function to Enablement.
type EnablementFunc func(string) bool

func (f EnablementFunc) Enabled(capability string) bool { return f(capability) }

// AllowAll exposes every installed capability.
func AllowAll() Enablement { return EnablementFunc(func(string) bool { return true }) }

// Select exposes the listed capabilities plus anything without a capability tag.
func Select(capabilities ...string) Enablement {
	allowed := map[string]bool{}
	for _, capability := range capabilities {
		allowed[capability] = true
	}
	return EnablementFunc(func(capability string) bool {
		return capability == "" || allowed[capability]
	})
}

// Admission is the tool set for one turn.
type Admission struct {
	Tools []LoadedTool
}

// Admit filters the catalog by enablement. Plugins that are installed but not
// enabled simply do not appear, which is the point: presence is not exposure.
func (c *Catalog) Admit(enable Enablement) Admission {
	if enable == nil {
		enable = AllowAll()
	}
	out := make([]LoadedTool, 0, len(c.tools))
	for _, tool := range c.Tools() {
		if enable.Enabled(tool.Tool.Capability) {
			out = append(out, tool)
		}
	}
	return Admission{Tools: out}
}
