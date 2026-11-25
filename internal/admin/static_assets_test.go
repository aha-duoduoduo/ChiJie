package admin

import (
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"

	"chijie/internal/fingerprint"
	"chijie/internal/pool"
)

func TestAdminHTMLLoadsContentHashedAssets(t *testing.T) {
	s := NewServer("127.0.0.1:0", pool.NewManager(), fingerprint.NewManager(), t.TempDir(), "", "test-secret-long-enough", "1h", nil)
	for _, path := range []string{"/", "/traffic", "/system"} {
		rec := httptest.NewRecorder()
		s.httpServer.Handler.ServeHTTP(rec, newAdminRequest(http.MethodGet, path, nil))
		if rec.Code != http.StatusOK || rec.Header().Get("Cache-Control") != "no-store" {
			t.Fatalf("%s: status=%d cache=%q", path, rec.Code, rec.Header().Get("Cache-Control"))
		}
		entry := regexp.MustCompile(`src="(/app-[A-Z0-9]+\.js)"`).FindStringSubmatch(rec.Body.String())
		if len(entry) != 2 {
			t.Fatalf("%s: missing content-hashed script", path)
		}
		asset := httptest.NewRecorder()
		s.httpServer.Handler.ServeHTTP(asset, newAdminRequest(http.MethodGet, entry[1], nil))
		if asset.Code != http.StatusOK || asset.Header().Get("Cache-Control") != "public, max-age=31536000, immutable" || !strings.Contains(asset.Header().Get("Content-Type"), "javascript") {
			t.Fatalf("%s: invalid script response: %d %v", entry[1], asset.Code, asset.Header())
		}
	}
}
