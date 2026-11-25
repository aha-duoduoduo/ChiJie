package admin

import (
	"encoding/json"
	stderrors "errors"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"chijie/internal/fingerprint"
)

// handleFingerprints GET 查看指纹列表 / POST 添加指纹
func (s *Server) handleFingerprints(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case "GET":
		s.getFingerprints(w)
	case "POST":
		s.addFingerprint(w, r)
	default:
		http.Error(w, `{"error":"method not allowed"}`, http.StatusMethodNotAllowed)
	}
}

// getFingerprints 返回所有指纹
func (s *Server) getFingerprints(w http.ResponseWriter) {
	fps := s.fpManager.List()

	type fingerprintView struct {
		Name   string                         `json:"name"`
		Type   string                         `json:"type"`
		Config *fingerprint.FingerprintConfig `json:"config,omitempty"`
		Preset string                         `json:"preset,omitempty"`
		JA3    string                         `json:"ja3,omitempty"`
		JA4    string                         `json:"ja4,omitempty"`
		Akamai string                         `json:"akamai,omitempty"`
	}

	result := make([]fingerprintView, 0, len(fps))
	for _, name := range fps {
		spec, ok := s.fpManager.Get(name)
		if !ok {
			continue
		}

		view := fingerprintView{
			Name:   name,
			Config: spec.Canonical(),
		}

		// 判断是预设还是自定义
		if spec.Preset != "" {
			view.Type = "preset"
			view.Preset = spec.Preset
		} else {
			view.Type = "custom"
			view.JA3 = spec.JA3
		}
		view.JA4 = spec.JA4
		view.Akamai = spec.Akamai

		result = append(result, view)
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"count":        len(result),
		"fingerprints": result,
	})
}

// addFingerprint 添加指纹
func (s *Server) addFingerprint(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name       string                         `json:"name"`
		Config     *fingerprint.FingerprintConfig `json:"config"`
		ConfigText string                         `json:"config_text"`
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
			"error": "fingerprint name is required",
		})
		return
	}

	if req.Config == nil && strings.TrimSpace(req.ConfigText) != "" {
		parsed, err := fingerprint.ParseConfigText(req.ConfigText)
		if err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]any{
				"error": "failed to parse fingerprint config: " + err.Error(),
			})
			return
		}
		req.Config = parsed
	}

	if req.Config == nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{
			"error": "fingerprint config is required",
		})
		return
	}

	req.Config = req.Config.Canonical()

	// 验证配置：必须有 preset、ja3 或 ja4 raw。Akamai/HTTP2 raw 是补充字段。
	if req.Config.Preset == "" && req.Config.JA3 == "" && req.Config.JA4 == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{
			"error": "fingerprint must have preset, ja3, or ja4 raw",
		})
		return
	}

	if _, _, err := fingerprint.BuildSpecFromConfig(req.Config); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{
			"error": "invalid fingerprint config: " + err.Error(),
		})
		return
	}

	s.fileMu.Lock()
	defer s.fileMu.Unlock()

	// 读取现有指纹配置
	fpPath := filepath.Join(s.configDir, "fingerprints.yaml")
	config, err := loadFingerprintsConfig(fpPath)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{
			"error": "failed to load fingerprints: " + err.Error(),
		})
		return
	}

	// 检查是否已存在
	if _, exists := config.Fingerprints[req.Name]; exists {
		writeJSON(w, http.StatusConflict, map[string]any{
			"error": "fingerprint already exists",
		})
		return
	}

	// 添加新指纹
	config.Fingerprints[req.Name] = req.Config

	// 保存到文件
	if err := s.saveFingerprintsToFile(fpPath, config); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{
			"error": "failed to save fingerprints: " + err.Error(),
		})
		return
	}

	// 重新加载指纹管理器
	if err := s.fpManager.LoadFromFile(fpPath); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{
			"error": "failed to reload fingerprints: " + err.Error(),
		})
		return
	}

	writeJSON(w, http.StatusCreated, map[string]any{
		"message": "fingerprint added successfully",
		"name":    req.Name,
	})
}

// handleFingerprintByName DELETE 删除指纹
func (s *Server) handleFingerprintByName(w http.ResponseWriter, r *http.Request) {
	if r.Method != "DELETE" {
		http.Error(w, `{"error":"method not allowed"}`, http.StatusMethodNotAllowed)
		return
	}

	// 提取指纹名称
	path := strings.TrimPrefix(r.URL.Path, "/api/fingerprints/")
	if path == "" || path == "test" {
		http.Error(w, `{"error":"fingerprint name required"}`, http.StatusBadRequest)
		return
	}
	fpName := path

	s.fileMu.Lock()
	defer s.fileMu.Unlock()

	// 读取现有指纹配置
	fpPath := filepath.Join(s.configDir, "fingerprints.yaml")
	config, err := loadFingerprintsConfig(fpPath)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{
			"error": "failed to load fingerprints: " + err.Error(),
		})
		return
	}

	// 检查指纹是否存在
	if _, exists := config.Fingerprints[fpName]; !exists {
		writeJSON(w, http.StatusNotFound, map[string]any{
			"error": "fingerprint not found",
		})
		return
	}

	// 删除指纹
	delete(config.Fingerprints, fpName)

	// 保存到文件
	if err := s.saveFingerprintsToFile(fpPath, config); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{
			"error": "failed to save fingerprints: " + err.Error(),
		})
		return
	}

	// 重新加载指纹管理器
	if err := s.fpManager.LoadFromFile(fpPath); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{
			"error": "failed to reload fingerprints: " + err.Error(),
		})
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"message": "fingerprint deleted successfully",
	})
}

// saveFingerprintsToFile 保存指纹到 YAML 文件（保留兼容签名）。
func (s *Server) saveFingerprintsToFile(path string, config *fingerprint.FileConfig) error {
	return atomicWriteYAML(path, config)
}

func loadFingerprintsConfig(path string) (*fingerprint.FileConfig, error) {
	var config fingerprint.FileConfig
	if err := loadYAML(path, &config); err != nil {
		if !stderrors.Is(err, os.ErrNotExist) {
			return nil, err
		}
	}
	if config.Fingerprints == nil {
		config.Fingerprints = make(map[string]*fingerprint.FingerprintConfig)
	}
	return &config, nil
}
