package server

import (
	"fmt"
	"net/http"
	"net/url"
	"slices"
	"strings"

	"golang.org/x/net/idna"
)

// DomainRetryRule applies only to GET/HEAD requests for this exact hostname.
// MaxRetries counts additional attempts, including transport failures.
type DomainRetryRule struct {
	Domain          string `json:"domain" yaml:"domain"`
	Enabled         bool   `json:"enabled" yaml:"enabled"`
	MaxRetries      int    `json:"max_retries" yaml:"max_retries"`
	RetryOtherNodes bool   `json:"retry_other_nodes" yaml:"retry_other_nodes"`
	StatusCodes     []int  `json:"status_codes" yaml:"status_codes"`
}

func normalizeRetryDomain(domain string) (string, error) {
	host, err := idna.Lookup.ToASCII(strings.TrimSuffix(strings.ToLower(strings.TrimSpace(domain)), "."))
	if err != nil || host == "" || len(host) > 253 {
		return "", fmt.Errorf("domain must be a hostname, for example api.example.com")
	}
	for _, label := range strings.Split(host, ".") {
		if len(label) == 0 || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
			return "", fmt.Errorf("invalid domain hostname")
		}
		for _, c := range label {
			if !(c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '-') {
				return "", fmt.Errorf("domain must contain only a hostname, without scheme, port, path or wildcard")
			}
		}
	}
	return host, nil
}

func validateDomainRetryRules(input []DomainRetryRule) ([]DomainRetryRule, error) {
	if len(input) > 100 {
		return nil, fmt.Errorf("proxy.domain_retries supports at most 100 rules")
	}
	rules := make([]DomainRetryRule, 0, len(input))
	seen := make(map[string]bool)
	for i, rule := range input {
		domain, err := normalizeRetryDomain(rule.Domain)
		if err != nil {
			return nil, fmt.Errorf("domain retry rule %d: %w", i+1, err)
		}
		if seen[domain] {
			return nil, fmt.Errorf("duplicate domain retry rule: %s", domain)
		}
		seen[domain] = true
		rule.Domain = domain
		if rule.MaxRetries < 0 || rule.MaxRetries > 20 {
			return nil, fmt.Errorf("domain retry rule %s: max_retries must be between 0 and 20", domain)
		}
		if len(rule.StatusCodes) == 0 {
			return nil, fmt.Errorf("domain retry rule %s: select at least one retry status", domain)
		}
		rule.StatusCodes = slices.Clone(rule.StatusCodes)
		for _, code := range rule.StatusCodes {
			switch code {
			case 408, 425, 429, 500, 502, 503, 504:
			default:
				return nil, fmt.Errorf("domain retry rule %s: status %d is not retryable; use 408, 425, 429, 500, 502, 503 or 504", domain, code)
			}
		}
		slices.Sort(rule.StatusCodes)
		rule.StatusCodes = slices.Compact(rule.StatusCodes)
		rules = append(rules, rule)
	}
	return rules, nil
}

func domainRetryRule(req *ProxyRequest, settings ProxySettings) *DomainRetryRule {
	if req.Method != "" && req.Method != http.MethodGet && req.Method != http.MethodHead {
		return nil
	}
	u, err := url.Parse(req.URL)
	if err != nil {
		return nil
	}
	host, err := normalizeRetryDomain(u.Hostname())
	if err != nil {
		return nil
	}
	for _, rule := range settings.DomainRetries {
		if rule.Enabled && rule.Domain == host {
			return &rule
		}
	}
	return nil
}

// A rule for the original host must not cause retries of a different host
// reached through a redirect, even if that host has its own separate rule.
func retryResponseDomain(req *ProxyRequest, resp *proxyResponse, settings ProxySettings) bool {
	if resp.FinalURL == "" {
		return true
	}
	final := *req
	final.URL = resp.FinalURL
	before, after := domainRetryRule(req, settings), domainRetryRule(&final, settings)
	return before != nil && after != nil && before.Domain == after.Domain
}
