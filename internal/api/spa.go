package api

import (
	"io/fs"
	"net/http"
	"path"
	"strings"
)

const placeholderHTML = `<!doctype html><meta charset="utf-8"><title>ShareDirStat</title>
<style>body{font:15px/1.5 system-ui,sans-serif;max-width:40rem;margin:4rem auto;padding:0 1rem;color:#1c1d1f;background:#fff}code{background:#eef;padding:.1em .3em;border-radius:3px}@media(prefers-color-scheme:dark){body{background:#151617;color:#e7e7e7}code{background:#2a2b3a}}</style>
<h1>ShareDirStat</h1>
<p>The server is running, but the web UI was not built into this binary.</p>
<p>Build it with <code>make web</code> (or use the container image), then rebuild. The JSON API is available under <code>api/v1/</code> — try <code><a href="api/v1/shares">api/v1/shares</a></code>.</p>`

// spaHandler serves the embedded single-page app with hash routing:
// existing files are served (hashed assets as immutable), everything else
// falls back to index.html. When index.html is absent a placeholder is served.
func spaHandler(ui fs.FS) http.Handler {
	if _, err := fs.Stat(ui, "index.html"); err != nil {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path != "/" {
				http.NotFound(w, r)
				return
			}
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			w.Header().Set("Cache-Control", "no-cache")
			_, _ = w.Write([]byte(placeholderHTML))
		})
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		p := strings.TrimPrefix(path.Clean("/"+r.URL.Path), "/")
		if p == "" {
			p = "index.html"
		}
		if st, err := fs.Stat(ui, p); err == nil && !st.IsDir() {
			if strings.HasPrefix(p, "assets/") {
				w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
			} else {
				w.Header().Set("Cache-Control", "no-cache")
			}
			http.ServeFileFS(w, r, ui, p)
			return
		}
		// Unknown path: hash-routed SPA, serve the shell.
		w.Header().Set("Cache-Control", "no-cache")
		r.URL.Path = "/"
		http.ServeFileFS(w, r, ui, "index.html")
	})
}
