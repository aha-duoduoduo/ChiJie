package admin

import (
	"encoding/json"
	stderrors "errors"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"chijie/internal/pool"
	proxyserver "chijie/internal/server"
	"chijie/internal/util"
)

// handleReload POST 热重载全部配置
func (s *Server) handleReload(w http.ResponseWriter, r *http.Request) {
	if r.Method != "POST" {
		http.Error(w, `{"error":"method not allowed"}`, http.StatusMethodNotAllowed)
		return
	}

	var errors []string

	// 重载指纹库
	if s.fpManager != nil {
		fpPath := s.configDir + "/fingerprints.yaml"
		if _, err := os.Stat(fpPath); stderrors.Is(err, os.ErrNotExist) {
			log.Printf("admin: fingerprints config not found, skipping: %s", fpPath)
		} else if err := s.fpManager.LoadFromFile(fpPath); err != nil {
			errors = append(errors, "fingerprints: "+err.Error())
		}
	}
	if s.poolManager != nil {
		nodesPath := filepath.Join(s.configDir, "nodes.yaml")
		if err := s.poolManager.LoadFromFile(nodesPath); err != nil {
			errors = append(errors, "nodes: "+err.Error())
		} else {
			s.poolManager.StartSubscriptionUpdater()
		}
	}
	if s.healthChecker != nil {
		defaults, err := s.loadHealthCheckSettings()
		if err != nil {
			errors = append(errors, "health_check: "+err.Error())
		} else {
			s.healthChecker.UpdateDefaults(defaults)
		}
	}
	if s.proxyRuntime != nil {
		settings, err := s.loadProxySettings()
		if err != nil {
			errors = append(errors, "proxy: "+err.Error())
		} else {
			s.proxyRuntime.UpdateProxySettings(settings)
		}
	}
	if s.traffic != nil {
		trafficCfg, err := s.loadTrafficConfig()
		if err != nil {
			errors = append(errors, "traffic: "+err.Error())
		} else {
			s.traffic.UpdateConfig(trafficCfg)
		}
	}

	if len(errors) > 0 {
		log.Printf("admin: reload partial failure: %v", errors)
		writeJSON(w, http.StatusInternalServerError, map[string]any{
			"message": "reload completed with errors",
			"errors":  errors,
		})
		return
	}

	log.Printf("admin: all configs reloaded")
	writeJSON(w, http.StatusOK, map[string]any{
		"message": "all configs reloaded",
	})
}

// handleStats GET 基础统计信息
func (s *Server) handleStats(w http.ResponseWriter, r *http.Request) {
	if r.Method != "GET" {
		http.Error(w, `{"error":"method not allowed"}`, http.StatusMethodNotAllowed)
		return
	}

	uptime := time.Since(s.startTime)

	writeJSON(w, http.StatusOK, map[string]any{
		"uptime":       uptime.String(),
		"uptime_sec":   int(uptime.Seconds()),
		"pools_count":  len(s.poolManager.ListPools()),
		"fingerprints": len(s.fpManager.List()),
		"traffic":      s.traffic.Metrics(),
		"runtime":      s.runtimeSnapshot(),
		"health_check": s.healthCheckSettingsSnapshot(),
		"proxy":        s.proxySettingsSnapshot(),
	})
}

func (s *Server) handleLogging(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case "GET":
		writeJSON(w, http.StatusOK, s.runtimeSnapshot())
	case "PUT":
		var req struct {
			Level string `json:"level"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]any{"error": "invalid request body"})
			return
		}
		util.SetLogLevel(req.Level)
		if err := s.persistLogLevel(util.LogLevelName()); err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
			return
		}
		info := s.runtimeSnapshot()
		writeJSON(w, http.StatusOK, info)
	default:
		http.Error(w, `{"error":"method not allowed"}`, http.StatusMethodNotAllowed)
	}
}

func (s *Server) persistLogLevel(level string) error {
	s.fileMu.Lock()
	defer s.fileMu.Unlock()

	path := filepath.Join(s.configDir, "gateway.yaml")
	var cfg map[string]any
	if err := loadYAML(path, &cfg); err != nil {
		return err
	}
	logCfg, ok := cfg["log"].(map[string]any)
	if !ok || logCfg == nil {
		logCfg = map[string]any{}
		cfg["log"] = logCfg
	}
	logCfg["level"] = level
	if _, exists := logCfg["file"]; !exists {
		logCfg["file"] = ""
	}
	if err := atomicWriteYAML(path, cfg); err != nil {
		return err
	}

	s.runtimeMu.Lock()
	info := s.runtimeInfo
	info.LogLevel = level
	s.runtimeInfo = info
	s.runtimeMu.Unlock()
	return nil
}

func (s *Server) handleHealthCheckSettings(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case "GET":
		writeJSON(w, http.StatusOK, s.healthCheckSettingsSnapshot())
	case "PUT":
		var req pool.HealthCheckConfig
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]any{"error": "invalid request body"})
			return
		}
		defaults, err := pool.ParseHealthCheckDefaults(&req)
		if err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]any{"error": err.Error()})
			return
		}
		cfg := pool.HealthCheckDefaultsConfig(defaults)
		if err := s.persistHealthCheckSettings(cfg); err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
			return
		}
		if s.healthChecker != nil {
			s.healthChecker.UpdateDefaults(defaults)
		}
		writeJSON(w, http.StatusOK, s.healthCheckSettingsSnapshot())
	default:
		http.Error(w, `{"error":"method not allowed"}`, http.StatusMethodNotAllowed)
	}
}

func (s *Server) healthCheckSettingsSnapshot() map[string]any {
	defaults := pool.DefaultHealthCheckDefaults()
	if s.healthChecker != nil {
		defaults = s.healthChecker.Defaults()
	}
	cfg := pool.HealthCheckDefaultsConfig(defaults)
	return map[string]any{
		"interval": cfg.Interval,
		"timeout":  cfg.Timeout,
		"url":      cfg.URL,
		"max_fail": cfg.MaxFail,
	}
}

func (s *Server) persistHealthCheckSettings(healthCfg *pool.HealthCheckConfig) error {
	s.fileMu.Lock()
	defer s.fileMu.Unlock()

	path := filepath.Join(s.configDir, "gateway.yaml")
	var cfg map[string]any
	if err := loadYAML(path, &cfg); err != nil {
		return err
	}
	cfg["health_check"] = map[string]any{
		"interval": healthCfg.Interval,
		"timeout":  healthCfg.Timeout,
		"url":      healthCfg.URL,
		"max_fail": healthCfg.MaxFail,
	}
	if err := atomicWriteYAML(path, cfg); err != nil {
		return err
	}
	return nil
}

func (s *Server) loadHealthCheckSettings() (pool.HealthCheckDefaults, error) {
	path := filepath.Join(s.configDir, "gateway.yaml")
	var cfg struct {
		HealthCheck *pool.HealthCheckConfig `yaml:"health_check"`
	}
	if err := loadYAML(path, &cfg); err != nil {
		return pool.DefaultHealthCheckDefaults(), err
	}
	return pool.ParseHealthCheckDefaults(cfg.HealthCheck)
}

func (s *Server) handleProxySettings(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case "GET":
		writeJSON(w, http.StatusOK, s.proxySettingsSnapshot())
	case "PUT":
		var req proxyserver.ProxySettingsConfig
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]any{"error": "invalid request body"})
			return
		}
		settings, err := proxyserver.ParseProxySettings(&req)
		if err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]any{"error": err.Error()})
			return
		}
		if err := s.persistProxySettings(settings); err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
			return
		}
		writeJSON(w, http.StatusOK, s.proxySettingsSnapshot())
	default:
		http.Error(w, `{"error":"method not allowed"}`, http.StatusMethodNotAllowed)
	}
}

func (s *Server) proxySettingsSnapshot() map[string]any {
	settings := proxyserver.DefaultProxySettings()
	if s.proxyRuntime != nil {
		settings = s.proxyRuntime.ProxySettings()
	} else if loaded, err := s.loadProxySettings(); err == nil {
		settings = loaded
	}
	return map[string]any{
		"domain_retries":                   settings.DomainRetries,
		"max_attempts":                     settings.MaxAttempts,
		"max_redirects":                    settings.MaxRedirects,
		"template_fallback_after_attempts": settings.TemplateFallbackAfterAttempts,
		"response_header_timeout":          settings.ResponseHeaderTimeout.String(),
		"total_timeout":                    settings.TotalTimeout.String(),
	}
}

func (s *Server) persistProxySettings(settings proxyserver.ProxySettings) error {
	s.fileMu.Lock()
	defer s.fileMu.Unlock()

	return s.writeProxySettingsLocked(settings)
}

func (s *Server) writeProxySettingsLocked(settings proxyserver.ProxySettings) error {
	path := filepath.Join(s.configDir, "gateway.yaml")
	var cfg map[string]any
	if err := loadYAML(path, &cfg); err != nil {
		return err
	}
	cfg["proxy"] = map[string]any{
		"domain_retries":                   settings.DomainRetries,
		"max_attempts":                     settings.MaxAttempts,
		"max_redirects":                    settings.MaxRedirects,
		"template_fallback_after_attempts": settings.TemplateFallbackAfterAttempts,
		"response_header_timeout":          settings.ResponseHeaderTimeout.String(),
		"total_timeout":                    settings.TotalTimeout.String(),
	}
	if err := atomicWriteYAML(path, cfg); err != nil {
		return err
	}
	if s.proxyRuntime != nil {
		s.proxyRuntime.UpdateProxySettings(settings)
	}
	return nil
}

func (s *Server) loadProxySettings() (proxyserver.ProxySettings, error) {
	path := filepath.Join(s.configDir, "gateway.yaml")
	var cfg struct {
		Proxy *proxyserver.ProxySettingsConfig `yaml:"proxy"`
	}
	if err := loadYAML(path, &cfg); err != nil {
		return proxyserver.DefaultProxySettings(), err
	}
	return proxyserver.ParseProxySettings(cfg.Proxy)
}

func (s *Server) handleConfigExport(w http.ResponseWriter, r *http.Request) {
	if r.Method != "GET" {
		http.Error(w, `{"error":"method not allowed"}`, http.StatusMethodNotAllowed)
		return
	}
	readText := func(name string) string {
		data, err := os.ReadFile(filepath.Join(s.configDir, name))
		if err != nil {
			return ""
		}
		return string(data)
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"exported_at":       time.Now().UTC().Format(time.RFC3339),
		"gateway_yaml":      readText("gateway.yaml"),
		"nodes_yaml":        readText("nodes.yaml"),
		"fingerprints_yaml": readText("fingerprints.yaml"),
	})
}
