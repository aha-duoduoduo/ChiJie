package server

import (
	"container/list"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"chijie/internal/auth"
	"chijie/internal/dialer"
	"chijie/internal/fingerprint"
	"chijie/internal/pool"
	"chijie/internal/traffic"
	"chijie/internal/util"

	"github.com/gorilla/websocket"
)

// MaxProxyBodyBytes 是 /proxy 单次请求允许的最大 body 大小。
const MaxProxyBodyBytes = 10 * 1024 * 1024

// MaxProxyResponseBytes 是 /proxy 单次上游响应允许读取的最大 body 大小。
const MaxProxyResponseBytes = 32 * 1024 * 1024

// DefaultProxyMaxAttempts 是一次 /proxy 请求在普通出口执行失败时默认允许尝试的可用节点数量。
const DefaultProxyMaxAttempts = 5

// DefaultProxyResponseHeaderTimeout 是一次 /proxy 出口 HTTP 请求等待目标响应头的默认超时。
const DefaultProxyResponseHeaderTimeout = 3 * time.Second

// DefaultProxyTotalTimeout 是一次 /proxy 出口 HTTP 请求从开始到响应体读取完成的默认总超时。
const DefaultProxyTotalTimeout = 30 * time.Second

// DefaultProxyMaxRedirects 是 follow_redirects=true 时单次 /proxy 请求允许跟随的默认跳转次数。
const DefaultProxyMaxRedirects = 5

// MaxChijieProxyHops 限制 Chijie 之间互相转发时的最大跳数，防止 A/B 循环。
const MaxChijieProxyHops = 3

const chijieHopHeader = "X-Chijie-Hop"

const chijieErrorHeader = "X-Chijie-Error"

const chijieFinalURLHeader = "X-Chijie-Final-URL"

const chijieRedirectCountHeader = "X-Chijie-Redirect-Count"

const chijieMaxRedirectsHeader = "X-Chijie-Max-Redirects"

const chijieRedirectsHeader = "X-Chijie-Redirects"

const chijieRedirectLimitReachedHeader = "X-Chijie-Redirect-Limit-Reached"

var errProxyResponseTooLarge = errors.New("upstream response body is too large")

// Server HTTP 服务器
type Server struct {
	pendingTunnels      chan struct{}
	sessions            sessionRegistry
	auth                *Auth
	directOnce          sync.Once
	directDialer        *dialer.Managed
	poolManager         *pool.Manager
	fpManager           *fingerprint.Manager
	traffic             *traffic.Store
	httpServer          *http.Server
	allowPrivateTargets bool
	remoteChijieClient  *http.Client
	proxySettingsMu     sync.RWMutex
	proxySettings       ProxySettings
	socksAccounts       *SocksAccounts
	socksListen         string
	socksAdvertise      string
	socksTCPListener    net.Listener
	socksUDPListener    *net.UDPConn
	udpMu               sync.Mutex
	udpAssociations     map[string][]*socksAssociation
	relayUpstreamURL    string
	relayUpstreamToken  string
	relayDialer         *websocket.Dialer
	upstreamDialer      *websocket.Dialer
	tunRuntime          *tunRuntime
	affinityMu          sync.Mutex
	affinities          map[string]relayAffinity
	affinityOrder       list.List
}

// Config 服务器配置
type Config struct {
	Listen              string
	JWTSecret           string
	TLSCert             string
	TLSKey              string
	AllowPrivateTargets bool
	ProxySettings       *ProxySettings
}

var errInvalidRegion = errors.New("region must be an empty string or a two-letter region code")

// NewServer 创建服务器
func NewServer(cfg *Config, poolManager *pool.Manager, fpManager *fingerprint.Manager, trafficStores ...*traffic.Store) *Server {
	var trafficStore *traffic.Store
	if len(trafficStores) > 0 {
		trafficStore = trafficStores[0]
	}
	if trafficStore == nil {
		trafficStore = traffic.NewStore(1000)
	}

	s := &Server{
		pendingTunnels:      make(chan struct{}, 64),
		auth:                NewAuth(cfg.JWTSecret),
		poolManager:         poolManager,
		fpManager:           fpManager,
		traffic:             trafficStore,
		allowPrivateTargets: cfg.AllowPrivateTargets,
		proxySettings:       DefaultProxySettings(),
		affinities:          make(map[string]relayAffinity),
	}
	if cfg.ProxySettings != nil {
		settings, err := ValidateProxySettings(*cfg.ProxySettings)
		if err == nil {
			s.proxySettings = settings
		}
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/proxy", s.handleProxy)
	mux.HandleFunc("/tunnel", s.handleTunnel)
	mux.HandleFunc("/relay/v1", s.handleRelay)
	mux.HandleFunc("/health", s.handleHealth)

	s.httpServer = &http.Server{
		Addr:         cfg.Listen,
		Handler:      mux,
		ReadTimeout:  60 * time.Second,
		WriteTimeout: 120 * time.Second,
		IdleTimeout:  120 * time.Second,
	}

	return s
}

func (s *Server) SetAccessKeys(keys *auth.AccessKeys) { s.auth.SetAccessKeys(keys) }

// Start 启动服务器
func (s *Server) Start() error {
	log.Printf("server listening on %s", s.httpServer.Addr)
	return s.httpServer.ListenAndServe()
}

// StartTLS 启动 TLS 服务器
func (s *Server) StartTLS(certFile, keyFile string) error {
	log.Printf("server listening on %s (TLS)", s.httpServer.Addr)
	return s.httpServer.ListenAndServeTLS(certFile, keyFile)
}

// Shutdown 优雅关闭服务器，等待在途请求完成。
func (s *Server) Shutdown(ctx context.Context) error {
	done := s.sessions.closeAll()
	defer s.direct().Close()
	if s.tunRuntime != nil {
		_ = s.tunRuntime.Close()
	}
	if s.socksTCPListener != nil {
		_ = s.socksTCPListener.Close()
	}
	if s.socksUDPListener != nil {
		_ = s.socksUDPListener.Close()
	}
	var err error
	if s.httpServer != nil {
		err = s.httpServer.Shutdown(ctx)
	}
	select {
	case <-done:
		return err
	case <-ctx.Done():
		return errors.Join(err, ctx.Err())
	}
}

// handleHealth 健康检查
func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	w.WriteHeader(http.StatusOK)
	w.Write([]byte(`{"status":"ok"}`))
}

// handleProxy 处理代理请求
func (s *Server) handleProxy(w http.ResponseWriter, r *http.Request) {
	requestStarted := time.Now()

	// 仅允许同源请求或无 Origin 的服务端客户端。CORS 通配符放任浏览器跨站滥用网关。
	if origin := strings.TrimSpace(r.Header.Get("Origin")); origin != "" {
		if parsed, err := url.Parse(origin); err == nil && strings.EqualFold(parsed.Host, r.Host) {
			w.Header().Set("Access-Control-Allow-Origin", origin)
			w.Header().Set("Vary", "Origin")
			w.Header().Set("Access-Control-Allow-Methods", "POST, OPTIONS")
			w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization")
		}
	}

	if r.Method == "OPTIONS" {
		w.WriteHeader(http.StatusOK)
		return
	}

	if r.Method != "POST" {
		writeProxyError(w, http.StatusMethodNotAllowed, "method not allowed", "")
		return
	}

	if !s.auth.VerifyRequest(r) {
		writeProxyError(w, http.StatusForbidden, "unauthorized", "")
		return
	}
	proxyHop := chijieHopFromRequest(r)
	if proxyHop >= MaxChijieProxyHops {
		writeProxyError(w, http.StatusLoopDetected, "proxy loop detected", fmt.Sprintf("max %s is %d", chijieHopHeader, MaxChijieProxyHops))
		return
	}

	r.Body = http.MaxBytesReader(w, r.Body, MaxProxyBodyBytes)
	body, err := io.ReadAll(r.Body)
	if err != nil {
		writeProxyError(w, http.StatusBadRequest, "read body failed", err.Error())
		return
	}
	defer r.Body.Close()

	var req ProxyRequest
	if err := json.Unmarshal(body, &req); err != nil {
		writeProxyError(w, http.StatusBadRequest, "invalid json", err.Error())
		return
	}
	if req.URL == "" {
		writeProxyError(w, http.StatusBadRequest, "url required", "")
		return
	}
	if err := s.validateTargetURL(r.Context(), req.URL); err != nil {
		writeProxyError(w, http.StatusBadRequest, "invalid target", err.Error())
		return
	}
	if req.Method == "" {
		req.Method = "GET"
	}
	if req.Headers == nil {
		req.Headers = map[string]string{}
	}
	req.Hop = proxyHop

	requestBytes := int64(len(body))
	route, err := s.resolveEgress(req.Egress)
	if err != nil {
		util.Warnf("egress resolve error: %v", err)
		s.recordProxyTrace(&req, nil, http.StatusBadRequest, requestBytes, 0, time.Since(requestStarted), err.Error())
		writeProxyError(w, http.StatusBadRequest, "egress failed", err.Error())
		return
	}

	util.Debugf("[egress] %s %s → group:%s strategy:%s residential:%t premium:%t",
		req.Method, req.URL, route.Group, route.Strategy, route.Residential, route.Premium)

	proxyResp, finalRoute, err := s.doProxyWithRetry(r.Context(), &req, route)
	if finalRoute != nil && finalRoute.RetryOutcome != "" {
		w.Header().Set("X-Chijie-Retry-Outcome", finalRoute.RetryOutcome)
		w.Header().Set("X-Chijie-Attempts", strconv.Itoa(finalRoute.Attempts))
	}
	if err != nil {
		util.Warnf("[egress] proxy failed: %v", err)
		s.recordProxyTrace(&req, finalRoute, http.StatusBadGateway, requestBytes, 0, time.Since(requestStarted), err.Error())
		writeProxyError(w, http.StatusBadGateway, "proxy request failed", err.Error())
		return
	}

	s.recordProxyTrace(&req, finalRoute, proxyResp.StatusCode, requestBytes, int64(len(proxyResp.Body)), time.Since(requestStarted), "")
	writeProxyResponse(w, proxyResp)
}

func writeProxyResponse(w http.ResponseWriter, resp *proxyResponse) {
	if resp.Attempts > 0 {
		w.Header().Set("X-Chijie-Attempts", strconv.Itoa(resp.Attempts))
	}
	if resp.RetryAfter != "" {
		w.Header().Set("Retry-After", resp.RetryAfter)
	}
	if resp.ContentType != "" {
		w.Header().Set("Content-Type", resp.ContentType)
	}
	if resp.Location != "" {
		w.Header().Set("Location", resp.Location)
	}
	for _, cookie := range resp.SetCookies {
		if strings.TrimSpace(cookie) != "" {
			w.Header().Add("Set-Cookie", cookie)
		}
	}
	writeRedirectHeaders(w, resp)
	w.WriteHeader(resp.StatusCode)
	w.Write(resp.Body)
}

func writeProxyError(w http.ResponseWriter, status int, code string, detail string) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set(chijieErrorHeader, code)
	w.WriteHeader(status)
	payload := map[string]string{"error": code}
	if detail != "" {
		payload["detail"] = detail
	}
	_ = json.NewEncoder(w).Encode(payload)
}

func (s *Server) recordProxyTrace(req *ProxyRequest, route *egressRoute, status int, requestBytes, responseBytes int64, elapsed time.Duration, errText string) {
	if s.traffic == nil || req == nil {
		return
	}

	trace := traffic.Trace{
		Kind:          "proxy",
		Method:        req.Method,
		URL:           req.URL,
		Target:        traceTarget(req.URL),
		Status:        status,
		LatencyMS:     elapsed.Milliseconds(),
		RequestBytes:  requestBytes,
		ResponseBytes: responseBytes,
		Error:         errText,
	}
	if route != nil {
		trace.Attempts = route.Attempts
		trace.RetryOutcome = route.RetryOutcome
	}
	applyRouteTrace(&trace, route)
	s.traffic.Record(trace)
}

func applyRouteTrace(trace *traffic.Trace, route *egressRoute) {
	if trace == nil || route == nil {
		return
	}
	trace.EgressGroup = route.Group
	trace.Region = route.Region
	trace.Strategy = route.Strategy
	trace.Residential = route.Residential
	trace.Premium = route.Premium
	trace.TLSFingerprint = route.TLSFingerprint
	if route.Choice != nil {
		trace.EgressPool = route.Choice.PoolName
		trace.EgressNode = route.Choice.NodeName
		trace.EgressSource = route.Choice.Source
		trace.EgressTemplate = route.Choice.Template
	}
}

func traceTarget(rawURL string) string {
	parsed, err := url.Parse(rawURL)
	if err != nil || parsed.Host == "" {
		return rawURL
	}
	if parsed.Path == "" {
		return parsed.Host
	}
	return parsed.Host + parsed.Path
}

func chijieHopFromRequest(r *http.Request) int {
	if r == nil {
		return 0
	}
	hop, _ := strconv.Atoi(strings.TrimSpace(r.Header.Get(chijieHopHeader)))
	if hop < 0 {
		return 0
	}
	return hop
}
