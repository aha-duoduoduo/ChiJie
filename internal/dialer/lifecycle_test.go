package dialer

import (
	"context"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
)

type closingDialer struct {
	*DirectDialer
	closed atomic.Bool
}

func (d *closingDialer) Close() error { d.closed.Store(true); return nil }

func TestManagedRetirementPreservesActiveConnection(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	go func() {
		conn, err := listener.Accept()
		if err == nil {
			defer conn.Close()
			_, _ = io.Copy(conn, conn)
		}
	}()
	base := &closingDialer{DirectDialer: NewDirectDialer()}
	d := Manage(base)
	conn, err := d.DialContext(context.Background(), "tcp", listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	_ = d.Close()
	if base.closed.Load() {
		t.Fatal("closed outbound with an active stream")
	}
	if _, err := d.DialContext(context.Background(), "tcp", listener.Addr().String()); err == nil {
		t.Fatal("retired outbound accepted new work")
	}
	_, _ = conn.Write([]byte("ok"))
	reply := make([]byte, 2)
	if _, err := io.ReadFull(conn, reply); err != nil || string(reply) != "ok" {
		t.Fatalf("active stream interrupted: %v", err)
	}
	_ = conn.Close()
	if !base.closed.Load() {
		t.Fatal("outbound was not released after its final stream")
	}
}

func TestManagedTransportReusesConnections(t *testing.T) {
	var connections atomic.Int32
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = io.WriteString(w, "ok") }))
	server.Config.ConnState = func(_ net.Conn, state http.ConnState) {
		if state == http.StateNew {
			connections.Add(1)
		}
	}
	server.Start()
	defer server.Close()
	d := Manage(NewDirectDialer())
	defer d.Close()
	for i := 0; i < 2; i++ {
		transport, release, err := Transport(d, "default", func(*http.Transport) {})
		if err != nil {
			t.Fatal(err)
		}
		resp, err := (&http.Client{Transport: transport}).Get(server.URL)
		if err != nil {
			t.Fatal(err)
		}
		_, _ = io.Copy(io.Discard, resp.Body)
		resp.Body.Close()
		release()
	}
	if connections.Load() != 1 {
		t.Fatalf("connections = %d, expected reuse", connections.Load())
	}
}
