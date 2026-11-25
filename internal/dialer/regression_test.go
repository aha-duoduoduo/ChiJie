package dialer

import (
	"context"
	"io"
	"net"
	"strconv"
	"testing"
	"time"
)

func TestRegressionProxyDialCancellation(t *testing.T) {
	for _, kind := range []string{"socks5", "http_proxy"} {
		t.Run(kind, func(t *testing.T) {
			listener, err := net.Listen("tcp", "127.0.0.1:0")
			if err != nil {
				t.Fatal(err)
			}
			defer listener.Close()
			accepted := make(chan net.Conn, 1)
			go func() { c, _ := listener.Accept(); accepted <- c }()
			host, port, _ := net.SplitHostPort(listener.Addr().String())
			n, _ := strconv.Atoi(port)
			d, err := NewDialer(&Node{Name: "audit", Type: kind, Server: host, Port: n})
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			done := make(chan error, 1)
			go func() {
				c, e := d.DialContext(ctx, "tcp", "example.com:443")
				if c != nil {
					c.Close()
				}
				done <- e
			}()
			peer := <-accepted
			defer peer.Close()
			first := make([]byte, 1)
			_ = peer.SetReadDeadline(time.Now().Add(time.Second))
			if _, err := io.ReadFull(peer, first); err != nil {
				t.Fatal(err)
			}
			cancel()
			select {
			case e := <-done:
				if e == nil {
					t.Fatal("canceled handshake succeeded")
				}
			case <-time.After(time.Second):
				_ = peer.Close()
				t.Fatal("handshake ignored cancellation")
			}

		})
	}
}
