package main

import (
	"net/http"
	"os"
	"path/filepath"
	"strings"
)

// These pages get iterated on a lot and are opened straight from bookmarks and
// home-screen shortcuts on phones, which cache far more aggressively than
// desktop browsers — without no-store, a stale copy can silently keep being
// served after every fix, making it look like nothing changed.
func serveNoStore(w http.ResponseWriter, r *http.Request, name, contentType string) {
	path := filepath.Join(frontendDir, name)
	if _, err := os.Stat(path); err != nil {
		http.NotFound(w, r)
		return
	}
	if contentType != "" {
		w.Header().Set("Content-Type", contentType)
	}
	w.Header().Set("Cache-Control", "no-store")
	http.ServeFile(w, r, path)
}

func handleIndex(w http.ResponseWriter, r *http.Request) {
	// "/" is the catch-all pattern, so everything unmatched lands here; only
	// the bare root gets index.html, the rest falls through to static files.
	if r.URL.Path != "/" {
		serveStatic(w, r)
		return
	}
	if _, err := os.Stat(filepath.Join(frontendDir, "index.html")); err != nil {
		writeJSON(w, 200, map[string]string{
			"message": "Monitor API is running. Frontend not found.",
		})
		return
	}
	serveNoStore(w, r, "index.html", "")
}

var staticFS http.Handler

func serveStatic(w http.ResponseWriter, r *http.Request) {
	if staticFS == nil {
		http.NotFound(w, r)
		return
	}
	// Directory listings would expose the frontend tree for no benefit; a
	// trailing-slash request falls back to that directory's index.html.
	if strings.HasSuffix(r.URL.Path, "/") {
		candidate := filepath.Join(frontendDir, filepath.Clean(r.URL.Path), "index.html")
		if _, err := os.Stat(candidate); err != nil {
			http.NotFound(w, r)
			return
		}
	}
	staticFS.ServeHTTP(w, r)
}

func initStatic() {
	if _, err := os.Stat(frontendDir); err == nil {
		staticFS = http.FileServer(http.Dir(frontendDir))
	}
}
