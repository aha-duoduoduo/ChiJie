package dialer

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"sync"
	"time"
)

// Managed owns a configured outbound. Retirement rejects new connections while
// established streams keep the underlying outbound alive until their close.
type Managed struct {
	Dialer
	mu              sync.Mutex
	active          int
	retired, closed bool
	transports      map[string]*http.Transport
}

func Manage(d Dialer) *Managed {
	return &Managed{Dialer: d, transports: make(map[string]*http.Transport)}
}

func (d *Managed) acquire() error {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.retired {
		return net.ErrClosed
	}
	d.active++
	return nil
}

func (d *Managed) release() {
	d.mu.Lock()
	d.active--
	closeNow := d.retired && d.active == 0 && !d.closed
	if closeNow {
		d.closed = true
	}
	d.mu.Unlock()
	if closeNow {
		_ = Close(d.Dialer)
	}
}

func (d *Managed) dial(ctx context.Context, network, addr string, base func(context.Context, string, string) (net.Conn, error)) (net.Conn, error) {
	if err := d.acquire(); err != nil {
		return nil, err
	}
	conn, err := base(ctx, network, addr)
	if err != nil {
		d.release()
		return nil, err
	}
	return &ownedConn{Conn: conn, release: d.release}, nil
}

func (d *Managed) DialContext(ctx context.Context, network, addr string) (net.Conn, error) {
	return d.dial(ctx, network, addr, d.Dialer.DialContext)
}

func (d *Managed) ListenPacket(ctx context.Context, addr string) (net.PacketConn, error) {
	base, ok := d.Dialer.(PacketDialer)
	if !ok {
		return nil, fmt.Errorf("outbound does not support UDP")
	}
	if err := d.acquire(); err != nil {
		return nil, err
	}
	conn, err := base.ListenPacket(ctx, addr)
	if err != nil {
		d.release()
		return nil, err
	}
	return &ownedPacketConn{PacketConn: conn, release: d.release}, nil
}

func (d *Managed) GetHTTPTransport() *http.Transport {
	t := d.Dialer.GetHTTPTransport()
	base := t.DialContext
	if base == nil {
		base = (&net.Dialer{Timeout: 30 * time.Second}).DialContext
	}
	t.DialContext = func(ctx context.Context, network, addr string) (net.Conn, error) {
		return d.dial(ctx, network, addr, base)
	}
	return t
}

func (d *Managed) CachedTransport(key string, configure func(*http.Transport)) (*http.Transport, error) {
	d.mu.Lock()
	if d.retired {
		d.mu.Unlock()
		return nil, net.ErrClosed
	}
	if transport := d.transports[key]; transport != nil {
		d.mu.Unlock()
		return transport, nil
	}
	transport := d.GetHTTPTransport()
	configure(transport)
	var evicted *http.Transport
	if len(d.transports) >= 4 {
		for name, value := range d.transports {
			delete(d.transports, name)
			evicted = value
			break
		}
	}
	d.transports[key] = transport
	d.mu.Unlock()
	if evicted != nil {
		evicted.CloseIdleConnections()
	}
	return transport, nil
}

func (d *Managed) Close() error {
	d.mu.Lock()
	d.retired = true
	transports := d.transports
	d.transports = nil
	closeNow := d.active == 0 && !d.closed
	if closeNow {
		d.closed = true
	}
	d.mu.Unlock()
	for _, transport := range transports {
		transport.CloseIdleConnections()
	}
	if closeNow {
		return Close(d.Dialer)
	}
	return nil
}

func Close(d Dialer) error {
	if closer, ok := d.(io.Closer); ok {
		return closer.Close()
	}
	return nil
}

func IsDirect(d Dialer) bool {
	if managed, ok := d.(*Managed); ok {
		d = managed.Dialer
	}
	_, ok := d.(*DirectDialer)
	return ok
}

// Transport returns a reusable transport when the outbound has an owner;
// otherwise its caller must release the temporary transport after the request.
func Transport(d Dialer, key string, configure func(*http.Transport)) (*http.Transport, func(), error) {
	if cache, ok := d.(interface {
		CachedTransport(string, func(*http.Transport)) (*http.Transport, error)
	}); ok {
		t, err := cache.CachedTransport(key, configure)
		return t, func() {}, err
	}
	t := d.GetHTTPTransport()
	configure(t)
	return t, t.CloseIdleConnections, nil
}

func NewHTTPTransport(dial func(context.Context, string, string) (net.Conn, error)) *http.Transport {
	return &http.Transport{DialContext: dial, MaxIdleConns: 100, MaxIdleConnsPerHost: 8, IdleConnTimeout: 90 * time.Second, TLSHandshakeTimeout: 10 * time.Second, ExpectContinueTimeout: time.Second}
}

type ownedConn struct {
	net.Conn
	once    sync.Once
	release func()
}

func (c *ownedConn) Close() error { err := c.Conn.Close(); c.once.Do(c.release); return err }

type ownedPacketConn struct {
	net.PacketConn
	once    sync.Once
	release func()
}

func (c *ownedPacketConn) Close() error {
	err := c.PacketConn.Close()
	c.once.Do(c.release)
	return err
}
