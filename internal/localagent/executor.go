package localagent

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"chat-agent/internal/storage"
)

type Executor struct {
	Guard   *Guard
	Actions map[string][]string
	Store   *ReceiptStore
	Timeout time.Duration
}

func (e *Executor) Execute(parent context.Context, task Task) Receipt {
	started := time.Now().UTC()
	receipt := Receipt{Protocol: Protocol, OperationID: task.OperationID, StartedAt: started,
		Guard: map[string]any{"kind": "workspace-guard", "workspace": e.Guard.Path(), "os_sandbox": false, "shell": false, "privilege_escalation": false}}
	raw, _ := json.Marshal(task)
	digest := sha256.Sum256(raw)
	receipt.RequestSHA256 = hex.EncodeToString(digest[:])
	if prior, ok := e.Store.Get(task.OperationID); ok {
		if prior.RequestSHA256 == receipt.RequestSHA256 {
			return prior
		}
		receipt.Status, receipt.Error, receipt.CompletedAt = "rejected", "operation identity was already used for a different task", time.Now().UTC()
		return receipt
	}
	if task.Protocol != Protocol || !storage.ValidID(task.OperationID) || !strings.HasPrefix(task.OperationID, "op_") {
		receipt.Status, receipt.Error, receipt.CompletedAt = "rejected", "invalid task protocol or operation identity", time.Now().UTC()
		return receipt
	}
	ctx, cancel := context.WithTimeout(parent, e.timeout())
	defer cancel()
	var err error
	switch task.Kind {
	case "list":
		receipt.Entries, err = e.Guard.List(task.Path)
	case "read":
		receipt.Output, err = e.Guard.Read(task.Path)
	case "search":
		receipt.Entries, err = e.Guard.Search(task.Query)
	case "exec":
		receipt.Output, err = e.runAction(ctx, task.Action, task.OperationID)
	default:
		// Name the accepted kinds: a caller that guessed "action" for the fixed
		// allow-list has no way to discover "exec" from a bare refusal.
		err = fmt.Errorf("unsupported local agent task kind %q; expected one of %s", task.Kind, strings.Join(Kinds, ", "))
	}
	if err == nil && len(task.Artifacts) > 16 {
		err = errors.New("a task may publish at most 16 artifacts")
	}
	if err == nil {
		for _, path := range task.Artifacts {
			artifact, artifactErr := e.Guard.Artifact(path)
			if artifactErr != nil {
				err = artifactErr
				break
			}
			receipt.Artifacts = append(receipt.Artifacts, artifact)
		}
	}
	receipt.CompletedAt = time.Now().UTC()
	if err != nil {
		receipt.Status, receipt.Error = "failed", err.Error()
	} else {
		receipt.Status = "completed"
	}
	_ = e.Store.Put(receipt)
	return receipt
}

func (e *Executor) timeout() time.Duration {
	if e.Timeout <= 0 || e.Timeout > 5*time.Minute {
		return 2 * time.Minute
	}
	return e.Timeout
}

func (e *Executor) runAction(ctx context.Context, name, operationID string) (string, error) {
	argv := e.Actions[name]
	if len(argv) == 0 || !filepath.IsAbs(argv[0]) {
		return "", errors.New("action is not in the local allow-list")
	}
	// The operation id travels in the environment (CHAT_OPERATION_ID), not on the
	// command line. Appending it as an argument made every ordinary program reject
	// the call: `go version --operation-id op_x` fails with an unknown flag, so no
	// allow-listed action could run at all.
	args := append([]string(nil), argv[1:]...)
	cmd := exec.CommandContext(ctx, argv[0], args...)
	cmd.Dir = e.Guard.Path()
	cmd.Env = processEnvironment(operationID)
	cmd.WaitDelay = time.Second
	var stdout, stderr cappedBuffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		detail := strings.TrimSpace(stderr.String())
		if detail == "" {
			detail = err.Error()
		}
		return stdout.String(), errors.New(detail)
	}
	return stdout.String(), nil
}

func processEnvironment(operationID string) []string {
	env := []string{"CHAT_LOCAL_AGENT=1", "CHAT_OPERATION_ID=" + operationID}
	if runtime.GOOS == "windows" {
		for _, key := range []string{"SystemRoot", "WINDIR", "TEMP", "TMP", "PATH"} {
			if value := os.Getenv(key); value != "" {
				env = append(env, key+"="+value)
			}
		}
		return env
	}
	return append(env, "PATH=/usr/bin:/bin", "LANG=C")
}

type cappedBuffer struct{ bytes.Buffer }

func (b *cappedBuffer) Write(p []byte) (int, error) {
	const limit = 256 << 10
	written := len(p)
	if b.Len() < limit {
		remaining := limit - b.Len()
		if len(p) > remaining {
			p = p[:remaining]
		}
		_, _ = b.Buffer.Write(p)
	}
	return written, nil
}
