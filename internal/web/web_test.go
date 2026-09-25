package web

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func get(t *testing.T, h http.Handler, method, path string, header map[string]string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, path, nil)
	for k, v := range header {
		req.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func TestIndexHasSecurityHeadersAndETag(t *testing.T) {
	rec := get(t, Handler(), "GET", "/", nil)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "<title>") {
		t.Fatalf("index: %d %q", rec.Code, rec.Body.String())
	}
	csp := rec.Header().Get("Content-Security-Policy")
	for _, want := range []string{"default-src 'self'", "script-src 'self'", "style-src 'self'", "object-src 'none'", "frame-ancestors 'none'"} {
		if !strings.Contains(csp, want) {
			t.Errorf("CSP lacks %q: %s", want, csp)
		}
	}
	if strings.Contains(csp, "unsafe-inline") || strings.Contains(csp, "unsafe-eval") {
		t.Errorf("CSP must not allow unsafe code: %s", csp)
	}
	h := rec.Header()
	if h.Get("Cache-Control") != "no-cache" || h.Get("X-Content-Type-Options") != "nosniff" || h.Get("X-Frame-Options") != "DENY" || h.Get("Referrer-Policy") != "same-origin" {
		t.Errorf("headers %v", h)
	}
	if !strings.HasPrefix(h.Get("Etag"), `"`) {
		t.Errorf("etag %q", h.Get("Etag"))
	}
	if ct := h.Get("Content-Type"); !strings.HasPrefix(ct, "text/html") {
		t.Errorf("content type %q", ct)
	}
}

func TestETagGivesNotModified(t *testing.T) {
	etag := get(t, Handler(), "GET", "/", nil).Header().Get("Etag")
	rec := get(t, Handler(), "GET", "/", map[string]string{"If-None-Match": etag})
	if rec.Code != http.StatusNotModified || rec.Body.Len() != 0 {
		t.Fatalf("conditional get: %d body %q", rec.Code, rec.Body.String())
	}
}

func TestContentTypes(t *testing.T) {
	for path, want := range map[string]string{
		"/js/app.js": "text/javascript",
	} {
		rec := get(t, Handler(), "GET", path, nil)
		if rec.Code != http.StatusOK || !strings.HasPrefix(rec.Header().Get("Content-Type"), want) {
			t.Errorf("%s: %d %q", path, rec.Code, rec.Header().Get("Content-Type"))
		}
	}
}

func TestUnknownAndUnsafePaths(t *testing.T) {
	h := Handler()
	for _, p := range []string{"/nope.txt", "/js", "/js/", "/../go.mod", "/%2e%2e/go.mod", "/js/../../go.mod"} {
		if rec := get(t, h, "GET", p, nil); rec.Code != http.StatusNotFound && rec.Code != http.StatusMovedPermanently {
			t.Errorf("%s: %d", p, rec.Code)
		}
	}
	if rec := get(t, h, "POST", "/", nil); rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("POST: %d", rec.Code)
	}
}

func TestMountRoutesAPIAndStatic(t *testing.T) {
	api := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write([]byte("api:" + r.URL.Path)) })
	h := Mount(api)
	if rec := get(t, h, "GET", "/api/status", nil); rec.Body.String() != "api:/api/status" {
		t.Fatalf("api route: %q", rec.Body.String())
	}
	if rec := get(t, h, "GET", "/", nil); !strings.Contains(rec.Body.String(), "<title>") {
		t.Fatalf("static route: %q", rec.Body.String())
	}
}
