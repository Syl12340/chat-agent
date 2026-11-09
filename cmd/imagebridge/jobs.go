package main

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"time"

	"chat-agent/internal/imagegen"
)

func (s *service) submitJob(w http.ResponseWriter, r *http.Request) {
	var input generateRequest
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&input) != nil || decoder.Decode(new(any)) != io.EOF {
		writeJSON(w, 400, map[string]string{"error": "请求格式不正确"})
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 45*time.Second)
	defer cancel()
	id, err := s.client.SubmitJob(ctx, input.request())
	if err != nil {
		writeJSON(w, 502, map[string]string{"error": "图像作业未返回提交回执"})
		return
	}
	writeJSON(w, 202, map[string]string{"job_id": id})
}

func (s *service) waitJob(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Minute)
	defer cancel()
	result, err := s.client.WaitJob(ctx, r.PathValue("job"))
	if err != nil {
		if errors.Is(err, imagegen.ErrJobCanceled) {
			writeJSON(w, http.StatusGone, map[string]string{"error": "图像渲染器确认作业中断"})
			return
		}
		writeJSON(w, 502, map[string]string{"error": "图像作业结果不可读取"})
		return
	}
	images := make([]map[string]string, 0, len(result.Images))
	for _, image := range result.Images {
		images = append(images, map[string]string{"mime": image.MIME, "base64": base64.StdEncoding.EncodeToString(image.Data)})
	}
	writeJSON(w, 200, map[string]any{"job_id": result.JobID, "profile": result.Profile, "seed": result.Seed, "images": images})
}

func (s *service) cancelJob(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 8*time.Second)
	defer cancel()
	id := r.PathValue("job")
	if err := s.client.CancelJob(ctx, id); err != nil {
		writeJSON(w, 502, map[string]string{"error": "渲染器未确认收到取消请求"})
		return
	}
	writeJSON(w, 202, map[string]any{"job_id": id, "cancel_requested": true})
}
