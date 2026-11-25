package admin

import (
	"encoding/json"
	"log"
	"net/http"
	"path/filepath"
	"strings"

	"chijie/internal/pool"
	"chijie/internal/util"
)

// handleSubscriptionNodeConfig PUT 更新订阅节点元数据
func (s *Server) handleSubscriptionNodeConfig(w http.ResponseWriter, r *http.Request) {
	if r.Method != "PUT" {
		http.Error(w, `{"error":"method not allowed"}`, http.StatusMethodNotAllowed)
		return
	}

	var req struct {
		Pool        string   `json:"pool"`
		Node        string   `json:"node"`
		Server      string   `json:"server"`
		Port        int      `json:"port"`
		Region      string   `json:"region"`
		Alias       string   `json:"alias"`
		Tags        []string `json:"tags"`
		Residential *bool    `json:"residential"`
		Premium     *bool    `json:"premium"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{
			"error": "invalid request body: " + err.Error(),
		})
		return
	}

	req.Pool = strings.TrimSpace(req.Pool)
	req.Node = strings.TrimSpace(req.Node)
	req.Server = strings.TrimSpace(req.Server)
	req.Region = strings.ToUpper(strings.TrimSpace(req.Region))
	req.Alias = strings.TrimSpace(req.Alias)
	req.Tags = cleanStringSlice(req.Tags)
	if req.Residential != nil {
		req.Tags = setSpecialTag(req.Tags, "residential", *req.Residential)
	}
	if req.Premium != nil {
		req.Tags = setSpecialTag(req.Tags, "premium", *req.Premium)
	}
	if req.Pool == "" || req.Node == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{
			"error": "pool and node are required",
		})
		return
	}

	s.fileMu.Lock()
	defer s.fileMu.Unlock()

	nodesPath := filepath.Join(s.configDir, "nodes.yaml")
	config, err := loadNodesConfig(nodesPath)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{
			"error": "failed to load nodes: " + err.Error(),
		})
		return
	}

	poolCfg := config.NodePools[req.Pool]
	if poolCfg == nil {
		writeJSON(w, http.StatusNotFound, map[string]any{
			"error": "pool not found",
		})
		return
	}
	if poolCfg.Source != "subscription" {
		writeJSON(w, http.StatusBadRequest, map[string]any{
			"error": "subscription node metadata is only supported for subscription pools",
		})
		return
	}

	serverKey := pool.ServerKey(req.Server, req.Port)
	if serverKey == "" {
		serverKey = s.findNodeServerKey(req.Pool, req.Node)
	}

	if req.Region == "" {
		delete(poolCfg.NodeRegions, req.Node)
		delete(poolCfg.NodeServerRegions, serverKey)
	} else {
		if poolCfg.NodeRegions == nil {
			poolCfg.NodeRegions = make(map[string]string)
		}
		poolCfg.NodeRegions[req.Node] = req.Region
		if serverKey != "" {
			if poolCfg.NodeServerRegions == nil {
				poolCfg.NodeServerRegions = make(map[string]string)
			}
			poolCfg.NodeServerRegions[serverKey] = req.Region
		}
	}

	if poolCfg.NodeAliases == nil {
		poolCfg.NodeAliases = make(map[string]string)
	}
	if poolCfg.NodeServerAliases == nil {
		poolCfg.NodeServerAliases = make(map[string]string)
	}
	if req.Alias != "" {
		if existing, exists := poolCfg.NodeAliases[req.Alias]; exists && existing != req.Node {
			writeJSON(w, http.StatusConflict, map[string]any{
				"error": "alias already points to another node",
			})
			return
		}
		for key, alias := range poolCfg.NodeServerAliases {
			if alias == req.Alias && key != serverKey {
				writeJSON(w, http.StatusConflict, map[string]any{
					"error": "alias already points to another server",
				})
				return
			}
		}
	}
	for alias, target := range poolCfg.NodeAliases {
		if target == req.Node {
			delete(poolCfg.NodeAliases, alias)
		}
	}
	if serverKey != "" {
		delete(poolCfg.NodeServerAliases, serverKey)
	}
	if req.Alias != "" {
		poolCfg.NodeAliases[req.Alias] = req.Node
		if serverKey != "" {
			poolCfg.NodeServerAliases[serverKey] = req.Alias
		}
	}

	if len(req.Tags) == 0 {
		delete(poolCfg.NodeTags, req.Node)
		delete(poolCfg.NodeServerTags, serverKey)
	} else {
		if poolCfg.NodeTags == nil {
			poolCfg.NodeTags = make(map[string][]string)
		}
		poolCfg.NodeTags[req.Node] = req.Tags
		if serverKey != "" {
			if poolCfg.NodeServerTags == nil {
				poolCfg.NodeServerTags = make(map[string][]string)
			}
			poolCfg.NodeServerTags[serverKey] = req.Tags
		}
	}

	if err := s.saveAndReloadNodes(nodesPath, config); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{
			"error": err.Error(),
		})
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"message":     "subscription node metadata updated",
		"pool":        req.Pool,
		"node":        req.Node,
		"region":      req.Region,
		"alias":       req.Alias,
		"tags":        req.Tags,
		"residential": util.ContainsString(req.Tags, "residential"),
		"premium":     util.ContainsString(req.Tags, "premium"),
		"server_key":  serverKey,
	})
}

func (s *Server) findNodeServerKey(poolName string, nodeName string) string {
	for _, ps := range s.poolManager.GetPoolStatus() {
		if ps.Name != poolName {
			continue
		}
		for _, node := range ps.Nodes {
			if node.Name == nodeName {
				return pool.ServerKey(node.Server, node.Port)
			}
		}
	}
	return ""
}

func cleanStringSlice(values []string) []string {
	result := make([]string, 0, len(values))
	seen := map[string]bool{}
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" || seen[value] {
			continue
		}
		seen[value] = true
		result = append(result, value)
	}
	return result
}

func setSpecialTag(tags []string, tag string, enabled bool) []string {
	tag = strings.TrimSpace(strings.ToLower(tag))
	if tag == "" {
		return tags
	}
	result := make([]string, 0, len(tags)+1)
	found := false
	for _, value := range tags {
		if strings.EqualFold(strings.TrimSpace(value), tag) {
			found = true
			if enabled {
				result = append(result, tag)
			}
			continue
		}
		result = append(result, value)
	}
	if enabled && !found {
		result = append(result, tag)
	}
	return cleanStringSlice(result)
}

// handleNodesRefresh POST 刷新订阅池
func (s *Server) handleNodesRefresh(w http.ResponseWriter, r *http.Request) {
	if r.Method != "POST" {
		http.Error(w, `{"error":"method not allowed"}`, http.StatusMethodNotAllowed)
		return
	}

	poolName := r.URL.Query().Get("pool")
	if poolName == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{
			"error": "pool parameter required",
		})
		return
	}

	if err := s.poolManager.RefreshSubscription(poolName); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{
			"error": err.Error(),
		})
		return
	}

	log.Printf("admin: pool %s refreshed", poolName)
	writeJSON(w, http.StatusOK, map[string]any{
		"message": "pool refreshed",
		"pool":    poolName,
	})
}
