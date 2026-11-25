package server

import (
	"fmt"
	"net/http"
	"strings"
	"time"

	"chijie/internal/dialer"
)

// ProxySettingsConfig 是 gateway.yaml / Admin API 使用的 /proxy 重试配置。
// TemplateFallbackAfterAttempts 使用指针以区分“未配置”和“显式 false”。
type ProxySettingsConfig struct {
	DomainRetries                 []DomainRetryRule `yaml:"domain_retries" json:"domain_retries"`
	MaxAttempts                   int               `yaml:"max_attempts" json:"max_attempts"`
	MaxRedirects                  int               `yaml:"max_redirects" json:"max_redirects"`
	TemplateFallbackAfterAttempts *bool             `yaml:"template_fallback_after_attempts" json:"template_fallback_after_attempts"`
	ResponseHeaderTimeout         string            `yaml:"response_header_timeout" json:"response_header_timeout"`
	TotalTimeout                  string            `yaml:"total_timeout" json:"total_timeout"`
	RequestTimeout                string            `yaml:"request_timeout,omitempty" json:"request_timeout,omitempty"`
}

// ProxySettings 是运行时归一化后的 /proxy 重试配置。
type ProxySettings struct {
	DomainRetries                 []DomainRetryRule `yaml:"domain_retries" json:"domain_retries"`
	MaxAttempts                   int               `json:"max_attempts" yaml:"max_attempts"`
	MaxRedirects                  int               `json:"max_redirects" yaml:"max_redirects"`
	TemplateFallbackAfterAttempts bool              `json:"template_fallback_after_attempts" yaml:"template_fallback_after_attempts"`
	ResponseHeaderTimeout         time.Duration     `json:"response_header_timeout" yaml:"response_header_timeout"`
	TotalTimeout                  time.Duration     `json:"total_timeout" yaml:"total_timeout"`
}

func DefaultProxySettings() ProxySettings {
	return ProxySettings{
		MaxAttempts:                   DefaultProxyMaxAttempts,
		MaxRedirects:                  DefaultProxyMaxRedirects,
		TemplateFallbackAfterAttempts: true,
		ResponseHeaderTimeout:         DefaultProxyResponseHeaderTimeout,
		TotalTimeout:                  DefaultProxyTotalTimeout,
	}
}

func ParseProxySettings(cfg *ProxySettingsConfig) (ProxySettings, error) {
	settings := DefaultProxySettings()
	if cfg == nil {
		return settings, nil
	}
	settings.DomainRetries = cfg.DomainRetries
	if cfg.MaxAttempts > 0 {
		settings.MaxAttempts = cfg.MaxAttempts
	}
	if cfg.MaxRedirects > 0 {
		settings.MaxRedirects = cfg.MaxRedirects
	}
	if cfg.TemplateFallbackAfterAttempts != nil {
		settings.TemplateFallbackAfterAttempts = *cfg.TemplateFallbackAfterAttempts
	}
	headerTimeoutText := strings.TrimSpace(cfg.ResponseHeaderTimeout)
	if headerTimeoutText != "" {
		timeout, err := time.ParseDuration(headerTimeoutText)
		if err != nil || timeout <= 0 {
			return settings, fmt.Errorf("proxy.response_header_timeout must be a positive duration")
		}
		settings.ResponseHeaderTimeout = timeout
	}
	totalTimeoutText := strings.TrimSpace(cfg.TotalTimeout)
	if totalTimeoutText == "" {
		totalTimeoutText = strings.TrimSpace(cfg.RequestTimeout)
	}
	if totalTimeoutText != "" {
		timeout, err := time.ParseDuration(totalTimeoutText)
		if err != nil || timeout <= 0 {
			return settings, fmt.Errorf("proxy.total_timeout must be a positive duration")
		}
		settings.TotalTimeout = timeout
	}
	return ValidateProxySettings(settings)
}

func ValidateProxySettings(settings ProxySettings) (ProxySettings, error) {
	rules, err := validateDomainRetryRules(settings.DomainRetries)
	if err != nil {
		return settings, err
	}
	settings.DomainRetries = rules
	if settings.MaxAttempts <= 0 {
		settings.MaxAttempts = DefaultProxyMaxAttempts
	}
	if settings.MaxAttempts > 50 {
		return settings, fmt.Errorf("proxy.max_attempts must be between 1 and 50")
	}
	if settings.MaxRedirects <= 0 {
		settings.MaxRedirects = DefaultProxyMaxRedirects
	}
	if settings.MaxRedirects > 50 {
		return settings, fmt.Errorf("proxy.max_redirects must be between 1 and 50")
	}
	if settings.ResponseHeaderTimeout <= 0 {
		settings.ResponseHeaderTimeout = DefaultProxyResponseHeaderTimeout
	}
	if settings.TotalTimeout <= 0 {
		settings.TotalTimeout = DefaultProxyTotalTimeout
	}
	return settings, nil
}

func (s *Server) ProxySettings() ProxySettings {
	if s == nil {
		return DefaultProxySettings()
	}
	s.proxySettingsMu.RLock()
	defer s.proxySettingsMu.RUnlock()
	settings := s.proxySettings
	if settings.MaxAttempts <= 0 {
		settings = DefaultProxySettings()
	}
	settings, err := ValidateProxySettings(settings)
	if err != nil {
		return DefaultProxySettings()
	}
	return settings
}

func (s *Server) UpdateProxySettings(settings ProxySettings) {
	if s == nil {
		return
	}
	settings, err := ValidateProxySettings(settings)
	if err != nil {
		return
	}
	s.proxySettingsMu.Lock()
	s.proxySettings = settings
	s.proxySettingsMu.Unlock()
}

func (s *Server) proxyResponseHeaderTimeout() time.Duration {
	return s.ProxySettings().ResponseHeaderTimeout
}

func (s *Server) proxyTotalTimeout() time.Duration {
	return s.ProxySettings().TotalTimeout
}

func (s *Server) proxyMaxRedirects() int {
	return s.ProxySettings().MaxRedirects
}

func (s *Server) applyProxyResponseHeaderTimeout(transport *http.Transport) {
	if transport == nil {
		return
	}
	transport.ResponseHeaderTimeout = s.proxyResponseHeaderTimeout()
}

func (s *Server) newRemoteChijieClient() (*http.Client, error) {
	transport, _, err := dialer.Transport(s.direct(), "remote:"+s.proxyResponseHeaderTimeout().String(), s.applyProxyResponseHeaderTimeout)
	if err != nil {
		return nil, err
	}
	return &http.Client{Transport: transport, Timeout: s.proxyTotalTimeout(), CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}, nil
}
