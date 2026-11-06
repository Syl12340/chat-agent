// Package media stores role- and session-scoped image attachments. Image bytes
// are not copied into chat journals; DataURL is only populated for model IO.
package media

import (
	"bytes"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	"image/jpeg"
	"image/png"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"pocket-agent/internal/storage"
)

const (
	MaxImageBytes       = 4 << 20
	MaxDimension        = 4096
	MaxPixels           = 12_000_000
	MaxSessionBytes     = 64 << 20
	MaxStoreBytes       = 256 << 20
	MaxImagesPerSession = 64
	MaxSessions         = 1024
	MaxImages           = 4096
)

var (
	ErrInvalid  = errors.New("invalid image attachment")
	ErrLimit    = errors.New("image attachment limit reached")
	ErrNotFound = errors.New("image attachment not found")
	ErrScope    = errors.New("image attachment scope mismatch")
	ErrClosed   = errors.New("image store is closed")
)

type Image struct {
	ID       string `json:"id"`
	MIMEType string `json:"mime_type"`
	Size     int    `json:"size"`
	Width    int    `json:"width"`
	Height   int    `json:"height"`
	SHA256   string `json:"sha256,omitempty"`
	DataURL  string `json:"-"`
}

func ValidSHA256(value string) bool {
	decoded, err := hex.DecodeString(value)
	return err == nil && len(decoded) == sha256.Size
}

type metadata struct {
	Version   int    `json:"version"`
	RoleID    string `json:"role_id"`
	SessionID string `json:"session_id"`
	Image     Image  `json:"image"`
	SHA256    string `json:"sha256"`
}
type scope struct {
	Version   int    `json:"version"`
	RoleID    string `json:"role_id"`
	SessionID string `json:"session_id"`
}
type usage struct {
	sessions, images, bytes, sessionImages, sessionBytes int
}
type Store struct {
	mu     sync.Mutex
	dir    string
	closed bool
}

func Open(dir string) (_ *Store, err error) {
	if err = os.MkdirAll(dir, 0700); err != nil {
		return nil, err
	}
	// Resolve the configured root once, then refuse symlink components below it.
	dir, err = filepath.EvalSymlinks(dir)
	if err != nil {
		return nil, err
	}
	dir, err = filepath.Abs(dir)
	if err != nil {
		return nil, err
	}
	f, err := os.OpenFile(filepath.Join(dir, ".store.lock"), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return nil, fmt.Errorf("image store lock unavailable: %w", err)
	}
	_, writeErr := fmt.Fprintf(f, "pid=%d\n", os.Getpid())
	if err = errors.Join(writeErr, f.Close()); err != nil {
		_ = os.Remove(filepath.Join(dir, ".store.lock"))
		return nil, err
	}
	s := &Store{dir: dir}
	defer func() {
		if err != nil {
			_ = s.Close()
		}
	}()
	if err = s.cleanStaging(); err != nil {
		return nil, err
	}
	if _, err = s.scan(""); err != nil {
		return nil, err
	}
	return s, nil
}

func (s *Store) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil
	}
	s.closed = true
	return os.Remove(filepath.Join(s.dir, ".store.lock"))
}

func (s *Store) validScope(role, session string) error {
	if s.closed {
		return ErrClosed
	}
	if !storage.ValidID(role) || !storage.ValidID(session) {
		return ErrInvalid
	}
	return nil
}

func imageID(value string) bool {
	if len(value) != 32 {
		return false
	}
	for _, char := range value {
		if !(char >= '0' && char <= '9' || char >= 'a' && char <= 'f') {
			return false
		}
	}
	return true
}

func regularDir(path string) error {
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return ErrNotFound
	}
	if err != nil {
		return err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return ErrInvalid
	}
	return nil
}

func boundedFile(path string, limit int64) ([]byte, error) {
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() || info.Size() < 0 || info.Size() > limit {
		return nil, ErrInvalid
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	data, readErr := io.ReadAll(io.LimitReader(f, limit+1))
	err = errors.Join(readErr, f.Close())
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > limit {
		return nil, ErrLimit
	}
	return data, nil
}

func decodeJSON(data []byte, target any) error {
	d := json.NewDecoder(bytes.NewReader(data))
	d.DisallowUnknownFields()
	var extra any
	if d.Decode(target) != nil || d.Decode(&extra) != io.EOF {
		return ErrInvalid
	}
	return nil
}

func (s *Store) sessionScope(session string) (scope, error) {
	dir := filepath.Join(s.dir, session)
	if err := regularDir(dir); err != nil {
		return scope{}, err
	}
	data, err := boundedFile(filepath.Join(dir, ".scope.json"), 1024)
	if err != nil {
		return scope{}, err
	}
	var value scope
	if decodeJSON(data, &value) != nil || value.Version != 1 || value.SessionID != session || !storage.ValidID(value.RoleID) {
		return scope{}, ErrInvalid
	}
	return value, nil
}

func (s *Store) requireSession(role, session string) error {
	value, err := s.sessionScope(session)
	if err != nil {
		return err
	}
	if value.RoleID != role {
		return ErrScope
	}
	return nil
}

// entries bounds both allocation and traversal cost, including unknown files.
func entries(path string, limit int) ([]os.DirEntry, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	items, readErr := f.ReadDir(limit + 1)
	if errors.Is(readErr, io.EOF) {
		readErr = nil
	}
	if err = errors.Join(readErr, f.Close()); err != nil {
		return nil, err
	}
	if len(items) > limit {
		return nil, ErrLimit
	}
	return items, nil
}

func objectSize(dir string) (int, error) {
	if err := regularDir(dir); err != nil {
		return 0, err
	}
	files, err := entries(dir, 2)
	if err != nil || len(files) != 2 {
		return 0, ErrInvalid
	}
	for _, name := range []string{"meta.json", "image.bin"} {
		info, err := os.Lstat(filepath.Join(dir, name))
		if err != nil || !info.Mode().IsRegular() || info.Size() <= 0 {
			return 0, ErrInvalid
		}
		if name == "meta.json" && info.Size() > 2048 || name == "image.bin" && info.Size() > MaxImageBytes {
			return 0, ErrInvalid
		}
		if name == "image.bin" {
			return int(info.Size()), nil
		}
	}
	return 0, ErrInvalid
}

func (s *Store) scan(session string) (usage, error) {
	var total usage
	dirs, err := entries(s.dir, MaxSessions+2)
	if err != nil {
		return total, err
	}
	for _, entry := range dirs {
		if entry.Name() == ".store.lock" || strings.HasPrefix(entry.Name(), ".session-") {
			continue
		}
		if !storage.ValidID(entry.Name()) || !entry.IsDir() || entry.Type()&os.ModeSymlink != 0 {
			return total, ErrInvalid
		}
		total.sessions++
		if total.sessions > MaxSessions {
			return total, ErrLimit
		}
		if _, err = s.sessionScope(entry.Name()); err != nil {
			return total, err
		}
		objects, err := entries(filepath.Join(s.dir, entry.Name()), MaxImagesPerSession+2)
		if err != nil {
			return total, err
		}
		count, size := 0, 0
		for _, object := range objects {
			if object.Name() == ".scope.json" || strings.HasPrefix(object.Name(), ".upload-") {
				continue
			}
			if !imageID(object.Name()) {
				return total, ErrInvalid
			}
			bytes, err := objectSize(filepath.Join(s.dir, entry.Name(), object.Name()))
			if err != nil {
				return total, err
			}
			count++
			size += bytes
		}
		if count > MaxImagesPerSession || size > MaxSessionBytes {
			return total, ErrLimit
		}
		total.images += count
		total.bytes += size
		if entry.Name() == session {
			total.sessionImages, total.sessionBytes = count, size
		}
		if total.images > MaxImages || total.bytes > MaxStoreBytes {
			return total, ErrLimit
		}
	}
	return total, nil
}

func (s *Store) safeRemove(path string) error {
	relative, err := filepath.Rel(s.dir, path)
	if err != nil || relative == "." || relative == ".." || strings.HasPrefix(relative, ".."+string(os.PathSeparator)) || filepath.IsAbs(relative) {
		return ErrInvalid
	}
	return os.RemoveAll(path)
}

// The exclusive store lock permits removal of our unpublished upload stages on
// restart. Normal objects are never garbage-collected without explicit keep IDs.
func (s *Store) cleanStaging() error {
	dirs, err := entries(s.dir, MaxSessions+2)
	if err != nil {
		return err
	}
	for _, entry := range dirs {
		if entry.Name() == ".store.lock" {
			continue
		}
		if strings.HasPrefix(entry.Name(), ".session-") {
			if err = s.safeRemove(filepath.Join(s.dir, entry.Name())); err != nil {
				return err
			}
			continue
		}
		if !storage.ValidID(entry.Name()) || !entry.IsDir() || entry.Type()&os.ModeSymlink != 0 {
			return ErrInvalid
		}
		objects, err := entries(filepath.Join(s.dir, entry.Name()), MaxImagesPerSession+2)
		if err != nil {
			return err
		}
		for _, object := range objects {
			if strings.HasPrefix(object.Name(), ".upload-") {
				if err = s.safeRemove(filepath.Join(s.dir, entry.Name(), object.Name())); err != nil {
					return err
				}
			}
		}
	}
	return nil
}

type limitedBuffer struct{ bytes.Buffer }

func (b *limitedBuffer) Write(data []byte) (int, error) {
	if b.Len()+len(data) > MaxImageBytes {
		return 0, ErrLimit
	}
	return b.Buffer.Write(data)
}

func normalize(reader io.Reader) (Image, []byte, error) {
	data, err := io.ReadAll(io.LimitReader(reader, MaxImageBytes+1))
	if err != nil {
		return Image{}, nil, err
	}
	if len(data) > MaxImageBytes {
		return Image{}, nil, ErrLimit
	}
	config, format, err := image.DecodeConfig(bytes.NewReader(data))
	if err != nil || format != "png" && format != "jpeg" {
		return Image{}, nil, ErrInvalid
	}
	if config.Width < 1 || config.Height < 1 || config.Width > MaxDimension || config.Height > MaxDimension || int64(config.Width)*int64(config.Height) > MaxPixels {
		return Image{}, nil, ErrLimit
	}
	picture, decoded, err := image.Decode(bytes.NewReader(data))
	if err != nil || decoded != format || picture.Bounds().Dx() != config.Width || picture.Bounds().Dy() != config.Height {
		return Image{}, nil, ErrInvalid
	}
	var output limitedBuffer
	mime := "image/png"
	if format == "jpeg" {
		mime = "image/jpeg"
		err = jpeg.Encode(&output, picture, &jpeg.Options{Quality: 85})
	} else {
		err = png.Encode(&output, picture)
	}
	if err != nil {
		return Image{}, nil, err
	}
	return Image{MIMEType: mime, Size: output.Len(), Width: config.Width, Height: config.Height}, output.Bytes(), nil
}

func writeFile(path string, data []byte) error {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	n, writeErr := f.Write(data)
	if writeErr == nil && n != len(data) {
		writeErr = io.ErrShortWrite
	}
	if writeErr == nil {
		writeErr = f.Sync()
	}
	return errors.Join(writeErr, f.Close())
}

func (s *Store) Upload(role, session string, reader io.Reader) (Image, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.validScope(role, session); err != nil {
		return Image{}, err
	}
	if reader == nil {
		return Image{}, ErrInvalid
	}
	existing := true
	if err := s.requireSession(role, session); errors.Is(err, ErrNotFound) {
		existing = false
	} else if err != nil {
		return Image{}, err
	}
	info, data, err := normalize(reader)
	if err != nil {
		return Image{}, err
	}
	total, err := s.scan(session)
	if err != nil {
		return Image{}, err
	}
	if !existing && total.sessions >= MaxSessions || total.images >= MaxImages || total.sessionImages >= MaxImagesPerSession || total.bytes+len(data) > MaxStoreBytes || total.sessionBytes+len(data) > MaxSessionBytes {
		return Image{}, ErrLimit
	}
	dir := filepath.Join(s.dir, session)
	if !existing {
		dir, err = os.MkdirTemp(s.dir, ".session-")
		if err != nil {
			return Image{}, err
		}
		defer func() { _ = s.safeRemove(dir) }()
		binding, _ := json.Marshal(scope{Version: 1, RoleID: role, SessionID: session})
		if err = writeFile(filepath.Join(dir, ".scope.json"), binding); err != nil {
			return Image{}, err
		}
	}
	var random [16]byte
	if _, err = rand.Read(random[:]); err != nil {
		return Image{}, err
	}
	info.ID = hex.EncodeToString(random[:])
	if _, err = os.Lstat(filepath.Join(dir, info.ID)); !errors.Is(err, os.ErrNotExist) {
		return Image{}, ErrInvalid
	}
	staging, err := os.MkdirTemp(dir, ".upload-")
	if err != nil {
		return Image{}, err
	}
	defer func() { _ = s.safeRemove(staging) }()
	digest := sha256.Sum256(data)
	info.SHA256 = hex.EncodeToString(digest[:])
	meta, _ := json.Marshal(metadata{Version: 1, RoleID: role, SessionID: session, Image: info, SHA256: info.SHA256})
	if err = writeFile(filepath.Join(staging, "meta.json"), meta); err != nil {
		return Image{}, err
	}
	if err = writeFile(filepath.Join(staging, "image.bin"), data); err != nil {
		return Image{}, err
	}
	if err = os.Rename(staging, filepath.Join(dir, info.ID)); err != nil {
		return Image{}, err
	}
	if !existing {
		if err = os.Rename(dir, filepath.Join(s.dir, session)); err != nil {
			return Image{}, err
		}
	}
	return info, nil
}

func (s *Store) get(role, session, id string) (Image, []byte, error) {
	if err := s.validScope(role, session); err != nil {
		return Image{}, nil, err
	}
	if !imageID(id) {
		return Image{}, nil, ErrInvalid
	}
	if err := s.requireSession(role, session); err != nil {
		return Image{}, nil, err
	}
	dir := filepath.Join(s.dir, session, id)
	if err := regularDir(dir); err != nil {
		return Image{}, nil, err
	}
	meta, err := boundedFile(filepath.Join(dir, "meta.json"), 2048)
	if err != nil {
		return Image{}, nil, err
	}
	var value metadata
	if decodeJSON(meta, &value) != nil || value.Version != 1 || value.Image.ID != id {
		return Image{}, nil, ErrInvalid
	}
	if value.RoleID != role || value.SessionID != session {
		return Image{}, nil, ErrScope
	}
	if value.Image.Size <= 0 || value.Image.Size > MaxImageBytes || value.Image.Width < 1 || value.Image.Height < 1 || value.Image.Width > MaxDimension || value.Image.Height > MaxDimension || int64(value.Image.Width)*int64(value.Image.Height) > MaxPixels {
		return Image{}, nil, ErrInvalid
	}
	data, err := boundedFile(filepath.Join(dir, "image.bin"), MaxImageBytes)
	if err != nil {
		return Image{}, nil, err
	}
	digest := sha256.Sum256(data)
	if len(data) != value.Image.Size || value.SHA256 != hex.EncodeToString(digest[:]) || value.Image.SHA256 != "" && value.Image.SHA256 != value.SHA256 {
		return Image{}, nil, ErrInvalid
	}
	config, format, err := image.DecodeConfig(bytes.NewReader(data))
	if err != nil || format != "png" && format != "jpeg" || value.Image.MIMEType != "image/"+format || config.Width != value.Image.Width || config.Height != value.Image.Height {
		return Image{}, nil, ErrInvalid
	}
	value.Image.SHA256 = value.SHA256
	return value.Image, data, nil
}

func (s *Store) Get(role, session, id string) (Image, []byte, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.get(role, session, id)
}

func (s *Store) Delete(role, session, id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.validScope(role, session); err != nil {
		return err
	}
	if !imageID(id) {
		return ErrInvalid
	}
	if err := s.requireSession(role, session); err != nil {
		return err
	}
	return s.safeRemove(filepath.Join(s.dir, session, id))
}

// Prune deletes only objects not referenced by the caller's complete journal AND
// queue keep set. Callers serialize this with conversation writes and uploads.
func (s *Store) Prune(role, session string, keepIDs map[string]bool) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.validScope(role, session); err != nil {
		return err
	}
	if err := s.requireSession(role, session); err != nil {
		if errors.Is(err, ErrNotFound) {
			return nil
		}
		return err
	}
	for id := range keepIDs {
		if !imageID(id) {
			return ErrInvalid
		}
	}
	objects, err := entries(filepath.Join(s.dir, session), MaxImagesPerSession+2)
	if err != nil {
		return err
	}
	for _, object := range objects {
		if object.Name() == ".scope.json" || strings.HasPrefix(object.Name(), ".upload-") {
			continue
		}
		if !imageID(object.Name()) {
			return ErrInvalid
		}
	}
	for _, object := range objects {
		if imageID(object.Name()) && !keepIDs[object.Name()] {
			if err = s.safeRemove(filepath.Join(s.dir, session, object.Name())); err != nil {
				return err
			}
		}
	}
	return nil
}

func (s *Store) DeleteSession(session string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return ErrClosed
	}
	if !storage.ValidID(session) {
		return ErrInvalid
	}
	dir := filepath.Join(s.dir, session)
	if err := regularDir(dir); err != nil {
		if errors.Is(err, ErrNotFound) {
			return nil
		}
		return err
	}
	return s.safeRemove(dir)
}
