package server

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"chijie/internal/pool"
)

func retryTestSettings() ProxySettings {
	s := DefaultProxySettings()
	s.DomainRetries = []DomainRetryRule{{Domain: "api.example.com", Enabled: true, MaxRetries: 2, RetryOtherNodes: true, StatusCodes: []int{429, 500, 502, 503, 504}}}
	return s
}

// Each candidate reaches an independent real HTTP server. This checks the
// complete response/body lifecycle and which candidate serves each attempt.
func retryTestRoute(t *testing.T, statuses ...int) (*egressRoute, []*atomic.Int32) {
	t.Helper()
	route := &egressRoute{Region: "US", Group: "US", Strategy: "least-latency"}
	var counts []*atomic.Int32
	for i, status := range statuses {
		count := new(atomic.Int32)
		counts = append(counts, count)
		target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			count.Add(1)
			w.Header().Set("Content-Type", "text/plain")
			if status == http.StatusTooManyRequests {
				w.Header().Set("Retry-After", "3600")
			}
			w.WriteHeader(status)
			fmt.Fprintf(w, "node-%d:%d", i, status)
		}))
		t.Cleanup(target.Close)
		d := &testDialer{name: fmt.Sprintf("node-%d", i), dial: func(ctx context.Context, network, _ string) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, network, strings.TrimPrefix(target.URL, "http://"))
		}}
		route.Choices = append(route.Choices, &pool.EgressChoice{Dialer: d, PoolName: "test", NodeName: d.name, Region: "US", Group: "US"})
	}
	return route, counts
}

func TestDomainRetrySwitchesNodeForTemporaryResponses(t *testing.T) {
	for _, status := range []int{429, 500, 502, 503, 504} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			t.Parallel()
			route, counts := retryTestRoute(t, status, 200)
			s := &Server{proxySettings: retryTestSettings()}
			resp, final, err := s.doProxyWithRetry(context.Background(), &ProxyRequest{URL: "http://api.example.com/api/items/test", Method: "GET"}, route)
			if err != nil || resp.StatusCode != 200 || string(resp.Body) != "node-1:200" || final.Choice.NodeName != "node-1" || resp.Attempts != 2 || final.Region != "US" || final.RetryOutcome != "recovered" {
				t.Fatalf("response=%+v route=%+v error=%v", resp, final, err)
			}
			if counts[0].Load() != 1 || counts[1].Load() != 1 {
				t.Fatal("unexpected candidate count")
			}
			rec := httptest.NewRecorder()
			writeProxyResponse(rec, resp)
			if rec.Header().Get("X-Chijie-Attempts") != "2" {
				t.Fatal("missing attempts header")
			}
		})
	}
}

func TestDomainRetryDoesNotRetryPermanentErrorsOrUnmatchedRequests(t *testing.T) {
	for _, tc := range []struct {
		name, method, url string
		status            int
		enabled           bool
	}{
		{"404", "GET", "http://api.example.com/x", 404, true},
		{"401", "GET", "http://api.example.com/x", 401, true},
		{"403", "GET", "http://api.example.com/x", 403, true},
		{"other host", "GET", "http://api.example.com.other.test/x", 429, true},
		{"POST", "POST", "http://api.example.com/x", 429, true},
		{"disabled", "GET", "http://api.example.com/x", 429, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			route, counts := retryTestRoute(t, tc.status, 200)
			settings := retryTestSettings()
			settings.DomainRetries[0].Enabled = tc.enabled
			resp, _, err := (&Server{proxySettings: settings}).doProxyWithRetry(context.Background(), &ProxyRequest{URL: tc.url, Method: tc.method}, route)
			if err != nil || resp.StatusCode != tc.status || counts[1].Load() != 0 {
				t.Fatalf("resp=%+v err=%v attempts=%d", resp, err, counts[1].Load())
			}
		})
	}
}

func TestDomainRetryLimitAndSameNode(t *testing.T) {
	for _, other := range []bool{true, false} {
		t.Run(fmt.Sprint(other), func(t *testing.T) {
			route, counts := retryTestRoute(t, 429, 429, 200)
			settings := retryTestSettings()
			settings.DomainRetries[0].MaxRetries = 1
			settings.DomainRetries[0].RetryOtherNodes = other
			resp, final, err := (&Server{proxySettings: settings}).doProxyWithRetry(context.Background(), &ProxyRequest{URL: "http://api.example.com/x", Method: "HEAD"}, route)
			if err != nil || resp.StatusCode != 429 || resp.Attempts != 2 || counts[2].Load() != 0 || final.RetryOutcome != "retry_limit" {
				t.Fatalf("resp=%+v err=%v", resp, err)
			}
			want := int32(2)
			if other {
				want = 1
			}
			if counts[0].Load() != want {
				t.Fatalf("first node count=%d want=%d", counts[0].Load(), want)
			}
		})
	}
}

func TestDomainRetryStopsWithoutOtherNodes(t *testing.T) {
	route, counts := retryTestRoute(t, 429)
	route.Choices = append(route.Choices, route.Choices[0])
	resp, final, err := (&Server{proxySettings: retryTestSettings()}).doProxyWithRetry(context.Background(), &ProxyRequest{URL: "http://api.example.com/x"}, route)
	if err != nil || resp.Attempts != 1 || counts[0].Load() != 1 || final.RetryOutcome != "no_other_nodes" {
		t.Fatalf("resp=%+v err=%v", resp, err)
	}
}

func TestDomainRetryPreservesTotalBudgetAndFinalRetryAfter(t *testing.T) {
	var count atomic.Int32
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		count.Add(1)
		w.Header().Set("Retry-After", "60")
		w.WriteHeader(429)
	}))
	defer target.Close()
	settings := retryTestSettings()
	settings.DomainRetries[0].Domain = "127.0.0.1"
	settings.DomainRetries[0].RetryOtherNodes = false
	settings.TotalTimeout = 100 * time.Millisecond
	s := &Server{proxySettings: settings, allowPrivateTargets: true}
	started := time.Now()
	resp, final, err := s.doProxyWithRetry(context.Background(), &ProxyRequest{URL: target.URL, Method: "GET"}, &egressRoute{Direct: true})
	if err != nil || resp.StatusCode != 429 || count.Load() != 1 || time.Since(started) > time.Second || final.RetryOutcome != "total_timeout" {
		t.Fatalf("resp=%+v err=%v count=%d", resp, err, count.Load())
	}
	rec := httptest.NewRecorder()
	writeProxyResponse(rec, resp)
	if rec.Header().Get("Retry-After") != "60" {
		t.Fatal("Retry-After lost")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if waitProxyRetry(ctx, time.Second) || waitProxyRetry(ctx, 0) {
		t.Fatal("retry after cancellation")
	}
}

func TestDomainRetrySwitchesImmediatelyDespiteRetryAfter(t *testing.T) {
	route, counts := retryTestRoute(t, 429, 200)
	settings := retryTestSettings()
	// Shorter than the former 250 ms minimum delay, far below Retry-After.
	settings.TotalTimeout = 200 * time.Millisecond
	resp, final, err := (&Server{proxySettings: settings}).doProxyWithRetry(context.Background(), &ProxyRequest{URL: "http://api.example.com/x", Method: "GET"}, route)
	if err != nil || resp.StatusCode != 200 || resp.Attempts != 2 || counts[1].Load() != 1 || final.RetryOutcome != "recovered" {
		t.Fatalf("response=%+v route=%+v error=%v", resp, final, err)
	}
}

func TestDomainRetryValidationAndSnapshotIsolation(t *testing.T) {
	settings := retryTestSettings()
	settings.DomainRetries[0].Domain = " API.EXAMPLE.COM. "
	s := &Server{}
	s.UpdateProxySettings(settings)
	settings.DomainRetries[0].StatusCodes[0] = 404
	snapshot := s.ProxySettings()
	if snapshot.DomainRetries[0].Domain != "api.example.com" || snapshot.DomainRetries[0].StatusCodes[0] != 429 {
		t.Fatal("settings not normalized or cloned")
	}
	snapshot.DomainRetries[0].StatusCodes[0] = 404
	if s.ProxySettings().DomainRetries[0].StatusCodes[0] != 429 {
		t.Fatal("snapshot aliases runtime")
	}
	for _, domain := range []string{"https://api.example.com", "*.example.com", "api.example.com/path", "-invalid.app", "api.example.com:443"} {
		bad := retryTestSettings()
		bad.DomainRetries[0].Domain = domain
		if _, err := ValidateProxySettings(bad); err == nil {
			t.Fatalf("accepted %q", domain)
		}
	}
	if _, err := ValidateProxySettings(settings); err == nil {
		t.Fatal("accepted 404")
	}
	bad := retryTestSettings()
	bad.DomainRetries[0].MaxRetries = 21
	if _, err := ValidateProxySettings(bad); err == nil {
		t.Fatal("accepted unbounded retries")
	}
}

func TestDomainRetryRedirectDoesNotApplyToOtherHosts(t *testing.T) {
	settings := retryTestSettings()
	settings.DomainRetries = append(settings.DomainRetries, DomainRetryRule{Domain: "other.example", Enabled: true, MaxRetries: 2, StatusCodes: []int{429}})
	req := &ProxyRequest{URL: "https://api.example.com/start", Method: "GET"}
	if retryResponseDomain(req, &proxyResponse{FinalURL: "https://other.example/end"}, settings) {
		t.Fatal("applied rule across domains")
	}
	if !retryResponseDomain(req, &proxyResponse{FinalURL: "https://API.EXAMPLE.COM/end"}, settings) {
		t.Fatal("same domain redirect rejected")
	}
}
