package admin

import (
	"bytes"
	"encoding/json"
	"net/http"
	"os"
	"sync/atomic"
	"time"

	"chijie/internal/dialer"
	"chijie/internal/pool"
	proxyserver "chijie/internal/server"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
)

func TestAddedSubscriptionStartsAutomaticRefresh(t *testing.T) {
	var requests atomic.Int32
	refreshed := make(chan struct{})
	subscription := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if requests.Add(1) == 2 {
			close(refreshed)
		}
		_, _ = w.Write([]byte("proxies:\n  - name: local-echo\n    type: socks5\n    server: 127.0.0.1\n    port: 1080\n"))
	}))
	defer subscription.Close()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "nodes.yaml"), []byte("node_pools: {}\n"), 0600); err != nil {
		t.Fatal(err)
	}
	manager := pool.NewManager()
	defer manager.Close()
	s := NewServer("127.0.0.1:0", manager, nil, dir, "", "test-admin-secret-12345", "1h", nil)
	body, err := json.Marshal(map[string]any{"name": "automatic", "config": &pool.PoolConfig{Source: "subscription", URL: subscription.URL, AllowPrivateHost: true, UpdateInterval: "20ms"}})
	if err != nil {
		t.Fatal(err)
	}
	w := httptest.NewRecorder()
	s.httpServer.Handler.ServeHTTP(w, newAdminRequest("POST", "/api/nodes", bytes.NewReader(body)))
	if w.Code != http.StatusCreated {
		t.Fatalf("add subscription: %d %s", w.Code, w.Body)
	}
	select {
	case <-refreshed:
	case <-time.After(2 * time.Second):
		t.Fatal("new subscription did not refresh automatically")
	}
}

func TestNodeValidationAcceptsSupportedTLSProtocols(t *testing.T) {
	for _, protocol := range []string{"anytls", "tuic"} {
		if err := validateNodeConfig(&dialer.Node{Name: protocol, Type: protocol, Server: "proxy.example", Port: 443}); err != nil {
			t.Fatalf("%s: %v", protocol, err)
		}
	}
}

func TestRegressionLoginHeaderBypass(t *testing.T) {
	s := NewServer("127.0.0.1:0", nil, nil, t.TempDir(), "correct-password", "audit-secret-at-least-16", "1h", nil)
	statuses := []int{}
	for i := 0; i < 7; i++ {
		r := newAdminRequest("POST", "/api/auth/login", strings.NewReader(`{"password":"wrong"}`))
		r.RemoteAddr = "192.0.2.1:1234"
		if i == 6 {
			r.Header.Set("CF-Connecting-IP", "192.0.2.99")
		}
		w := httptest.NewRecorder()
		s.httpServer.Handler.ServeHTTP(w, r)
		statuses = append(statuses, w.Code)
	}
	if statuses[5] != 429 || statuses[6] != 429 {
		t.Fatalf("unexpected statuses %v", statuses)
	}
}

func TestRegressionLocalAdminCrossOrigin(t *testing.T) {
	dir := t.TempDir()
	s := NewServer("127.0.0.1:0", nil, nil, dir, "", "audit-secret-at-least-16", "1h", nil)
	accounts, err := proxyserver.LoadSocksAccounts(filepath.Join(dir, "socks.json"))
	if err != nil {
		t.Fatal(err)
	}
	s.SetSocksAccounts(accounts, "")
	r := newAdminRequest("POST", "/api/socks/accounts", strings.NewReader(`{"account":{"username":"audit","enabled":true,"egress":{"direct":true}},"password":"audit-password-123"}`))
	r.Header.Set("Content-Type", "text/plain")
	r.Header.Set("Origin", "https://untrusted.example")
	r.Header.Set("Sec-Fetch-Site", "cross-site")
	w := httptest.NewRecorder()
	s.httpServer.Handler.ServeHTTP(w, r)
	if w.Code != 403 {
		t.Fatalf("status=%d body=%s", w.Code, w.Body)
	}
	if _, ok := accounts.Authenticate("audit", "audit-password-123"); ok {
		t.Fatal("cross-site request created an account")
	}
}

func TestRegressionInvalidSubscriptionIntervals(t *testing.T) {
	for _, raw := range []string{"0s", "-1s"} {
		cfg := &pool.PoolConfig{Source: "subscription", URL: "https://example.com/sub", UpdateInterval: raw}
		if err := validatePoolConfig(cfg); err == nil {
			t.Fatalf("accepted unsafe interval %s", raw)
		}
		if _, err := pool.ParseDurationWithDays(raw); err == nil {
			t.Fatalf("parsed unsafe interval %s", raw)
		}

	}
}
