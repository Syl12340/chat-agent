package web

import (
	"embed"
	"net/http"
)

//go:embed index.html app.js theme.js tasks.js companion.js life.js turns.js activities.js native.js native-care.js voice.js device-auth.js role-proposals.js topics.js style.css sw.js manifest.webmanifest icon.svg icon-192.png icon-512.png
var assets embed.FS

func Handler() http.Handler {
	files := http.FileServer(http.FS(assets))
	allowed := map[string]bool{"/": true, "/index.html": true, "/app.js": true, "/theme.js": true, "/tasks.js": true, "/companion.js": true, "/life.js": true, "/turns.js": true, "/native.js": true, "/style.css": true, "/sw.js": true, "/manifest.webmanifest": true, "/icon.svg": true, "/icon-192.png": true, "/icon-512.png": true}
	allowed["/activities.js"] = true
	allowed["/native-care.js"] = true
	allowed["/voice.js"] = true
	allowed["/device-auth.js"] = true
	allowed["/role-proposals.js"] = true
	allowed["/topics.js"] = true
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !allowed[r.URL.Path] {
			http.NotFound(w, r)
			return
		}
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		w.Header().Set("Cache-Control", "no-cache")
		if r.URL.Path == "/manifest.webmanifest" {
			w.Header().Set("Content-Type", "application/manifest+json")
		}
		files.ServeHTTP(w, r)
	})
}
