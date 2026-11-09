package api

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
)

// Each publication is an immutable directory. Resolve the current symlink once
// per request so metadata and APK always refer to the same publication.
type androidRelease struct {
	VersionCode int64  `json:"version_code"`
	VersionName string `json:"version_name"`
	PackageName string `json:"package_name"`
	SHA256      string `json:"sha256"`
	Size        int64  `json:"size"`
	APKPath     string `json:"apk_path,omitempty"`
}

func openAndroidRelease(dir string) (androidRelease, *os.File, error) {
	var release androidRelease
	if dir == "" {
		return release, nil, os.ErrNotExist
	}
	resolved, err := filepath.EvalSymlinks(dir)
	if err != nil {
		return release, nil, err
	}
	metadata, err := os.Open(filepath.Join(resolved, "manifest.json"))
	if err != nil {
		return release, nil, err
	}
	decoder := json.NewDecoder(io.LimitReader(metadata, 4097))
	decoder.DisallowUnknownFields()
	err = decoder.Decode(&release)
	var extra any
	if err == nil && decoder.Decode(&extra) != io.EOF {
		err = errors.New("invalid release metadata")
	}
	metadata.Close()
	if err != nil {
		return release, nil, err
	}
	hash, err := hex.DecodeString(release.SHA256)
	if err != nil || len(hash) != sha256.Size || release.SHA256 != hex.EncodeToString(hash) || release.VersionCode < 1 || release.VersionCode > 2100000000 || len(release.VersionName) == 0 || len(release.VersionName) > 64 || (release.PackageName != "dev.chatagent.ai.debug" && release.PackageName != "dev.chatagent.ai") || release.Size < 1 || release.Size > 64<<20 {
		return release, nil, errors.New("invalid release metadata")
	}
	apk, err := os.Open(filepath.Join(resolved, "app.apk"))
	if err != nil {
		return release, nil, err
	}
	valid := false
	defer func() {
		if !valid {
			apk.Close()
		}
	}()
	info, err := apk.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Size() != release.Size {
		return release, nil, errors.New("APK size mismatch")
	}
	digest := sha256.New()
	if _, err = io.Copy(digest, apk); err != nil {
		return release, nil, err
	}
	if hex.EncodeToString(digest.Sum(nil)) != release.SHA256 {
		return release, nil, errors.New("APK hash mismatch")
	}
	if _, err = apk.Seek(0, io.SeekStart); err != nil {
		return release, nil, err
	}
	release.APKPath = "/api/updates/android/" + release.SHA256 + "/app.apk"
	valid = true
	return release, apk, nil
}

func (s *Server) androidUpdate(w http.ResponseWriter, r *http.Request) {
	release, apk, err := openAndroidRelease(s.config.AndroidReleaseDir)
	if errors.Is(err, os.ErrNotExist) {
		fail(w, 404, "尚未发布 Android 安装包")
		return
	}
	if err != nil {
		fail(w, 503, "更新包校验失败，请联系管理员")
		return
	}
	defer apk.Close()
	writeJSON(w, 200, release)
}

func (s *Server) androidAPK(w http.ResponseWriter, r *http.Request) {
	release, apk, err := openAndroidRelease(s.config.AndroidReleaseDir)
	if err != nil {
		fail(w, 503, "更新包不可用")
		return
	}
	defer apk.Close()
	if r.PathValue("sha") != release.SHA256 {
		fail(w, 409, "更新包已变化，请重新检查更新")
		return
	}
	info, err := apk.Stat()
	if err != nil {
		fail(w, 503, "更新包不可用")
		return
	}
	w.Header().Set("Content-Type", "application/vnd.android.package-archive")
	w.Header().Set("Content-Disposition", `attachment; filename="chat-agent.apk"`)
	w.Header().Set("ETag", `"`+release.SHA256+`"`)
	http.ServeContent(w, r, "chat-agent.apk", info.ModTime(), apk)
}
