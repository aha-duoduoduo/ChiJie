package pool

import (
	"fmt"
	"log"
	"net"
	"net/url"
	"sort"
	"strings"

	"chijie/internal/dialer"
)

func (m *Manager) templateChoices(region string, selector EgressSelector) []*EgressChoice {
	m.mu.RLock()
	defer m.mu.RUnlock()

	choices := make([]*EgressChoice, 0)
	for poolName, pool := range m.pools {
		if pool.Config.Source != "template" || !poolEnabled(pool.Config) || !TemplateCoversSelector(pool.Config, selector) {
			continue
		}

		templateType := NormalizeTemplateType(pool.Config.TemplateType)
		endpoint := ""
		var d dialer.Dialer
		if templateType == "chijie" {
			var err error
			endpoint, err = ChijieProxyURL(pool.Config.Endpoint, pool.Config.Port)
			if err != nil {
				log.Printf("template %s unavailable for %s: %v", poolName, region, err)
				continue
			}
		} else {
			var err error
			d, err = m.templateDialer(pool, region)
			if err != nil {
				log.Printf("template %s unavailable for %s: %v", poolName, region, err)
				continue
			}
		}
		choices = append(choices, &EgressChoice{
			Dialer:       d,
			PoolName:     poolName,
			NodeName:     fmt.Sprintf("%s-%s", poolName, strings.ToLower(region)),
			Source:       "template",
			TemplateType: templateType,
			Region:       region,
			Group:        EgressGroupFor(region, selector),
			Residential:  selector.Residential,
			Premium:      pool.Config.Premium,
			Template:     true,
			Priority:     pool.Config.Priority,
			Endpoint:     endpoint,
			BearerToken:  pool.Config.BearerToken,
		})
	}
	sort.Slice(choices, func(i, j int) bool {
		if selector.Premium && choices[i].Premium != choices[j].Premium {
			return choices[i].Premium
		}
		if choices[i].Priority != choices[j].Priority {
			return choices[i].Priority > choices[j].Priority
		}
		return choices[i].PoolName < choices[j].PoolName
	})
	return choices
}

func (m *Manager) buildTemplateDialer(poolName string, cfg *PoolConfig, region string) (dialer.Dialer, error) {
	node := &dialer.Node{
		Name:        fmt.Sprintf("%s-%s", poolName, strings.ToLower(region)),
		Type:        cfg.Type,
		Server:      cfg.Server,
		Port:        cfg.Port,
		Username:    templateUsername(cfg.UsernameTemplate, region),
		Password:    cfg.Password,
		Residential: cfg.Residential,
		Premium:     cfg.Premium,
		Tags:        normalizeTags(cfg.Tags),
	}
	return dialer.NewDialer(node)
}

func templateUsername(usernameTemplate string, region string) string {
	username := strings.ReplaceAll(usernameTemplate, "{region}", strings.ToLower(region))
	username = strings.ReplaceAll(username, "{REGION}", strings.ToUpper(region))
	return username
}

// NormalizeTemplateType 标准化模板提供方类型。空值保持旧配置语义，按普通代理模板处理。
func NormalizeTemplateType(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "", "proxy", "generic", "generic-proxy":
		return "proxy"
	case "chijie":
		return "chijie"
	default:
		return strings.ToLower(strings.TrimSpace(value))
	}
}

// NormalizeTemplateCoverage 标准化模板覆盖范围。旧配置没有 coverage 时按 residential 布尔值区分。
func NormalizeTemplateCoverage(value string, residential bool, templateType string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "normal", "residential", "both":
		return strings.ToLower(strings.TrimSpace(value))
	}
	if NormalizeTemplateType(templateType) == "chijie" {
		return "both"
	}
	if residential {
		return "residential"
	}
	return "normal"
}

// TemplateCoversResidential 判断模板是否服务当前普通/家宽请求。
func TemplateCoversResidential(cfg *PoolConfig, residential bool) bool {
	if cfg == nil {
		return false
	}
	switch NormalizeTemplateCoverage(cfg.Coverage, cfg.Residential, cfg.TemplateType) {
	case "both":
		return true
	case "residential":
		return residential
	default:
		return !residential
	}
}

// TemplateCoversSelector 判断模板是否服务当前普通/家宽请求。Premium 只影响优先级，不影响可用性。
func TemplateCoversSelector(cfg *PoolConfig, selector EgressSelector) bool {
	if cfg == nil {
		return false
	}
	return TemplateCoversResidential(cfg, selector.Residential)
}

func entryMatchesSelector(entry *NodeEntry, selector EgressSelector) bool {
	return entry != nil && entry.Residential == selector.Residential
}

func noEgressChoicesError(region string, selector EgressSelector) error {
	if selector.Residential {
		return fmt.Errorf("no residential nodes or residential templates available for region %s", region)
	}
	return fmt.Errorf("no nodes or templates available for region %s", region)
}

// ChijieProxyURL 返回远端 Chijie 的 /proxy HTTPS 地址。endpoint 可只填写域名。
func ChijieProxyURL(endpoint string, port int) (string, error) {
	endpoint = strings.TrimSpace(endpoint)
	if endpoint == "" {
		return "", fmt.Errorf("chijie endpoint is required")
	}
	if !strings.Contains(endpoint, "://") {
		endpoint = "https://" + endpoint
	}
	parsed, err := url.Parse(endpoint)
	if err != nil {
		return "", fmt.Errorf("parse chijie endpoint: %w", err)
	}
	if parsed.Scheme != "https" {
		return "", fmt.Errorf("chijie template endpoint must use https")
	}
	if parsed.Host == "" {
		return "", fmt.Errorf("chijie endpoint has no host")
	}
	if port > 0 && parsed.Port() == "" {
		parsed.Host = net.JoinHostPort(parsed.Hostname(), fmt.Sprintf("%d", port))
	}
	parsed.RawQuery = ""
	parsed.Fragment = ""
	basePath := strings.TrimRight(parsed.Path, "/")
	if basePath == "" {
		parsed.Path = "/proxy"
	} else if !strings.HasSuffix(basePath, "/proxy") {
		parsed.Path = basePath + "/proxy"
	} else {
		parsed.Path = basePath
	}
	return parsed.String(), nil
}

// SelectNamedNode selects a currently enabled node by pool and node name.
func (m *Manager) SelectNamedNode(poolName, nodeName string) (*EgressChoice, error) {
	m.mu.RLock()
	p := m.pools[poolName]
	m.mu.RUnlock()
	if p == nil || !poolEnabled(p.Config) {
		return nil, fmt.Errorf("pool unavailable: %s", poolName)
	}
	p.mu.RLock()
	defer p.mu.RUnlock()
	for _, entry := range p.Entries {
		if entry.Node.Name == nodeName && entry.Enabled && (entry.Alive || p.Config.TryOffline) {
			return &EgressChoice{Dialer: entry.Dialer, PoolName: poolName, NodeName: nodeName, Source: p.Config.Source, Region: entry.Region, Group: EgressGroup(entry.Region, entry.Residential), Residential: entry.Residential, Premium: entry.Premium}, nil
		}
	}
	return nil, fmt.Errorf("node unavailable: %s/%s", poolName, nodeName)
}

// SelectGateway selects a configured Chijie instance for an explicit remote exit.
func (m *Manager) SelectGateway(name string) (*EgressChoice, error) {
	m.mu.RLock()
	p := m.pools[name]
	m.mu.RUnlock()
	if p == nil || !poolEnabled(p.Config) || p.Config.Source != "template" || NormalizeTemplateType(p.Config.TemplateType) != "chijie" {
		return nil, fmt.Errorf("gateway unavailable: %s", name)
	}
	endpoint, err := ChijieProxyURL(p.Config.Endpoint, p.Config.Port)
	if err != nil {
		return nil, err
	}
	return &EgressChoice{PoolName: name, NodeName: name, Source: "template", TemplateType: "chijie", Template: true, Endpoint: endpoint, BearerToken: p.Config.BearerToken}, nil
}
