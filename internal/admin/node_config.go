package admin

import (
	"fmt"
	"regexp"
	"strings"

	"chijie/internal/dialer"
	"chijie/internal/pool"
)

func loadNodesConfig(path string) (*pool.NodesFileConfig, error) {
	var config pool.NodesFileConfig
	if err := loadYAML(path, &config); err != nil {
		return nil, err
	}
	if config.NodePools == nil {
		config.NodePools = make(map[string]*pool.PoolConfig)
	}
	return &config, nil
}

func (s *Server) saveAndReloadNodes(path string, config *pool.NodesFileConfig) error {
	if err := s.saveNodesToFile(path, config); err != nil {
		return fmt.Errorf("failed to save nodes: %w", err)
	}
	if err := s.poolManager.LoadFromFile(path); err != nil {
		return fmt.Errorf("failed to reload nodes: %w", err)
	}
	s.poolManager.StartSubscriptionUpdater()
	return nil
}

func validatePoolConfig(config *pool.PoolConfig) error {
	config.Source = strings.TrimSpace(config.Source)
	for _, pattern := range config.RejectRegex {
		pattern = strings.TrimSpace(pattern)
		if pattern == "" {
			continue
		}
		if _, err := regexp.Compile(pattern); err != nil {
			return fmt.Errorf("invalid reject regex %q: %w", pattern, err)
		}
	}
	switch config.Source {
	case "direct":
		return nil
	case "subscription":
		if strings.TrimSpace(config.URL) == "" {
			return fmt.Errorf("subscription url is required")
		}
		config.UpdateInterval = strings.TrimSpace(config.UpdateInterval)
		if config.UpdateInterval != "" {
			if _, err := pool.ParseDurationWithDays(config.UpdateInterval); err != nil {
				return fmt.Errorf("invalid update_interval: %w", err)
			}
		}
		return nil
	case "template":
		config.TemplateType = pool.NormalizeTemplateType(config.TemplateType)
		switch config.TemplateType {
		case "chijie":
			if _, err := pool.ChijieProxyURL(config.Endpoint, config.Port); err != nil {
				return fmt.Errorf("invalid chijie template endpoint: %w", err)
			}
			if strings.TrimSpace(config.BearerToken) == "" {
				return fmt.Errorf("chijie template bearer_token is required")
			}
		case "proxy":
			node := &dialer.Node{
				Name:   "template",
				Type:   config.Type,
				Server: config.Server,
				Port:   config.Port,
			}
			if err := validateNodeConfig(node); err != nil {
				return fmt.Errorf("invalid template config: %w", err)
			}
		default:
			return fmt.Errorf("unsupported template_type: %s", config.TemplateType)
		}
		config.Coverage = pool.NormalizeTemplateCoverage(config.Coverage, config.Residential, config.TemplateType)
		switch config.Coverage {
		case "normal", "residential", "both":
		default:
			return fmt.Errorf("invalid coverage: %s", config.Coverage)
		}
		return nil
	case "static":
		seen := make(map[string]struct{}, len(config.Nodes))
		for i := range config.Nodes {
			if err := validateNodeConfig(&config.Nodes[i]); err != nil {
				return fmt.Errorf("invalid static node %d: %w", i+1, err)
			}
			if _, exists := seen[config.Nodes[i].Name]; exists {
				return fmt.Errorf("duplicate node name: %s", config.Nodes[i].Name)
			}
			seen[config.Nodes[i].Name] = struct{}{}
		}
		return nil
	default:
		return fmt.Errorf("pool source is required (static/subscription/template/direct)")
	}
}

func validateNodeConfig(node *dialer.Node) error {
	node.Name = strings.TrimSpace(node.Name)
	node.Type = strings.TrimSpace(node.Type)
	node.Server = strings.TrimSpace(node.Server)

	if node.Name == "" {
		return fmt.Errorf("node name is required")
	}
	if !dialer.SupportedNodeType(node.Type) {
		return fmt.Errorf("unsupported node type: %s", node.Type)
	}
	if node.Type == "direct" {
		return nil
	}
	if node.Server == "" {
		return fmt.Errorf("node server is required")
	}
	if node.Port <= 0 || node.Port > 65535 {
		return fmt.Errorf("node port must be between 1 and 65535")
	}
	return nil
}

// saveNodesToFile 保存节点配置到 YAML 文件（保留兼容签名）。
func (s *Server) saveNodesToFile(path string, config *pool.NodesFileConfig) error {
	return atomicWriteYAML(path, config)
}
