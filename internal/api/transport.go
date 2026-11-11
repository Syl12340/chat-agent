package api

import (
	"net/http"
	"time"
)

const remoteProtocolVersion = 1

var remoteCapabilities = []string{
	"ca.remote.v1",
	"ca.snapshot.v1",
	"ca.operation-id.v1",
	"ca.device-challenge.v1",
}

type transportInfo struct {
	ProtocolVersion int            `json:"protocol_version"`
	Generation      string         `json:"generation"`
	PeerID          string         `json:"peer_id"`
	Capabilities    []string       `json:"capabilities"`
	AllowedGroups   []string       `json:"allowed_groups"`
	Limits          map[string]int `json:"limits"`
	StartedAt       time.Time      `json:"started_at"`
	Carrier         string         `json:"carrier"`
}

func (s *Server) transportInfo(r *http.Request) transportInfo {
	carrier := r.Header.Get("X-Chat-Carrier")
	if carrier != "securewire" && carrier != "http" {
		carrier = "unknown"
	}
	return transportInfo{
		ProtocolVersion: remoteProtocolVersion,
		Generation:      s.transportGeneration,
		PeerID:          s.devices.ServerID(),
		Capabilities:    append([]string(nil), remoteCapabilities...),
		AllowedGroups:   []string{"status", "sessions", "turns", "plugins", "media", "settings"},
		Limits: map[string]int{
			"request_bytes":       128 << 10,
			"image_request_bytes": 4 << 20,
			"concurrent_runs":     4,
		},
		StartedAt: s.transportStartedAt,
		Carrier:   carrier,
	}
}

func (s *Server) transportStatus(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, s.transportInfo(r))
}

func (s *Server) transportResync(r *http.Request) bool {
	return r.Header.Get("X-Chat-Generation") != s.transportGeneration
}

func (s *Server) setTransportHeaders(w http.ResponseWriter) {
	w.Header().Set("X-Chat-Protocol", "1")
	w.Header().Set("X-Chat-Generation", s.transportGeneration)
}
