package admin

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"chijie/internal/fingerprint"
	"chijie/internal/pool"
	proxyserver "chijie/internal/server"
	"chijie/internal/traffic"
)

type fakeProxyRuntime struct {
	settings proxyserver.ProxySettings
}

func (f *fakeProxyRuntime) ProxySettings() proxyserver.ProxySettings {
	return f.settings
}

func (f *fakeProxyRuntime) UpdateProxySettings(settings proxyserver.ProxySettings) {
	f.settings = settings
}

func TestParseTokenDurationSupportsDays(t *testing.T) {
	got, err := parseTokenDuration("30d", 24*time.Hour)
	if err != nil {
		t.Fatalf("parse duration: %v", err)
	}
	if got != 30*24*time.Hour {
		t.Fatalf("duration = %s, want 720h", got)
	}
}

func TestParseTokenDurationRejectsOverLimit(t *testing.T) {
	if _, err := parseTokenDuration("366d", 24*time.Hour); err == nil {
		t.Fatalf("expected over-limit duration error")
	}
}

func TestClientIPFromRequestIgnoresUntrustedCloudflareHeader(t *testing.T) {
	req := newAdminRequest("POST", "/api/auth/login", nil)
	req.RemoteAddr = "10.0.0.10:3456"
	req.Header.Set("CF-Connecting-IP", "203.0.113.9")
	req.Header.Set("X-Forwarded-For", "198.51.100.7")

	if got := clientIPFromRequest(req); got != "10.0.0.10" {
		t.Fatalf("clientIPFromRequest = %q, want immediate peer IP", got)
	}
}

func TestClientIPFromRequestIgnoresUntrustedForwardedValues(t *testing.T) {
	req := newAdminRequest("POST", "/api/auth/login", nil)
	req.RemoteAddr = "10.0.0.10:3456"
	req.Header.Set("CF-Connecting-IP", "not-an-ip")
	req.Header.Set("X-Forwarded-For", "bad, 198.51.100.7")

	if got := clientIPFromRequest(req); got != "10.0.0.10" {
		t.Fatalf("clientIPFromRequest = %q, want immediate peer IP", got)
	}
}

func TestLoadFingerprintsConfigAllowsMissingFile(t *testing.T) {
	cfg, err := loadFingerprintsConfig(filepath.Join(t.TempDir(), "fingerprints.yaml"))
	if err != nil {
		t.Fatalf("load missing fingerprints config: %v", err)
	}
	if cfg.Fingerprints == nil {
		t.Fatalf("fingerprints map should be initialized")
	}
	if len(cfg.Fingerprints) != 0 {
		t.Fatalf("expected empty fingerprints map, got %d entries", len(cfg.Fingerprints))
	}
}

func TestValidatePoolConfigAcceptsHTTPSChijieTemplate(t *testing.T) {
	err := validatePoolConfig(&pool.PoolConfig{
		Source:       "template",
		TemplateType: "chijie",
		Endpoint:     "https://b.example.com",
		BearerToken:  "token",
		Coverage:     "both",
		Priority:     100,
	})
	if err != nil {
		t.Fatalf("validate chijie template: %v", err)
	}
}

func TestValidatePoolConfigRejectsHTTPChijieTemplate(t *testing.T) {
	err := validatePoolConfig(&pool.PoolConfig{
		Source:       "template",
		TemplateType: "chijie",
		Endpoint:     "http://b.example.com",
		BearerToken:  "token",
	})
	if err == nil {
		t.Fatalf("expected http chijie template to be rejected")
	}
}

func TestValidatePoolConfigAcceptsManualAndDailySubscriptionRefresh(t *testing.T) {
	for _, interval := range []string{"", "3d"} {
		err := validatePoolConfig(&pool.PoolConfig{
			Source:         "subscription",
			URL:            "https://example.com/sub",
			UpdateInterval: interval,
		})
		if err != nil {
			t.Fatalf("validate subscription interval %q: %v", interval, err)
		}
	}
}

func TestSubscriptionNodeConfigPersistsPremiumTag(t *testing.T) {
	dir := t.TempDir()
	subscription := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("ss://Y2hhY2hhMjAtaWV0Zi1wb2x5MTMwNTpwYXNz@proxy.example.com:1080#US%20Node%2001"))
	}))
	defer subscription.Close()

	nodesPath := filepath.Join(dir, "nodes.yaml")
	if err := os.WriteFile(nodesPath, []byte(`
node_pools:
  provider-a:
    source: subscription
    url: `+subscription.URL+`
`), 0600); err != nil {
		t.Fatalf("write nodes config: %v", err)
	}

	manager := pool.NewManager()
	server := NewServer("127.0.0.1:0", manager, fingerprint.NewManager(), dir, "", "1234567890123456", "24h", nil)

	reqBody := []byte(`{
		"pool":"provider-a",
		"node":"US Node 01",
		"server":"proxy.example.com",
		"port":1080,
		"region":"US",
		"alias":"us-premium",
		"tags":["streaming"],
		"residential":true,
		"premium":true
	}`)
	req := newAdminRequest(http.MethodPut, "/api/nodes/subscription/node", bytes.NewReader(reqBody))
	rec := httptest.NewRecorder()
	server.httpServer.Handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("PUT subscription node status = %d, body: %s", rec.Code, rec.Body.String())
	}

	stored, err := loadNodesConfig(nodesPath)
	if err != nil {
		t.Fatalf("load stored nodes config: %v", err)
	}
	cfg := stored.NodePools["provider-a"]
	if got := cfg.NodeTags["US Node 01"]; !containsAll(got, "streaming", "residential", "premium") {
		t.Fatalf("unexpected node tags: %#v", got)
	}
	if got := cfg.NodeServerTags["proxy.example.com:1080"]; !containsAll(got, "streaming", "residential", "premium") {
		t.Fatalf("unexpected server tags: %#v", got)
	}
	if cfg.NodeServerRegions["proxy.example.com:1080"] != "US" || cfg.NodeServerAliases["proxy.example.com:1080"] != "us-premium" {
		t.Fatalf("unexpected server metadata: %#v %#v", cfg.NodeServerRegions, cfg.NodeServerAliases)
	}
}

func containsAll(values []string, wants ...string) bool {
	seen := map[string]bool{}
	for _, value := range values {
		seen[value] = true
	}
	for _, want := range wants {
		if !seen[want] {
			return false
		}
	}
	return true
}

func TestHealthCheckSettingsPersistsAndUpdatesChecker(t *testing.T) {
	dir := t.TempDir()
	gatewayPath := filepath.Join(dir, "gateway.yaml")
	if err := os.WriteFile(gatewayPath, []byte(`
server:
  listen: ":8080"
admin:
  jwt_secret: "1234567890123456"
log:
  level: "info"
  file: ""
`), 0600); err != nil {
		t.Fatalf("write gateway config: %v", err)
	}

	manager := pool.NewManager()
	checker := pool.NewHealthChecker(manager, 0, 0, "")
	server := NewServer("127.0.0.1:0", manager, fingerprint.NewManager(), dir, "", "1234567890123456", "24h", nil)
	server.SetHealthChecker(checker)

	reqBody := []byte(`{"interval":"2m","timeout":"9s","url":"https://example.com/health","max_fail":7}`)
	req := newAdminRequest(http.MethodPut, "/api/system/health-check", bytes.NewReader(reqBody))
	rec := httptest.NewRecorder()
	server.httpServer.Handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("PUT health-check status = %d, body: %s", rec.Code, rec.Body.String())
	}

	defaults := checker.Defaults()
	if defaults.Interval != 2*time.Minute || defaults.Timeout != 9*time.Second || defaults.MaxFail != 7 {
		t.Fatalf("checker defaults not updated: %#v", defaults)
	}
	if defaults.TestURL != "https://example.com/health" {
		t.Fatalf("checker test url = %q", defaults.TestURL)
	}

	var stored struct {
		HealthCheck pool.HealthCheckConfig `yaml:"health_check"`
	}
	if err := loadYAML(gatewayPath, &stored); err != nil {
		t.Fatalf("load persisted gateway config: %v", err)
	}
	if stored.HealthCheck.Interval != "2m0s" || stored.HealthCheck.Timeout != "9s" || stored.HealthCheck.URL != "https://example.com/health" || stored.HealthCheck.MaxFail != 7 {
		t.Fatalf("unexpected persisted health_check: %#v", stored.HealthCheck)
	}

	getReq := newAdminRequest(http.MethodGet, "/api/system/health-check", nil)
	getRec := httptest.NewRecorder()
	server.httpServer.Handler.ServeHTTP(getRec, getReq)
	if getRec.Code != http.StatusOK {
		t.Fatalf("GET health-check status = %d, body: %s", getRec.Code, getRec.Body.String())
	}
	var got map[string]any
	if err := json.Unmarshal(getRec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode health-check response: %v", err)
	}
	if got["interval"] != "2m0s" || got["timeout"] != "9s" || got["url"] != "https://example.com/health" || got["max_fail"] != float64(7) {
		t.Fatalf("unexpected health-check response: %#v", got)
	}
}

func TestProxySettingsPersistsAndUpdatesRuntime(t *testing.T) {
	dir := t.TempDir()
	gatewayPath := filepath.Join(dir, "gateway.yaml")
	if err := os.WriteFile(gatewayPath, []byte(`
server:
  listen: ":8080"
admin:
  jwt_secret: "1234567890123456"
log:
  level: "info"
  file: ""
`), 0600); err != nil {
		t.Fatalf("write gateway config: %v", err)
	}

	manager := pool.NewManager()
	runtime := &fakeProxyRuntime{settings: proxyserver.DefaultProxySettings()}
	server := NewServer("127.0.0.1:0", manager, fingerprint.NewManager(), dir, "", "1234567890123456", "24h", nil)
	server.SetProxySettingsRuntime(runtime)

	reqBody := []byte(`{"max_attempts":8,"max_redirects":6,"template_fallback_after_attempts":false,"response_header_timeout":"4s","total_timeout":"45s","domain_retries":[{"domain":"api.example.com","enabled":true,"max_retries":3,"retry_other_nodes":true,"status_codes":[429,503]}]}`)
	req := newAdminRequest(http.MethodPut, "/api/system/proxy", bytes.NewReader(reqBody))
	rec := httptest.NewRecorder()
	server.httpServer.Handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("PUT proxy settings status = %d, body: %s", rec.Code, rec.Body.String())
	}
	if runtime.settings.MaxAttempts != 8 || runtime.settings.MaxRedirects != 6 || runtime.settings.TemplateFallbackAfterAttempts || runtime.settings.ResponseHeaderTimeout != 4*time.Second || runtime.settings.TotalTimeout != 45*time.Second {
		t.Fatalf("runtime settings not updated: %#v", runtime.settings)
	}

	var stored struct {
		Proxy proxyserver.ProxySettingsConfig `yaml:"proxy"`
	}
	if err := loadYAML(gatewayPath, &stored); err != nil {
		t.Fatalf("load persisted gateway config: %v", err)
	}
	if stored.Proxy.MaxAttempts != 8 || stored.Proxy.MaxRedirects != 6 || stored.Proxy.TemplateFallbackAfterAttempts == nil || *stored.Proxy.TemplateFallbackAfterAttempts || stored.Proxy.ResponseHeaderTimeout != "4s" || stored.Proxy.TotalTimeout != "45s" {
		t.Fatalf("unexpected persisted proxy settings: %#v", stored.Proxy)
	}
	if len(stored.Proxy.DomainRetries) != 1 || len(runtime.settings.DomainRetries) != 1 || stored.Proxy.DomainRetries[0].MaxRetries != 3 {
		t.Fatal("retry rule not persisted/applied")
	}
	loaded, err := server.loadProxySettings()
	if err != nil || len(loaded.DomainRetries) != 1 || loaded.DomainRetries[0].Domain != "api.example.com" {
		t.Fatalf("reload failed: %+v %v", loaded, err)
	}
	before, _ := os.ReadFile(gatewayPath)
	bad := newAdminRequest(http.MethodPut, "/api/system/proxy", strings.NewReader(`{"domain_retries":[{"domain":"api.example.com","enabled":true,"max_retries":3,"status_codes":[404]}]}`))
	badRec := httptest.NewRecorder()
	server.httpServer.Handler.ServeHTTP(badRec, bad)
	after, _ := os.ReadFile(gatewayPath)
	if badRec.Code != http.StatusBadRequest || !bytes.Equal(before, after) || runtime.settings.DomainRetries[0].StatusCodes[0] != 429 {
		t.Fatal("invalid rule changed persisted/runtime settings")
	}
	for _, body := range []string{
		`{"domain":"other.example","enabled":true,"max_retries":1,"retry_other_nodes":true,"status_codes":[429]}`,
		`{"domain":"API.EXAMPLE.COM.","enabled":true,"max_retries":2,"retry_other_nodes":false,"status_codes":[429]}`,
	} {
		rec := httptest.NewRecorder()
		server.httpServer.Handler.ServeHTTP(rec, newAdminRequest(http.MethodPut, "/api/system/proxy/domain-retries", strings.NewReader(body)))
		if rec.Code != 200 {
			t.Fatalf("single-domain update: %d %s", rec.Code, rec.Body.String())
		}
	}
	if len(runtime.settings.DomainRetries) != 2 || runtime.settings.DomainRetries[0].MaxRetries != 2 || runtime.settings.MaxAttempts != 8 || runtime.settings.TotalTimeout != 45*time.Second {
		t.Fatal("single-domain update overwrote unrelated settings or duplicated the domain")
	}
	if err := loadYAML(gatewayPath, &stored); err != nil || len(stored.Proxy.DomainRetries) != 2 {
		t.Fatal("single-domain rules not persisted")
	}

}

func TestTrafficGroupingRulePersistsAndUpdatesRuntime(t *testing.T) {
	dir := t.TempDir()
	gatewayPath := filepath.Join(dir, "gateway.yaml")
	if err := os.WriteFile(gatewayPath, []byte(`
server:
  listen: ":8080"
admin:
  jwt_secret: "1234567890123456"
log:
  level: "info"
  file: ""
`), 0600); err != nil {
		t.Fatalf("write gateway config: %v", err)
	}

	store := traffic.NewStore(1000)
	server := NewServer("127.0.0.1:0", pool.NewManager(), fingerprint.NewManager(), dir, "", "1234567890123456", "24h", nil, store)

	reqBody := []byte(`{
		"name":"sample-hls",
		"match":{"host_pattern":"*.cdn.example.com","path_pattern":"/media/*/segments/*/playlist.m3u8"},
		"query":{"drop_keys":["vendtime","vhash"],"sort":true}
	}`)
	req := newAdminRequest(http.MethodPost, "/api/traffic/grouping-rules", bytes.NewReader(reqBody))
	rec := httptest.NewRecorder()
	server.httpServer.Handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("POST traffic grouping rule status = %d, body: %s", rec.Code, rec.Body.String())
	}

	var stored struct {
		Traffic traffic.Config `yaml:"traffic"`
	}
	if err := loadYAML(gatewayPath, &stored); err != nil {
		t.Fatalf("load persisted gateway config: %v", err)
	}
	rules := stored.Traffic.FailureGrouping.URLNormalization.Rules
	if len(rules) != 1 {
		t.Fatalf("stored rules = %d want 1", len(rules))
	}
	if rules[0].Match.HostPattern != "*.cdn.example.com" || rules[0].Match.PathPattern != "/media/*/segments/*/playlist.m3u8" {
		t.Fatalf("unexpected stored rule match: %#v", rules[0].Match)
	}
	if len(rules[0].Query.DropKeys) != 2 {
		t.Fatalf("unexpected stored drop keys: %#v", rules[0].Query.DropKeys)
	}

	store.Record(traffic.Trace{
		Kind:        "proxy",
		URL:         "https://edge.cdn.example.com/media/stream-a/segments/a.mp4/playlist.m3u8?vendtime=1&vhash=a&lb=same",
		EgressGroup: "CA",
		Status:      502,
		Error:       "bad gateway",
	})
	store.Record(traffic.Trace{
		Kind:        "proxy",
		URL:         "https://edge.cdn.example.com/media/stream-a/segments/a.mp4/playlist.m3u8?vendtime=2&vhash=b&lb=same",
		EgressGroup: "CA",
		Status:      502,
		Error:       "bad gateway",
	})
	if got := store.Snapshot(10).Metrics.Failures; got != 1 {
		t.Fatalf("runtime traffic config did not merge failures, got %d", got)
	}
}

func TestTrafficSuccessFoldingRulePersistsAndUpdatesRuntime(t *testing.T) {
	dir := t.TempDir()
	gatewayPath := filepath.Join(dir, "gateway.yaml")
	if err := os.WriteFile(gatewayPath, []byte(`
server:
  listen: ":8080"
admin:
  jwt_secret: "1234567890123456"
log:
  level: "info"
  file: ""
`), 0600); err != nil {
		t.Fatalf("write gateway config: %v", err)
	}

	store := traffic.NewStore(1000)
	server := NewServer("127.0.0.1:0", pool.NewManager(), fingerprint.NewManager(), dir, "", "1234567890123456", "24h", nil, store)
	req := newAdminRequest(http.MethodPost, "/api/traffic/success-folding-rules", bytes.NewReader([]byte(`{
		"name":"frequent-search",
		"match":{"host_pattern":"api.example.com","path_pattern":"/v1/search"}
	}`)))
	rec := httptest.NewRecorder()
	server.httpServer.Handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("POST success folding rule status = %d, body: %s", rec.Code, rec.Body.String())
	}

	var stored struct {
		Traffic traffic.Config `yaml:"traffic"`
	}
	if err := loadYAML(gatewayPath, &stored); err != nil {
		t.Fatalf("load persisted gateway config: %v", err)
	}
	if len(stored.Traffic.SuccessFolding.Rules) != 1 {
		t.Fatalf("stored success folding rules = %d want 1", len(stored.Traffic.SuccessFolding.Rules))
	}

	store.Record(traffic.Trace{Kind: "proxy", URL: "https://api.example.com/v1/search?q=one", EgressGroup: "US", Status: 200})
	store.Record(traffic.Trace{Kind: "proxy", URL: "https://api.example.com/v1/search?q=two", EgressGroup: "US", Status: 200})
	snapshot := store.Snapshot(10)
	if snapshot.Metrics.Requests != 0 || snapshot.Metrics.IgnoredSuccess != 2 {
		t.Fatalf("runtime success folding not applied: %#v", snapshot.Metrics)
	}
	if len(snapshot.DisplayTraces) != 1 || snapshot.DisplayTraces[0].GroupCount != 2 {
		t.Fatalf("unexpected folded rows: %#v", snapshot.DisplayTraces)
	}
}

func TestTrafficPersistenceSettingsPersistAndUpdateRuntime(t *testing.T) {
	dir := t.TempDir()
	gatewayPath := filepath.Join(dir, "gateway.yaml")
	if err := os.WriteFile(gatewayPath, []byte(`
admin:
  jwt_secret: "1234567890123456"
traffic:
  persistence:
    enabled: true
    retention_days: 7
`), 0600); err != nil {
		t.Fatalf("write gateway config: %v", err)
	}

	store := traffic.NewStore(1000)
	server := NewServer("127.0.0.1:0", pool.NewManager(), fingerprint.NewManager(), dir, "", "1234567890123456", "24h", nil, store)
	req := newAdminRequest(http.MethodPut, "/api/traffic/settings", bytes.NewReader([]byte(`{"enabled":true,"retention_days":30}`)))
	rec := httptest.NewRecorder()
	server.httpServer.Handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("PUT traffic settings status = %d, body: %s", rec.Code, rec.Body.String())
	}
	if got := store.Config().Persistence.RetentionDays; got != 30 {
		t.Fatalf("runtime retention_days = %d want 30", got)
	}

	var stored struct {
		Traffic traffic.Config `yaml:"traffic"`
	}
	if err := loadYAML(gatewayPath, &stored); err != nil {
		t.Fatalf("load persisted gateway config: %v", err)
	}
	if stored.Traffic.Persistence.RetentionDays != 30 {
		t.Fatalf("persisted retention_days = %d want 30", stored.Traffic.Persistence.RetentionDays)
	}
}

func newAdminRequest(method, target string, body io.Reader) *http.Request {
	req := httptest.NewRequest(method, target, body)
	if strings.HasPrefix(target, "/") {
		req.Host = "localhost"
	}
	return req
}
