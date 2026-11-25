package api

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/url"
	"path"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"chat-agent/internal/channel"
	"chat-agent/internal/courseware"
	"chat-agent/internal/media"
)

const qqAttachmentLimit = 4 << 20

func qqAttachmentRedirect(req *http.Request, via []*http.Request) error {
	host := strings.ToLower(req.URL.Hostname())
	if len(via) <= 2 && req.URL.Scheme == "https" && req.URL.User == nil && req.URL.Fragment == "" {
		for _, domain := range []string{"qq.com", "qq.com.cn", "qpic.cn"} {
			if host == domain || strings.HasSuffix(host, "."+domain) {
				return nil
			}
		}
	}
	return http.ErrUseLastResponse
}

func qqAttachmentName(item channel.Attachment) string {
	name := strings.TrimSpace(path.Base(strings.ReplaceAll(item.Filename, "\\", "/")))
	if name == "." || name == "/" || name == "" {
		if u, err := url.Parse(item.URL); err == nil && path.Ext(u.Path) != "" {
			name = path.Base(u.Path)
		} else {
			name = "未命名附件"
		}
	}
	name = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return -1
		}
		return r
	}, name)
	runes := []rune(name)
	return string(runes[:min(len(runes), 140)])
}

func qqImageAttachment(item channel.Attachment) bool {
	contentType := strings.ToLower(strings.TrimSpace(strings.SplitN(item.ContentType, ";", 2)[0]))
	if strings.HasPrefix(contentType, "image/") || contentType == "image" || contentType == "sticker" {
		return true
	}
	switch strings.ToLower(path.Ext(qqAttachmentName(item))) {
	case ".jpg", ".jpeg", ".png", ".gif", ".webp":
		return true
	}
	return false
}

func qqReadableDocument(item channel.Attachment) string {
	name := qqAttachmentName(item)
	if path.Ext(name) == "" {
		kind, _, _ := mime.ParseMediaType(item.ContentType)
		switch kind {
		case "text/plain":
			name += ".txt"
		case "text/csv":
			name += ".csv"
		case "application/json":
			name += ".json"
		case "application/vnd.openxmlformats-officedocument.wordprocessingml.document":
			name += ".docx"
		case "application/vnd.openxmlformats-officedocument.presentationml.presentation":
			name += ".pptx"
		}
	}
	switch strings.ToLower(path.Ext(name)) {
	case ".txt", ".md", ".markdown", ".csv", ".json", ".log", ".docx", ".pptx", ".html", ".htm":
		return name
	}
	return ""
}

func (q *qqOfficialIntegration) chatSelection(sessionID string, attachments []channel.Attachment) chatSelection {
	choice, err := q.server.selectChat(q.server.turns.LastModel(sessionID), "")
	if err != nil {
		choice, _ = q.server.selectChat("", "")
	}
	for _, item := range attachments {
		if !choice.Vision && qqImageAttachment(item) {
			for _, model := range q.server.chatModels() {
				if candidate, err := q.server.selectChat(model.ID, ""); err == nil && candidate.Vision {
					return candidate
				}
			}
			break
		}
	}
	return choice
}

type qqAttachmentReceipt struct {
	Name    string `json:"name"`
	Status  string `json:"status"`
	Excerpt string `json:"excerpt,omitempty"`
}

func (q *qqOfficialIntegration) prepareAttachments(ctx context.Context, role, session, text string, attachments []channel.Attachment, vision bool) (string, []media.Image) {
	if len(attachments) == 0 {
		return text, nil
	}
	ctx, cancel := context.WithTimeout(ctx, 12*time.Second)
	defer cancel()
	var images []media.Image
	var receipts []qqAttachmentReceipt
	budget := 4500 // characters of document text across the whole message
	for _, item := range attachments {
		receipt := qqAttachmentReceipt{Name: qqAttachmentName(item)}
		picture, document := qqImageAttachment(item), qqReadableDocument(item)
		switch {
		case picture && !vision:
			receipt.Status = "当前没有可接收图片的模型"
		case picture && len(images) >= 3:
			receipt.Status = "超过本条消息的 3 张图片上限"
		case !picture && document == "":
			receipt.Status = "收到文件名，当前无法读取此格式的正文"
		case !picture && budget == 0:
			receipt.Status = "超过本条消息的附件正文容量"
		default:
			data, err := q.fetchAttachment(ctx, item)
			if err != nil {
				receipt.Status = err.Error()
			} else if picture {
				q.server.mediaMu.Lock()
				image, err := q.server.images.Upload(role, session, bytes.NewReader(data))
				q.server.mediaMu.Unlock()
				if err != nil {
					receipt.Status = "图片解码失败或超出尺寸限制"
				} else {
					images = append(images, image)
					receipt.Status = fmt.Sprintf("已附上原图的可见画面（图片 %d，动画取首帧）", len(images))
				}
			} else {
				body, err := courseware.ExtractText(document, data)
				runes := []rune(strings.TrimSpace(body))
				if err != nil || len(runes) == 0 {
					receipt.Status = "无法提取附件正文"
				} else {
					length := min(len(runes), budget)
					receipt.Status = "已提取附件正文"
					if length < len(runes) {
						receipt.Status = "已提取附件正文开头，内容还有后续"
					}
					receipt.Excerpt = string(runes[:length])
					budget -= length
				}
			}
		}
		receipts = append(receipts, receipt)
	}
	const label = "\n\n[QQ 附件接收结果]\n"
	data, _ := json.Marshal(receipts)
	if len(text)+len(label)+len(data) > 64<<10 {
		for i := range receipts {
			if receipts[i].Excerpt != "" {
				receipts[i].Status = "附件正文超出本轮容量"
			}
			receipts[i].Excerpt = ""
		}
		data, _ = json.Marshal(receipts)
		limit := min(len(text), (64<<10)-len(label)-len(data))
		for limit > 0 && limit < len(text) && !utf8.RuneStart(text[limit]) {
			limit--
		}
		text = text[:limit]
	}
	return text + label + string(data), images
}

func (q *qqOfficialIntegration) fetchAttachment(ctx context.Context, item channel.Attachment) ([]byte, error) {
	if item.Size > qqAttachmentLimit {
		return nil, fmt.Errorf("附件超过 4 MiB")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, item.URL, nil)
	if err != nil || req.URL.Scheme != "https" || req.URL.User != nil || req.URL.Fragment != "" {
		return nil, fmt.Errorf("QQ 未提供可用的 HTTPS 附件地址")
	}
	resp, err := q.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("未能从 QQ 下载附件")
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("QQ 附件下载返回 HTTP %d", resp.StatusCode)
	}
	if resp.ContentLength > qqAttachmentLimit {
		return nil, fmt.Errorf("附件超过 4 MiB")
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, qqAttachmentLimit+1))
	if err != nil {
		return nil, fmt.Errorf("QQ 附件下载中断")
	}
	if len(data) > qqAttachmentLimit {
		return nil, fmt.Errorf("附件超过 4 MiB")
	}
	return data, nil
}
