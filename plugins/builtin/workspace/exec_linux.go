//go:build linux

package workspace

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"chat-agent/internal/sandbox"
)

// canExecute reports whether a confused-deputy run is even possible here. The
// service account is what makes process-level restrictions meaningful.
func canExecute() bool { return os.Geteuid() != 0 }

// sandboxReady reports whether real confinement is available.
func sandboxReady() bool { return sandbox.Probe().ABI >= 1 }

// readOnlyPaths are the system directories a confined command may read. Writes
// are granted only for the workspace, so a command that needs to change
// something outside it fails instead of succeeding quietly.
var readOnlyPaths = []string{"/usr", "/bin", "/sbin", "/lib", "/lib64", "/etc", "/proc", "/sys"}

func openText(root *os.Root, path string) (*os.File, error) {
	return root.OpenFile(path, os.O_RDONLY|syscall.O_NONBLOCK|syscall.O_NOFOLLOW, 0)
}

type cappedOutput struct{ limit int; buf []byte }

func (b *cappedOutput) Write(p []byte) (int, error) {
	room := b.limit - len(b.buf)
	if room > 0 {
		b.buf = append(b.buf, p[:min(len(p), room)]...)
	}
	return len(p), nil
}

func (b *cappedOutput) String() string { return string(b.buf) }

func runAction(ctx context.Context, dir string, argv []string) (string, string) {
	if !canExecute() {
		return "", "SANDBOX_UNAVAILABLE"
	}
	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
	cmd.Dir = dir
	cmd.Env = []string{"PATH=/usr/bin:/bin", "LANG=C.UTF-8", "HOME=/nonexistent"}
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true, Pdeathsig: syscall.SIGKILL}
	cmd.Cancel = func() error {
		err := syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		if errors.Is(err, syscall.ESRCH) {
			return os.ErrProcessDone
		}
		return err
	}
	cmd.WaitDelay = time.Second
	var output cappedOutput
	output.limit = limit
	cmd.Stdout, cmd.Stderr = &output, &output
	err := cmd.Run()
	status := "succeeded"
	if ctx.Err() != nil {
		status = "canceled"
	} else if err != nil {
		status = "failed"
	}
	return strings.ToValidUTF8(output.String(), "\uFFFD"), status
}

// runShell runs one command line inside the Landlock sandbox.
//
// The command is handed to /bin/sh on purpose: a terminal is what makes the
// capability useful, and the boundary is the filesystem policy, not the
// characters in the string. If confinement is unavailable the command does not
// run at all -- an unconfined shell would be a different product.
func runShell(ctx context.Context, root, dir, command string) (string, string, error) {
	status := sandbox.Probe()
	if status.ABI < 1 {
		return "", "SANDBOX_UNAVAILABLE", nil
	}
	if !canExecute() {
		return "", "SANDBOX_UNAVAILABLE", nil
	}
	self, err := os.Executable()
	if err != nil {
		return "", "SANDBOX_UNAVAILABLE", err
	}
	// A private temp directory inside the workspace keeps two sessions from
	// sharing scratch files and keeps /tmp out of the writable set.
	tmp := filepath.Join(root, ".tmp")
	if err := os.MkdirAll(tmp, 0o700); err != nil {
		return "", "SANDBOX_UNAVAILABLE", err
	}
	argv := []string{self, sandbox.Marker}
	for _, path := range readOnlyPaths {
		argv = append(argv, "--ro", path)
	}
	// /dev/null is a write sink that every shell expects; writing existing device
	// files has to work even though nothing under /dev may be created.
	argv = append(argv, "--rwfile", "/dev")
	argv = append(argv, "--rw", root, "--no-tcp", "--", "/bin/sh", "-c", command)

	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
	cmd.Dir = dir
	cmd.Env = []string{"PATH=/usr/bin:/bin:/usr/local/bin", "LANG=C.UTF-8", "HOME=/nonexistent", "TMPDIR=" + tmp}
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true, Pdeathsig: syscall.SIGKILL}
	cmd.Cancel = func() error {
		err := syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		if errors.Is(err, syscall.ESRCH) {
			return os.ErrProcessDone
		}
		return err
	}
	cmd.WaitDelay = time.Second
	var output cappedOutput
	output.limit = limit
	cmd.Stdout, cmd.Stderr = &output, &output
	runErr := cmd.Run()

	result := "succeeded"
	if ctx.Err() != nil {
		result = "canceled"
	} else if runErr != nil {
		result = "failed"
	}
	// A confinement failure must be named as such rather than reported as a
	// command that happened to fail.
	var exitErr *exec.ExitError
	if errors.As(runErr, &exitErr) && exitErr.ExitCode() == 70 {
		message := strings.ToValidUTF8(output.String(), "\uFFFD")
		if message != "" {
			return message, "SANDBOX_UNAVAILABLE", nil
		}
		return "", "SANDBOX_UNAVAILABLE", nil
	}
	return strings.ToValidUTF8(output.String(), "\uFFFD"), result, nil
}

// sandboxStatus describes the enforcement available to this plugin.
func sandboxStatus() string { return sandbox.Describe() }

var _ = time.Second
