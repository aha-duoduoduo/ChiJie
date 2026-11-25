package server

import (
	"bytes"
	"context"
	"crypto/tls"
	"fmt"
	"io"
	"net"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"chijie/internal/auth"
	"chijie/internal/pool"
	"chijie/pkg/relay"

	"github.com/gorilla/websocket"
)

func startTCPEcho(t *testing.T) net.Listener {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { listener.Close() })
	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			go func() { defer conn.Close(); _, _ = io.Copy(conn, conn) }()
		}
	}()
	return listener
}

func startUDPEcho(t *testing.T) *net.UDPConn {
	t.Helper()
	conn, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { conn.Close() })
	go func() {
		buf := make([]byte, 65535)
		for {
			n, addr, err := conn.ReadFromUDP(buf)
			if err != nil {
				return
			}
			_, _ = conn.WriteToUDP(buf[:n], addr)
		}
	}()
	return conn
}

func newRelayTestServer(t *testing.T) (*Server, string) {
	t.Helper()
	s := NewServer(&Config{JWTSecret: "test-secret-for-relay", AllowPrivateTargets: true}, pool.NewManager(), nil)
	httpServer := httptest.NewServer(s.httpServer.Handler)
	t.Cleanup(httpServer.Close)
	return s, "ws" + strings.TrimPrefix(httpServer.URL, "http") + "/relay/v1"
}

func TestRelayTCPAndUDPDirect(t *testing.T) {
	_, endpoint := newRelayTestServer(t)
	token := signedAdminToken(t, "test-secret-for-relay")
	tcp := startTCPEcho(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	stream, result, err := relay.DialTCP(ctx, endpoint, token, relay.OpenRequest{Address: tcp.Addr().String(), Egress: EgressOptions{Direct: true}})
	if err != nil {
		t.Fatal(err)
	}
	defer stream.Close()
	if result.Pool != "DIRECT" {
		t.Fatalf("pool: %q", result.Pool)
	}
	_ = stream.SetDeadline(time.Now().Add(3 * time.Second))
	if _, err := stream.Write([]byte("tcp-test")); err != nil {
		t.Fatal(err)
	}
	buf := make([]byte, 8)
	if _, err := io.ReadFull(stream, buf); err != nil || string(buf) != "tcp-test" {
		t.Fatalf("TCP echo = %q, %v", buf, err)
	}

	udp := startUDPEcho(t)
	packets, _, err := relay.DialUDP(ctx, endpoint, token, relay.OpenRequest{Address: udp.LocalAddr().String(), Egress: EgressOptions{Direct: true}})
	if err != nil {
		t.Fatal(err)
	}
	defer packets.Close()
	_ = packets.WS.SetReadDeadline(time.Now().Add(3 * time.Second))
	if err := packets.Send([]byte("udp-test")); err != nil {
		t.Fatal(err)
	}
	received, err := packets.Receive()
	if err != nil || string(received) != "udp-test" {
		t.Fatalf("UDP echo = %q, %v", received, err)
	}
}

func TestRelayRejectsMissingToken(t *testing.T) {
	_, endpoint := newRelayTestServer(t)
	_, _, err := relay.DialTCP(context.Background(), endpoint, "", relay.OpenRequest{Address: "127.0.0.1:80"})
	if err == nil || !strings.Contains(err.Error(), "401") {
		t.Fatalf("expected HTTP 401, got %v", err)
	}
}

func TestRelayPersistentKeyAndRevocation(t *testing.T) {
	s, endpoint := newRelayTestServer(t)
	keys, err := auth.LoadAccessKeys(filepath.Join(t.TempDir(), "keys.json"), "test-secret-for-relay")
	if err != nil {
		t.Fatal(err)
	}
	s.SetAccessKeys(keys)
	key, err := keys.Create("native-client", 0)
	if err != nil {
		t.Fatal(err)
	}
	target := startTCPEcho(t)
	request := relay.OpenRequest{Address: target.Addr().String(), Egress: EgressOptions{Direct: true}}
	stream, _, err := relay.DialTCP(context.Background(), endpoint, key.Token, request)
	if err != nil {
		t.Fatalf("persistent key rejected by WSS: %v", err)
	}
	_ = stream.Close()
	if err := keys.Revoke(key.ID); err != nil {
		t.Fatal(err)
	}
	if _, _, err := relay.DialTCP(context.Background(), endpoint, key.Token, request); err == nil || !strings.Contains(err.Error(), "401") {
		t.Fatalf("revoked WSS key accepted: %v", err)
	}
}

func TestRelayGatewayDirectAndRegionFallback(t *testing.T) {
	secret := "test-secret-for-relay"
	token := signedAdminToken(t, secret)
	remoteNodes := filepath.Join(t.TempDir(), "remote-nodes.yaml")
	if err := os.WriteFile(remoteNodes, []byte("node_pools:\n  us:\n    source: static\n    nodes:\n      - name: remote-direct\n        type: direct\n        region: US\n"), 0600); err != nil {
		t.Fatal(err)
	}
	remoteManager := pool.NewManager()
	if err := remoteManager.LoadFromFile(remoteNodes); err != nil {
		t.Fatal(err)
	}
	remote := NewServer(&Config{JWTSecret: secret, AllowPrivateTargets: true}, remoteManager, nil)
	remoteHTTP := httptest.NewTLSServer(remote.httpServer.Handler)
	defer remoteHTTP.Close()

	localNodes := filepath.Join(t.TempDir(), "local-nodes.yaml")
	configuration := fmt.Sprintf("node_pools:\n  remote:\n    source: template\n    template_type: chijie\n    endpoint: %q\n    bearer_token: %q\n    coverage: both\n", remoteHTTP.URL, token)
	if err := os.WriteFile(localNodes, []byte(configuration), 0600); err != nil {
		t.Fatal(err)
	}
	localManager := pool.NewManager()
	if err := localManager.LoadFromFile(localNodes); err != nil {
		t.Fatal(err)
	}
	local := NewServer(&Config{JWTSecret: secret, AllowPrivateTargets: true}, localManager, nil)
	local.relayDialer = &websocket.Dialer{HandshakeTimeout: 5 * time.Second, TLSClientConfig: &tls.Config{InsecureSkipVerify: true}}
	localHTTP := httptest.NewServer(local.httpServer.Handler)
	defer localHTTP.Close()
	endpoint := "ws" + strings.TrimPrefix(localHTTP.URL, "http") + "/relay/v1"
	target := startTCPEcho(t)
	for _, egress := range []EgressOptions{{Gateway: "remote", Direct: true}, {Region: "US"}} {
		stream, response, err := relay.DialTCP(context.Background(), endpoint, token, relay.OpenRequest{Address: target.Addr().String(), Egress: egress})
		if err != nil {
			t.Fatal(err)
		}
		_ = stream.SetDeadline(time.Now().Add(3 * time.Second))
		if response.Pool != "remote" {
			t.Fatalf("unexpected gateway %q", response.Pool)
		}
		_, _ = stream.Write([]byte("via-remote"))
		buf := make([]byte, 10)
		if _, err := io.ReadFull(stream, buf); err != nil || string(buf) != "via-remote" {
			t.Fatalf("remote echo: %q %v", buf, err)
		}
		stream.Close()
	}
	udp := startUDPEcho(t)
	packets, response, err := relay.DialUDP(context.Background(), endpoint, token, relay.OpenRequest{Address: udp.LocalAddr().String(), Egress: EgressOptions{Gateway: "remote", Direct: true}})
	if err != nil {
		t.Fatal(err)
	}
	defer packets.Close()
	if response.Pool != "remote" {
		t.Fatalf("UDP gateway = %q", response.Pool)
	}
	_ = packets.WS.SetReadDeadline(time.Now().Add(3 * time.Second))
	if err := packets.Send([]byte("nested-udp")); err != nil {
		t.Fatal(err)
	}
	data, err := packets.Receive()
	if err != nil || string(data) != "nested-udp" {
		t.Fatalf("nested UDP echo: %q %v", data, err)
	}
}

func TestRelayAffinityPrefersSavedNodeUntilExpiry(t *testing.T) {
	nodes := filepath.Join(t.TempDir(), "nodes.yaml")
	configuration := "node_pools:\n  us:\n    source: static\n    nodes:\n      - name: first\n        type: direct\n        region: US\n      - name: second\n        type: direct\n        region: US\n"
	if err := os.WriteFile(nodes, []byte(configuration), 0600); err != nil {
		t.Fatal(err)
	}
	manager := pool.NewManager()
	if err := manager.LoadFromFile(nodes); err != nil {
		t.Fatal(err)
	}
	s := NewServer(&Config{JWTSecret: "test-secret-for-affinity"}, manager, nil)
	options := EgressOptions{Region: "US", Strategy: "least-latency"}
	choices, err := s.orderedRelayChoices(options, "client-a", time.Hour)
	if err != nil || len(choices) != 2 {
		t.Fatalf("choices = %d, %v", len(choices), err)
	}
	s.rememberRelayChoice(options, "client-a", time.Hour, choices[1])
	sticky, err := s.orderedRelayChoices(options, "client-a", time.Hour)
	if err != nil || sticky[0].NodeName != choices[1].NodeName {
		t.Fatalf("sticky choice = %v, %v", sticky, err)
	}
	s.affinityMu.Lock()
	for key, stored := range s.affinities {
		stored.until = time.Now().Add(-time.Second)
		s.affinities[key] = stored
	}
	s.affinityMu.Unlock()
	expired, err := s.orderedRelayChoices(options, "client-a", time.Hour)
	if err != nil || expired[0].NodeName != choices[0].NodeName {
		t.Fatalf("expired choice = %v, %v", expired, err)
	}
}

func TestSocksAccountsPersistHashesAndDenyEmpty(t *testing.T) {
	path := filepath.Join(t.TempDir(), "runtime", "accounts.json")
	store, err := LoadSocksAccounts(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := store.Authenticate("missing", "password"); ok {
		t.Fatal("empty store accepted credentials")
	}
	password, err := store.Create(SocksAccount{Username: "bot", Enabled: true, Egress: EgressOptions{Direct: true}, AffinityTTL: "1h"}, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(password) < 16 {
		t.Fatal("generated password too short")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(data, []byte(password)) {
		t.Fatal("plaintext password persisted")
	}
	reloaded, err := LoadSocksAccounts(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := reloaded.Authenticate("bot", password); !ok {
		t.Fatal("persisted account rejected password")
	}
	if _, ok := reloaded.Authenticate("bot", "wrong"); ok {
		t.Fatal("wrong password accepted")
	}
	if err := reloaded.Delete("bot"); err != nil {
		t.Fatal(err)
	}
	if _, ok := reloaded.Authenticate("bot", password); ok {
		t.Fatal("deleted account accepted")
	}
}

func socksAuthenticate(t *testing.T, address, username, password string) net.Conn {
	t.Helper()
	conn, err := net.DialTimeout("tcp", address, 3*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { conn.Close() })
	_ = conn.SetDeadline(time.Now().Add(5 * time.Second))
	_, _ = conn.Write([]byte{5, 1, 2})
	var reply [2]byte
	if _, err := io.ReadFull(conn, reply[:]); err != nil || reply != [2]byte{5, 2} {
		t.Fatalf("method reply: %v %v", reply, err)
	}
	auth := append([]byte{1, byte(len(username))}, []byte(username)...)
	auth = append(auth, byte(len(password)))
	auth = append(auth, []byte(password)...)
	_, _ = conn.Write(auth)
	if _, err := io.ReadFull(conn, reply[:]); err != nil || reply != [2]byte{1, 0} {
		t.Fatalf("auth reply: %v %v", reply, err)
	}
	return conn
}

func socksRequestFor(address string, command byte) []byte {
	return append([]byte{5, command, 0}, socksAddressBytes(address)...)
}

func readSocksReply(t *testing.T, conn net.Conn) string {
	t.Helper()
	var header [4]byte
	if _, err := io.ReadFull(conn, header[:]); err != nil {
		t.Fatal(err)
	}
	if header[0] != 5 || header[1] != 0 {
		t.Fatalf("SOCKS reply: %v", header)
	}
	address, err := readSOCKSAddress(conn, header[3])
	if err != nil {
		t.Fatal(err)
	}
	return address
}

func TestSOCKSConnectAndUDPAssociate(t *testing.T) {
	s, _ := newRelayTestServer(t)
	store, err := LoadSocksAccounts(filepath.Join(t.TempDir(), "accounts.json"))
	if err != nil {
		t.Fatal(err)
	}
	password, err := store.Create(SocksAccount{Username: "echo", Enabled: true, Egress: EgressOptions{Direct: true}}, "")
	if err != nil {
		t.Fatal(err)
	}
	s.SetSocksAccounts(store)
	s.ConfigureSOCKS("127.0.0.1:0", "", "", "")
	if err := s.StartSOCKS(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.socksTCPListener.Close(); s.socksUDPListener.Close() })
	tcp := startTCPEcho(t)
	client := socksAuthenticate(t, s.SOCKSAddress(), "echo", password)
	_, _ = client.Write(socksRequestFor(tcp.Addr().String(), socksConnect))
	readSocksReply(t, client)
	_, _ = client.Write([]byte("hello"))
	buf := make([]byte, 5)
	if _, err := io.ReadFull(client, buf); err != nil || string(buf) != "hello" {
		t.Fatalf("SOCKS TCP echo: %q %v", buf, err)
	}
	client.Close()

	udp := startUDPEcho(t)
	control := socksAuthenticate(t, s.SOCKSAddress(), "echo", password)
	_, _ = control.Write(socksRequestFor("0.0.0.0:0", socksUDPAssociate))
	relayAddr := readSocksReply(t, control)
	destination, err := net.ResolveUDPAddr("udp", relayAddr)
	if err != nil {
		t.Fatal(err)
	}
	packetClient, err := net.DialUDP("udp", nil, destination)
	if err != nil {
		t.Fatal(err)
	}
	defer packetClient.Close()
	_ = packetClient.SetDeadline(time.Now().Add(4 * time.Second))
	packet := append([]byte{0, 0, 0}, socksAddressBytes(udp.LocalAddr().String())...)
	packet = append(packet, []byte("udp-echo")...)
	if _, err := packetClient.Write(packet); err != nil {
		t.Fatal(err)
	}
	response := make([]byte, 512)
	n, err := packetClient.Read(response)
	if err != nil {
		t.Fatal(err)
	}
	if n < 12 || string(response[n-8:n]) != "udp-echo" {
		t.Fatalf("SOCKS UDP echo: %q", response[:n])
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := s.Shutdown(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := control.Read(response); err == nil {
		t.Fatal("SOCKS control connection remains open")
	}
	metrics := s.traffic.Metrics()
	if metrics.ActiveTunnels != 0 || metrics.RawRequests != 2 || metrics.RequestBytes != 13 || metrics.ResponseBytes != 13 {
		t.Fatalf("incorrect final SOCKS metrics: %+v", metrics)
	}
}

func TestSOCKSWithoutAccountsRejectsAllMethods(t *testing.T) {
	s, _ := newRelayTestServer(t)
	store, err := LoadSocksAccounts(filepath.Join(t.TempDir(), "accounts.json"))
	if err != nil {
		t.Fatal(err)
	}
	s.SetSocksAccounts(store)
	s.ConfigureSOCKS("127.0.0.1:0", "", "", "")
	if err := s.StartSOCKS(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.socksTCPListener.Close(); s.socksUDPListener.Close() })
	conn, err := net.Dial("tcp", s.SOCKSAddress())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	_, _ = conn.Write([]byte{5, 2, 0, 2})
	var reply [2]byte
	if _, err := io.ReadFull(conn, reply[:]); err != nil || reply != [2]byte{5, 0xff} {
		t.Fatalf("empty store reply %v %v", reply, err)
	}
}

func TestSOCKSLocalAdapterUsesWSSForTCPAndUDP(t *testing.T) {
	secret := "test-secret-for-relay"
	remote := NewServer(&Config{JWTSecret: secret, AllowPrivateTargets: true}, pool.NewManager(), nil)
	remoteHTTP := httptest.NewTLSServer(remote.httpServer.Handler)
	defer remoteHTTP.Close()
	local := NewServer(&Config{JWTSecret: secret, AllowPrivateTargets: true}, pool.NewManager(), nil)
	accounts, err := LoadSocksAccounts(filepath.Join(t.TempDir(), "accounts.json"))
	if err != nil {
		t.Fatal(err)
	}
	password, err := accounts.Create(SocksAccount{Username: "adapter", Enabled: true, Egress: EgressOptions{Direct: true}}, "")
	if err != nil {
		t.Fatal(err)
	}
	local.SetSocksAccounts(accounts)
	local.ConfigureSOCKS("127.0.0.1:0", "", "wss"+strings.TrimPrefix(remoteHTTP.URL, "https")+"/relay/v1", signedAdminToken(t, secret))
	local.upstreamDialer = &websocket.Dialer{TLSClientConfig: &tls.Config{InsecureSkipVerify: true}}
	if err := local.StartSOCKS(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { local.socksTCPListener.Close(); local.socksUDPListener.Close() })

	tcp := startTCPEcho(t)
	client := socksAuthenticate(t, local.SOCKSAddress(), "adapter", password)
	_, _ = client.Write(socksRequestFor(tcp.Addr().String(), socksConnect))
	readSocksReply(t, client)
	_, _ = client.Write([]byte("adapter-tcp"))
	got := make([]byte, 11)
	if _, err := io.ReadFull(client, got); err != nil || string(got) != "adapter-tcp" {
		t.Fatalf("SOCKS/WSS TCP = %q, %v", got, err)
	}
	client.Close()

	udp := startUDPEcho(t)
	control := socksAuthenticate(t, local.SOCKSAddress(), "adapter", password)
	_, _ = control.Write(socksRequestFor("0.0.0.0:0", socksUDPAssociate))
	relayAddress := readSocksReply(t, control)
	endpoint, err := net.ResolveUDPAddr("udp", relayAddress)
	if err != nil {
		t.Fatal(err)
	}
	packetClient, err := net.DialUDP("udp", nil, endpoint)
	if err != nil {
		t.Fatal(err)
	}
	defer packetClient.Close()
	_ = packetClient.SetDeadline(time.Now().Add(5 * time.Second))
	packet := append([]byte{0, 0, 0}, socksAddressBytes(udp.LocalAddr().String())...)
	_, _ = packetClient.Write(append(packet, []byte("adapter-udp")...))
	response := make([]byte, 512)
	n, err := packetClient.Read(response)
	if err != nil || n < 11 || string(response[n-11:n]) != "adapter-udp" {
		t.Fatalf("SOCKS/WSS UDP = %q, %v", response[:n], err)
	}
}
