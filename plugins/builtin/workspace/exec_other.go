//go:build !linux

package workspace

import (
	"context"
	"os"
)

func canExecute() bool { return false }

func openText(root *os.Root, path string) (*os.File, error) { return root.Open(path) }

func runAction(context.Context, string, []string) (string, string) {
	return "", "SANDBOX_UNAVAILABLE"
}
