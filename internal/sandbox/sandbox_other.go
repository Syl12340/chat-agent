//go:build !linux

package sandbox

import "errors"

// Probe reports that confinement is unavailable on this platform. The caller
// must refuse to run rather than proceed unconfined.
func Probe() Status {
	return Status{Reason: "这个平台没有可用的进程沙箱"}
}

// Restrict always fails here: running a command without confinement is worse
// than not running it.
func Restrict(Policy) error {
	return errors.New("这个平台没有可用的进程沙箱")
}

// Run never executes anything.
func Run(Policy, []string) error {
	return errors.New("这个平台没有可用的进程沙箱")
}

// ParseArgs keeps the internal form parseable so a mistaken invocation reports
// a sandbox failure instead of an argument error.
func ParseArgs(args []string) (Policy, []string, error) {
	return Policy{}, args, nil
}

// Describe renders the status.
func Describe() string { return "不可用：这个平台没有可用的进程沙箱" }
