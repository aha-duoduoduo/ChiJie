package pool

import (
	"fmt"
	"regexp"
	"strings"

	"chijie/internal/dialer"
	"chijie/internal/util"
)

// SetNodeEnabled 更新运行时节点启停状态。
func (m *Manager) SetNodeEnabled(poolName, nodeName string, enabled bool) error {
	m.mu.RLock()
	pool, ok := m.pools[poolName]
	m.mu.RUnlock()
	if !ok {
		return fmt.Errorf("pool not found: %s", poolName)
	}

	pool.mu.Lock()
	defer pool.mu.Unlock()
	for _, entry := range pool.Entries {
		if entry.Node.Name == nodeName {
			entry.Enabled = enabled
			entry.Node.Enabled = &enabled
			UpdateConfigNodeEnabled(pool.Config, nodeName, enabled)
			return nil
		}
	}
	return fmt.Errorf("node not found in pool %s: %s", poolName, nodeName)
}

// MarkNodeUnavailable 将一次 /proxy 出口执行失败对应的运行时节点立即标记为不可用。
// 后台健康检查或手动测试后续成功时会再次把 Alive 恢复为 true。
func (m *Manager) MarkNodeUnavailable(poolName, nodeName string) bool {
	if m == nil || strings.TrimSpace(poolName) == "" || strings.TrimSpace(nodeName) == "" {
		return false
	}
	m.mu.RLock()
	pool, ok := m.pools[poolName]
	m.mu.RUnlock()
	if !ok || pool == nil {
		return false
	}

	pool.mu.Lock()
	defer pool.mu.Unlock()
	for _, entry := range pool.Entries {
		if entry == nil || entry.Node == nil || entry.Node.Name != nodeName {
			continue
		}
		if strings.EqualFold(entry.Node.Type, "direct") {
			return false
		}
		entry.Alive = false
		entry.FailCount++
		return true
	}
	return false
}

func UpdateConfigNodeEnabled(cfg *PoolConfig, nodeName string, enabled bool) {
	for i := range cfg.Nodes {
		if cfg.Nodes[i].Name == nodeName {
			cfg.Nodes[i].Enabled = &enabled
			return
		}
	}
	if enabled {
		cfg.DisabledNodes = util.RemoveString(cfg.DisabledNodes, nodeName)
	} else if !util.ContainsString(cfg.DisabledNodes, nodeName) {
		cfg.DisabledNodes = append(cfg.DisabledNodes, nodeName)
	}
}

func isNodeEnabled(node *dialer.Node, cfg *PoolConfig) bool {
	if node.Enabled != nil {
		return *node.Enabled
	}
	return !util.ContainsString(cfg.DisabledNodes, node.Name)
}

func poolEnabled(cfg *PoolConfig) bool {
	return cfg == nil || cfg.Enabled == nil || *cfg.Enabled
}

func newNodeEntry(node *dialer.Node, d dialer.Dialer, cfg *PoolConfig) *NodeEntry {
	tags := tagsForNode(node, cfg)
	return &NodeEntry{
		Node:        node,
		Dialer:      d,
		Enabled:     isNodeEnabled(node, cfg),
		Alive:       true,
		Region:      regionForNode(node, cfg),
		Alias:       aliasForNode(node, cfg),
		Residential: residentialForNode(node, cfg, tags),
		Premium:     premiumForNode(node, cfg, tags),
		Tags:        tags,
	}
}

// NormalizeRegionCode 对外提供二字母地区码标准化。
func NormalizeRegionCode(value string) string {
	region := normalizeRegionCode(value)
	if len(region) != 2 {
		return ""
	}
	return region
}

// NormalizeStrategy 标准化节点选择策略。
func NormalizeStrategy(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "least-latency", "least_latency", "latency":
		return "least-latency"
	case "round-robin", "round_robin", "rr":
		return "round-robin"
	case "random", "":
		return "random"
	default:
		return "random"
	}
}

// EgressGroup 返回前端和日志使用的地区组代码。
func EgressGroup(region string, residential bool) string {
	return EgressGroupFor(region, EgressSelector{Residential: residential})
}

// EgressGroupFor 返回地区组代码。Premium 不改变地区组，只作为节点偏好。
func EgressGroupFor(region string, selector EgressSelector) string {
	region = NormalizeRegionCode(region)
	if region == "" {
		return "DIRECT"
	}
	if selector.Residential {
		return region + "-RES"
	}
	return region
}

// AnyEgressGroupFor 返回地区无关出口组代码。Premium 不改变组名。
func AnyEgressGroupFor(selector EgressSelector) string {
	if selector.Residential {
		return "ANY-RES"
	}
	return "ANY"
}

func regionForNode(node *dialer.Node, cfg *PoolConfig) string {
	if region := NormalizeRegionCode(node.Region); region != "" {
		return region
	}
	if cfg != nil && cfg.NodeServerRegions != nil {
		if region := normalizeRegionCode(cfg.NodeServerRegions[nodeServerKey(node)]); region != "" {
			return region
		}
	}
	if cfg != nil && cfg.NodeRegions != nil {
		if region := normalizeRegionCode(cfg.NodeRegions[node.Name]); region != "" {
			return region
		}
	}
	return detectRegionFromNodeName(node.Name)
}

func applyRejectRegexes(nodes []dialer.Node, patterns []string) ([]dialer.Node, error) {
	if len(patterns) == 0 {
		return nodes, nil
	}

	regexes := make([]*regexp.Regexp, 0, len(patterns))
	for _, pattern := range patterns {
		pattern = strings.TrimSpace(pattern)
		if pattern == "" {
			continue
		}
		compiled, err := regexp.Compile(pattern)
		if err != nil {
			return nil, fmt.Errorf("invalid reject regex %q: %w", pattern, err)
		}
		regexes = append(regexes, compiled)
	}
	if len(regexes) == 0 {
		return nodes, nil
	}

	filtered := make([]dialer.Node, 0, len(nodes))
	for _, node := range nodes {
		rejected := false
		for _, regex := range regexes {
			if regex.MatchString(node.Name) {
				rejected = true
				break
			}
		}
		if !rejected {
			filtered = append(filtered, node)
		}
	}
	return filtered, nil
}

// filterByRegion 按地区代码过滤节点（从节点名称中匹配）
func filterByRegion(nodes []dialer.Node, regions []string) []dialer.Node {
	if len(regions) == 0 {
		return nodes
	}

	regionSet := make(map[string]bool)
	for _, c := range regions {
		if region := normalizeRegionCode(c); region != "" {
			regionSet[region] = true
		}
	}

	var filtered []dialer.Node
	for _, node := range nodes {
		if regionSet[detectRegionFromNodeName(node.Name)] {
			filtered = append(filtered, node)
		}
	}

	return filtered
}

func detectRegionFromNodeName(name string) string {
	if region := flagRegionCode(name); region != "" {
		if region == "CN" && containsRegionCodeToken(strings.ToUpper(name), "TW") {
			return "TW"
		}
		return region
	}

	lowerName := strings.ToLower(name)
	for alias, region := range regionAliasToCode {
		if strings.Contains(lowerName, alias) {
			return region
		}
	}

	upperName := strings.ToUpper(name)
	for code := range regionCodeToName {
		if code != "UN" && containsRegionCodeToken(upperName, code) {
			return code
		}
	}
	return "UN"
}

func containsRegionCodeToken(upperName string, region string) bool {
	code := strings.ToUpper(strings.TrimSpace(region))
	if code == "" {
		return false
	}
	for idx := strings.Index(upperName, code); idx >= 0; {
		beforeOK := idx == 0 || !isASCIIUpperLetter(upperName[idx-1])
		afterIdx := idx + len(code)
		for afterIdx < len(upperName) && upperName[afterIdx] >= '0' && upperName[afterIdx] <= '9' {
			afterIdx++
		}
		afterOK := afterIdx == len(upperName) || !isASCIIUpperLetter(upperName[afterIdx])
		if beforeOK && afterOK {
			return true
		}
		nextStart := idx + len(code)
		if nextStart >= len(upperName) {
			break
		}
		next := strings.Index(upperName[nextStart:], code)
		if next < 0 {
			break
		}
		idx = nextStart + next
	}
	return false
}

func isASCIIUpperLetter(b byte) bool {
	return b >= 'A' && b <= 'Z'
}

func flagRegionCode(name string) string {
	runes := []rune(name)
	for i := 0; i < len(runes)-1; i++ {
		first := regionalIndicatorLetter(runes[i])
		second := regionalIndicatorLetter(runes[i+1])
		if first != 0 && second != 0 {
			return string([]rune{first, second})
		}
	}
	return ""
}

func regionalIndicatorLetter(r rune) rune {
	if r < 0x1F1E6 || r > 0x1F1FF {
		return 0
	}
	return 'A' + (r - 0x1F1E6)
}

func normalizeRegionCode(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	if region := regionAliasToCode[strings.ToLower(value)]; region != "" {
		return region
	}
	upper := strings.ToUpper(value)
	if _, ok := regionCodeToName[upper]; ok {
		return upper
	}
	return upper
}
