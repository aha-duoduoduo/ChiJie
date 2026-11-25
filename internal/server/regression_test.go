package server

import (
	"chijie/internal/dnsresolver"
	"chijie/internal/netguard"
	"chijie/internal/pool"
	"chijie/pkg/relay"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/miekg/dns"
)

func TestTunnelLimitsPendingAuthentication(t *testing.T) {
	s := NewServer(&Config{JWTSecret: "audit-secret-at-least-16"}, nil, nil)
	s.pendingTunnels = make(chan struct{}, 1)
	srv := httptest.NewServer(s.httpServer.Handler)
	defer srv.Close()
	url := "ws" + strings.TrimPrefix(srv.URL, "http") + "/tunnel"
	first, _, err := websocket.DefaultDialer.Dial(url, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer first.Close()
	extra, response, err := websocket.DefaultDialer.Dial(url, nil)
	if extra != nil {
		extra.Close()
	}
	if response != nil {
		defer response.Body.Close()
	}
	if err == nil || response == nil || response.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("pending authentication not bounded: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := s.Shutdown(ctx); err != nil {
		t.Fatal(err)
	}
	if len(s.pendingTunnels) != 0 {
		t.Fatal("pending authentication slot leaked")
	}
}

func TestRegressionDNSRebinding(t *testing.T) {
	for _, mode := range []string{"guarded-direct", "relay-direct"} {
		t.Run(mode, func(t *testing.T) {
			echo := startTCPEcho(t)
			_, port, _ := net.SplitHostPort(echo.Addr().String())
			udp, err := net.ListenPacket("udp", "127.0.0.1:0")
			if err != nil {
				t.Fatal(err)
			}
			var queries atomic.Int32
			dnsServer := &dns.Server{PacketConn: udp, Handler: dns.HandlerFunc(func(w dns.ResponseWriter, req *dns.Msg) {
				resp := new(dns.Msg)
				resp.SetReply(req)
				for _, q := range req.Question {
					if q.Qtype == dns.TypeA {
						ip := net.ParseIP("192.0.2.10")
						if queries.Add(1) > 1 {
							ip = net.ParseIP("127.0.0.1")
						}
						resp.Answer = append(resp.Answer, &dns.A{Hdr: dns.RR_Header{Name: q.Name, Rrtype: dns.TypeA, Class: dns.ClassINET, Ttl: 0}, A: ip})
					}
				}
				_ = w.WriteMsg(resp)
			})}
			go dnsServer.ActivateAndServe()
			defer dnsServer.Shutdown()
			resolver := dnsresolver.Resolver()
			old := resolver.Dial
			resolver.Dial = func(ctx context.Context, network, address string) (net.Conn, error) {
				return (&net.Dialer{}).DialContext(ctx, "udp", udp.LocalAddr().String())
			}
			defer func() { resolver.Dial = old }()
			ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
			defer cancel()
			var conn net.Conn
			if mode == "guarded-direct" {
				d := &guardedDialer{Dialer: &testDialer{dial: func(_ context.Context, _, addr string) (net.Conn, error) {
					if addr != "192.0.2.10:"+port {
						t.Errorf("dial did not pin validated IP: %s", addr)
					}
					return nil, errors.New("isolated dial boundary")
				}}}
				conn, err = d.DialContext(ctx, "tcp", "rebind.audit.test:"+port)
			} else {
				s := NewServer(&Config{}, pool.NewManager(), nil)
				conn, _, err = s.openRelayTCP(ctx, relay.OpenRequest{Address: "rebind.audit.test:" + port, Egress: EgressOptions{Direct: true}})
				if !errors.Is(err, netguard.ErrBlockedAddress) {
					t.Fatalf("rebound address was not blocked: %v", err)
				}
			}
			if err == nil {
				conn.Close()
				t.Fatal("DNS rebinding reached a blocked address")
			}

		})
	}
}

func TestRegressionTunnelOversizedUnauthenticatedFrame(t *testing.T) {
	s := NewServer(&Config{JWTSecret: "audit-secret-at-least-16"}, pool.NewManager(), nil)
	srv := httptest.NewServer(s.httpServer.Handler)
	defer srv.Close()
	ws, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(srv.URL, "http")+"/tunnel", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer ws.Close()
	_ = ws.SetReadDeadline(time.Now().Add(3 * time.Second))
	_ = ws.WriteJSON(map[string]string{"payload": strings.Repeat("x", 2*1024*1024)})
	_, _, err = ws.ReadMessage()
	if !websocket.IsCloseError(err, websocket.CloseMessageTooBig) {
		t.Fatalf("oversized message was not rejected: %v", err)
	}

}

func TestRegressionAffinityLimit(t *testing.T) {
	s := NewServer(&Config{}, pool.NewManager(), nil)
	for i := 0; i < 10002; i++ {
		s.rememberRelayChoice(EgressOptions{}, fmt.Sprint(i), time.Hour, &pool.EgressChoice{PoolName: "p", NodeName: "n"})
	}
	if len(s.affinities) != maxRelayAffinities {
		t.Fatalf("got %d", len(s.affinities))
	}
}

func TestRegressionRelayAbsentFromTraffic(t *testing.T) {
	s, endpoint := newRelayTestServer(t)
	echo := startTCPEcho(t)
	conn, _, err := relay.DialTCP(context.Background(), endpoint, signedAdminToken(t, "test-secret-for-relay"), relay.OpenRequest{Address: echo.Addr().String(), Egress: EgressOptions{Direct: true}})
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(time.Second))
	_, _ = conn.Write([]byte("audit"))
	data := make([]byte, 5)
	_, err = io.ReadFull(conn, data)
	if err != nil {
		t.Fatal(err)
	}
	m := s.traffic.Metrics()
	if m.ActiveTunnels != 1 || m.RawRequests != 0 {
		t.Fatalf("unexpected metrics: %+v", m)
	}
}

func TestRegressionInvalidHeaderPoisonsPool(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nodes.yaml")
	if err := os.WriteFile(path, []byte("node_pools:\n  audit:\n    source: static\n    nodes:\n      - name: healthy\n        type: http_proxy\n        server: 192.0.2.1\n        port: 8080\n        region: US\n"), 0600); err != nil {
		t.Fatal(err)
	}
	manager := pool.NewManager()
	if err := manager.LoadFromFile(path); err != nil {
		t.Fatal(err)
	}
	s := NewServer(&Config{}, manager, nil)
	req := &ProxyRequest{URL: "http://example.com/", Method: "GET", Headers: map[string]string{"Invalid Header": "x"}, Egress: EgressOptions{Region: "US"}}
	route, err := s.resolveEgress(req.Egress)
	if err != nil {
		t.Fatal(err)
	}
	_, _, err = s.doProxyWithRetry(context.Background(), req, route)
	if err == nil || !strings.Contains(err.Error(), "invalid header") {
		t.Fatalf("unexpected error %v", err)
	}
	state := manager.GetPoolStatus()[0].Nodes[0]
	if !state.Alive {
		t.Fatal("caller error poisoned node health")
	}
}

func TestRegressionRelayLargeWrite(t *testing.T) {
	_, endpoint := newRelayTestServer(t)
	echo := startTCPEcho(t)
	conn, _, err := relay.DialTCP(context.Background(), endpoint, signedAdminToken(t, "test-secret-for-relay"), relay.OpenRequest{Address: echo.Addr().String(), Egress: EgressOptions{Direct: true}})
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(2 * time.Second))
	n, err := conn.Write([]byte(strings.Repeat("x", 256*1024)))
	if err != nil || n != 256*1024 {
		t.Fatalf("unexpected write: n=%d err=%v", n, err)
	}
	data := make([]byte, n)
	if _, err := io.ReadFull(conn, data); err != nil {
		t.Fatal(err)
	}
	if string(data) != strings.Repeat("x", n) {
		t.Fatal("relay changed payload")
	}

}
