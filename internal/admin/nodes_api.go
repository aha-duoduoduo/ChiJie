package admin

import (
	"encoding/json"
	"net/http"
	"path/filepath"
	"strings"

	"chijie/internal/dialer"
	"chijie/internal/pool"
	"chijie/internal/util"
)

// handleNodeConfig PUT 更新静态节点 / DELETE 删除静态节点
func (s *Server) handleNodeConfig(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case "PUT":
		s.updateNode(w, r)
	case "DELETE":
		s.deleteNode(w, r)
	default:
		http.Error(w, `{"error":"method not allowed"}`, http.StatusMethodNotAllowed)
	}
}

// updateNode 更新 static 节点池中的单个节点
func (s *Server) updateNode(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Pool        string       `json:"pool"`
		Node        string       `json:"node"`
		UpdatedNode *dialer.Node `json:"updated_node"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{
			"error": "invalid request body: " + err.Error(),
		})
		return
	}

	req.Pool = strings.TrimSpace(req.Pool)
	req.Node = strings.TrimSpace(req.Node)
	if req.Pool == "" || req.Node == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{
			"error": "pool and node are required",
		})
		return
	}
	if req.UpdatedNode == nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{
			"error": "updated_node is required",
		})
		return
	}
	if err := validateNodeConfig(req.UpdatedNode); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{
			"error": err.Error(),
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
	if poolCfg.Source != "static" {
		writeJSON(w, http.StatusBadRequest, map[string]any{
			"error": "node editing is only supported for static pools",
		})
		return
	}

	for _, existing := range poolCfg.Nodes {
		if existing.Name == req.UpdatedNode.Name && existing.Name != req.Node {
			writeJSON(w, http.StatusConflict, map[string]any{
				"error": "node name already exists",
			})
			return
		}
	}

	found := false
	for i := range poolCfg.Nodes {
		if poolCfg.Nodes[i].Name == req.Node {
			poolCfg.Nodes[i] = *req.UpdatedNode
			found = true
			break
		}
	}
	if !found {
		writeJSON(w, http.StatusNotFound, map[string]any{
			"error": "node not found",
		})
		return
	}
	if req.Node != req.UpdatedNode.Name {
		poolCfg.DisabledNodes = util.RemoveString(poolCfg.DisabledNodes, req.Node)
	}

	if err := s.saveAndReloadNodes(nodesPath, config); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{
			"error": err.Error(),
		})
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"message": "node updated successfully",
		"pool":    req.Pool,
		"node":    req.UpdatedNode.Name,
	})
}

// deleteNode 删除 static 节点池中的单个节点
func (s *Server) deleteNode(w http.ResponseWriter, r *http.Request) {
	poolName := strings.TrimSpace(r.URL.Query().Get("pool"))
	nodeName := strings.TrimSpace(r.URL.Query().Get("node"))
	if poolName == "" || nodeName == "" {
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

	poolCfg := config.NodePools[poolName]
	if poolCfg == nil {
		writeJSON(w, http.StatusNotFound, map[string]any{
			"error": "pool not found",
		})
		return
	}
	if poolCfg.Source != "static" {
		writeJSON(w, http.StatusBadRequest, map[string]any{
			"error": "node deletion is only supported for static pools",
		})
		return
	}

	nodes := make([]dialer.Node, 0, len(poolCfg.Nodes))
	found := false
	for _, node := range poolCfg.Nodes {
		if node.Name == nodeName {
			found = true
			continue
		}
		nodes = append(nodes, node)
	}
	if !found {
		writeJSON(w, http.StatusNotFound, map[string]any{
			"error": "node not found",
		})
		return
	}

	poolCfg.Nodes = nodes
	poolCfg.DisabledNodes = util.RemoveString(poolCfg.DisabledNodes, nodeName)
	if err := s.saveAndReloadNodes(nodesPath, config); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{
			"error": err.Error(),
		})
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"message": "node deleted successfully",
		"pool":    poolName,
		"node":    nodeName,
	})
}

// handleNodeEnabled POST 持久化更新节点启停状态
func (s *Server) handleNodeEnabled(w http.ResponseWriter, r *http.Request) {
	if r.Method != "POST" {
		http.Error(w, `{"error":"method not allowed"}`, http.StatusMethodNotAllowed)
		return
	}

	var req struct {
		Pool    string `json:"pool"`
		Node    string `json:"node"`
		Enabled bool   `json:"enabled"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{
			"error": "invalid request body",
		})
		return
	}
	if req.Pool == "" || req.Node == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{
			"error": "pool and node are required",
		})
		return
	}

	if err := s.poolManager.SetNodeEnabled(req.Pool, req.Node, req.Enabled); err != nil {
		writeJSON(w, http.StatusNotFound, map[string]any{
			"error": err.Error(),
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
	pool.UpdateConfigNodeEnabled(poolCfg, req.Node, req.Enabled)

	if err := s.saveNodesToFile(nodesPath, config); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{
			"error": "failed to save nodes: " + err.Error(),
		})
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"message": "node state updated",
		"pool":    req.Pool,
		"node":    req.Node,
		"enabled": req.Enabled,
	})
}
