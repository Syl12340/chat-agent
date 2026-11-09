package imagegen

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"

	"chat-agent/internal/storage"
)

// JobBackend exposes the renderer's own job identifier for targeted cancellation.
type JobBackend interface {
	SubmitJob(context.Context, Request) (string, error)
	WaitJob(context.Context, string) (*Result, error)
	CancelJob(context.Context, string) error
}

var ErrJobAPIUnavailable = errors.New("图像桥需要支持 /jobs 的版本")
var ErrJobCanceled = errors.New("图像渲染器确认作业中断")

func ValidRendererJobID(id string) bool {
	return storage.ValidID(id) && !strings.HasPrefix(id, "-")
}

func (r *Remote) SubmitJob(ctx context.Context, req Request) (string, error) {
	payload := map[string]any{"profile": req.Profile, "prompt": req.Prompt, "negative": req.Negative, "width": req.Width, "height": req.Height, "steps": req.Steps, "seed": req.Seed, "cfg": req.CFG, "min_free_ram_gb": req.MinFreeRAMGB}
	var reply remoteGeneration
	if err := r.jobRequest(ctx, http.MethodPost, "/jobs", payload, &reply); err != nil {
		return "", err
	}
	if !ValidRendererJobID(reply.JobID) {
		return "", errors.New("图像服务没有返回有效作业号")
	}
	return reply.JobID, nil
}

func (r *Remote) WaitJob(ctx context.Context, id string) (*Result, error) {
	if !ValidRendererJobID(id) {
		return nil, errors.New("invalid renderer job id")
	}
	var reply remoteGeneration
	if err := r.jobRequest(ctx, http.MethodGet, "/jobs/"+id+"/result", nil, &reply); err != nil {
		return nil, err
	}
	if reply.JobID != id {
		return nil, errors.New("图像作业结果不匹配")
	}
	return decodeRemoteImages(reply)
}

func (r *Remote) CancelJob(ctx context.Context, id string) error {
	if !ValidRendererJobID(id) {
		return errors.New("invalid renderer job id")
	}
	var reply struct {
		ID        string `json:"job_id"`
		Requested bool   `json:"cancel_requested"`
	}
	if err := r.jobRequest(ctx, http.MethodPost, "/jobs/"+id+"/cancel", nil, &reply); err != nil {
		return err
	}
	if reply.ID != id || !reply.Requested {
		return errors.New("图像桥未确认收到取消请求")
	}
	return nil
}

func (r *Remote) jobRequest(ctx context.Context, method, path string, payload, result any) error {
	var body io.Reader
	if payload != nil {
		raw, err := json.Marshal(payload)
		if err != nil {
			return err
		}
		body = bytes.NewReader(raw)
	}
	request, err := http.NewRequestWithContext(ctx, method, r.baseURL+path, body)
	if err != nil {
		return err
	}
	request.Header.Set("Content-Type", "application/json")
	r.authorize(request)
	response, err := r.client.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if path == "/jobs" && response.StatusCode == http.StatusNotFound {
		return ErrJobAPIUnavailable
	}
	if method == http.MethodGet && response.StatusCode == http.StatusGone {
		return ErrJobCanceled
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return ErrUnavailable
	}
	return json.NewDecoder(io.LimitReader(response.Body, 32<<20)).Decode(result)
}
