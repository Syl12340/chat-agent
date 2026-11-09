//go:build !linux

package server

import "pocket-agent/internal/pluginapi"

const ID = "builtin.server"
const Permission = "filesystem:diagnostics://host"

func New() pluginapi.Plugin { return nil }
