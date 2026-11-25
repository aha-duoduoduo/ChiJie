package pool

import (
	"maps"
	"slices"
)

func clonePoolConfig(cfg *PoolConfig) *PoolConfig {
	if cfg == nil {
		return nil
	}
	copy := *cfg
	if cfg.Enabled != nil {
		value := *cfg.Enabled
		copy.Enabled = &value
	}
	if cfg.Filter != nil {
		copy.Filter = &FilterConfig{Region: slices.Clone(cfg.Filter.Region)}
	}
	if cfg.HealthCheck != nil {
		value := *cfg.HealthCheck
		copy.HealthCheck = &value
	}
	copy.Nodes = slices.Clone(cfg.Nodes)
	for i := range copy.Nodes {
		node := &copy.Nodes[i]
		if node.Enabled != nil {
			value := *node.Enabled
			node.Enabled = &value
		}
		node.Tags = slices.Clone(node.Tags)
		node.Extra = maps.Clone(node.Extra)
	}
	copy.DisabledNodes = slices.Clone(cfg.DisabledNodes)
	copy.Tags = slices.Clone(cfg.Tags)
	copy.RejectRegex = slices.Clone(cfg.RejectRegex)
	copy.NodeRegions = maps.Clone(cfg.NodeRegions)
	copy.NodeAliases = maps.Clone(cfg.NodeAliases)
	copy.NodeServerRegions = maps.Clone(cfg.NodeServerRegions)
	copy.NodeServerAliases = maps.Clone(cfg.NodeServerAliases)
	copy.RegionGroupNames = maps.Clone(cfg.RegionGroupNames)
	copy.NodeTags = cloneTags(cfg.NodeTags)
	copy.NodeServerTags = cloneTags(cfg.NodeServerTags)
	return &copy
}

func cloneTags(tags map[string][]string) map[string][]string {
	copy := maps.Clone(tags)
	for key, values := range copy {
		copy[key] = slices.Clone(values)
	}
	return copy
}
