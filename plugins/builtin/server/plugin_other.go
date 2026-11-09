//go:build !linux

package server

import "chat-agent/internal/pluginapi"

const ID = "builtin.server"
const Permission = "filesystem:diagnostics://host"

func New() pluginapi.Plugin { return nil }
