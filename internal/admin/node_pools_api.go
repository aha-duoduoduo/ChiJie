package admin

import (
	"encoding/json"
	"net/http"
	"path/filepath"
	"strings"

	"chijie/internal/pool"
)

// handleNodes GET 查看节点池状态 / POST 添加节点池
func (s *Server) handleNodes(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case "GET":
		s.getNodes(w)
	case "POST":
		s.addNodePool(w, r)
	default:
		http.Error(w, `{"error":"method not allowed"}`, http.StatusMethodNotAllowed)
	}
}

// getNodes 返回所有节点池
func (s *Server) getNodes(w http.ResponseWriter) {
	pools := s.poolManager.GetPoolStatus()
	writeJSON(w, http.StatusOK, map[string]any{
		"count": len(pools),
		"pools": pools,
	})
}

// addNodePool 添加节点池
func (s *Server) addNodePool(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name   string           `json:"name"`
		Config *pool.PoolConfig `json:"config"`
	}

	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{
			"error": "invalid request body: " + err.Error(),
		})
		return
	}

	req.Name = strings.TrimSpace(req.Name)
	if req.Name == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{
			"error": "pool name is required",
		})
		return
	}

	if req.Config == nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{
			"error": "pool config is required",
		})
		return
	}

	// 验证配置
	if err := validatePoolConfig(req.Config); err != nil {
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

	// 检查是否已存在
	if _, exists := config.NodePools[req.Name]; exists {
		writeJSON(w, http.StatusConflict, map[string]any{
			"error": "pool already exists",
		})
		return
	}

	// 添加新节点池
	config.NodePools[req.Name] = req.Config

	if err := s.saveAndReloadNodes(nodesPath, config); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
		return
	}

	writeJSON(w, http.StatusCreated, map[string]any{
		"message": "pool added successfully",
		"name":    req.Name,
	})
}

// handleNodePoolConfig PUT 更新节点池 / DELETE 删除节点池
func (s *Server) handleNodePoolConfig(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case "PUT":
		s.updateNodePool(w, r)
	case "DELETE":
		s.deleteNodePool(w, r)
	default:
		http.Error(w, `{"error":"method not allowed"}`, http.StatusMethodNotAllowed)
	}
}

// updateNodePool 更新节点池配置
func (s *Server) updateNodePool(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name    string           `json:"name"`
		NewName string           `json:"new_name"`
		Config  *pool.PoolConfig `json:"config"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{
			"error": "invalid request body: " + err.Error(),
		})
		return
	}

	req.Name = strings.TrimSpace(req.Name)
	req.NewName = strings.TrimSpace(req.NewName)
	if req.Name == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{
			"error": "pool name is required",
		})
		return
	}
	if req.Config == nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{
			"error": "pool config is required",
		})
		return
	}
	if err := validatePoolConfig(req.Config); err != nil {
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

	if _, exists := config.NodePools[req.Name]; !exists {
		writeJSON(w, http.StatusNotFound, map[string]any{
			"error": "pool not found",
		})
		return
	}

	targetName := req.Name
	if req.NewName != "" && req.NewName != req.Name {
		if _, exists := config.NodePools[req.NewName]; exists {
			writeJSON(w, http.StatusConflict, map[string]any{
				"error": "pool name already exists",
			})
			return
		}
		delete(config.NodePools, req.Name)
		targetName = req.NewName
	}

	config.NodePools[targetName] = req.Config
	if err := s.saveAndReloadNodes(nodesPath, config); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{
			"error": err.Error(),
		})
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"message": "pool updated successfully",
		"name":    targetName,
	})
}

// deleteNodePool 删除节点池配置
func (s *Server) deleteNodePool(w http.ResponseWriter, r *http.Request) {
	poolName := strings.TrimSpace(r.URL.Query().Get("name"))
	if poolName == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{
			"error": "pool name is required",
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

	if _, exists := config.NodePools[poolName]; !exists {
		writeJSON(w, http.StatusNotFound, map[string]any{
			"error": "pool not found",
		})
		return
	}

	delete(config.NodePools, poolName)
	if err := s.saveAndReloadNodes(nodesPath, config); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{
			"error": err.Error(),
		})
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"message": "pool deleted successfully",
		"name":    poolName,
	})
}
