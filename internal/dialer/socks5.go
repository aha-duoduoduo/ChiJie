package dialer

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"time"

	"chijie/internal/dnsresolver"

	"golang.org/x/net/proxy"
)

// SOCKS5Dialer SOCKS5 代理
type SOCKS5Dialer struct {
	node         *Node
	dialer       proxy.Dialer
	packetDialer *SingBoxDialer
}

func NewSOCKS5Dialer(node *Node) (*SOCKS5Dialer, error) {
	addr := fmt.Sprintf("%s:%d", node.Server, node.Port)

	var auth *proxy.Auth
	if node.Username != "" {
		auth = &proxy.Auth{
			User:     node.Username,
			Password: node.Password,
		}
	}

	dialer, err := proxy.SOCKS5("tcp", addr, auth, endpointDialer{dnsresolver.NewDialer(30 * time.Second)})
	if err != nil {
		return nil, fmt.Errorf("create socks5 dialer: %w", err)
	}
	packetDialer, err := NewSingBoxDialer(node)
	if err != nil {
		return nil, fmt.Errorf("create socks5 UDP dialer: %w", err)
	}

	return &SOCKS5Dialer{
		node:         node,
		dialer:       dialer,
		packetDialer: packetDialer,
	}, nil
}

func (d *SOCKS5Dialer) ListenPacket(ctx context.Context, addr string) (net.PacketConn, error) {
	return d.packetDialer.ListenPacket(ctx, addr)
}

func (d *SOCKS5Dialer) DialContext(ctx context.Context, network, addr string) (net.Conn, error) {
	return d.dialer.(proxy.ContextDialer).DialContext(ctx, network, addr)
}

func (d *SOCKS5Dialer) GetHTTPTransport() *http.Transport {
	return NewHTTPTransport(d.DialContext)
}

func (d *SOCKS5Dialer) Name() string {
	return fmt.Sprintf("socks5://%s", d.node.Name)
}

func (d *SOCKS5Dialer) Close() error { return d.packetDialer.Close() }
