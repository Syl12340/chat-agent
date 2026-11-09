package api

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"strings"

	"chat-agent/internal/deviceauth"
)

type deviceAuthContextKey struct{}
type requestAuth struct {
	Method string
	Device deviceauth.Device
}

func currentAuth(r *http.Request) requestAuth {
	value, _ := r.Context().Value(deviceAuthContextKey{}).(requestAuth)
	return value
}
func publicDeviceRequest(r *http.Request) bool {
	if r.Method != http.MethodPost {
		return false
	}
	switch r.URL.Path {
	case "/api/device/register", "/api/device/challenge", "/api/device/verify":
		return true
	}
	return false
}
func deviceFail(w http.ResponseWriter, status int, code, message string) {
	writeJSON(w, status, map[string]string{"error": message, "code": code})
}
func deviceFailure(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, deviceauth.ErrInvalid):
		deviceFail(w, 400, "invalid_device_request", "设备请求格式无效")
	case errors.Is(err, deviceauth.ErrNotFound):
		deviceFail(w, 404, "device_not_found", "设备尚未注册")
	case errors.Is(err, deviceauth.ErrPending):
		deviceFail(w, 403, "device_pending", "设备正在等待管理员批准")
	case errors.Is(err, deviceauth.ErrRevoked):
		deviceFail(w, 403, "device_revoked", "设备授权已撤销")
	case errors.Is(err, deviceauth.ErrConflict):
		deviceFail(w, 409, "device_revision_conflict", "设备记录已变更，请刷新后重试")
	case errors.Is(err, deviceauth.ErrAdmin):
		deviceFail(w, 403, "device_admin_required", "此操作需要设备管理员权限")
	case errors.Is(err, deviceauth.ErrChallenge):
		deviceFail(w, 401, "challenge_invalid", "登录挑战无效、已使用或已过期")
	case errors.Is(err, deviceauth.ErrSignature):
		deviceFail(w, 401, "signature_invalid", "设备签名无效，请重新获取挑战")
	case errors.Is(err, deviceauth.ErrSession):
		deviceFail(w, 401, "device_session_invalid", "设备会话无效或已过期，请重新验证")
	case errors.Is(err, deviceauth.ErrRateLimited):
		w.Header().Set("Retry-After", "60")
		deviceFail(w, 429, "device_rate_limited", "设备请求过于频繁，请稍后重试")
	case errors.Is(err, deviceauth.ErrLimit):
		deviceFail(w, 429, "device_capacity_reached", "设备认证容量已满，请稍后重试或联系管理员")
	default:
		deviceFail(w, 503, "device_store_unavailable", "设备认证暂时不可用")
	}
}
func (s *Server) authenticateDeviceRequest(w http.ResponseWriter, r *http.Request) (*http.Request, bool) {
	auth := r.Header.Get("Authorization")
	if !strings.HasPrefix(auth, "Bearer ") || len(auth) > 4096 {
		w.Header().Set("WWW-Authenticate", `Bearer realm="chat-agent"`)
		deviceFail(w, 401, "authentication_required", "请提供有效的访问凭据")
		return r, false
	}
	token := strings.TrimPrefix(auth, "Bearer ")
	hash := sha256.Sum256([]byte(token))
	identity := requestAuth{Method: "owner"}
	if subtle.ConstantTimeCompare(hash[:], s.tokenHash[:]) != 1 {
		device, err := s.devices.Authenticate(token)
		if err != nil {
			w.Header().Set("WWW-Authenticate", `Bearer realm="chat-agent"`)
			deviceFailure(w, err)
			return r, false
		}
		identity = requestAuth{Method: "device", Device: device}
	}
	return r.WithContext(context.WithValue(r.Context(), deviceAuthContextKey{}, identity)), true
}
func decodeDeviceInput(w http.ResponseWriter, r *http.Request, input any) bool {
	d := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096))
	d.DisallowUnknownFields()
	if err := d.Decode(input); err != nil {
		deviceFailure(w, deviceauth.ErrInvalid)
		return false
	}
	var extra any
	if d.Decode(&extra) != io.EOF {
		deviceFailure(w, deviceauth.ErrInvalid)
		return false
	}
	return true
}
func (s *Server) allowDeviceRequest(w http.ResponseWriter, r *http.Request, operation string) bool {
	peer, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		peer = r.RemoteAddr
	}
	if err := s.devices.Allow(operation, peer); err != nil {
		deviceFailure(w, err)
		return false
	}
	return true
}
func (s *Server) registerDevice(w http.ResponseWriter, r *http.Request) {
	if !s.allowDeviceRequest(w, r, "register") {
		return
	}
	var input struct {
		PublicKey string `json:"public_key"`
		Name      string `json:"name"`
	}
	if !decodeDeviceInput(w, r, &input) {
		return
	}
	device, err := s.devices.Register(input.PublicKey, input.Name)
	if err != nil {
		deviceFailure(w, err)
		return
	}
	writeJSON(w, 200, map[string]any{"device": device, "server_id": s.devices.ServerID()})
}
func (s *Server) challengeDevice(w http.ResponseWriter, r *http.Request) {
	if !s.allowDeviceRequest(w, r, "challenge") {
		return
	}
	var input struct {
		DeviceID string `json:"device_id"`
	}
	if !decodeDeviceInput(w, r, &input) {
		return
	}
	challenge, err := s.devices.Challenge(input.DeviceID)
	if err != nil {
		deviceFailure(w, err)
		return
	}
	writeJSON(w, 200, challenge)
}
func (s *Server) verifyDevice(w http.ResponseWriter, r *http.Request) {
	if !s.allowDeviceRequest(w, r, "verify") {
		return
	}
	var input struct {
		DeviceID    string `json:"device_id"`
		ChallengeID string `json:"challenge_id"`
		Signature   string `json:"signature"`
	}
	if !decodeDeviceInput(w, r, &input) {
		return
	}
	session, err := s.devices.Verify(input.DeviceID, input.ChallengeID, input.Signature)
	if err != nil {
		deviceFailure(w, err)
		return
	}
	writeJSON(w, 200, session)
}
func requireDeviceAdmin(w http.ResponseWriter, r *http.Request) bool {
	identity := currentAuth(r)
	if identity.Method == "owner" || (identity.Method == "device" && identity.Device.Admin) {
		return true
	}
	deviceFail(w, 403, "device_admin_required", "此操作需要设备管理员权限")
	return false
}
func (s *Server) listDevices(w http.ResponseWriter, r *http.Request) {
	if !requireDeviceAdmin(w, r) {
		return
	}
	identity := currentAuth(r)
	items, err := s.devices.List(identity.Device.ID, identity.Method == "owner")
	if err != nil {
		deviceFailure(w, err)
		return
	}
	writeJSON(w, 200, map[string]any{"devices": items, "server_id": s.devices.ServerID()})
}
func (s *Server) updateDevice(w http.ResponseWriter, r *http.Request) {
	if !requireDeviceAdmin(w, r) {
		return
	}
	var input struct {
		Revision int    `json:"revision"`
		Status   string `json:"status"`
		Admin    *bool  `json:"admin"`
	}
	if !decodeDeviceInput(w, r, &input) {
		return
	}
	identity := currentAuth(r)
	device, err := s.devices.Update(r.PathValue("id"), input.Revision, input.Status, input.Admin, identity.Device.ID, identity.Method == "owner")
	if err != nil {
		deviceFailure(w, err)
		return
	}
	if device.Status != "approved" {
		_ = s.push.set(device.ID, false)
	}
	writeJSON(w, 200, map[string]any{"device": device, "server_id": s.devices.ServerID()})
}
func (s *Server) logoutDevice(w http.ResponseWriter, r *http.Request) {
	if currentAuth(r).Method == "device" {
		_ = s.push.set(currentAuth(r).Device.ID, false)
		s.devices.Logout(strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer "))
	}
	writeJSON(w, 200, map[string]bool{"ok": true})
}
