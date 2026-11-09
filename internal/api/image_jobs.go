package api

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"image"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"chat-agent/internal/imagegen"
	"chat-agent/internal/probe"
	"chat-agent/internal/storage"
)

// Job ownership and operation receipts follow DSH tool-jobs (MIT). Submission
// and lookup are separate; completion is delivered to the UI without a new turn.
type imageJob struct {
	ID              string `json:"job_id"`
	Status          string `json:"status"`
	Profile         string `json:"profile,omitempty"`
	RendererID      string `json:"renderer_id,omitempty"`
	Seed            int64  `json:"seed,omitempty"`
	Width           int    `json:"width,omitempty"`
	Height          int    `json:"height,omitempty"`
	Hash            string `json:"sha256,omitempty"`
	ImageID         string `json:"image_id,omitempty"`
	CancelForwarded bool   `json:"cancel_forwarded,omitempty"`
	Detail          string `json:"detail,omitempty"`
}

type imageJobRecord struct {
	imageJob
	RequestDigest string `json:"request_sha256"`
}

type imageWork struct{ session, id string }

func imageJobID(session, request string) string {
	sum := sha256.Sum256([]byte(session + "\x00" + request))
	return hex.EncodeToString(sum[:16])
}

func (s *Server) imageJobPath(session, id string) string {
	return filepath.Join(s.runtime.Root(), session, "image-jobs", id+".json")
}

func (s *Server) readImageJob(session, id string) (imageJobRecord, error) {
	if s.runtime == nil || !storage.ValidID(session) || len(id) != 32 {
		return imageJobRecord{}, os.ErrNotExist
	}
	if _, err := hex.DecodeString(id); err != nil {
		return imageJobRecord{}, os.ErrNotExist
	}
	f, err := os.Open(s.imageJobPath(session, id))
	if err != nil {
		return imageJobRecord{}, err
	}
	defer f.Close()
	var record imageJobRecord
	if err := json.NewDecoder(io.LimitReader(f, 4096)).Decode(&record); err != nil {
		return record, err
	}
	if record.ID != id || len(record.RequestDigest) != 64 {
		return record, errors.New("invalid image receipt")
	}
	if record.Status == "running" || record.Status == "cancel_requested" {
		if s.activeImageJob != (imageWork{session: session, id: id}) {
			record.Status = "unknown"
		}
	}
	return record, nil
}

func (s *Server) saveImageJob(session string, job imageJobRecord) error {
	path := s.imageJobPath(session, job.ID)
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	raw, err := json.Marshal(job)
	if err != nil {
		return err
	}
	return storage.Replace(path, raw)
}

func (s *Server) submitImage(ctx context.Context, role, session, request string, req imagegen.Request) (imageJob, error) {
	if ctx.Err() != nil {
		return imageJob{}, ctx.Err()
	}
	if !storage.ValidID(session) || !storage.ValidID(request) || probe.ToolOperationID(ctx) == "" {
		return imageJob{}, errors.New("图像作业需要已记录的聊天请求")
	}
	body, err := json.Marshal(req)
	if err != nil {
		return imageJob{}, err
	}
	sum := sha256.Sum256(body)
	digest := hex.EncodeToString(sum[:])
	id := imageJobID(session, request)
	s.mediaMu.Lock()
	defer s.mediaMu.Unlock()
	if previous, err := s.readImageJob(session, id); err == nil {
		if previous.RequestDigest != digest {
			return imageJob{}, errors.New("本轮已有图像作业，请查询原作业")
		}
		return previous.imageJob, nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return imageJob{}, errors.New("无法核对已有图像作业")
	}
	if s.activeImageJob.id != "" {
		return imageJob{}, errors.New("图像服务正在处理另一项作业")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closing || ctx.Err() != nil {
		return imageJob{}, errors.New("图像作业未受理")
	}
	record := imageJobRecord{imageJob: imageJob{ID: id, Status: "running", Profile: req.Profile}, RequestDigest: digest}
	if err := s.saveImageJob(session, record); err != nil {
		return imageJob{}, errors.New("无法保存图像作业")
	}
	s.activeImageJob = imageWork{session: session, id: id}
	s.workWG.Add(1)
	go s.runImageJob(role, session, record, req)
	return record.imageJob, nil
}

func (s *Server) runImageJob(role, session string, record imageJobRecord, req imagegen.Request) {
	defer s.workWG.Done()
	ctx, cancel := context.WithTimeout(s.workCtx, 5*time.Minute)
	defer cancel()
	result, err := s.renderImageJob(ctx, session, record.ID, req)
	s.mediaMu.Lock()
	defer s.mediaMu.Unlock()
	defer func() { s.activeImageJob = imageWork{} }()
	current, readErr := s.readImageJob(session, record.ID)
	if readErr != nil {
		log.Print("image job receipt unavailable during settlement")
		return
	}
	record = current
	switch {
	case errors.Is(err, imagegen.ErrJobAPIUnavailable):
		record.Status, record.Detail = "failed", "请升级本地图像桥到支持作业接口的版本。"
	case errors.Is(err, imagegen.ErrJobCanceled):
		record.Status, record.Detail = "canceled", "渲染器确认作业已中断。"
	case err != nil:
		// The synchronous renderer might have accepted the request before a
		// connection loss. Keep the identity and avoid any replay.
		record.Status = "unknown"
	case result == nil || len(result.Images) == 0:
		record.Status = "failed"
	default:
		record.RendererID, record.Profile, record.Seed = result.JobID, result.Profile, result.Seed
		first := result.Images[0]
		hash := sha256.Sum256(first.Data)
		record.Hash = hex.EncodeToString(hash[:])
		if shape, _, err := image.DecodeConfig(bytes.NewReader(first.Data)); err == nil {
			record.Width, record.Height = shape.Width, shape.Height
		}
		if record.Status == "cancel_requested" {
			record.Status = "discarded"
		} else {
			item, err := s.images.Upload(role, session, bytes.NewReader(first.Data))
			if err != nil {
				record.Status = "failed"
			} else {
				record.Status, record.ImageID = "completed", item.ID
			}
		}
	}
	if err := s.saveImageJob(session, record); err != nil {
		if record.ImageID != "" {
			_ = s.images.Delete(role, session, record.ImageID)
		}
		log.Print("image job receipt could not be settled")
	}
}

func (s *Server) renderImageJob(ctx context.Context, session, id string, req imagegen.Request) (*imagegen.Result, error) {
	backend, ok := s.imagegen.(imagegen.JobBackend)
	if !ok {
		return s.imagegen.Generate(ctx, req)
	}
	rendererID, err := backend.SubmitJob(ctx, req)
	if err != nil {
		return nil, err
	}
	s.mediaMu.Lock()
	record, err := s.readImageJob(session, id)
	if err == nil {
		record.RendererID = rendererID
		err = s.saveImageJob(session, record)
	}
	s.mediaMu.Unlock()
	if err != nil {
		return nil, err
	}
	if record.Status == "cancel_requested" {
		s.forwardImageCancel(ctx, session, record.imageJob)
	}
	return backend.WaitJob(ctx, rendererID)
}

func (s *Server) forwardImageCancel(ctx context.Context, session string, job imageJob) {
	backend, ok := s.imagegen.(imagegen.JobBackend)
	if !ok || job.RendererID == "" {
		return
	}
	ctx, cancel := context.WithTimeout(ctx, 9*time.Second)
	defer cancel()
	err := backend.CancelJob(ctx, job.RendererID)
	s.mediaMu.Lock()
	defer s.mediaMu.Unlock()
	record, readErr := s.readImageJob(session, job.ID)
	if readErr != nil || record.Status != "cancel_requested" {
		return
	}
	if err == nil {
		record.CancelForwarded = true
		record.Detail = "渲染器已收到针对该作业的取消请求。"
	} else {
		record.Detail = "未确认渲染器收到取消请求。生成结果将不交付。"
	}
	if err := s.saveImageJob(session, record); err != nil {
		log.Print("image cancellation acknowledgment could not be saved")
	}
}

func (s *Server) lookupImageJob(ctx context.Context, session, id string, cancel bool) (imageJob, error) {
	s.mediaMu.Lock()
	record, err := s.readImageJob(session, id)
	forward := err == nil && cancel && record.Status == "running"
	if forward {
		record.Status = "cancel_requested"
		err = s.saveImageJob(session, record)
	}
	s.mediaMu.Unlock()
	if err != nil {
		return imageJob{}, err
	}
	if forward && record.RendererID != "" {
		s.forwardImageCancel(ctx, session, record.imageJob)
		return s.lookupImageJob(ctx, session, id, false)
	}
	return record.imageJob, nil
}

func (s *Server) imageJobHTTP(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.get(w, r); !ok {
		return
	}
	job, err := s.lookupImageJob(r.Context(), r.PathValue("id"), r.PathValue("job"), r.Method == http.MethodPost)
	if errors.Is(err, os.ErrNotExist) {
		fail(w, 404, "此会话没有该图像作业")
		return
	}
	if err != nil {
		fail(w, 503, "图像作业暂时不可读取")
		return
	}
	writeJSON(w, 200, job)
}

func imageJobResult(job imageJob) (probe.ToolResult, error) {
	data, err := json.Marshal(job)
	return probe.ToolResult{Output: string(data)}, err
}

func (s *Server) imageControlTools(session string) []probe.Tool {
	tools := []probe.Tool{}
	for i, operation := range []struct {
		name, description string
		cancel            bool
	}{
		{"image_status", "Get a previously submitted image job and its result in this conversation.", false},
		{"image_cancel", "Request cancellation of image delivery. The renderer may still finish; only call when the user asks to cancel.", true},
	} {
		tools = append(tools, probe.Tool{
			Name: operation.name, Description: operation.description, Order: 661 + i,
			Parameters: json.RawMessage(`{"type":"object","properties":{"job_id":{"type":"string","minLength":32,"maxLength":32}},"required":["job_id"],"additionalProperties":false}`),
			Execute: func(ctx context.Context, raw json.RawMessage) (probe.ToolResult, error) {
				if ctx.Err() != nil {
					return probe.ToolResult{}, ctx.Err()
				}
				var args struct {
					ID string `json:"job_id"`
				}
				if err := toolInput(raw, &args); err != nil {
					return probe.ToolResult{}, err
				}
				job, err := s.lookupImageJob(ctx, session, args.ID, operation.cancel)
				if err != nil {
					return probe.ToolResult{}, errors.New("无法查询此会话的图像作业")
				}
				return imageJobResult(job)
			},
		})
	}
	return tools
}
