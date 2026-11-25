package dialer

import (
	"bufio"
	"context"
	"encoding/base64"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"time"

	"chijie/internal/dnsresolver"
)

// HTTPProxyDialer HTTP 代理
type HTTPProxyDialer struct {
	node      *Node
	proxyURL  *url.URL
	basicAuth string
	dialer    *net.Dialer
}

func NewHTTPProxyDialer(node *Node) (*HTTPProxyDialer, error) {
	proxyURL := &url.URL{
		Scheme: "http",
		Host:   fmt.Sprintf("%s:%d", node.Server, node.Port),
	}

	var basicAuth string
	if node.Username != "" {
		proxyURL.User = url.UserPassword(node.Username, node.Password)
		basicAuth = "Basic " + base64.StdEncoding.EncodeToString(
			[]byte(node.Username+":"+node.Password),
		)
	}

	return &HTTPProxyDialer{
		node:      node,
		proxyURL:  proxyURL,
		basicAuth: basicAuth,
		dialer:    dnsresolver.NewDialer(30 * time.Second),
	}, nil
}

func (d *HTTPProxyDialer) DialContext(ctx context.Context, network, addr string) (net.Conn, error) {
	// 连接到代理服务器
	conn, err := (endpointDialer{d.dialer}).DialContext(ctx, network, d.proxyURL.Host)
	if err != nil {
		return nil, fmt.Errorf("connect to proxy: %w", err)
	}

	// Bound the complete handshake, including a proxy that accepts but never replies.
	deadline := time.Now().Add(30 * time.Second)
	if value, ok := ctx.Deadline(); ok && value.Before(deadline) {
		deadline = value
	}
	_ = conn.SetDeadline(deadline)
	stopped := make(chan struct{})
	stop := context.AfterFunc(ctx, func() { _ = conn.Close(); close(stopped) })
	defer stop()

	// 发送 CONNECT 请求
	connectReq := &http.Request{
		Method: "CONNECT",
		URL:    &url.URL{Opaque: addr},
		Host:   addr,
		Header: make(http.Header),
	}

	if d.basicAuth != "" {
		connectReq.Header.Set("Proxy-Authorization", d.basicAuth)
	}

	if err := connectReq.Write(conn); err != nil {
		conn.Close()
		return nil, fmt.Errorf("send CONNECT: %w", err)
	}

	// 读取响应
	br := bufio.NewReader(conn)
	resp, err := http.ReadResponse(br, connectReq)
	if err != nil {
		conn.Close()
		return nil, fmt.Errorf("read CONNECT response: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		conn.Close()
		return nil, fmt.Errorf("proxy returned %d", resp.StatusCode)
	}

	if !stop() {
		<-stopped
		return nil, ctx.Err()
	}
	if err := ctx.Err(); err != nil {
		conn.Close()
		return nil, err
	}
	_ = conn.SetDeadline(time.Time{})
	return &bufferedConn{Conn: conn, reader: br}, nil
}

func (d *HTTPProxyDialer) GetHTTPTransport() *http.Transport {
	t := NewHTTPTransport((endpointDialer{d.dialer}).DialContext)
	t.Proxy = http.ProxyURL(d.proxyURL)
	return t
}

func (d *HTTPProxyDialer) Name() string {
	return fmt.Sprintf("http://%s", d.node.Name)
}

// Preserve bytes buffered beyond the CONNECT response headers.
type bufferedConn struct {
	net.Conn
	reader *bufio.Reader
}

func (c *bufferedConn) Read(p []byte) (int, error) { return c.reader.Read(p) }
