package server

import (
	"bytes"
	"context"
	"crypto/tls"
	"errors"
	"io"
	"net"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"chijie/internal/pool"

	"github.com/gorilla/websocket"
	"github.com/sagernet/sing/common/buf"
	M "github.com/sagernet/sing/common/metadata"
)

func TestValidateTUNConfig(t *testing.T) {
	upstream := "wss://relay.example.com/relay/v1"
	cfg, err := validateTUNConfig(TUNConfig{Enabled: true, Egress: EgressOptions{Direct: true}}, upstream)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Interface != "chijie0" || cfg.MTU != 1500 || cfg.IPv4 == "" || cfg.IPv6 == "" {
		t.Fatalf("unexpected defaults: %+v", cfg)
	}
	disabled := false
	withoutIPv6, err := validateTUNConfig(TUNConfig{IPv6Enabled: &disabled}, upstream)
	if err != nil || withoutIPv6.IPv6 != "" {
		t.Fatalf("IPv6 disabled: %+v, %v", withoutIPv6, err)
	}
	for _, test := range []struct {
		name string
		cfg  TUNConfig
		url  string
	}{
		{"plain WebSocket", TUNConfig{}, "ws://relay.example.com/relay/v1"},
		{"wrong path", TUNConfig{}, "wss://relay.example.com/proxy"},
		{"invalid interface", TUNConfig{Interface: "bad/name"}, upstream},
		{"wrong IP family", TUNConfig{IPv4: "fd00::1/126"}, upstream},
		{"single IPv4 address", TUNConfig{IPv4: "198.18.0.1/32"}, upstream},
		{"IPv6 conflict", TUNConfig{IPv6Enabled: &disabled, IPv6: "fd00::1/126"}, upstream},
		{"invalid bypass", TUNConfig{BypassCIDRs: []string{"bad"}}, upstream},
		{"invalid egress", TUNConfig{Egress: EgressOptions{Direct: true, Region: "US"}}, upstream},
	} {
		t.Run(test.name, func(t *testing.T) {
			if _, err := validateTUNConfig(test.cfg, test.url); err == nil {
				t.Fatal("invalid TUN config accepted")
			}
		})
	}
}

func newTUNHandlerTest(t *testing.T) *tunRelayHandler {
	t.Helper()
	secret := "test-secret-for-relay"
	s := NewServer(&Config{JWTSecret: secret, AllowPrivateTargets: true}, pool.NewManager(), nil)
	remote := httptest.NewTLSServer(s.httpServer.Handler)
	t.Cleanup(remote.Close)
	s.relayUpstreamURL = "wss" + strings.TrimPrefix(remote.URL, "https") + "/relay/v1"
	s.relayUpstreamToken = signedAdminToken(t, secret)
	return &tunRelayHandler{server: s, egress: EgressOptions{Direct: true}, upstreamDialer: &websocket.Dialer{TLSClientConfig: &tls.Config{InsecureSkipVerify: true}}}
}

func TestTUNHandlerTCPOverWSS(t *testing.T) {
	handler := newTUNHandlerTest(t)
	target := startTCPEcho(t)
	client, captured := net.Pipe()
	done := make(chan struct{})
	go func() {
		handler.NewConnectionEx(context.Background(), captured, M.Socksaddr{}, M.ParseSocksaddr(target.Addr().String()), nil)
		close(done)
	}()
	_ = client.SetDeadline(time.Now().Add(5 * time.Second))
	if _, err := client.Write([]byte("tun-tcp")); err != nil {
		t.Fatal(err)
	}
	data := make([]byte, 7)
	if _, err := io.ReadFull(client, data); err != nil || string(data) != "tun-tcp" {
		t.Fatalf("TCP relay = %q, %v", data, err)
	}
	_ = client.Close()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("TUN TCP handler did not close")
	}
}

type tunTestPacket struct {
	address M.Socksaddr
	data    []byte
}

type tunTestPacketConn struct {
	in     chan tunTestPacket
	out    chan tunTestPacket
	done   chan struct{}
	once   sync.Once
	broken chan error
}

func (c *tunTestPacketConn) FrontHeadroom() int { return 48 }
func (c *tunTestPacketConn) ReadPacket(buffer *buf.Buffer) (M.Socksaddr, error) {
	select {
	case packet := <-c.in:
		_, err := buffer.Write(packet.data)
		return packet.address, err
	case <-c.done:
		return M.Socksaddr{}, io.EOF
	}
}
func (c *tunTestPacketConn) WritePacket(buffer *buf.Buffer, destination M.Socksaddr) error {
	defer buffer.Release()
	if buffer.Start() < c.FrontHeadroom() {
		select {
		case c.broken <- errors.New("missing packet headroom"):
		default:
		}
	}
	select {
	case c.out <- tunTestPacket{address: destination, data: append([]byte(nil), buffer.Bytes()...)}:
		return nil
	case <-c.done:
		return io.EOF
	}
}
func (c *tunTestPacketConn) Close() error                     { c.once.Do(func() { close(c.done) }); return nil }
func (c *tunTestPacketConn) LocalAddr() net.Addr              { return &net.UDPAddr{} }
func (c *tunTestPacketConn) SetDeadline(time.Time) error      { return nil }
func (c *tunTestPacketConn) SetReadDeadline(time.Time) error  { return nil }
func (c *tunTestPacketConn) SetWriteDeadline(time.Time) error { return nil }

func TestTUNHandlerUDPOverWSS(t *testing.T) {
	handler := newTUNHandlerTest(t)
	target := startUDPEcho(t)
	client := &tunTestPacketConn{in: make(chan tunTestPacket, 1), out: make(chan tunTestPacket, 1), done: make(chan struct{}), broken: make(chan error, 1)}
	ended := make(chan struct{})
	go func() {
		handler.NewPacketConnectionEx(context.Background(), client, M.Socksaddr{}, M.ParseSocksaddr(target.LocalAddr().String()), nil)
		close(ended)
	}()
	payload := bytes.Repeat([]byte("tun-udp"), 1000)
	client.in <- tunTestPacket{address: M.ParseSocksaddr(target.LocalAddr().String()), data: payload}
	select {
	case packet := <-client.out:
		if !bytes.Equal(packet.data, payload) || packet.address.String() != target.LocalAddr().String() {
			t.Fatalf("UDP relay length = %d, address = %s", len(packet.data), packet.address)
		}
	case err := <-client.broken:
		t.Fatal(err)
	case <-time.After(5 * time.Second):
		t.Fatal("TUN UDP response timed out")
	}
	_ = client.Close()
	select {
	case <-ended:
	case <-time.After(5 * time.Second):
		t.Fatal("TUN UDP handler did not close")
	}
}
