// Package courseware stores role-scoped learning materials and extracts bounded
// text without calling a model. Extracted content is untrusted reference data.
package courseware

import (
	"archive/zip"
	"bytes"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"encoding/xml"
	"errors"
	"fmt"
	"html"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"

	"chat-agent/internal/storage"
)

const sourceLimit = 16 << 20
const textLimit = 2 << 20

type Document struct {
	ID        string    `json:"id"`
	RoleID    string    `json:"role_id"`
	Name      string    `json:"name"`
	MediaType string    `json:"media_type"`
	Bytes     int       `json:"bytes"`
	SHA256    string    `json:"sha256"`
	TextBytes int       `json:"text_bytes"`
	Revision  int       `json:"revision"`
	Locator   string    `json:"locator,omitempty"`
	CreatedAt time.Time `json:"created_at"`
}

type SourceRef struct {
	DocumentID string `json:"document_id"`
	Revision   int    `json:"revision"`
	SHA256     string `json:"sha256"`
	Start      int    `json:"start"`
	End        int    `json:"end"`
	StartLine  int    `json:"start_line"`
	EndLine    int    `json:"end_line"`
	Locator    string `json:"locator,omitempty"`
	StartPage  int    `json:"start_page,omitempty"`
	EndPage    int    `json:"end_page,omitempty"`
}

type snapshot struct {
	Version   int        `json:"version"`
	Documents []Document `json:"documents"`
}
type Store struct {
	mu    sync.Mutex
	dir   string
	state snapshot
}

func Open(dir string) (*Store, error) {
	if err := os.MkdirAll(filepath.Join(dir, "text"), 0700); err != nil {
		return nil, err
	}
	if err := os.MkdirAll(filepath.Join(dir, "source"), 0700); err != nil {
		return nil, err
	}
	s := &Store{dir: dir, state: snapshot{Version: 1, Documents: []Document{}}}
	raw, err := os.ReadFile(filepath.Join(dir, "index.json"))
	if errors.Is(err, os.ErrNotExist) {
		return s, nil
	}
	if err != nil {
		return nil, err
	}
	if len(raw) > 2<<20 || json.Unmarshal(raw, &s.state) != nil || s.state.Version != 1 || len(s.state.Documents) > 512 {
		return nil, errors.New("damaged courseware store")
	}
	for _, d := range s.state.Documents {
		if !storage.ValidID(d.RoleID) || len(d.ID) != 32 || strings.ContainsAny(d.ID, `/\\`) || d.Revision < 1 || len(d.SHA256) != 64 || d.TextBytes < 1 || d.TextBytes > textLimit {
			return nil, errors.New("damaged courseware store")
		}
	}
	return s, nil
}

func (s *Store) Close() error { return nil }

func (s *Store) List(role string) ([]Document, error) {
	if !storage.ValidID(role) {
		return nil, errors.New("invalid role")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	out := []Document{}
	for _, d := range s.state.Documents {
		if d.RoleID == role {
			out = append(out, d)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.After(out[j].CreatedAt) })
	return out, nil
}

func (s *Store) Add(role, name, mediaType string, raw []byte) (Document, error) {
	name = strings.TrimSpace(filepath.Base(name))
	if !storage.ValidID(role) || name == "." || name == "" || len(name) > 180 || len(raw) == 0 || len(raw) > sourceLimit {
		return Document{}, errors.New("invalid courseware upload")
	}
	text, err := ExtractText(name, raw)
	if err != nil {
		return Document{}, err
	}
	text = strings.TrimSpace(text)
	if text == "" {
		return Document{}, errors.New("课件中没有可提取的文本")
	}
	if len(text) > textLimit {
		text = text[:textLimit]
	}
	if !utf8.ValidString(text) {
		return Document{}, errors.New("提取结果不是 UTF-8 文本")
	}
	var idRaw [16]byte
	if _, err := rand.Read(idRaw[:]); err != nil {
		return Document{}, err
	}
	id := hex.EncodeToString(idRaw[:])
	digest := sha256.Sum256(raw)
	locator := ""
	if strings.EqualFold(filepath.Ext(name), ".pptx") {
		locator = "slide"
	}
	doc := Document{ID: id, RoleID: role, Name: name, MediaType: mediaType, Bytes: len(raw), SHA256: fmt.Sprintf("%x", digest[:]), TextBytes: len(text), Revision: 1, Locator: locator, CreatedAt: time.Now().UTC()}
	s.mu.Lock()
	defer s.mu.Unlock()
	count := 0
	for _, old := range s.state.Documents {
		if old.RoleID == role {
			count++
		}
	}
	if count >= 64 {
		return Document{}, errors.New("每个角色最多保存 64 份课件")
	}
	if err := storage.Replace(filepath.Join(s.dir, "text", id+".txt"), []byte(text)); err != nil {
		return Document{}, err
	}
	// Keep the original bytes next to the extracted text. Without them the store
	// only remembers what one extractor made of the file: the page a reader was
	// looking at cannot be shown again, and a later extractor cannot be re-run on
	// real input. Written after the text so a crash leaves an orphaned source
	// rather than a document whose text is missing.
	if err := storage.Replace(filepath.Join(s.dir, "source", id+sourceExt(name)), raw); err != nil {
		_ = os.Remove(filepath.Join(s.dir, "text", id+".txt"))
		return Document{}, err
	}
	next := snapshot{Version: 1, Documents: append(append([]Document(nil), s.state.Documents...), doc)}
	if err := s.save(next); err != nil {
		_ = os.Remove(filepath.Join(s.dir, "text", id+".txt"))
		_ = os.Remove(filepath.Join(s.dir, "source", id+sourceExt(name)))
		return Document{}, err
	}
	return doc, nil
}

func (s *Store) Delete(role, id string, revision int) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	next := snapshot{Version: 1, Documents: make([]Document, 0, len(s.state.Documents))}
	found := false
	docName := ""
	for _, d := range s.state.Documents {
		if d.RoleID == role && d.ID == id {
			if d.Revision != revision {
				return errors.New("课件已变化，请刷新")
			}
			found = true
			docName = d.Name
			continue
		}
		next.Documents = append(next.Documents, d)
	}
	if !found {
		return os.ErrNotExist
	}
	if err := s.save(next); err != nil {
		return err
	}
	_ = os.Remove(filepath.Join(s.dir, "text", id+".txt"))
	_ = os.Remove(filepath.Join(s.dir, "source", id+sourceExt(docName)))
	return nil
}

// sourceExt keeps the stored original recognisable by whatever opens it later.
// Only an extension made of letters and digits is kept, so a crafted name cannot
// steer the write outside the source directory.
func sourceExt(name string) string {
	ext := strings.ToLower(filepath.Ext(name))
	if len(ext) < 2 || len(ext) > 12 {
		return ".bin"
	}
	for _, c := range ext[1:] {
		if !(c >= 'a' && c <= 'z' || c >= '0' && c <= '9') {
			return ".bin"
		}
	}
	return ext
}

// Source returns the bytes that were actually uploaded. The text projection is
// what the model reads; this is what the person opened, so an extractor's guess
// can always be checked against the real page instead of being taken on trust.
func (s *Store) Source(role, id string) (Document, []byte, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, doc := range s.state.Documents {
		if doc.RoleID != role || doc.ID != id {
			continue
		}
		raw, err := os.ReadFile(filepath.Join(s.dir, "source", id+sourceExt(doc.Name)))
		if err != nil {
			return Document{}, nil, err
		}
		return doc, raw, nil
	}
	return Document{}, nil, os.ErrNotExist
}

func (s *Store) Read(role, id, query string, offset, limit int) (Document, string, SourceRef, error) {
	if limit <= 0 || limit > 12000 {
		limit = 6000
	}
	if offset < 0 {
		offset = 0
	}
	s.mu.Lock()
	var doc Document
	found := false
	for _, d := range s.state.Documents {
		if d.RoleID == role && d.ID == id {
			doc = d
			found = true
			break
		}
	}
	s.mu.Unlock()
	if !found {
		return Document{}, "", SourceRef{}, os.ErrNotExist
	}
	raw, err := os.ReadFile(filepath.Join(s.dir, "text", id+".txt"))
	if err != nil {
		return Document{}, "", SourceRef{}, err
	}
	text := string(raw)
	if query = strings.TrimSpace(query); query != "" {
		if at := strings.Index(strings.ToLower(text), strings.ToLower(query)); at >= 0 {
			offset = max(0, at-limit/3)
		} else {
			return doc, "", SourceRef{}, errors.New("课件中没有匹配内容")
		}
	}
	if offset >= len(text) {
		return doc, "", SourceRef{}, errors.New("读取位置超出课件范围")
	}
	end := min(len(text), offset+limit)
	for offset > 0 && !utf8.RuneStart(text[offset]) {
		offset--
	}
	for end < len(text) && !utf8.RuneStart(text[end]) {
		end--
	}
	locator := doc.Locator
	if locator == "" && strings.EqualFold(filepath.Ext(doc.Name), ".pptx") && strings.Contains(text, "[幻灯片 ") {
		// Slide-aware text created before Locator was persisted remains locatable.
		locator, doc.Locator = "slide", "slide"
	}
	ref := SourceRef{DocumentID: doc.ID, Revision: doc.Revision, SHA256: doc.SHA256, Start: offset, End: end, StartLine: 1 + strings.Count(text[:offset], "\n"), EndLine: 1 + strings.Count(text[:end], "\n"), Locator: locator}
	if locator == "slide" {
		ref.StartPage = slideAt(text, offset)
		ref.EndPage = slideAt(text, max(offset, end-1))
	}
	return doc, text[offset:end], ref, nil
}

func (s *Store) save(next snapshot) error {
	raw, err := json.Marshal(next)
	if err != nil {
		return err
	}
	if err = storage.Replace(filepath.Join(s.dir, "index.json"), raw); err != nil {
		return err
	}
	s.state = next
	return nil
}

// ExtractText is the bounded document parser used for both uploads and chat attachments.
func ExtractText(name string, raw []byte) (string, error) {
	if len(raw) == 0 || len(raw) > sourceLimit {
		return "", errors.New("文件为空或超过大小限制")
	}
	ext := strings.ToLower(filepath.Ext(name))
	switch ext {
	case ".txt", ".md", ".markdown", ".csv", ".json", ".log":
		if !utf8.Valid(raw) {
			return "", errors.New("文本课件必须是 UTF-8")
		}
		return string(raw), nil
	case ".html", ".htm":
		if !utf8.Valid(raw) {
			return "", errors.New("HTML 课件必须是 UTF-8")
		}
		return stripHTML(string(raw)), nil
	case ".docx":
		return extractZIPXML(raw, "word/")
	case ".pptx":
		return extractPPTX(raw)
	case ".pdf":
		return extractPDF(raw)
	default:
		return "", fmt.Errorf("不支持的课件格式 %q", ext)
	}
}

type pptxSlide struct {
	number int
	file   *zip.File
}

func extractPPTX(raw []byte) (string, error) {
	r, err := zip.NewReader(bytes.NewReader(raw), int64(len(raw)))
	if err != nil {
		return "", errors.New("课件压缩包损坏")
	}
	slides := make([]pptxSlide, 0)
	var expanded uint64
	for _, file := range r.File {
		name := strings.TrimSuffix(strings.TrimPrefix(file.Name, "ppt/slides/slide"), ".xml")
		number, numberErr := strconv.Atoi(name)
		if numberErr != nil || number < 1 {
			continue
		}
		if file.UncompressedSize64 > 4<<20 {
			return "", errors.New("课件单张幻灯片过大")
		}
		expanded += file.UncompressedSize64
		if expanded > 32<<20 || len(slides) >= 500 {
			return "", errors.New("课件包含过多幻灯片")
		}
		slides = append(slides, pptxSlide{number: number, file: file})
	}
	if len(slides) == 0 {
		return "", errors.New("PPTX 中没有可读取的幻灯片")
	}
	sort.Slice(slides, func(i, j int) bool { return slides[i].number < slides[j].number })
	var out strings.Builder
	for _, slide := range slides {
		out.WriteString("\n\n[幻灯片 ")
		out.WriteString(strconv.Itoa(slide.number))
		out.WriteString("]\n")
		text, readErr := extractXMLFile(slide.file)
		if readErr != nil {
			return "", readErr
		}
		out.WriteString(text)
		if out.Len() > textLimit {
			return out.String()[:textLimit], nil
		}
	}
	return out.String(), nil
}

func extractXMLFile(file *zip.File) (string, error) {
	rc, err := file.Open()
	if err != nil {
		return "", errors.New("课件 XML 无法读取")
	}
	defer rc.Close()
	var out strings.Builder
	dec := xml.NewDecoder(io.LimitReader(rc, textLimit+1))
	for {
		token, tokenErr := dec.Token()
		if tokenErr == io.EOF {
			break
		}
		if tokenErr != nil {
			return "", errors.New("课件 XML 损坏")
		}
		if chars, ok := token.(xml.CharData); ok {
			value := strings.TrimSpace(string(chars))
			if value != "" {
				out.WriteString(value)
				out.WriteByte('\n')
			}
		}
	}
	return out.String(), nil
}

func slideAt(text string, offset int) int {
	if offset < 0 {
		return 0
	}
	if offset > len(text) {
		offset = len(text)
	}
	page := 0
	prefix := text[:offset]
	if offset == 0 || text[offset-1] == '\n' {
		if end := strings.IndexByte(text[offset:], '\n'); end >= 0 {
			prefix += text[offset : offset+end+1]
		}
	}
	for _, line := range strings.Split(prefix, "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "[幻灯片 ") || !strings.HasSuffix(line, "]") {
			continue
		}
		value := strings.TrimSuffix(strings.TrimPrefix(line, "[幻灯片 "), "]")
		if parsed, err := strconv.Atoi(value); err == nil && parsed > 0 {
			page = parsed
		}
	}
	return page
}

func extractZIPXML(raw []byte, prefix string) (string, error) {
	r, err := zip.NewReader(bytes.NewReader(raw), int64(len(raw)))
	if err != nil {
		return "", errors.New("课件压缩包损坏")
	}
	var out strings.Builder
	files := 0
	var expanded uint64
	for _, f := range r.File {
		if !strings.HasPrefix(f.Name, prefix) || !strings.HasSuffix(f.Name, ".xml") {
			continue
		}
		files++
		expanded += f.UncompressedSize64
		if files > 500 || f.UncompressedSize64 > 4<<20 || expanded > 32<<20 {
			return "", errors.New("课件包含过多文件")
		}
		rc, err := f.Open()
		if err != nil {
			continue
		}
		dec := xml.NewDecoder(io.LimitReader(rc, textLimit+1))
		for {
			tok, e := dec.Token()
			if e == io.EOF {
				break
			}
			if e != nil {
				rc.Close()
				return "", errors.New("课件 XML 损坏")
			}
			if ch, ok := tok.(xml.CharData); ok {
				value := strings.TrimSpace(string(ch))
				if value != "" {
					out.WriteString(value)
					out.WriteByte('\n')
					if out.Len() > textLimit {
						return out.String()[:textLimit], nil
					}
				}
			}
		}
		rc.Close()
	}
	return out.String(), nil
}

func stripHTML(value string) string {
	var out strings.Builder
	inTag := false
	for _, r := range value {
		if r == '<' {
			inTag = true
			out.WriteByte('\n')
			continue
		}
		if r == '>' {
			inTag = false
			continue
		}
		if !inTag {
			if unicode.IsSpace(r) {
				out.WriteByte(' ')
			} else {
				out.WriteRune(r)
			}
		}
	}
	return html.UnescapeString(out.String())
}
