package pool

import (
	"context"
	"fmt"
	"log"
	"math"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"chijie/internal/dialer"

	"gopkg.in/yaml.v3"
)

// PoolConfig 节点池配置
type PoolConfig struct {
	Source            string              `yaml:"source" json:"source"` // direct, static, template, subscription
	Enabled           *bool               `yaml:"enabled,omitempty" json:"enabled,omitempty"`
	Residential       bool                `yaml:"residential,omitempty" json:"residential,omitempty"`
	Premium           bool                `yaml:"premium,omitempty" json:"premium,omitempty"`
	URL               string              `yaml:"url" json:"url"`                                                   // 订阅链接（subscription）
	AllowPrivateHost  bool                `yaml:"allow_private_host,omitempty" json:"allow_private_host,omitempty"` // 允许订阅地址使用私网、回环或保留地址
	UpdateInterval    string              `yaml:"update_interval" json:"update_interval"`                           // 订阅更新间隔
	Filter            *FilterConfig       `yaml:"filter" json:"filter"`                                             // 节点过滤
	HealthCheck       *HealthCheckConfig  `yaml:"health_check" json:"health_check"`                                 // 健康检查配置
	TryOffline        bool                `yaml:"try_offline,omitempty" json:"try_offline,omitempty"`               // 唯一地区节点离线时仍尝试
	Nodes             []dialer.Node       `yaml:"nodes" json:"nodes"`                                               // 静态节点列表
	DisabledNodes     []string            `yaml:"disabled_nodes" json:"disabled_nodes"`                             // 禁用节点名
	TemplateType      string              `yaml:"template_type,omitempty" json:"template_type,omitempty"`           // proxy, chijie
	Type              string              `yaml:"type" json:"type"`                                                 // 代理模板类型（template）
	Server            string              `yaml:"server" json:"server"`                                             // 模板服务器
	Port              int                 `yaml:"port" json:"port"`                                                 // 模板端口
	Endpoint          string              `yaml:"endpoint,omitempty" json:"endpoint,omitempty"`                     // 远端 Chijie HTTPS 地址
	BearerToken       string              `yaml:"bearer_token,omitempty" json:"bearer_token,omitempty"`
	UsernameTemplate  string              `yaml:"username_template" json:"username_template"` // 用户名模板
	Password          string              `yaml:"password" json:"password"`                   // 模板密码
	Priority          int                 `yaml:"priority,omitempty" json:"priority,omitempty"`
	Coverage          string              `yaml:"coverage,omitempty" json:"coverage,omitempty"` // normal, residential, both
	Tags              []string            `yaml:"tags,omitempty" json:"tags,omitempty"`         // 节点池标签
	RejectRegex       []string            `yaml:"reject_regex,omitempty" json:"reject_regex,omitempty"`
	NodeRegions       map[string]string   `yaml:"node_regions,omitempty" json:"node_regions,omitempty"`               // 节点名 → 地区代码
	NodeAliases       map[string]string   `yaml:"node_aliases,omitempty" json:"node_aliases,omitempty"`               // 别名 → 节点名
	NodeTags          map[string][]string `yaml:"node_tags,omitempty" json:"node_tags,omitempty"`                     // 节点名 → 标签列表
	NodeServerRegions map[string]string   `yaml:"node_server_regions,omitempty" json:"node_server_regions,omitempty"` // server:port → 地区代码
	NodeServerAliases map[string]string   `yaml:"node_server_aliases,omitempty" json:"node_server_aliases,omitempty"` // server:port → 别名
	NodeServerTags    map[string][]string `yaml:"node_server_tags,omitempty" json:"node_server_tags,omitempty"`       // server:port → 标签列表
	RegionGroupNames  map[string]string   `yaml:"region_group_names,omitempty" json:"region_group_names,omitempty"`   // 地区代码 → 分组展示名
}

type FilterConfig struct {
	Region []string `yaml:"region" json:"region"`
}

type HealthCheckConfig struct {
	Interval string `yaml:"interval" json:"interval"`
	URL      string `yaml:"url" json:"url"`
	Timeout  string `yaml:"timeout" json:"timeout"`
	MaxFail  int    `yaml:"max_fail" json:"max_fail"`
}

// NodeEntry 节点池中的节点条目
type NodeEntry struct {
	Node        *dialer.Node
	Dialer      dialer.Dialer
	Enabled     bool
	Alive       bool
	Latency     time.Duration // 最近一次健康检查延迟
	FailCount   int           // 连续失败次数
	Region      string
	Alias       string
	Residential bool
	Premium     bool
	Tags        []string
}

// Pool 节点池
type Pool struct {
	retired           bool
	templateDialers   map[string]dialer.Dialer
	Name              string
	Config            *PoolConfig
	Entries           []*NodeEntry
	Error             string
	LastRefreshAt     time.Time
	LastRefreshFailed bool
	mu                sync.RWMutex
}

// Manager 节点池管理器
type Manager struct {
	reloadMu  sync.Mutex
	pools     map[string]*Pool
	mu        sync.RWMutex
	rrMu      sync.Mutex
	rrByGroup map[string]int

	subscriptionCache *subscriptionCache

	// 订阅自动更新协程的生命周期管理
	updaterMu     sync.Mutex
	updaterCancel context.CancelFunc
	updaterWG     sync.WaitGroup
}

// NodesFileConfig nodes.yaml 顶层结构
type NodesFileConfig struct {
	NodePools map[string]*PoolConfig `yaml:"node_pools"`
}

// EgressChoice 是参数驱动出口选择的结果。
type EgressChoice struct {
	Dialer       dialer.Dialer
	PoolName     string        `json:"pool"`
	NodeName     string        `json:"node"`
	Source       string        `json:"source"`
	TemplateType string        `json:"template_type,omitempty"`
	Region       string        `json:"region"`
	Group        string        `json:"group"`
	Residential  bool          `json:"residential"`
	Premium      bool          `json:"premium,omitempty"`
	Template     bool          `json:"template"`
	Priority     int           `json:"priority,omitempty"`
	Endpoint     string        `json:"endpoint,omitempty"`
	BearerToken  string        `json:"-"`
	Latency      time.Duration `json:"-"`
}

// EgressSelector describes the node class requested by a proxy call. Premium is
// a preference: premium nodes are tried first, then ordinary fallback remains available.
type EgressSelector struct {
	Residential bool
	Premium     bool
}

// NewManager 创建节点池管理器
func NewManager() *Manager {
	return &Manager{
		pools:     make(map[string]*Pool),
		rrByGroup: make(map[string]int),
	}
}

// SetSubscriptionCachePath 配置订阅成功结果缓存。缓存只保存 URL 哈希和解析后的节点，
// 用于进程重启后首次拉取失败时恢复上一次可用节点。
func (m *Manager) SetSubscriptionCachePath(path string) {
	m.subscriptionCache = newSubscriptionCache(path)
}

// ParseDurationWithDays 兼容 Go duration，并额外支持 d 表示天。
func ParseDurationWithDays(raw string) (time.Duration, error) {
	value := strings.TrimSpace(strings.ToLower(raw))
	var duration time.Duration
	var err error
	if strings.HasSuffix(value, "d") {
		var days float64
		days, err = strconv.ParseFloat(strings.TrimSuffix(value, "d"), 64)
		nanos := days * float64(24*time.Hour)
		if err != nil || math.IsNaN(nanos) || math.IsInf(nanos, 0) || nanos <= 0 || nanos >= float64(math.MaxInt64) {
			return 0, fmt.Errorf("invalid duration: %s", raw)
		}
		duration = time.Duration(nanos)
	} else {
		duration, err = time.ParseDuration(value)
	}
	if err != nil || duration <= 0 {
		return 0, fmt.Errorf("duration must be positive: %s", raw)
	}
	return duration, nil
}

// LoadFromFile 从 YAML 文件加载节点池配置
func (m *Manager) LoadFromFile(path string) error {
	m.reloadMu.Lock()
	defer m.reloadMu.Unlock()
	data, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("read nodes config: %w", err)
	}

	var config NodesFileConfig
	if err := yaml.Unmarshal(data, &config); err != nil {
		return fmt.Errorf("parse nodes config: %w", err)
	}

	m.mu.RLock()
	oldPools := make(map[string]*Pool, len(m.pools))
	for name, oldPool := range m.pools {
		oldPools[name] = oldPool
	}
	m.mu.RUnlock()

	newPools := make(map[string]*Pool, len(config.NodePools))
	applied := false
	defer func() {
		if !applied {
			closePoolsExcept(newPools, oldPools)
		}
	}()
	for name, poolCfg := range config.NodePools {
		pool, err := m.buildPool(name, poolCfg)
		if err != nil {
			return fmt.Errorf("build pool %s: %w", name, err)
		}
		if preserved := preserveSubscriptionEntriesOnError(name, poolCfg, pool, previousSubscriptionPool(name, poolCfg, oldPools)); preserved != nil {
			pool.closeExcept(nil)
			pool = preserved
		}
		newPools[name] = pool
	}

	m.mu.Lock()
	m.pools = newPools
	m.mu.Unlock()
	applied = true
	closePoolsExcept(oldPools, newPools)

	return nil
}

// buildPool 根据配置构建节点池
func (m *Manager) buildPool(name string, cfg *PoolConfig) (result *Pool, err error) {
	if cfg == nil {
		return nil, fmt.Errorf("pool config is required")
	}
	pool := &Pool{
		Name:   name,
		Config: cfg,
	}
	defer func() {
		if err != nil {
			pool.closeExcept(nil)
		}
	}()
	if cfg.Source == "subscription" && !poolEnabled(cfg) {
		log.Printf("pool %s: disabled; subscription fetch skipped", name)
		return pool, nil
	}

	switch cfg.Source {
	case "direct":
		node := &dialer.Node{Name: "direct", Type: "direct"}
		d := dialer.Manage(dialer.NewDirectDialer())
		pool.Entries = []*NodeEntry{newNodeEntry(node, d, cfg)}

	case "static":
		for i := range cfg.Nodes {
			node := &cfg.Nodes[i]
			d, err := dialer.NewDialer(node)
			if err != nil {
				return nil, fmt.Errorf("create dialer for %s: %w", node.Name, err)
			}
			pool.Entries = append(pool.Entries, newNodeEntry(node, d, cfg))
		}

	case "template":
		if NormalizeTemplateType(cfg.TemplateType) == "chijie" {
			if _, err := ChijieProxyURL(cfg.Endpoint, cfg.Port); err != nil {
				return nil, fmt.Errorf("invalid chijie template endpoint: %w", err)
			}
			if strings.TrimSpace(cfg.BearerToken) == "" {
				return nil, fmt.Errorf("chijie template bearer_token is required")
			}
		}
		// 模板池不预创建节点，按需动态生成。这里只保存配置。

	case "subscription":
		parser := NewSubscriptionParserWithOptions(SubscriptionParserOptions{
			AllowPrivateHost: cfg.AllowPrivateHost,
		})
		nodes, err := parser.Fetch(cfg.URL)
		if err != nil {
			message := fmt.Sprintf("fetch subscription: %v", err)
			cachedNodes, cachedAt, cacheErr := m.loadCachedSubscription(cfg.URL)
			if cacheErr == nil {
				entries, _, prepareErr := prepareSubscriptionEntries(cachedNodes, cfg)
				if prepareErr == nil && len(entries) > 0 {
					pool.restoreSubscriptionEntriesAfterFailure(entries, message)
					log.Printf("pool %s: restored %d cached subscription nodes from %s after refresh error: %s", name, len(entries), cachedAt.Format(time.RFC3339), message)
					return pool, nil
				}
				if prepareErr != nil {
					message += fmt.Sprintf("; cached subscription rejected: %v", prepareErr)
				}
			}
			pool.recordSubscriptionRefreshFailure(message)
			log.Printf("pool %s: %s", name, message)
			return pool, nil
		}
		m.cacheSubscription(cfg.URL, nodes, name)

		entries, warning, prepareErr := prepareSubscriptionEntries(nodes, cfg)
		if prepareErr != nil {
			pool.recordSubscriptionRefreshFailure(prepareErr.Error())
			log.Printf("pool %s: %s", name, prepareErr)
			return pool, nil
		}
		pool.recordSubscriptionRefreshSuccess(entries, warning)
		log.Printf("pool %s: loaded %d nodes from subscription", name, len(pool.Entries))

	default:
		return nil, fmt.Errorf("unknown pool source: %s", cfg.Source)
	}

	return pool, nil
}

func previousSubscriptionPool(name string, cfg *PoolConfig, oldPools map[string]*Pool) *Pool {
	if cfg == nil || cfg.Source != "subscription" {
		return nil
	}
	if previous := oldPools[name]; previous != nil && previous.Config != nil && previous.Config.Source == "subscription" {
		return previous
	}
	for _, previous := range oldPools {
		if previous == nil || previous.Config == nil || previous.Config.Source != "subscription" {
			continue
		}
		if strings.TrimSpace(previous.Config.URL) == strings.TrimSpace(cfg.URL) {
			return previous
		}
	}
	return nil
}

func preserveSubscriptionEntriesOnError(name string, cfg *PoolConfig, current *Pool, previous *Pool) *Pool {
	if cfg == nil || cfg.Source != "subscription" || current == nil || previous == nil {
		return nil
	}
	current.mu.RLock()
	currentError := current.Error
	lastRefreshAt := current.LastRefreshAt
	lastRefreshFailed := current.LastRefreshFailed
	current.mu.RUnlock()
	if !lastRefreshFailed {
		return nil
	}

	previous.mu.RLock()
	defer previous.mu.RUnlock()
	if len(previous.Entries) == 0 {
		return nil
	}
	entries := make([]*NodeEntry, 0, len(previous.Entries))
	for _, entry := range previous.Entries {
		if entry == nil || entry.Node == nil {
			continue
		}
		node := *entry.Node
		if node.Enabled != nil {
			enabled := *node.Enabled
			node.Enabled = &enabled
		}
		next := newNodeEntry(&node, entry.Dialer, cfg)
		next.Alive = entry.Alive
		next.Latency = entry.Latency
		next.FailCount = entry.FailCount
		entries = append(entries, next)
	}
	if len(entries) == 0 {
		return nil
	}
	log.Printf("pool %s: keeping %d previous subscription nodes after refresh error: %s", name, len(entries), currentError)
	return &Pool{
		Name:              name,
		Config:            cfg,
		Entries:           entries,
		Error:             currentError,
		LastRefreshAt:     lastRefreshAt,
		LastRefreshFailed: lastRefreshFailed,
	}
}

// GetPool 获取指定池
func (m *Manager) GetPool(name string) *Pool {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.pools[name]
}

// ListPools 列出所有池名称
func (m *Manager) ListPools() []string {
	m.mu.RLock()
	defer m.mu.RUnlock()

	names := make([]string, 0, len(m.pools))
	for name := range m.pools {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// ListPoolEntries 返回所有池的节点条目（供健康检查使用）
func (m *Manager) ListPoolEntries() map[string][]*NodeEntry {
	m.mu.RLock()
	defer m.mu.RUnlock()

	result := make(map[string][]*NodeEntry)
	for name, pool := range m.pools {
		pool.mu.RLock()
		entries := make([]*NodeEntry, len(pool.Entries))
		copy(entries, pool.Entries)
		pool.mu.RUnlock()
		result[name] = entries
	}
	return result
}
