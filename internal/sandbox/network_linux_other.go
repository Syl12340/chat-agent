//go:build linux && !amd64

package sandbox

import "errors"

func denySockets() error { return errors.New("network sandbox is unavailable on this architecture") }
