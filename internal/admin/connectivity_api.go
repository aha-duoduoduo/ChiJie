package admin

import (
	"encoding/json"
	"net/http"
	"strings"
)

// handleNodeTest POST 测试节点连通性
func (s *Server) handleNodeTest(w http.ResponseWriter, r *http.Request) {
	if r.Method != "POST" {
		http.Error(w, `{"error":"method not allowed"}`, http.StatusMethodNotAllowed)
		return
	}

	var req struct {
		Pool string `json:"pool"`
		Node string `json:"node"`
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

	result, err := s.poolManager.TestNodeConnectivity(req.Pool, req.Node, "", 0)
	if err != nil {
		writeJSON(w, http.StatusNotFound, map[string]any{
			"error": err.Error(),
		})
		return
	}

	writeJSON(w, http.StatusOK, result)
}

// handleTemplateTest POST 即时测试模板池指定地区的连通性
func (s *Server) handleTemplateTest(w http.ResponseWriter, r *http.Request) {
	if r.Method != "POST" {
		http.Error(w, `{"error":"method not allowed"}`, http.StatusMethodNotAllowed)
		return
	}

	var req struct {
		Pool   string `json:"pool"`
		Region string `json:"region"`
		URL    string `json:"url"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{
			"error": "invalid request body",
		})
		return
	}
	req.Pool = strings.TrimSpace(req.Pool)
	req.Region = strings.ToUpper(strings.TrimSpace(req.Region))
	if req.Pool == "" || req.Region == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{
			"error": "pool and region are required",
		})
		return
	}

	result, err := s.poolManager.TestTemplateConnectivity(req.Pool, req.Region, strings.TrimSpace(req.URL), 0)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{
			"error": err.Error(),
		})
		return
	}

	writeJSON(w, http.StatusOK, result)
}
