package pluginapi

import (
	"fmt"
	"sort"
	"strings"
)

// Policy is the host's actual decision about installed plugins: which may load,
// and which of their declared permissions are really granted.
//
// A manifest is a request; this is the answer. Nothing under Spec.Permissions
// opens anything by itself — a plugin either finds the exact permission granted
// here or it does not get the capability at all. The zero value enables every
// plugin and grants nothing, which is the safe default for a build that has not
// been configured yet.
type Policy struct {
	disabled map[string]bool
	grants   map[string]map[string]bool
}

// NewPolicy parses host configuration.
//
// disabled lists plugin ids to keep switched off. grants lists
// "plugin-id:permission" entries, where permission is the exact manifest form
// such as "filesystem:memory://role" or "network:public-https". Only the first
// colon separates the id, so a permission containing further colons survives
// intact.
func NewPolicy(disabled, grants []string) (Policy, error) {
	policy := Policy{disabled: map[string]bool{}, grants: map[string]map[string]bool{}}
	for _, raw := range disabled {
		id := strings.TrimSpace(raw)
		if id == "" {
			continue
		}
		if !validPluginID(id) {
			return Policy{}, fmt.Errorf("插件停用名单里有非法 id：%q", id)
		}
		policy.disabled[id] = true
	}
	for _, raw := range grants {
		value := strings.TrimSpace(raw)
		if value == "" {
			continue
		}
		id, permission, ok := strings.Cut(value, ":")
		if !ok || strings.TrimSpace(id) == "" || strings.TrimSpace(permission) == "" {
			return Policy{}, fmt.Errorf("插件授权条目格式应为 plugin-id:permission，收到 %q", value)
		}
		if !validPluginID(id) {
			return Policy{}, fmt.Errorf("插件授权里有非法 id：%q", id)
		}
		if policy.grants[id] == nil {
			policy.grants[id] = map[string]bool{}
		}
		policy.grants[id][permission] = true
	}
	return policy, nil
}

// validPluginID mirrors the manifest rule: dot-separated lowercase words.
func validPluginID(id string) bool {
	segments := strings.Split(id, ".")
	if len(segments) < 2 {
		return false
	}
	for _, segment := range segments {
		if segment == "" {
			return false
		}
		for _, r := range segment {
			if (r < 'a' || r > 'z') && (r < '0' || r > '9') && r != '-' && r != '_' {
				return false
			}
		}
	}
	return true
}

// PluginEnabled reports whether the host lets this plugin load at all.
func (p Policy) PluginEnabled(id string) bool { return !p.disabled[id] }

// Grant reports whether the host really granted one declared permission.
func (p Policy) Grant(pluginID, permission string) bool { return p.grants[pluginID][permission] }

// Enablement is a turn's capability set: only capabilities belonging to plugins
// the host left enabled. Disabled or unloaded plugins contribute nothing, so the
// model is never offered a capability the host did not stand behind.
func (p Policy) Enablement(catalog *Catalog) Enablement {
	allowed := map[string]bool{}
	if catalog != nil {
		for _, manifest := range catalog.Plugins() {
			if !p.PluginEnabled(manifest.Metadata.ID) {
				continue
			}
			for _, capability := range manifest.Spec.Capabilities {
				allowed[capability] = true
			}
		}
	}
	return EnablementFunc(func(capability string) bool { return capability == "" || allowed[capability] })
}

// DeclaredPermissions lists everything a manifest asks for, sorted so the page and
// the status endpoint show a stable order.
func DeclaredPermissions(m Manifest) []string {
	var out []string
	for _, namespace := range m.Spec.Permissions.Filesystem {
		out = append(out, "filesystem:"+namespace)
	}
	for _, host := range m.Spec.Permissions.Network {
		out = append(out, "network:"+host)
	}
	for _, role := range m.Spec.Permissions.Model {
		out = append(out, "model:"+role)
	}
	if m.Spec.Permissions.Sessions != "" {
		out = append(out, "sessions:"+m.Spec.Permissions.Sessions)
	}
	sort.Strings(out)
	return out
}
