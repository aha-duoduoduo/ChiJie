package admin

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestTrustedProxyHeadersRequireConfiguredPeer(t *testing.T) {
	s := &Server{}
	if err := s.SetTrustedProxies([]string{"10.0.0.0/24"}); err != nil {
		t.Fatal(err)
	}
	r := newAdminRequest("POST", "/api/auth/login", nil)
	r.RemoteAddr = "192.0.2.1:1234"
	r.Header.Set("CF-Connecting-IP", "203.0.113.9")
	if got := s.clientIP(r); got != "192.0.2.1" {
		t.Fatal(got)
	}
	r.RemoteAddr = "10.0.0.10:1234"
	if got := s.clientIP(r); got != "203.0.113.9" {
		t.Fatal(got)
	}
	r.Header.Del("CF-Connecting-IP")
	r.Header.Set("X-Forwarded-For", "1.1.1.1, 198.51.100.7, 10.0.0.11")
	if got := s.clientIP(r); got != "198.51.100.7" {
		t.Fatal(got)
	}
}

func TestLocalAdminHostAndOrigin(t *testing.T) {
	s := &Server{}
	handler := s.browserRequestGuard(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(204) }))
	for _, test := range []struct {
		host, origin, site string
		status             int
	}{
		{"localhost:9090", "http://localhost:9090", "same-origin", 204},
		{"127.0.0.1:9090", "", "", 204},
		{"rebound.example:9090", "http://rebound.example:9090", "same-origin", 403},
		{"localhost:9090", "https://evil.example", "cross-site", 403},
		{"localhost:9090", "null", "", 403},
	} {
		r := newAdminRequest("POST", "/api/socks/accounts", nil)
		r.Host = test.host
		r.Header.Set("Origin", test.origin)
		r.Header.Set("Sec-Fetch-Site", test.site)
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		if w.Code != test.status {
			t.Fatalf("%+v => %d", test, w.Code)
		}
	}
}
