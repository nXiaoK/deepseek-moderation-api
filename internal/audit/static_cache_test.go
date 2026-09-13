package audit

import (
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

func TestStaticAssetCachePolicy(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "assets"), 0700); err != nil {
		t.Fatal(err)
	}
	for name, body := range map[string]string{"index.html": "<!doctype html><title>Audit</title>", "assets/app-AbCd1234.js": "export default 1", "assets/style-EfGh5678.css": "body{}", "assets/plain.js": "export default 0"} {
		if err := os.WriteFile(filepath.Join(root, name), []byte(body), 0600); err != nil {
			t.Fatal(err)
		}
	}
	app, err := NewServer(&Store{}, "http://localhost:8090", root)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		path, cache string
		status      int
	}{
		{"/assets/app-AbCd1234.js", "public, max-age=31536000, immutable", 200},
		{"/assets/style-EfGh5678.css", "public, max-age=31536000, immutable", 200},
		{"/assets/plain.js", "no-cache", 200}, {"/", "no-cache", 200}, {"/index.html", "no-cache", 301},
		{"/assets/missing-AbCd1234.js", "no-cache", 200},
		{"/admin/session", "no-store", 401}, {"/healthz", "no-store", 200},
	} {
		w := httptest.NewRecorder()
		app.Handler().ServeHTTP(w, httptest.NewRequest("GET", tc.path, nil))
		if w.Code != tc.status || w.Header().Get("Cache-Control") != tc.cache {
			t.Fatalf("%s: %d, %s", tc.path, w.Code, w.Header().Get("Cache-Control"))
		}
	}
}
