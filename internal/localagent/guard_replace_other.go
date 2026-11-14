//go:build !windows

package localagent

import "os"

func replaceRoot(root *os.Root, _ string, from, to string) error { return root.Rename(from, to) }
