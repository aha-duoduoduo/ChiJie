package server

import (
	"context"
	"net"
	"sync"
	"sync/atomic"
	"time"

	"chijie/internal/traffic"
	"chijie/pkg/relay"

	"github.com/gorilla/websocket"
)

type sessionKindKey struct{}

type sessionTraffic struct {
	ioMu           sync.RWMutex
	closed         bool
	store          *traffic.Store
	trace          traffic.Trace
	started        time.Time
	sent, received atomic.Int64
	once           sync.Once
}

func (s *Server) sessionTraffic(ctx context.Context, req relay.OpenRequest, result relay.OpenResponse, started time.Time, err error) *sessionTraffic {
	kind, _ := ctx.Value(sessionKindKey{}).(string)
	if kind == "" {
		kind = "relay"
	}
	t := &sessionTraffic{store: s.traffic, started: started, trace: traffic.Trace{
		Kind: kind, Method: req.Network, URL: req.Network + "://" + req.Address, Target: req.Address,
		Region: req.Egress.Region, Strategy: req.Egress.Strategy, Residential: req.Egress.Residential,
		Premium: req.Egress.Premium, EgressPool: result.Pool, EgressNode: result.Node, Status: 200,
	}}
	if err != nil {
		t.trace.Status = 502
		t.trace.Error = err.Error()
		t.finish(false)
	} else if t.store != nil {
		t.store.TunnelOpened()
	}
	return t
}

func (t *sessionTraffic) begin() bool {
	t.ioMu.RLock()
	if t.closed {
		t.ioMu.RUnlock()
		return false
	}
	return true
}

func (t *sessionTraffic) finish(active bool) {
	t.once.Do(func() {
		t.ioMu.Lock()
		defer t.ioMu.Unlock()
		t.closed = true
		if t.store == nil {
			return
		}
		if active {
			t.store.TunnelClosed()
		}
		t.trace.LatencyMS = time.Since(t.started).Milliseconds()
		t.trace.RequestBytes = t.sent.Load()
		t.trace.ResponseBytes = t.received.Load()
		t.store.Record(t.trace)
	})
}

type meteredConn struct {
	net.Conn
	traffic *sessionTraffic
}

func (c *meteredConn) Read(p []byte) (int, error) {
	if !c.traffic.begin() {
		return 0, net.ErrClosed
	}
	defer c.traffic.ioMu.RUnlock()
	n, err := c.Conn.Read(p)
	c.traffic.received.Add(int64(n))
	return n, err
}
func (c *meteredConn) Write(p []byte) (int, error) {
	if !c.traffic.begin() {
		return 0, net.ErrClosed
	}
	defer c.traffic.ioMu.RUnlock()
	n, err := c.Conn.Write(p)
	c.traffic.sent.Add(int64(n))
	return n, err
}
func (c *meteredConn) Close() error { err := c.Conn.Close(); c.traffic.finish(true); return err }

type meteredPackets struct {
	packetSession
	traffic *sessionTraffic
}

func (p *meteredPackets) Send(data []byte) error {
	if !p.traffic.begin() {
		return net.ErrClosed
	}
	defer p.traffic.ioMu.RUnlock()
	err := p.packetSession.Send(data)
	if err == nil {
		p.traffic.sent.Add(int64(len(data)))
	}
	return err
}
func (p *meteredPackets) Receive() ([]byte, error) {
	if !p.traffic.begin() {
		return nil, net.ErrClosed
	}
	defer p.traffic.ioMu.RUnlock()
	data, err := p.packetSession.Receive()
	p.traffic.received.Add(int64(len(data)))
	return data, err
}
func (p *meteredPackets) Close() error {
	err := p.packetSession.Close()
	p.traffic.finish(true)
	return err
}

func (s *Server) openClientTCP(ctx context.Context, req relay.OpenRequest, wsDialer *websocket.Dialer, kind string) (net.Conn, relay.OpenResponse, error) {
	ctx = context.WithValue(ctx, sessionKindKey{}, kind)
	if s.relayUpstreamURL == "" {
		return s.openRelayTCP(ctx, req)
	}
	started := time.Now()
	conn, result, err := relay.DialTCPWithDialer(ctx, wsDialer, s.relayUpstreamURL, s.relayUpstreamToken, req)
	t := s.sessionTraffic(ctx, req, result, started, err)
	if err != nil {
		return nil, result, err
	}
	return &meteredConn{Conn: conn, traffic: t}, result, nil
}

func (s *Server) openClientUDP(ctx context.Context, req relay.OpenRequest, wsDialer *websocket.Dialer, kind string) (packetSession, relay.OpenResponse, error) {
	ctx = context.WithValue(ctx, sessionKindKey{}, kind)
	if s.relayUpstreamURL == "" {
		return s.openRelayUDP(ctx, req)
	}
	started := time.Now()
	conn, result, err := relay.DialUDPWithDialer(ctx, wsDialer, s.relayUpstreamURL, s.relayUpstreamToken, req)
	t := s.sessionTraffic(ctx, req, result, started, err)
	if err != nil {
		return nil, result, err
	}
	return &meteredPackets{packetSession: conn, traffic: t}, result, nil
}
