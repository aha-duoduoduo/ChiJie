package admin

import (
	"compress/flate"
	"compress/gzip"
	"compress/zlib"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"chijie/internal/fingerprint"
	"chijie/internal/netguard"

	"github.com/andybalholm/brotli"
	"github.com/klauspost/compress/zstd"
)

// handleFingerprintTest POST 测试指纹
func (s *Server) handleFingerprintTest(w http.ResponseWriter, r *http.Request) {
	if r.Method != "POST" {
		http.Error(w, `{"error":"method not allowed"}`, http.StatusMethodNotAllowed)
		return
	}

	var req struct {
		Fingerprint string `json:"fingerprint"`
		URL         string `json:"url"`
	}

	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{
			"error": "invalid request body",
		})
		return
	}

	if req.Fingerprint == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{
			"error": "fingerprint name is required",
		})
		return
	}

	if req.URL == "" {
		req.URL = "https://tls.browserleaks.com/json"
	}
	targetURL, err := url.Parse(req.URL)
	if err != nil || targetURL.Scheme != "https" || targetURL.Host == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{
			"error": "target URL must be an absolute https URL",
		})
		return
	}
	if guardErr := netguard.CheckHost(r.Context(), targetURL.Hostname()); guardErr != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{
			"error": "target host is not allowed: " + guardErr.Error(),
		})
		return
	}

	// 检查指纹是否存在
	spec, ok := s.fpManager.Get(req.Fingerprint)
	if !ok {
		writeJSON(w, http.StatusNotFound, map[string]any{
			"error": "fingerprint not found",
		})
		return
	}

	// 构建 TLS 配置
	helloID, helloSpec, err := s.fpManager.BuildSpec(req.Fingerprint)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{
			"error": "failed to build fingerprint spec: " + err.Error(),
		})
		return
	}
	fingerprintInfo := map[string]any{}
	if helloSpec != nil {
		fingerprintInfo["type"] = "custom"
	} else if helloID != nil {
		fingerprintInfo["type"] = "preset"
		fingerprintInfo["preset"] = spec.Preset
	}
	if spec.WantsHTTP2() {
		fingerprintInfo["http_version"] = "h2"
	}

	transport := &http.Transport{
		MaxIdleConns:          4,
		IdleConnTimeout:       30 * time.Second,
		TLSHandshakeTimeout:   10 * time.Second,
		ExpectContinueTimeout: 1 * time.Second,
	}
	defer transport.CloseIdleConnections()
	safeDial := netguard.Guard((&net.Dialer{Timeout: 10 * time.Second}).DialContext, false)
	var roundTripper http.RoundTripper = transport
	if spec.WantsHTTP2() {
		roundTripper = fingerprint.NewHTTP2RoundTripper(helloID, helloSpec, targetURL.Hostname(), safeDial, spec)
	} else {
		fingerprint.WrapTransportWithDialContext(transport, helloID, helloSpec, targetURL.Hostname(), safeDial)
	}
	client := &http.Client{
		Transport: roundTripper,
		Timeout:   20 * time.Second,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}

	started := time.Now()
	method := spec.EffectiveMethod(http.MethodGet)
	httpReq, err := http.NewRequestWithContext(r.Context(), method, targetURL.String(), nil)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{
			"error": "failed to create test request: " + err.Error(),
		})
		return
	}
	httpReq.Header.Set("Accept", "application/json,text/plain,*/*")
	spec.ApplyRequestDefaults(httpReq)
	if httpReq.Header.Get("User-Agent") == "" {
		httpReq.Header.Set("User-Agent", "Chijie-TLS-Probe/1.0")
	}

	resp, err := client.Do(httpReq)
	latency := time.Since(started)
	if err != nil {
		result := map[string]any{
			"fingerprint": req.Fingerprint,
			"url":         targetURL.String(),
			"status":      "failed",
			"latency_ms":  latency.Milliseconds(),
			"error":       err.Error(),
		}
		for key, value := range fingerprintInfo {
			result[key] = value
		}
		writeJSON(w, http.StatusOK, result)
		return
	}
	defer resp.Body.Close()

	body, readErr := readFingerprintTestBody(resp, 128*1024)
	if readErr != nil {
		result := map[string]any{
			"fingerprint":  req.Fingerprint,
			"url":          targetURL.String(),
			"status":       "failed",
			"http_status":  resp.StatusCode,
			"latency_ms":   latency.Milliseconds(),
			"content_type": resp.Header.Get("Content-Type"),
			"error":        "read response: " + readErr.Error(),
		}
		for key, value := range fingerprintInfo {
			result[key] = value
		}
		writeJSON(w, http.StatusOK, result)
		return
	}

	result := map[string]any{
		"fingerprint": req.Fingerprint,
		"url":         targetURL.String(),
		"status":      "ok",
		"http_status": resp.StatusCode,
		"latency_ms":  latency.Milliseconds(),
		"body_bytes":  len(body),
		"http_proto":  resp.Proto,
	}
	if contentType := resp.Header.Get("Content-Type"); contentType != "" {
		result["content_type"] = contentType
	}
	if encoding := resp.Header.Get("Content-Encoding"); encoding != "" {
		result["content_encoding"] = encoding
	}
	for key, value := range fingerprintInfo {
		result[key] = value
	}

	var observed map[string]any
	if err := json.Unmarshal(body, &observed); err == nil {
		result["observed"] = observed
	} else if len(body) > 0 {
		sample := string(body)
		if len(sample) > 4096 {
			sample = sample[:4096]
		}
		result["body_sample"] = sample
	}

	writeJSON(w, http.StatusOK, result)
}

func readFingerprintTestBody(resp *http.Response, limit int64) ([]byte, error) {
	if resp == nil || resp.Body == nil {
		return nil, nil
	}

	reader, closeFn, err := decodedBodyReader(resp)
	if err != nil {
		return nil, err
	}
	if closeFn != nil {
		defer closeFn()
	}
	return io.ReadAll(io.LimitReader(reader, limit))
}

func decodedBodyReader(resp *http.Response) (io.Reader, func(), error) {
	encoding := strings.ToLower(strings.TrimSpace(resp.Header.Get("Content-Encoding")))
	encoding = strings.Split(encoding, ",")[0]
	switch encoding {
	case "", "identity":
		return resp.Body, nil, nil
	case "gzip":
		reader, err := gzip.NewReader(resp.Body)
		if err != nil {
			return nil, nil, fmt.Errorf("decode gzip response: %w", err)
		}
		return reader, func() { reader.Close() }, nil
	case "deflate":
		reader, err := zlib.NewReader(resp.Body)
		if err == nil {
			return reader, func() { reader.Close() }, nil
		}
		flateReader := flate.NewReader(resp.Body)
		return flateReader, func() { flateReader.Close() }, nil
	case "br":
		return brotli.NewReader(resp.Body), nil, nil
	case "zstd":
		reader, err := zstd.NewReader(resp.Body)
		if err != nil {
			return nil, nil, fmt.Errorf("decode zstd response: %w", err)
		}
		return reader, func() { reader.Close() }, nil
	default:
		return resp.Body, nil, nil
	}
}
