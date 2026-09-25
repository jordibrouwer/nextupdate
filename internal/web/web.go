// Package web serves the embedded nextupdate UI.
package web

import (
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"io/fs"
	"mime"
	"net/http"
	"path"
	"strings"
)

//go:embed static
var files embed.FS

const csp = "default-src 'self'; script-src 'self'; style-src 'self'; img-src 'self' data:; connect-src 'self'; " +
	"manifest-src 'self'; worker-src 'self'; object-src 'none'; base-uri 'none'; form-action 'self'; frame-ancestors 'none'"

func init() {
	// Go's built-in table lacks these on some systems.
	mime.AddExtensionType(".webmanifest", "application/manifest+json")
	mime.AddExtensionType(".js", "text/javascript; charset=utf-8")
	mime.AddExtensionType(".mjs", "text/javascript; charset=utf-8")
}

type asset struct {
	body []byte
	etag string
	ct   string
}

// Handler serves the embedded files.
func Handler() http.Handler {
	sub, err := fs.Sub(files, "static")
	if err != nil {
		panic(err)
	}
	assets := map[string]asset{}
	err = fs.WalkDir(sub, ".", func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		b, err := fs.ReadFile(sub, p)
		if err != nil {
			return err
		}
		sum := sha256.Sum256(b)
		ct := mime.TypeByExtension(path.Ext(p))
		if ct == "" {
			ct = http.DetectContentType(b)
		}
		assets["/"+p] = asset{body: b, etag: `"` + hex.EncodeToString(sum[:8]) + `"`, ct: ct}
		return nil
	})
	if err != nil {
		panic(err)
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("Content-Security-Policy", csp)
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("X-Frame-Options", "DENY")
		h.Set("Referrer-Policy", "same-origin")
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			h.Set("Allow", "GET, HEAD")
			http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
			return
		}
		p := r.URL.Path
		if p == "/" {
			p = "/index.html"
		}
		if clean := path.Clean(p); clean != p || strings.Contains(p, "..") {
			http.NotFound(w, r)
			return
		}
		a, ok := assets[p]
		if !ok {
			http.NotFound(w, r)
			return
		}
		h.Set("Content-Type", a.ct)
		h.Set("Cache-Control", "no-cache")
		h.Set("ETag", a.etag)
		if match := r.Header.Get("If-None-Match"); match == a.etag {
			w.WriteHeader(http.StatusNotModified)
			return
		}
		if r.Method == http.MethodGet {
			w.Write(a.body)
		}
	})
}

// Mount serves the API under /api/ and the UI everywhere else.
func Mount(api http.Handler) http.Handler {
	mux := http.NewServeMux()
	mux.Handle("/api/", api)
	mux.Handle("/", Handler())
	return mux
}
