// Package modelconfig stores operator-managed chat routes. Credentials never
// leave this package through its public projection.
package modelconfig

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	"chat-agent/internal/storage"
)

var ErrNotFound = errors.New("model configuration not found")
var ErrInvalid = errors.New("invalid model configuration")

type Route struct {
	ID       string   `json:"id"`
	Label    string   `json:"label"`
	BaseURL  string   `json:"base_url"`
	APIKey   string   `json:"api_key"`
	Model    string   `json:"model"`
	Vision   bool     `json:"vision"`
	Thinking []string `json:"thinking"`
}

type PublicRoute struct {
	ID         string   `json:"id"`
	Label      string   `json:"label"`
	BaseURL    string   `json:"base_url"`
	Model      string   `json:"model"`
	Vision     bool     `json:"vision"`
	Thinking   []string `json:"thinking"`
	Configured bool     `json:"configured"`
}

type state struct {
	Version int     `json:"version"`
	Routes  []Route `json:"routes"`
}

type Store struct {
	mu     sync.RWMutex
	path   string
	routes map[string]Route
}

func Open(dir string) (*Store, error) {
	if err := os.MkdirAll(dir, 0700); err != nil {
		return nil, err
	}
	s := &Store{path: filepath.Join(dir, "state.json"), routes: map[string]Route{}}
	b, err := os.ReadFile(s.path)
	if errors.Is(err, os.ErrNotExist) {
		return s, nil
	}
	if err != nil {
		return nil, err
	}
	if len(b) > 256<<10 {
		return nil, ErrInvalid
	}
	d := json.NewDecoder(bytes.NewReader(b))
	d.DisallowUnknownFields()
	var st state
	var extra any
	if d.Decode(&st) != nil || d.Decode(&extra) != io.EOF || st.Version != 1 || len(st.Routes) > 64 {
		return nil, ErrInvalid
	}
	for _, route := range st.Routes {
		normalized, err := normalize(route)
		if err != nil || s.routes[normalized.ID].ID != "" {
			return nil, ErrInvalid
		}
		s.routes[normalized.ID] = normalized
	}
	return s, nil
}

func (s *Store) List() []Route {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]Route, 0, len(s.routes))
	for _, route := range s.routes {
		out = append(out, clone(route))
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

func (s *Store) Get(id string) (Route, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	route, ok := s.routes[id]
	return clone(route), ok
}

func (s *Store) Put(route Route) (Route, error) {
	normalized, err := normalize(route)
	if err != nil {
		return Route{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	next := cloneMap(s.routes)
	next[normalized.ID] = normalized
	if err := s.save(next); err != nil {
		return Route{}, err
	}
	s.routes = next
	return clone(normalized), nil
}

func (s *Store) Delete(id string) error {
	if !storage.ValidID(id) {
		return ErrInvalid
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.routes[id]; !ok {
		return ErrNotFound
	}
	next := cloneMap(s.routes)
	delete(next, id)
	if err := s.save(next); err != nil {
		return err
	}
	s.routes = next
	return nil
}

func (s *Store) save(routes map[string]Route) error {
	items := make([]Route, 0, len(routes))
	for _, route := range routes {
		items = append(items, route)
	}
	sort.Slice(items, func(i, j int) bool { return items[i].ID < items[j].ID })
	b, err := json.Marshal(state{Version: 1, Routes: items})
	if err != nil {
		return err
	}
	return storage.Replace(s.path, b)
}

func Public(route Route) PublicRoute {
	return PublicRoute{ID: route.ID, Label: route.Label, BaseURL: route.BaseURL, Model: route.Model, Vision: route.Vision, Thinking: append([]string(nil), route.Thinking...), Configured: route.APIKey != ""}
}

func normalize(route Route) (Route, error) {
	route.ID, route.Label, route.BaseURL, route.APIKey, route.Model = strings.TrimSpace(route.ID), strings.TrimSpace(route.Label), strings.TrimRight(strings.TrimSpace(route.BaseURL), "/"), strings.TrimSpace(route.APIKey), strings.TrimSpace(route.Model)
	if !storage.ValidID(route.ID) || route.Label == "" || len(route.Label) > 128 || route.Model == "" || len(route.Model) > 128 || len(route.APIKey) > 4096 || !printableKey(route.APIKey) {
		return Route{}, ErrInvalid
	}
	u, err := url.Parse(route.BaseURL)
	if err != nil || u.User != nil || u.RawQuery != "" || u.Fragment != "" || u.Host == "" || u.Path == "" {
		return Route{}, ErrInvalid
	}
	host := u.Hostname()
	ip := net.ParseIP(host)
	if u.Scheme != "https" && !(u.Scheme == "http" && (strings.EqualFold(host, "localhost") || ip != nil && ip.IsLoopback())) {
		return Route{}, ErrInvalid
	}
	if len(route.Thinking) == 0 || len(route.Thinking) > 4 {
		return Route{}, ErrInvalid
	}
	seen := map[string]bool{}
	off := false
	for _, level := range route.Thinking {
		if seen[level] || level != "off" && level != "medium" && level != "high" && level != "ultra" {
			return Route{}, ErrInvalid
		}
		seen[level] = true
		off = off || level == "off"
	}
	if !off {
		return Route{}, ErrInvalid
	}
	if len(route.Thinking) > 1 && route.Model != "ecnu-max" && route.Model != "ecnu-plus" {
		return Route{}, ErrInvalid
	}
	return route, nil
}

func printableKey(value string) bool {
	if value == "" {
		return false
	}
	for _, r := range value {
		if r < 0x21 || r > 0x7e {
			return false
		}
	}
	return true
}
func clone(route Route) Route {
	route.Thinking = append([]string(nil), route.Thinking...)
	return route
}
func cloneMap(in map[string]Route) map[string]Route {
	out := make(map[string]Route, len(in))
	for k, v := range in {
		out[k] = clone(v)
	}
	return out
}
