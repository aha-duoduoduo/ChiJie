package fingerprint

import (
	"bytes"
	"context"
	"io"
	"net"
	"net/http"
	"testing"
	"time"

	"golang.org/x/net/http2"
)

func TestHTTP2LargeUploadDownloadAndTrailers(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	upload := bytes.Repeat([]byte("upload"), 400000)
	download := bytes.Repeat([]byte("download"), 150000)
	served := make(chan error, 1)
	go func() {
		conn, err := listener.Accept()
		if err != nil {
			served <- err
			return
		}
		defer conn.Close()
		server := &http2.Server{MaxUploadBufferPerConnection: 128 * 1024, MaxUploadBufferPerStream: 128 * 1024}
		server.ServeConn(conn, &http2.ServeConnOpts{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			data, err := io.ReadAll(r.Body)
			if err == nil && !bytes.Equal(data, upload) {
				err = io.ErrUnexpectedEOF
			}
			if err != nil {
				served <- err
				return
			}
			w.Header().Set("Trailer", "X-Result")
			w.Header().Set("Content-Type", "application/octet-stream")
			w.WriteHeader(404)
			_, err = w.Write(download)
			w.Header().Set("X-Result", "complete")
			served <- err
		})})
	}()
	conn, err := net.Dial("tcp", listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	deadline, _ := ctx.Deadline()
	_ = conn.SetDeadline(deadline)
	req, _ := http.NewRequestWithContext(ctx, "POST", "https://local.test/upload", nil)
	f := http2.NewFramer(conn, conn)
	_, _ = io.WriteString(conn, http2.ClientPreface)
	settings := defaultHTTP2Settings()
	_ = f.WriteSettings(settings...)
	block, err := encodeHTTP2Headers(req, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := writeHTTP2Headers(f, block, false, http2.PriorityParam{}); err != nil {
		t.Fatal(err)
	}
	resp, err := exchangeHTTP2(conn, f, req, upload, settings, 4*1024*1024, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	if err != nil || !bytes.Equal(data, download) {
		t.Fatalf("download length %d: %v", len(data), err)
	}
	if resp.StatusCode != 404 || resp.Trailer.Get("X-Result") != "complete" {
		t.Fatalf("lost response status/trailer: %+v", resp)
	}
	if err := <-served; err != nil {
		t.Fatal(err)
	}
}
