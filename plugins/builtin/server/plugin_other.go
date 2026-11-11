//go:build !linux

package server

import "chat-agent/internal/pluginapi"

const ID = "builtin.server"
const Permission = "filesystem:diagnostics://host"
const MaintenancePermission = "filesystem:maintenance://host"

// New returns nil on hosts without the Linux service tooling: the catalog treats
// a nil plugin as absent rather than starting one that can only fail.
func New(_ []string) pluginapi.Plugin { return nil }

func NewWithActions(_ []string, _ string) (pluginapi.Plugin, error) { return nil, nil }
