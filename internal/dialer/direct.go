package dialer

import (
	"context"
	"net"
	"net/http"
	"time"

	"chijie/internal/dnsresolver"
)

// DirectDialer 直连
type DirectDialer struct {
	dialer *net.Dialer
}

func NewDirectDialer() *DirectDialer {
	return &DirectDialer{
		dialer: dnsresolver.NewDialer(30 * time.Second),
	}
}

func (d *DirectDialer) DialContext(ctx context.Context, network, addr string) (net.Conn, error) {
	return d.dialer.DialContext(ctx, network, addr)
}

func (d *DirectDialer) ListenPacket(ctx context.Context, addr string) (net.PacketConn, error) {
	return net.ListenPacket("udp", ":0")
}

func (d *DirectDialer) GetHTTPTransport() *http.Transport {
	return NewHTTPTransport(d.DialContext)
}

func (d *DirectDialer) Name() string {
	return "direct"
}
