package localagent

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"mime"
	"os"
	"path/filepath"
	"strings"
	"unicode/utf8"
)

const textLimit = 256 << 10
const artifactLimit = 32 << 20

type Guard struct {
	path string
	root *os.Root
}

// Write replaces one bounded UTF-8 file after verifying the exact previous
// content. It deliberately does not create directories or follow symlinks.
func (g *Guard) Write(path, content, expectedSHA256, operationID string) error {
	if !validRelative(path) || path == "." || len(content) > textLimit || !utf8.ValidString(content) || strings.IndexByte(content, 0) >= 0 {
		return errors.New("write requires a relative path and bounded UTF-8 text")
	}
	if expectedSHA256 != "missing" {
		if len(expectedSHA256) != 64 {
			return errors.New("expected_sha256 must be the current hash or missing")
		}
		if _, err := hex.DecodeString(expectedSHA256); err != nil || expectedSHA256 != strings.ToLower(expectedSHA256) {
			return errors.New("expected_sha256 must be lowercase hexadecimal")
		}
	}
	current, err := g.Read(path)
	if expectedSHA256 == "missing" {
		if err == nil {
			return errors.New("workspace file already exists")
		}
	} else {
		if err != nil {
			return errors.New("workspace file changed or is unavailable")
		}
		digest := sha256.Sum256([]byte(current))
		if fmt.Sprintf("%x", digest[:]) != expectedSHA256 {
			return errors.New("workspace file changed since it was read")
		}
	}
	temp := ".ca-write-" + strings.TrimPrefix(operationID, "op_")
	if !validRelative(temp) {
		return errors.New("invalid operation identity")
	}
	f, err := g.root.OpenFile(temp, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return errors.New("could not prepare workspace write")
	}
	removeTemp := true
	defer func() {
		if removeTemp {
			_ = g.root.Remove(temp)
		}
	}()
	if _, err = io.WriteString(f, content); err == nil {
		err = f.Sync()
	}
	err = errors.Join(err, f.Close())
	if err != nil {
		return errors.New("could not persist workspace write")
	}
	if err = replaceRoot(g.root, g.path, temp, path); err != nil {
		return errors.New("could not replace workspace file")
	}
	removeTemp = false
	return nil
}

func OpenGuard(path string) (*Guard, error) {
	if !filepath.IsAbs(path) {
		return nil, errors.New("local workspace must be an absolute path")
	}
	real, err := filepath.EvalSymlinks(path)
	if err != nil {
		return nil, fmt.Errorf("open local workspace: %w", err)
	}
	info, err := os.Stat(real)
	if err != nil || !info.IsDir() || filepath.Dir(real) == real {
		return nil, errors.New("local workspace must be a dedicated directory")
	}
	root, err := os.OpenRoot(real)
	if err != nil {
		return nil, fmt.Errorf("open local workspace: %w", err)
	}
	return &Guard{path: real, root: root}, nil
}

func (g *Guard) Close() error { return g.root.Close() }
func (g *Guard) Path() string { return g.path }

func validRelative(path string) bool {
	if path == "." {
		return true
	}
	if path == "" || !fs.ValidPath(path) || len(path) > 512 || strings.ContainsAny(path, "\\:\x00") {
		return false
	}
	return true
}

func (g *Guard) List(path string) ([]string, error) {
	if !validRelative(path) {
		return nil, errors.New("path must be relative to the selected workspace")
	}
	dir, err := g.root.OpenRoot(path)
	if err != nil {
		return nil, errors.New("workspace directory is unavailable")
	}
	defer dir.Close()
	f, err := dir.Open(".")
	if err != nil {
		return nil, errors.New("workspace directory is unavailable")
	}
	defer f.Close()
	items, err := f.ReadDir(200)
	if err != nil && !errors.Is(err, io.EOF) {
		return nil, errors.New("workspace directory is unavailable")
	}
	entries := make([]string, 0, len(items))
	for _, item := range items {
		if item.Type()&os.ModeSymlink != 0 {
			continue
		}
		name := item.Name()
		if item.IsDir() {
			name += "/"
		}
		entries = append(entries, name)
	}
	return entries, nil
}

func (g *Guard) Read(path string) (string, error) {
	if !validRelative(path) || path == "." {
		return "", errors.New("a relative file path is required")
	}
	f, err := g.root.Open(path)
	if err != nil {
		return "", errors.New("workspace file is unavailable")
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Size() > textLimit {
		return "", errors.New("workspace file is not bounded text")
	}
	raw, err := io.ReadAll(io.LimitReader(f, textLimit+1))
	if err != nil || len(raw) > textLimit || !utf8.Valid(raw) || strings.IndexByte(string(raw), 0) >= 0 {
		return "", errors.New("workspace file is not bounded UTF-8 text")
	}
	return string(raw), nil
}

func (g *Guard) Search(query string) ([]string, error) {
	if strings.TrimSpace(query) == "" || len(query) > 512 {
		return nil, errors.New("search query must be between 1 and 512 bytes")
	}
	matches := []string{}
	visited := 0
	err := fs.WalkDir(g.root.FS(), ".", func(path string, entry fs.DirEntry, walkErr error) error {
		visited++
		if visited > 1000 || len(matches) >= 100 {
			return fs.SkipAll
		}
		if walkErr != nil || entry.Type()&os.ModeSymlink != 0 {
			if entry != nil && entry.IsDir() {
				return fs.SkipDir
			}
			return nil
		}
		if entry.Type().IsRegular() {
			content, err := g.Read(path)
			if err == nil && strings.Contains(content, query) {
				matches = append(matches, path)
			}
		}
		return nil
	})
	return matches, err
}

func (g *Guard) Artifact(path string) (Artifact, error) {
	if !validRelative(path) || path == "." {
		return Artifact{}, errors.New("artifact path must be relative to the workspace")
	}
	f, err := g.root.Open(path)
	if err != nil {
		return Artifact{}, errors.New("artifact is unavailable")
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Size() < 0 || info.Size() > artifactLimit {
		return Artifact{}, errors.New("artifact must be a regular file no larger than 32 MiB")
	}
	hash := sha256.New()
	if _, err := io.Copy(hash, io.LimitReader(f, artifactLimit+1)); err != nil {
		return Artifact{}, errors.New("artifact could not be hashed")
	}
	mediaType := mime.TypeByExtension(strings.ToLower(filepath.Ext(path)))
	if mediaType == "" {
		mediaType = "application/octet-stream"
	}
	return Artifact{Path: path, Name: filepath.Base(path), MediaType: mediaType, Bytes: info.Size(), SHA256: fmt.Sprintf("%x", hash.Sum(nil))}, nil
}

func (g *Guard) OpenArtifact(path string) (*os.File, error) {
	if _, err := g.Artifact(path); err != nil {
		return nil, err
	}
	return g.root.Open(path)
}
