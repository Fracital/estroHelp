package server

import (
	"net/http"
	"os"
	"path"
	"path/filepath"
	"strings"
)

// static serves the Astro build with clean URLs: /chat resolves to chat.html or
// chat/index.html, hashed assets get long cache headers, and unknown paths get
// 404.html when the build has one.
type static struct {
	dir string
	fs  http.Handler
}

func newStatic(dir string) http.Handler {
	return &static{dir: dir, fs: http.FileServer(http.Dir(dir))}
}

func (s *static) resolve(p string) (string, bool) {
	clean := filepath.Join(s.dir, filepath.FromSlash(path.Clean("/"+p)))
	if !strings.HasPrefix(clean, filepath.Clean(s.dir)) {
		return "", false
	}
	candidates := []string{clean, clean + ".html", filepath.Join(clean, "index.html")}
	for _, c := range candidates {
		if st, err := os.Stat(c); err == nil && !st.IsDir() {
			return c, true
		}
	}
	return "", false
}

func (s *static) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if strings.HasPrefix(r.URL.Path, "/api/") || strings.HasPrefix(r.URL.Path, "/v1/") {
		http.NotFound(w, r)
		return
	}
	if strings.HasPrefix(r.URL.Path, "/_astro/") {
		w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
	}
	// crossOriginIsolated pages get SharedArrayBuffer, which the in-browser
	// workspace runtime needs. credentialless keeps third-party fonts loading.
	w.Header().Set("Cross-Origin-Opener-Policy", "same-origin")
	w.Header().Set("Cross-Origin-Embedder-Policy", "credentialless")
	file, ok := s.resolve(r.URL.Path)
	if !ok {
		if nf, ok := s.resolve("/404"); ok {
			w.WriteHeader(http.StatusNotFound)
			http.ServeFile(w, r, nf)
			return
		}
		http.NotFound(w, r)
		return
	}
	if strings.HasSuffix(file, ".html") {
		w.Header().Set("Cache-Control", "no-cache")
	}
	http.ServeFile(w, r, file)
}
