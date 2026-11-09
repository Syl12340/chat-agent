package workspace

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"strings"
	"syscall"
	"time"
)

func canExecute() bool { return os.Geteuid() != 0 }

func openText(root *os.Root, path string) (*os.File, error) {
	return root.OpenFile(path, os.O_RDONLY|syscall.O_NONBLOCK|syscall.O_NOFOLLOW, 0)
}

type cappedOutput struct{ bytes.Buffer }

func (b *cappedOutput) Write(p []byte) (int, error) {
	n := min(len(p), limit-b.Len())
	_, _ = b.Buffer.Write(p[:n])
	return len(p), nil
}

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
	cmd.Stdout, cmd.Stderr = &output, &output
	err := cmd.Run()
	status := "succeeded"
	if ctx.Err() != nil {
		status = "canceled"
	} else if err != nil {
		status = "failed"
	}
	return strings.ToValidUTF8(output.String(), "�"), status
}
