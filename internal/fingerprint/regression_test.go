package fingerprint

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"golang.org/x/net/http2"
	"golang.org/x/net/http2/hpack"
)

func TestRegressionFingerprintReloadRace(t *testing.T) {
	path := filepath.Join(t.TempDir(), "fingerprints.yaml")
	if err := os.WriteFile(path, []byte("fingerprints:\n  audit:\n    preset: chrome\n"), 0600); err != nil {
		t.Fatal(err)
	}
	m := NewManager()
	if err := m.LoadFromFile(path); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		for i := 0; i < 100; i++ {
			_ = m.LoadFromFile(path)
		}
	}()
	go func() {
		defer wg.Done()
		for i := 0; i < 10000; i++ {
			_, _ = m.ConfigFromValue("audit")
			_ = m.List()
		}
	}()
	wg.Wait()
}

func TestRegressionHTTP2FlowControl(t *testing.T) {
	for _, size := range []int{16 * 1024, 128 * 1024} {
		t.Run(fmt.Sprintf("%dKiB", size/1024), func(t *testing.T) {
			listener, err := net.Listen("tcp", "127.0.0.1:0")
			if err != nil {
				t.Fatal(err)
			}
			defer listener.Close()
			finished := make(chan struct{})
			go func() {
				defer close(finished)
				c, e := listener.Accept()
				if e != nil {
					return
				}
				defer c.Close()
				(&http2.Server{}).ServeConn(c, &http2.ServeConnOpts{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write(bytes.Repeat([]byte("x"), size)) })})
			}()
			conn, err := net.Dial("tcp", listener.Addr().String())
			if err != nil {
				t.Fatal(err)
			}
			defer func() { conn.Close(); <-finished }()
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			deadline, _ := ctx.Deadline()
			_ = conn.SetDeadline(deadline)
			req, _ := http.NewRequestWithContext(ctx, "GET", "https://audit.test/", nil)
			framer := http2.NewFramer(conn, conn)
			_, _ = io.WriteString(conn, http2.ClientPreface)
			_ = framer.WriteSettings(defaultHTTP2Settings()...)
			block, err := encodeHTTP2Headers(req, nil)
			if err != nil {
				t.Fatal(err)
			}
			_ = framer.WriteHeaders(http2.HeadersFrameParam{StreamID: 1, BlockFragment: block, EndHeaders: true, EndStream: true})
			resp, err := readHTTP2Response(conn, framer, req, 32*1024*1024, 0)
			if err != nil {
				t.Fatal(err)
			}
			data, err := io.ReadAll(resp.Body)
			resp.Body.Close()
			if err != nil || len(data) != size {
				t.Fatalf("body=%d, err=%v", len(data), err)
			}

		})
	}
}

func TestRegressionHTTP2TrailersOverwriteStatus(t *testing.T) {
	var wire bytes.Buffer
	f := http2.NewFramer(&wire, nil)
	encode := func(fields ...hpack.HeaderField) []byte {
		var b bytes.Buffer
		e := hpack.NewEncoder(&b)
		for _, field := range fields {
			_ = e.WriteField(field)
		}
		return b.Bytes()
	}
	_ = f.WriteHeaders(http2.HeadersFrameParam{StreamID: 1, BlockFragment: encode(hpack.HeaderField{Name: ":status", Value: "404"}, hpack.HeaderField{Name: "content-type", Value: "text/plain"}), EndHeaders: true})
	_ = f.WriteData(1, false, []byte("not found"))
	_ = f.WriteHeaders(http2.HeadersFrameParam{StreamID: 1, BlockFragment: encode(hpack.HeaderField{Name: "x-trailer", Value: "done"}), EndHeaders: true, EndStream: true})
	req, _ := http.NewRequest("GET", "https://audit.test/", nil)
	resp, err := readHTTP2Response(nil, http2.NewFramer(io.Discard, &wire), req, 1024, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 404 || resp.Header.Get("Content-Type") != "text/plain" || resp.Trailer.Get("X-Trailer") != "done" {
		t.Fatalf("unexpected %d %v", resp.StatusCode, resp.Header)
	}
}
