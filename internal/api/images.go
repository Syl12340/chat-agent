package api

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"net/http"
	"strconv"

	"pocket-agent/internal/media"
	"pocket-agent/internal/probe"
)

func imageFailure(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, media.ErrLimit):
		fail(w, 413, "图片过大或会话图片存储已满，请压缩图片或删除不再需要的会话")
	case errors.Is(err, media.ErrNotFound), errors.Is(err, media.ErrScope):
		fail(w, 404, "此会话的图片不存在")
	case errors.Is(err, media.ErrInvalid):
		fail(w, 400, "请选择有效的 JPEG 或 PNG 图片，单张不超过4 MiB、4096边长与1200万像素")
	default:
		fail(w, 503, "图片存储暂时不可用，请保留草稿后重试")
	}
}

func (s *Server) uploadImage(w http.ResponseWriter, r *http.Request) {
	s.mediaMu.Lock()
	defer s.mediaMu.Unlock()
	sess, ok := s.get(w, r)
	if !ok {
		return
	}
	s.mu.Lock()
	if s.closing {
		s.mu.Unlock()
		fail(w, 503, "服务正在关闭")
		return
	}
	s.workWG.Add(1)
	s.mu.Unlock()
	defer s.workWG.Done()
	stop := context.AfterFunc(s.workCtx, func() { _ = r.Body.Close() })
	defer stop()
	r.Body = http.MaxBytesReader(w, r.Body, media.MaxImageBytes)
	item, err := s.images.Upload(sess.RoleID(), r.PathValue("id"), r.Body)
	if err != nil {
		imageFailure(w, err)
		return
	}
	writeJSON(w, 201, item)
}

func (s *Server) readImage(w http.ResponseWriter, r *http.Request) {
	s.mediaMu.Lock()
	sess, ok := s.get(w, r)
	if !ok {
		s.mediaMu.Unlock()
		return
	}
	item, data, err := s.images.Get(sess.RoleID(), r.PathValue("id"), r.PathValue("image"))
	s.mediaMu.Unlock()
	if err != nil {
		imageFailure(w, err)
		return
	}
	w.Header().Set("Content-Type", item.MIMEType)
	w.Header().Set("Content-Length", strconv.Itoa(len(data)))
	w.Header().Set("Content-Disposition", `inline; filename="image"`)
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(data)
}

func (s *Server) referencedImages(sess *probe.Session, id string) (map[string]bool, error) {
	keep := map[string]bool{}
	for _, message := range sess.Messages() {
		for _, image := range message.Images {
			keep[image.ID] = true
		}
	}
	queued, err := s.turns.AllImages(id)
	if err != nil {
		return nil, err
	}
	for _, image := range queued {
		keep[image.ID] = true
	}
	return keep, nil
}

func (s *Server) deleteImage(w http.ResponseWriter, r *http.Request) {
	s.mediaMu.Lock()
	defer s.mediaMu.Unlock()
	sess, ok := s.get(w, r)
	if !ok {
		return
	}
	keep, err := s.referencedImages(sess, r.PathValue("id"))
	if err != nil {
		imageFailure(w, err)
		return
	}
	if keep[r.PathValue("image")] {
		fail(w, 409, "图片已属于聊天或发送队列，请删除对应轮次或会话")
		return
	}
	if err = s.images.Delete(sess.RoleID(), r.PathValue("id"), r.PathValue("image")); err != nil {
		imageFailure(w, err)
		return
	}
	writeJSON(w, 200, map[string]bool{"ok": true})
}

func (s *Server) imageResolver(role, session string) func(context.Context, media.Image) (string, error) {
	return func(ctx context.Context, reference media.Image) (string, error) {
		if err := ctx.Err(); err != nil {
			return "", err
		}
		image, data, err := s.images.Get(role, session, reference.ID)
		if err != nil {
			return "", fmt.Errorf("会话图片已不可用，未重试模型请求")
		}
		if image.MIMEType != reference.MIMEType || image.Size != reference.Size || image.Width != reference.Width || image.Height != reference.Height || reference.SHA256 != "" && image.SHA256 != reference.SHA256 {
			return "", errors.New("图片引用与已保存内容不一致")
		}
		return "data:" + image.MIMEType + ";base64," + base64.StdEncoding.EncodeToString(data), nil
	}
}

// Provider-supplied reasoning is visible to the authenticated conversation owner.
// Inline image payloads remain internal; clients use authorized image references.
func visibleMessages(messages []probe.Message) []probe.Message {
	for i := range messages {
		for j := range messages[i].Images {
			messages[i].Images[j].DataURL = ""
		}
	}
	return messages
}

func sameImages(a, b []media.Image) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i].ID != b[i].ID || a[i].MIMEType != b[i].MIMEType || a[i].Size != b[i].Size || a[i].Width != b[i].Width || a[i].Height != b[i].Height {
			return false
		}
	}
	return true
}
