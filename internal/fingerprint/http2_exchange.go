package fingerprint

import (
	"bytes"
	"fmt"
	"io"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"golang.org/x/net/http2"
	"golang.org/x/net/http2/hpack"
)

// Each fingerprint request owns one connection and stream. The read loop must
// run while uploading, since SETTINGS and WINDOW_UPDATE control the writer.
type http2Upload struct {
	mu                          sync.Mutex
	changed                     *sync.Cond
	writeMu                     sync.Mutex
	connection, stream, initial int64
	frameSize                   int
	ready, stopped              bool
}

func newHTTP2Upload() *http2Upload {
	u := &http2Upload{connection: 65535, stream: 65535, initial: 65535, frameSize: 16384}
	u.changed = sync.NewCond(&u.mu)
	return u
}

func (u *http2Upload) stop() {
	u.mu.Lock()
	u.stopped = true
	u.changed.Broadcast()
	u.mu.Unlock()
}

func (u *http2Upload) write(fn func() error) error {
	u.writeMu.Lock()
	defer u.writeMu.Unlock()
	return fn()
}

func (u *http2Upload) send(f *http2.Framer, body []byte) error {
	for len(body) > 0 {
		u.mu.Lock()
		for !u.stopped && (!u.ready || u.connection <= 0 || u.stream <= 0) {
			u.changed.Wait()
		}
		if u.stopped {
			u.mu.Unlock()
			return net.ErrClosed
		}
		n := min(len(body), u.frameSize, int(u.connection), int(u.stream))
		u.connection -= int64(n)
		u.stream -= int64(n)
		u.mu.Unlock()
		chunk := body[:n]
		body = body[n:]
		if err := u.write(func() error { return f.WriteData(1, len(body) == 0, chunk) }); err != nil {
			return err
		}
	}
	return nil
}

func (u *http2Upload) settings(f *http2.SettingsFrame) error {
	u.mu.Lock()
	defer u.mu.Unlock()
	if err := f.ForeachSetting(func(s http2.Setting) error {
		if err := s.Valid(); err != nil {
			return err
		}
		switch s.ID {
		case http2.SettingInitialWindowSize:
			u.stream += int64(s.Val) - u.initial
			u.initial = int64(s.Val)
			if u.stream > 1<<31-1 {
				return fmt.Errorf("HTTP/2 stream window overflow")
			}
		case http2.SettingMaxFrameSize:
			u.frameSize = int(s.Val)
		}
		return nil
	}); err != nil {
		return err
	}
	u.ready = true
	u.changed.Broadcast()
	return nil
}

func (u *http2Upload) update(f *http2.WindowUpdateFrame) error {
	u.mu.Lock()
	defer u.mu.Unlock()
	window := &u.connection
	if f.StreamID == 1 {
		window = &u.stream
	} else if f.StreamID != 0 {
		return nil
	}
	*window += int64(f.Increment)
	if *window > 1<<31-1 {
		return fmt.Errorf("HTTP/2 window overflow")
	}
	u.changed.Broadcast()
	return nil
}

func writeHTTP2Headers(f *http2.Framer, block []byte, endStream bool, priority http2.PriorityParam) error {
	n := min(len(block), 16384)
	if !priority.IsZero() {
		n = min(n, 16384-5)
	}
	if err := f.WriteHeaders(http2.HeadersFrameParam{StreamID: 1, BlockFragment: block[:n], EndHeaders: n == len(block), EndStream: endStream, Priority: priority}); err != nil {
		return err
	}
	block = block[n:]
	for len(block) > 0 {
		n = min(len(block), 16384)
		if err := f.WriteContinuation(1, n == len(block), block[:n]); err != nil {
			return err
		}
		block = block[n:]
	}
	return nil
}

func readHTTP2Response(conn net.Conn, f *http2.Framer, req *http.Request, maxBody int64, headerWait time.Duration) (*http.Response, error) {
	return exchangeHTTP2(conn, f, req, nil, defaultHTTP2Settings(), maxBody, headerWait)
}

func exchangeHTTP2(conn net.Conn, f *http2.Framer, req *http.Request, upload []byte, settings []http2.Setting, maxBody int64, headerWait time.Duration) (*http.Response, error) {
	if maxBody <= 0 {
		maxBody = defaultHTTP2ResponseBodyLimit
	}
	tableSize := uint32(4096)
	for _, setting := range settings {
		if setting.ID == http2.SettingHeaderTableSize {
			tableSize = setting.Val
		}
		if setting.ID == http2.SettingMaxFrameSize {
			f.SetMaxReadFrameSize(setting.Val)
		}
	}
	f.ReadMetaHeaders = hpack.NewDecoder(tableSize, nil)
	f.MaxHeaderListSize = 256 * 1024
	u := newHTTP2Upload()
	var deadlineMu sync.Mutex
	headersReceived := false
	setHeaderDeadline := func() {
		deadlineMu.Lock()
		defer deadlineMu.Unlock()
		if conn == nil || headersReceived || headerWait <= 0 {
			return
		}
		deadline := time.Now().Add(headerWait)
		if d, ok := req.Context().Deadline(); ok && d.Before(deadline) {
			deadline = d
		}
		_ = conn.SetReadDeadline(deadline)
	}
	clearHeaderDeadline := func() {
		deadlineMu.Lock()
		defer deadlineMu.Unlock()
		headersReceived = true
		if conn != nil {
			deadline, _ := req.Context().Deadline()
			_ = conn.SetReadDeadline(deadline)
		}
	}
	writerDone := make(chan error, 1)
	if len(upload) == 0 {
		writerDone <- nil
		setHeaderDeadline()
	} else {
		go func() {
			err := u.send(f, upload)
			if err == nil {
				setHeaderDeadline()
			}
			writerDone <- err
			if err != nil && conn != nil {
				_ = conn.Close()
			}
		}()
	}
	defer func() {
		u.stop()
		if conn != nil {
			_ = conn.Close()
		}
		<-writerDone
	}()
	var body bytes.Buffer
	var headers http.Header
	trailers := make(http.Header)
	status, informational := 0, 0
	for {
		frame, err := f.ReadFrame()
		if err != nil {
			return nil, fmt.Errorf("read http2 frame: %w", err)
		}
		ended := false
		switch frame := frame.(type) {
		case *http2.SettingsFrame:
			if !frame.IsAck() {
				if err := u.settings(frame); err != nil {
					return nil, err
				}
				if err := u.write(f.WriteSettingsAck); err != nil {
					return nil, err
				}
			}
		case *http2.WindowUpdateFrame:
			if err := u.update(frame); err != nil {
				return nil, err
			}
		case *http2.PingFrame:
			if !frame.IsAck() {
				if err := u.write(func() error { return f.WritePing(true, frame.Data) }); err != nil {
					return nil, err
				}
			}
		case *http2.MetaHeadersFrame:
			if frame.StreamID != 1 {
				return nil, fmt.Errorf("unexpected HTTP/2 response stream")
			}
			if frame.Truncated {
				return nil, fmt.Errorf("HTTP/2 response headers too large")
			}
			ended = frame.StreamEnded()
			if status == 0 {
				rawStatus := frame.PseudoValue("status")
				code, err := strconv.Atoi(rawStatus)
				if err != nil || len(rawStatus) != 3 || code < 100 || code > 599 || code == 101 {
					return nil, fmt.Errorf("invalid HTTP/2 response status %q", rawStatus)
				}
				for _, field := range frame.PseudoFields() {
					if field.Name != ":status" {
						return nil, fmt.Errorf("invalid response pseudo-header")
					}
				}
				if code < 200 {
					informational++
					if ended || informational > 10 {
						return nil, fmt.Errorf("invalid informational response")
					}
					continue
				}
				status = code
				headers = make(http.Header)
				for _, field := range frame.RegularFields() {
					headers.Add(field.Name, field.Value)
				}
				clearHeaderDeadline()
			} else {
				if len(frame.PseudoFields()) != 0 || !ended {
					return nil, fmt.Errorf("invalid HTTP/2 trailers")
				}
				for _, field := range frame.RegularFields() {
					switch strings.ToLower(field.Name) {
					case "content-length", "transfer-encoding", "trailer":
						return nil, fmt.Errorf("invalid trailer field")
					}
					trailers.Add(field.Name, field.Value)
				}
			}
		case *http2.DataFrame:
			if frame.StreamID != 1 || status == 0 {
				return nil, fmt.Errorf("unexpected HTTP/2 DATA")
			}
			if int64(body.Len())+int64(len(frame.Data())) > maxBody {
				return nil, fmt.Errorf("http2 response body exceeds %d bytes", maxBody)
			}
			body.Write(frame.Data())
			ended = frame.StreamEnded()
			if count := frame.Header().Length; count > 0 {
				if err := u.write(func() error {
					if err := f.WriteWindowUpdate(0, count); err != nil {
						return err
					}
					if !ended {
						return f.WriteWindowUpdate(1, count)
					}
					return nil
				}); err != nil {
					return nil, err
				}
			}
		case *http2.RSTStreamFrame:
			if frame.StreamID == 1 {
				return nil, fmt.Errorf("http2 stream reset: %s", frame.ErrCode)
			}
		case *http2.GoAwayFrame:
			if frame.ErrCode != http2.ErrCodeNo || frame.LastStreamID < 1 {
				return nil, fmt.Errorf("http2 goaway: %s", frame.ErrCode)
			}
		case *http2.PushPromiseFrame:
			return nil, fmt.Errorf("HTTP/2 server push is unsupported")
		}
		if ended {
			if status == 0 {
				return nil, fmt.Errorf("missing HTTP/2 status")
			}
			return &http.Response{Status: fmt.Sprintf("%d %s", status, http.StatusText(status)), StatusCode: status, Proto: "HTTP/2.0", ProtoMajor: 2, Header: headers, Trailer: trailers, Body: io.NopCloser(bytes.NewReader(body.Bytes())), ContentLength: int64(body.Len()), Request: req}, nil
		}
	}
}
