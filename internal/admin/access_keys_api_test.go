package admin

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"chijie/internal/auth"
	"chijie/internal/fingerprint"
	"chijie/internal/pool"
)

func TestProxyAccessKeyAdminLifecycle(t *testing.T) {
	secret := "test-secret-for-access-key-api"
	keys, err := auth.LoadAccessKeys(filepath.Join(t.TempDir(), "keys.json"), secret)
	if err != nil {
		t.Fatal(err)
	}
	s := NewServer("127.0.0.1:0", pool.NewManager(), fingerprint.NewManager(), t.TempDir(), "admin-password", secret, "1h", nil)
	s.SetAccessKeys(keys)
	request := func(method, path, bearer string, payload any) *httptest.ResponseRecorder {
		var body []byte
		if payload != nil {
			body, _ = json.Marshal(payload)
		}
		r := newAdminRequest(method, path, bytes.NewReader(body))
		if bearer != "" {
			r.Header.Set("Authorization", "Bearer "+bearer)
		}
		response := httptest.NewRecorder()
		s.httpServer.Handler.ServeHTTP(response, r)
		return response
	}
	if request(http.MethodGet, "/api/auth/proxy-token", "", nil).Code != http.StatusUnauthorized {
		t.Fatal("access key listing lacked admin authentication")
	}
	login := request(http.MethodPost, "/api/auth/login", "", map[string]string{"password": "admin-password"})
	var credentials struct {
		Token string `json:"token"`
	}
	if login.Code != http.StatusOK || json.Unmarshal(login.Body.Bytes(), &credentials) != nil {
		t.Fatalf("login: %d %s", login.Code, login.Body.String())
	}
	created := request(http.MethodPost, "/api/auth/proxy-token", credentials.Token, map[string]string{"name": "bot", "duration": "never"})
	if created.Code != http.StatusCreated {
		t.Fatalf("create: %d %s", created.Code, created.Body.String())
	}
	var key ProxyTokenResponse
	if err := json.Unmarshal(created.Body.Bytes(), &key); err != nil {
		t.Fatal(err)
	}
	if key.ID == "" || key.Token == "" || key.ExpiresAt != "" || !keys.Verify(key.Token) {
		t.Fatalf("created key: %#v", key)
	}
	listed := request(http.MethodGet, "/api/auth/proxy-token", credentials.Token, nil)
	if listed.Code != http.StatusOK || !bytes.Contains(listed.Body.Bytes(), []byte(key.Token)) {
		t.Fatalf("list: %d %s", listed.Code, listed.Body.String())
	}
	revoked := request(http.MethodDelete, "/api/auth/proxy-token/"+key.ID, credentials.Token, nil)
	if revoked.Code != http.StatusOK || keys.Verify(key.Token) {
		t.Fatalf("revoke: %d %s", revoked.Code, revoked.Body.String())
	}
}
