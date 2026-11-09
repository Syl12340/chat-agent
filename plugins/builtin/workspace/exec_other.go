//go:build !linux

package workspace

import (
	"context"
	"os"
)

func canExecute() bool { return false }

// sandboxReady is false here: there is no confinement to offer, and offering a
// shell without one would be a different capability than the one promised.
func sandboxReady() bool { return false }

func openText(root *os.Root, path string) (*os.File, error) { return root.Open(path) }

func runAction(context.Context, string, []string) (string, string) {
	return "", "SANDBOX_UNAVAILABLE"
}

func runShell(context.Context, string, string, string, string) (string, string, error) {
	return "", "SANDBOX_UNAVAILABLE", nil
}

func sandboxStatus() string { return "不可用：这个平台没有可用的进程沙箱" }
