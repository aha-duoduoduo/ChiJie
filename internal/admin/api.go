package admin

import (
	"context"
	"embed"
	"encoding/json"
	"io/fs"
	"log"
	"net"
	"net/http"
	"runtime"
	"strings"
	"sync"
	"time"

	"chijie/internal/auth"
	"chijie/internal/buildinfo"
	"chijie/internal/fingerprint"
	"chijie/internal/pool"
	proxyserver "chijie/internal/server"
	"chijie/internal/traffic"
	"chijie/internal/util"
)

// MaxAdminBodyBytes 是 Admin API 单次请求允许的最大 JSON body 大小。
const MaxAdminBodyBytes = 1 * 1024 * 1024

//go:embed all:dist
var webFS embed.FS

// Server Admin API 服务器
type Server struct {
	poolManager    *pool.Manager
	healthChecker  *pool.HealthChecker
	fpManager      *fingerprint.Manager
	traffic        *traffic.Store
	configDir      string
	startTime      time.Time
	httpServer     *http.Server
	fileMu         sync.Mutex    // 保护配置文件写入
	password       string        // 管理员密码
	jwtSecret      string        // JWT 签名密钥
	jwtExpire      time.Duration // JWT 过期时间
	loginLimiter   *loginLimiter // 登录速率限制
	runtimeMu      sync.RWMutex
	runtimeInfo    RuntimeInfo
	proxyRuntime   ProxySettingsRuntime
	socksAccounts  *proxyserver.SocksAccounts
	socksAdvertise string
	accessKeys     *auth.AccessKeys
	trustedProxies []*net.IPNet
}

// RuntimeInfo 是 Admin System 页面展示的运行时配置摘要。
type RuntimeInfo struct {
	ProxyListen    string `json:"proxy_listen"`
	ProxyTLS       bool   `json:"proxy_tls"`
	SocksListen    string `json:"socks_listen,omitempty"`
	SocksAdvertise string `json:"socks_advertise,omitempty"`
	AdminListen    string `json:"admin_listen"`
	AuthEnabled    bool   `json:"auth_enabled"`
	LogLevel       string `json:"log_level"`
	LogOutput      string `json:"log_output"`
	Version        string `json:"version"`
	Revision       string `json:"revision"`
	BuiltAt        string `json:"built_at"`
	GoVersion      string `json:"go_version"`
}

// ProxySettingsRuntime 是 Admin API 用来读取和更新 /proxy 运行时配置的最小接口。
type ProxySettingsRuntime interface {
	ProxySettings() proxyserver.ProxySettings
	UpdateProxySettings(proxyserver.ProxySettings)
}

// LoginLimitConfig 登录失败速率限制配置。
type LoginLimitConfig struct {
	MaxFailures int
	Window      time.Duration
	Lockout     time.Duration
}

// NewServer 创建 Admin API 服务器。
//
// loginLimit 为可选项；为 nil 时使用默认值 (5 次失败 / 60 秒窗口 / 5 分钟锁定)。
func NewServer(listen string, poolManager *pool.Manager, fpManager *fingerprint.Manager, configDir string, password string, jwtSecret string, jwtExpire string, loginLimit *LoginLimitConfig, trafficStores ...*traffic.Store) *Server {
	// 解析过期时间
	expire, err := time.ParseDuration(jwtExpire)
	if err != nil || expire == 0 {
		expire = 24 * time.Hour // 默认 24 小时
	}
	var trafficStore *traffic.Store
	if len(trafficStores) > 0 {
		trafficStore = trafficStores[0]
	}
	if trafficStore == nil {
		trafficStore = traffic.NewStore(1000)
	}

	maxFailures, window, lockout := 5, 60*time.Second, 5*time.Minute
	if loginLimit != nil {
		if loginLimit.MaxFailures > 0 {
			maxFailures = loginLimit.MaxFailures
		}
		if loginLimit.Window > 0 {
			window = loginLimit.Window
		}
		if loginLimit.Lockout > 0 {
			lockout = loginLimit.Lockout
		}
	}

	build := buildinfo.Current()
	s := &Server{
		poolManager:  poolManager,
		fpManager:    fpManager,
		traffic:      trafficStore,
		configDir:    configDir,
		startTime:    time.Now(),
		password:     password,
		jwtSecret:    jwtSecret,
		jwtExpire:    expire,
		loginLimiter: newLoginLimiter(maxFailures, window, lockout),
		runtimeInfo: RuntimeInfo{
			AdminListen: listen,
			AuthEnabled: password != "",
			LogLevel:    util.LogLevelName(),
			LogOutput:   "stdout",
			Version:     build.Version,
			Revision:    build.Revision,
			BuiltAt:     build.BuiltAt,
			GoVersion:   runtime.Version(),
		},
	}

	mux := http.NewServeMux()

	// 登录端点（不需要鉴权）
	mux.HandleFunc("/api/auth/login", s.handleLogin)

	// 其他端点（需要鉴权）
	mux.HandleFunc("/api/auth/proxy-token", s.authMiddleware(s.handleProxyToken))
	mux.HandleFunc("/api/auth/proxy-token/", s.authMiddleware(s.handleProxyTokenByID))
	mux.HandleFunc("/api/nodes", s.authMiddleware(s.handleNodes))
	mux.HandleFunc("/api/nodes/pool", s.authMiddleware(s.handleNodePoolConfig))
	mux.HandleFunc("/api/nodes/node", s.authMiddleware(s.handleNodeConfig))
	mux.HandleFunc("/api/nodes/subscription/node", s.authMiddleware(s.handleSubscriptionNodeConfig))
	mux.HandleFunc("/api/nodes/refresh", s.authMiddleware(s.handleNodesRefresh))
	mux.HandleFunc("/api/nodes/test", s.authMiddleware(s.handleNodeTest))
	mux.HandleFunc("/api/nodes/template/test", s.authMiddleware(s.handleTemplateTest))
	mux.HandleFunc("/api/nodes/enabled", s.authMiddleware(s.handleNodeEnabled))
	mux.HandleFunc("/api/fingerprints", s.authMiddleware(s.handleFingerprints))
	mux.HandleFunc("/api/fingerprints/", s.authMiddleware(s.handleFingerprintByName))
	mux.HandleFunc("/api/fingerprints/test", s.authMiddleware(s.handleFingerprintTest))
	mux.HandleFunc("/api/reload", s.authMiddleware(s.handleReload))
	mux.HandleFunc("/api/stats", s.authMiddleware(s.handleStats))
	mux.HandleFunc("/api/traffic", s.authMiddleware(s.handleTraffic))
	mux.HandleFunc("/api/traffic/grouping-rules", s.authMiddleware(s.handleTrafficGroupingRules))
	mux.HandleFunc("/api/traffic/success-folding-rules", s.authMiddleware(s.handleTrafficSuccessFoldingRules))
	mux.HandleFunc("/api/traffic/settings", s.authMiddleware(s.handleTrafficSettings))
	mux.HandleFunc("/api/system/logging", s.authMiddleware(s.handleLogging))
	mux.HandleFunc("/api/system/health-check", s.authMiddleware(s.handleHealthCheckSettings))
	mux.HandleFunc("/api/system/proxy/domain-retries", s.authMiddleware(s.handleDomainRetry))
	mux.HandleFunc("/api/system/proxy", s.authMiddleware(s.handleProxySettings))
	mux.HandleFunc("/api/socks/accounts", s.authMiddleware(s.handleSOCKSAccounts))
	mux.HandleFunc("/api/socks/accounts/", s.authMiddleware(s.handleSOCKSAccount))
	mux.HandleFunc("/api/config/export", s.authMiddleware(s.handleConfigExport))

	// 前端静态文件（SPA fallback）
	distFS, err := fs.Sub(webFS, "dist")
	if err != nil {
		log.Printf("admin: failed to load web assets: %v", err)
	} else {
		fileServer := http.FileServer(http.FS(distFS))
		mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
			// HTML must revalidate after deployment; content-hashed assets can be cached.
			w.Header().Set("Cache-Control", "no-store")
			// 尝试提供静态文件
			path := r.URL.Path
			if path == "/" {
				fileServer.ServeHTTP(w, r)
				return
			}
			// 检查文件是否存在
			f, err := distFS.Open(strings.TrimPrefix(path, "/"))
			if err == nil {
				f.Close()
				if strings.HasPrefix(path, "/app-") && strings.HasSuffix(path, ".js") {
					w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
				}
				fileServer.ServeHTTP(w, r)
				return
			}
			// 文件不存在，返回 index.html（SPA fallback）
			r.URL.Path = "/"
			fileServer.ServeHTTP(w, r)
		})
	}

	s.httpServer = &http.Server{
		Addr:         listen,
		Handler:      s.browserRequestGuard(withBodyLimit(mux, MaxAdminBodyBytes)),
		ReadTimeout:  10 * time.Second,
		WriteTimeout: 30 * time.Second,
	}

	return s
}

// SetRuntimeInfo 设置由主进程传入的运行时配置摘要。
func (s *Server) SetRuntimeInfo(info RuntimeInfo) {
	s.runtimeMu.Lock()
	defer s.runtimeMu.Unlock()
	if info.AdminListen == "" {
		info.AdminListen = s.httpServer.Addr
	}
	if info.LogLevel == "" {
		info.LogLevel = util.LogLevelName()
	}
	if info.LogOutput == "" {
		info.LogOutput = "stdout"
	}
	if info.Version == "" {
		build := buildinfo.Current()
		info.Version, info.Revision, info.BuiltAt = build.Version, build.Revision, build.BuiltAt
	}
	if info.GoVersion == "" {
		info.GoVersion = runtime.Version()
	}
	info.AuthEnabled = s.password != ""
	s.runtimeInfo = info
}

func (s *Server) SetHealthChecker(checker *pool.HealthChecker) {
	s.healthChecker = checker
}

func (s *Server) SetProxySettingsRuntime(runtime ProxySettingsRuntime) {
	s.proxyRuntime = runtime
}

func (s *Server) SetSocksAccounts(accounts *proxyserver.SocksAccounts, advertise string) {
	s.socksAccounts = accounts
	s.socksAdvertise = advertise
}

func (s *Server) SetAccessKeys(keys *auth.AccessKeys) { s.accessKeys = keys }

func (s *Server) runtimeSnapshot() RuntimeInfo {
	s.runtimeMu.RLock()
	defer s.runtimeMu.RUnlock()
	info := s.runtimeInfo
	info.LogLevel = util.LogLevelName()
	info.AuthEnabled = s.password != ""
	if info.GoVersion == "" {
		info.GoVersion = runtime.Version()
	}
	return info
}

// withBodyLimit 给所有 admin handler 套上 body 大小限制，避免恶意大 JSON 耗尽内存。
func withBodyLimit(next http.Handler, max int64) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Body != nil {
			r.Body = http.MaxBytesReader(w, r.Body, max)
		}
		next.ServeHTTP(w, r)
	})
}

// Start 启动 Admin API 服务器
func (s *Server) Start() error {
	log.Printf("admin api listening on %s", s.httpServer.Addr)
	return s.httpServer.ListenAndServe()
}

// Shutdown 优雅关闭 Admin API 服务器。
func (s *Server) Shutdown(ctx context.Context) error {
	if s.httpServer == nil {
		return nil
	}
	return s.httpServer.Shutdown(ctx)
}

// writeJSON 写入 JSON 响应
func writeJSON(w http.ResponseWriter, status int, data any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(data)
}
