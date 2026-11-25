// Package relay implements the public version-one WebSocket transport.
// Each WebSocket carries one TCP stream or one fixed-destination UDP association.
package relay

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

const MaxDatagram = 65507
const MaxMessageSize = 128 * 1024

type EgressOptions struct {
	Direct         bool   `json:"direct,omitempty" yaml:"direct,omitempty"`
	Gateway        string `json:"gateway,omitempty" yaml:"gateway,omitempty"`
	Pool           string `json:"pool,omitempty" yaml:"pool,omitempty"`
	Node           string `json:"node,omitempty" yaml:"node,omitempty"`
	Region         string `json:"region" yaml:"region"`
	Strategy       string `json:"strategy" yaml:"strategy"`
	Residential    bool   `json:"residential" yaml:"residential"`
	Premium        bool   `json:"premium" yaml:"premium"`
	TLSFingerprint string `json:"tls_fingerprint" yaml:"tls_fingerprint"`
	Any            bool   `json:"any" yaml:"any"`
	MaxLatencyMS   int    `json:"max_latency_ms" yaml:"max_latency_ms"`
}

type OpenRequest struct {
	Version     int           `json:"version" yaml:"version"`
	Network     string        `json:"network" yaml:"network"`
	Address     string        `json:"address" yaml:"address"`
	Egress      EgressOptions `json:"egress" yaml:"egress"`
	Hop         int           `json:"hop,omitempty" yaml:"hop,omitempty"`
	AffinityKey string        `json:"affinity_key,omitempty" yaml:"affinity_key,omitempty"`
	AffinityTTL string        `json:"affinity_ttl,omitempty" yaml:"affinity_ttl,omitempty"`
}

type OpenResponse struct {
	Status string `json:"status" yaml:"status"`
	Error  string `json:"error,omitempty" yaml:"error,omitempty"`
	Pool   string `json:"pool,omitempty" yaml:"pool,omitempty"`
	Node   string `json:"node,omitempty" yaml:"node,omitempty"`
}

func Open(ctx context.Context, endpoint, token string, request OpenRequest) (*websocket.Conn, OpenResponse, error) {
	return OpenWithDialer(ctx, nil, endpoint, token, request)
}

// OpenWithDialer permits callers to supply TLS settings, for example a private CA.
func OpenWithDialer(ctx context.Context, custom *websocket.Dialer, endpoint, token string, request OpenRequest) (*websocket.Conn, OpenResponse, error) {
	if request.Version == 0 {
		request.Version = 1
	}
	header := http.Header{}
	if token != "" {
		header.Set("Authorization", "Bearer "+token)
	}
	dialer := websocket.Dialer{HandshakeTimeout: 15 * time.Second, EnableCompression: false}
	if custom != nil {
		dialer = *custom
		dialer.EnableCompression = false
	}
	ws, response, err := dialer.DialContext(ctx, endpoint, header)
	if err != nil {
		if response != nil {
			return nil, OpenResponse{}, fmt.Errorf("relay upgrade: HTTP %d: %w", response.StatusCode, err)
		}
		return nil, OpenResponse{}, err
	}
	ws.SetReadLimit(MaxMessageSize)
	if err := ws.WriteJSON(request); err != nil {
		ws.Close()
		return nil, OpenResponse{}, err
	}
	_ = ws.SetReadDeadline(time.Now().Add(30 * time.Second))
	var result OpenResponse
	if err := ws.ReadJSON(&result); err != nil {
		ws.Close()
		return nil, OpenResponse{}, err
	}
	_ = ws.SetReadDeadline(time.Time{})
	if result.Status != "connected" {
		ws.Close()
		return nil, result, fmt.Errorf("relay open: %s", result.Error)
	}
	return ws, result, nil
}

// Stream presents a WebSocket connection as a TCP byte stream.
type Stream struct {
	WS      *websocket.Conn
	reader  io.Reader
	readMu  sync.Mutex
	writeMu sync.Mutex
}

func NewStream(ws *websocket.Conn) *Stream { return &Stream{WS: ws} }

func (s *Stream) Read(p []byte) (int, error) {
	s.readMu.Lock()
	defer s.readMu.Unlock()
	for {
		if s.reader != nil {
			n, err := s.reader.Read(p)
			if err == nil || n > 0 {
				return n, nil
			}
			if !errors.Is(err, io.EOF) {
				return n, err
			}
			s.reader = nil
		}
		kind, reader, err := s.WS.NextReader()
		if err != nil {
			return 0, err
		}
		if kind != websocket.BinaryMessage {
			return 0, fmt.Errorf("unexpected relay frame")
		}
		s.reader = reader
	}
}

func (s *Stream) Write(p []byte) (int, error) {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	written := 0
	for len(p) > 0 {
		size := min(len(p), MaxMessageSize)
		if err := s.WS.WriteMessage(websocket.BinaryMessage, p[:size]); err != nil {
			return written, err
		}
		written += size
		p = p[size:]
	}
	return written, nil
}

func (s *Stream) Close() error                       { return s.WS.Close() }
func (s *Stream) LocalAddr() net.Addr                { return s.WS.UnderlyingConn().LocalAddr() }
func (s *Stream) RemoteAddr() net.Addr               { return s.WS.UnderlyingConn().RemoteAddr() }
func (s *Stream) SetDeadline(t time.Time) error      { return s.WS.UnderlyingConn().SetDeadline(t) }
func (s *Stream) SetReadDeadline(t time.Time) error  { return s.WS.SetReadDeadline(t) }
func (s *Stream) SetWriteDeadline(t time.Time) error { return s.WS.SetWriteDeadline(t) }

// Datagram keeps one UDP packet per binary WebSocket frame.
type Datagram struct {
	WS      *websocket.Conn
	writeMu sync.Mutex
}

func NewDatagram(ws *websocket.Conn) *Datagram { return &Datagram{WS: ws} }
func (d *Datagram) Send(packet []byte) error {
	if len(packet) > MaxDatagram {
		return fmt.Errorf("UDP packet too large")
	}
	d.writeMu.Lock()
	defer d.writeMu.Unlock()
	return d.WS.WriteMessage(websocket.BinaryMessage, packet)
}
func (d *Datagram) Receive() ([]byte, error) {
	kind, data, err := d.WS.ReadMessage()
	if err != nil {
		return nil, err
	}
	if kind != websocket.BinaryMessage {
		return nil, fmt.Errorf("unexpected relay frame")
	}
	if len(data) > MaxDatagram {
		return nil, fmt.Errorf("UDP packet too large")
	}
	return bytes.Clone(data), nil
}
func (d *Datagram) Close() error { return d.WS.Close() }

// DialTCP and DialUDP are convenient entry points for clients using this module.
func DialTCP(ctx context.Context, endpoint, token string, request OpenRequest) (net.Conn, OpenResponse, error) {
	return DialTCPWithDialer(ctx, nil, endpoint, token, request)
}
func DialTCPWithDialer(ctx context.Context, dialer *websocket.Dialer, endpoint, token string, request OpenRequest) (net.Conn, OpenResponse, error) {
	request.Network = "tcp"
	ws, result, err := OpenWithDialer(ctx, dialer, endpoint, token, request)
	if err != nil {
		return nil, result, err
	}
	return NewStream(ws), result, nil
}
func DialUDP(ctx context.Context, endpoint, token string, request OpenRequest) (*Datagram, OpenResponse, error) {
	return DialUDPWithDialer(ctx, nil, endpoint, token, request)
}
func DialUDPWithDialer(ctx context.Context, dialer *websocket.Dialer, endpoint, token string, request OpenRequest) (*Datagram, OpenResponse, error) {
	request.Network = "udp"
	ws, result, err := OpenWithDialer(ctx, dialer, endpoint, token, request)
	if err != nil {
		return nil, result, err
	}
	return NewDatagram(ws), result, nil
}

var _ net.Conn = (*Stream)(nil)
