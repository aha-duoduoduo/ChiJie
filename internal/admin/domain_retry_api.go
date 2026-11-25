package admin

import (
	"encoding/json"
	"net/http"

	proxyserver "chijie/internal/server"
)

// A single-domain update from Traffic preserves all other proxy settings/rules.
func (s *Server) handleDomainRetry(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPut {
		http.Error(w, `{"error":"method not allowed"}`, http.StatusMethodNotAllowed)
		return
	}
	var rule proxyserver.DomainRetryRule
	if err := json.NewDecoder(r.Body).Decode(&rule); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "invalid request body"})
		return
	}
	validated, err := proxyserver.ParseProxySettings(&proxyserver.ProxySettingsConfig{DomainRetries: []proxyserver.DomainRetryRule{rule}})
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": err.Error()})
		return
	}
	rule = validated.DomainRetries[0]
	s.fileMu.Lock()
	defer s.fileMu.Unlock()
	settings, err := s.loadProxySettings()
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
		return
	}
	found := false
	for i := range settings.DomainRetries {
		if settings.DomainRetries[i].Domain == rule.Domain {
			settings.DomainRetries[i] = rule
			found = true
			break
		}
	}
	if !found {
		settings.DomainRetries = append(settings.DomainRetries, rule)
	}
	settings, err = proxyserver.ValidateProxySettings(settings)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": err.Error()})
		return
	}
	if err := s.writeProxySettingsLocked(settings); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, s.proxySettingsSnapshot())
}
