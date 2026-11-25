package server

import (
	"chijie/pkg/relay"
	"context"
	"io"
	"testing"
	"time"
)

func TestShutdownClosesActiveRelayAndRecordsBytes(t *testing.T) {
	s, endpoint := newRelayTestServer(t)
	echo := startTCPEcho(t)
	conn, _, err := relay.DialTCP(context.Background(), endpoint, signedAdminToken(t, "test-secret-for-relay"), relay.OpenRequest{Address: echo.Addr().String(), Egress: EgressOptions{Direct: true}})
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(2 * time.Second))
	_, _ = conn.Write([]byte("bytes"))
	data := make([]byte, 5)
	if _, err := io.ReadFull(conn, data); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := s.Shutdown(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := conn.Read(data); err == nil {
		t.Fatal("relay remains open after shutdown")
	}
	metrics := s.traffic.Metrics()
	if metrics.ActiveTunnels != 0 || metrics.RawRequests != 1 || metrics.RequestBytes != 5 || metrics.ResponseBytes != 5 {
		t.Fatalf("incorrect final metrics: %+v", metrics)
	}
}
