package pool

import (
	"context"
	"fmt"
	"log"
	"sort"
	"strings"
	"time"

	"chijie/internal/dialer"
	"chijie/internal/util"
)

// RefreshSubscription 刷新指定订阅池的节点
func (m *Manager) RefreshSubscription(poolName string) error {
	return m.refreshSubscription(context.Background(), poolName)
}

func (m *Manager) refreshSubscription(ctx context.Context, poolName string) error {
	m.mu.RLock()
	pool, ok := m.pools[poolName]
	m.mu.RUnlock()

	if !ok {
		return fmt.Errorf("pool not found: %s", poolName)
	}
	pool.mu.RLock()
	cfg := clonePoolConfig(pool.Config)
	pool.mu.RUnlock()
	if cfg.Source != "subscription" {
		return fmt.Errorf("pool %s is not a subscription pool", poolName)
	}
	if !poolEnabled(cfg) {
		return fmt.Errorf("pool %s is disabled", poolName)
	}

	parser := NewSubscriptionParserWithOptions(SubscriptionParserOptions{
		AllowPrivateHost: cfg.AllowPrivateHost,
	})
	nodes, err := parser.FetchContext(ctx, cfg.URL)
	if err != nil {
		pool.recordSubscriptionRefreshFailure(fmt.Sprintf("fetch subscription: %v", err))
		return fmt.Errorf("fetch subscription: %w", err)
	}
	m.cacheSubscription(cfg.URL, nodes, poolName)

	entries, warning, err := prepareSubscriptionEntries(nodes, cfg)
	if err != nil {
		pool.recordSubscriptionRefreshFailure(err.Error())
		return err
	}
	// Preserve/retire and refresh must not transfer the same outbound at once.
	m.reloadMu.Lock()
	defer m.reloadMu.Unlock()
	pool.recordSubscriptionRefreshSuccess(entries, warning)

	log.Printf("pool %s: refreshed %d nodes", poolName, len(entries))
	return nil
}

func prepareSubscriptionEntries(nodes []dialer.Node, cfg *PoolConfig) ([]*NodeEntry, string, error) {
	if cfg.Filter != nil && len(cfg.Filter.Region) > 0 {
		nodes = filterByRegion(nodes, cfg.Filter.Region)
	}
	filtered, err := applyRejectRegexes(nodes, cfg.RejectRegex)
	if err != nil {
		return nil, "", err
	}
	entries, warning := buildSubscriptionEntries(filtered, cfg)
	return entries, warning, nil
}

func (m *Manager) loadCachedSubscription(rawURL string) ([]dialer.Node, time.Time, error) {
	if m.subscriptionCache == nil {
		return nil, time.Time{}, fmt.Errorf("subscription cache is disabled")
	}
	return m.subscriptionCache.load(rawURL)
}

func (m *Manager) cacheSubscription(rawURL string, nodes []dialer.Node, poolName string) {
	if m.subscriptionCache == nil {
		return
	}
	if err := m.subscriptionCache.save(rawURL, nodes); err != nil {
		log.Printf("pool %s: save subscription cache failed: %v", poolName, err)
	}
}

func buildSubscriptionEntries(nodes []dialer.Node, cfg *PoolConfig) ([]*NodeEntry, string) {
	entries := make([]*NodeEntry, 0, len(nodes))
	skipped := make(map[string]int)
	skippedTotal := 0

	for i := range nodes {
		node := nodes[i]
		d, err := dialer.NewDialer(&node)
		if err != nil {
			reason := subscriptionSkipReason(&node, err)
			skipped[reason]++
			skippedTotal++
			log.Printf("skip node %s: %v", node.Name, err)
			continue
		}
		entries = append(entries, newNodeEntry(&node, d, cfg))
	}

	if skippedTotal == 0 {
		return entries, ""
	}
	if len(entries) == 0 && len(nodes) > 0 {
		return entries, fmt.Sprintf("subscription parsed %d nodes but loaded 0 supported nodes: %s", len(nodes), formatSkipReasons(skipped))
	}
	return entries, fmt.Sprintf("subscription skipped %d nodes: %s", skippedTotal, formatSkipReasons(skipped))
}

func subscriptionSkipReason(node *dialer.Node, err error) string {
	if node != nil {
		network := strings.ToLower(util.FirstNonEmpty(node.Extra["network"], node.Extra["transport"], node.Extra["transport_type"]))
		switch network {
		case "xhttp", "splithttp", "split-http":
			if strings.EqualFold(node.Type, "vless") {
				return err.Error()
			}
			return fmt.Sprintf("unsupported v2ray transport %q", network)
		}
	}
	return err.Error()
}

func formatSkipReasons(reasons map[string]int) string {
	if len(reasons) == 0 {
		return ""
	}
	keys := make([]string, 0, len(reasons))
	for reason := range reasons {
		keys = append(keys, reason)
	}
	sort.Strings(keys)

	parts := make([]string, 0, len(keys))
	for _, reason := range keys {
		if reasons[reason] > 1 {
			parts = append(parts, fmt.Sprintf("%s (%d)", reason, reasons[reason]))
			continue
		}
		parts = append(parts, reason)
	}
	return strings.Join(parts, "; ")
}

// StartSubscriptionUpdater 启动后台订阅自动更新。
// 重复调用会先停掉旧协程再启动新协程，避免 reload 后协程泄漏。
func (m *Manager) StartSubscriptionUpdater() {
	m.updaterMu.Lock()
	defer m.updaterMu.Unlock()
	m.stopSubscriptionUpdaterLocked()
	ctx, cancel := context.WithCancel(context.Background())
	m.updaterCancel = cancel

	m.mu.RLock()
	defer m.mu.RUnlock()

	for name, pool := range m.pools {
		pool.mu.RLock()
		cfg := clonePoolConfig(pool.Config)
		pool.mu.RUnlock()
		if cfg.Source != "subscription" || !poolEnabled(cfg) || cfg.UpdateInterval == "" {
			continue
		}

		interval, err := ParseDurationWithDays(cfg.UpdateInterval)
		if err != nil {
			log.Printf("invalid update_interval for %s: %v", name, err)
			continue
		}

		poolName := name
		m.updaterWG.Add(1)
		go func() {
			defer m.updaterWG.Done()
			ticker := time.NewTicker(interval)
			defer ticker.Stop()
			for {
				select {
				case <-ctx.Done():
					return
				case <-ticker.C:
					if err := m.refreshSubscription(ctx, poolName); err != nil {
						log.Printf("refresh %s failed: %v", poolName, err)
					}
				}
			}
		}()

		log.Printf("subscription updater started for %s (interval: %s)", name, interval)
	}
}

// StopSubscriptionUpdater 取消所有正在运行的订阅自动更新协程并等待退出。
func (m *Manager) StopSubscriptionUpdater() {
	m.updaterMu.Lock()
	defer m.updaterMu.Unlock()
	m.stopSubscriptionUpdaterLocked()
}

func (m *Manager) stopSubscriptionUpdaterLocked() {
	cancel := m.updaterCancel
	m.updaterCancel = nil
	if cancel != nil {
		cancel()
	}
	m.updaterWG.Wait()
}
