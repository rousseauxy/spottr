package api

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/fstest"
)

func TestSPAHandler(t *testing.T) {
	fsys := fstest.MapFS{
		"index.html":  &fstest.MapFile{Data: []byte("<!doctype html><title>app</title>")},
		"favicon.svg": &fstest.MapFile{Data: []byte("<svg/>")},
	}
	h := spaHandler(fsys)

	cases := []struct {
		path, wantType string
		wantStatus     int
	}{
		{"/favicon.svg", "image/svg+xml", http.StatusOK},
		{"/spots/123", "text/html", http.StatusOK},
		{"/", "text/html", http.StatusOK},
		// A missing file must not come back as the app page: Cloudflare caches it by extension.
		{"/missing.svg", "", http.StatusNotFound},
		{"/_app/immutable/gone.js", "", http.StatusNotFound},
	}
	for _, c := range cases {
		rec := httptest.NewRecorder()
		h(rec, httptest.NewRequest(http.MethodGet, c.path, nil))
		if rec.Code != c.wantStatus {
			t.Errorf("%s: status %d, want %d", c.path, rec.Code, c.wantStatus)
		}
		if c.wantType != "" && !strings.HasPrefix(rec.Header().Get("Content-Type"), c.wantType) {
			t.Errorf("%s: content type %q, want %s", c.path, rec.Header().Get("Content-Type"), c.wantType)
		}
	}
}
