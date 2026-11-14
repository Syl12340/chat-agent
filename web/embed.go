package web

import (
	"embed"
	"net/http"
)

//go:embed index.html app.js transport.js theme.js tasks.js companion.js life.js turns.js activities.js native.js native-care.js voice.js device-auth.js model-configs.js host-tools.js role-proposals.js topics.js shell-bridge.js native-capabilities.js generated-image.js tool-view.js tool-card.css surface.css chat-input.js composer-preferences.js markdown.js math-render.js richtext.css dsh-look.css tokens.css vendor/katex/katex.mjs vendor/katex/LICENSE.txt chat-ui.css desktop-workspace.js desktop-workspace.css ui/*.js ui/ui.css style.css sw.js manifest.webmanifest icon-192.png icon-512.png apple-touch-icon.png
var assets embed.FS

func Handler() http.Handler {
	files := http.FileServer(http.FS(assets))
	allowed := map[string]bool{"/": true, "/index.html": true, "/app.js": true, "/theme.js": true, "/tasks.js": true, "/companion.js": true, "/life.js": true, "/turns.js": true, "/native.js": true, "/style.css": true, "/tokens.css": true, "/dsh-look.css": true, "/sw.js": true, "/manifest.webmanifest": true, "/icon-192.png": true, "/icon-512.png": true, "/apple-touch-icon.png": true}
	allowed["/activities.js"] = true
	allowed["/native-care.js"] = true
	allowed["/voice.js"] = true
	allowed["/device-auth.js"] = true
	allowed["/model-configs.js"] = true
	allowed["/host-tools.js"] = true
	allowed["/role-proposals.js"] = true
	allowed["/topics.js"] = true
	allowed["/shell-bridge.js"] = true
	allowed["/native-capabilities.js"] = true
	allowed["/generated-image.js"] = true
	allowed["/tool-view.js"] = true
	allowed["/transport.js"] = true
	allowed["/tool-card.css"] = true
	allowed["/surface.css"] = true
	allowed["/chat-input.js"] = true
	allowed["/chat-ui.css"] = true
	for _, path := range []string{"/composer-preferences.js", "/markdown.js", "/math-render.js", "/richtext.css", "/vendor/katex/katex.mjs", "/vendor/katex/LICENSE.txt", "/ui/structures.js"} {
		allowed[path] = true
	}
	for _, path := range []string{"/desktop-workspace.js", "/desktop-workspace.css", "/ui/protocol.js", "/ui/presentation.js", "/ui/state.js", "/ui/render.js", "/ui/quiz.js", "/ui/charts.js", "/ui/vectors.js", "/ui/ui.css"} {
		allowed[path] = true
	}
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
		if r.URL.Path == "/vendor/katex/katex.mjs" {
			w.Header().Set("Content-Type", "text/javascript; charset=utf-8")
		}
		if r.URL.Path == "/manifest.webmanifest" {
			w.Header().Set("Content-Type", "application/manifest+json")
		}
		files.ServeHTTP(w, r)
	})
}
