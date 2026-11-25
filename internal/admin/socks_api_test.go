package admin

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"chijie/internal/fingerprint"
	"chijie/internal/pool"
	proxyserver "chijie/internal/server"
)

func TestSOCKSAccountAdminLifecycle(t *testing.T) {
	store, err := proxyserver.LoadSocksAccounts(filepath.Join(t.TempDir(), "runtime", "socks.json"))
	if err != nil {
		t.Fatal(err)
	}
	admin := NewServer("127.0.0.1:0", pool.NewManager(), fingerprint.NewManager(), t.TempDir(), "admin-password", "test-admin-secret-12345", "1h", nil)
	admin.SetSocksAccounts(store, "proxy.example.test:1080")
	call := func(method, path, token string, payload any) *httptest.ResponseRecorder {
		var body []byte
		if payload != nil {
			body, _ = json.Marshal(payload)
		}
		req := newAdminRequest(method, path, bytes.NewReader(body))
		if token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		rec := httptest.NewRecorder()
		admin.httpServer.Handler.ServeHTTP(rec, req)
		return rec
	}
	if got := call("GET", "/api/socks/accounts", "", nil).Code; got != http.StatusUnauthorized {
		t.Fatalf("unauthenticated GET = %d", got)
	}
	login := call("POST", "/api/auth/login", "", map[string]string{"password": "admin-password"})
	if login.Code != http.StatusOK {
		t.Fatalf("login = %d: %s", login.Code, login.Body.String())
	}
	var credentials struct {
		Token string `json:"token"`
	}
	if err := json.Unmarshal(login.Body.Bytes(), &credentials); err != nil || credentials.Token == "" {
		t.Fatalf("login token: %v", err)
	}
	account := proxyserver.SocksAccount{Username: "bot", Enabled: true, Egress: proxyserver.EgressOptions{Direct: true}, AffinityTTL: "1h"}
	created := call("POST", "/api/socks/accounts", credentials.Token, map[string]any{"account": account})
	if created.Code != http.StatusCreated {
		t.Fatalf("create = %d: %s", created.Code, created.Body.String())
	}
	var result struct {
		Password   string `json:"password"`
		Connection string `json:"connection"`
	}
	if err := json.Unmarshal(created.Body.Bytes(), &result); err != nil || result.Password == "" {
		t.Fatalf("create response: %v", err)
	}
	if !strings.HasPrefix(result.Connection, "socks5h://bot:") {
		t.Fatalf("connection = %q", result.Connection)
	}
	if _, ok := store.Authenticate("bot", result.Password); !ok {
		t.Fatal("new account rejected password")
	}
	listed := call("GET", "/api/socks/accounts", credentials.Token, nil)
	if listed.Code != http.StatusOK || strings.Contains(listed.Body.String(), result.Password) || strings.Contains(listed.Body.String(), "password_hash") {
		t.Fatalf("list leaked password: %s", listed.Body.String())
	}
	account.Enabled = false
	updated := call("PUT", "/api/socks/accounts/bot", credentials.Token, map[string]any{"account": account})
	if updated.Code != http.StatusOK {
		t.Fatalf("update = %d: %s", updated.Code, updated.Body.String())
	}
	if _, ok := store.Authenticate("bot", result.Password); ok {
		t.Fatal("disabled account accepted password")
	}
	deleted := call("DELETE", "/api/socks/accounts/bot", credentials.Token, nil)
	if deleted.Code != http.StatusOK || len(store.List()) != 0 {
		t.Fatalf("delete = %d", deleted.Code)
	}
}
