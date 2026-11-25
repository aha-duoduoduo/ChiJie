package admin

import (
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strings"
)

// SetTrustedProxies must be called before serving requests. Forwarded headers
// are only meaningful when the immediate peer is a configured, sanitizing proxy.
func (s *Server) SetTrustedProxies(cidrs []string) error {
	var networks []*net.IPNet
	for _, raw := range cidrs {
		_, network, err := net.ParseCIDR(strings.TrimSpace(raw))
		if err != nil {
			return fmt.Errorf("invalid trusted proxy CIDR %q: %w", raw, err)
		}
		networks = append(networks, network)
	}
	s.trustedProxies = networks
	return nil
}

func clientIPFromRequest(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

func (s *Server) trustedPeer(ip string) bool {
	for _, network := range s.trustedProxies {
		if network.Contains(net.ParseIP(ip)) {
			return true
		}
	}
	return false
}

func (s *Server) clientIP(r *http.Request) string {
	peer := clientIPFromRequest(r)
	if !s.trustedPeer(peer) {
		return peer
	}
	for _, header := range []string{"CF-Connecting-IP", "True-Client-IP", "X-Real-IP"} {
		if ip := net.ParseIP(strings.TrimSpace(r.Header.Get(header))); ip != nil {
			return ip.String()
		}
	}
	// Walk from the trusted edge, never accept a client-supplied leftmost value.
	chain := strings.Split(r.Header.Get("X-Forwarded-For"), ",")
	for i := len(chain) - 1; i >= 0; i-- {
		ip := net.ParseIP(strings.TrimSpace(chain[i]))
		if ip == nil {
			return peer
		}
		peer = ip.String()
		if !s.trustedPeer(peer) {
			break
		}
	}
	return peer
}

func (s *Server) browserRequestGuard(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if s.password == "" {
			host := r.Host
			if h, _, err := net.SplitHostPort(host); err == nil {
				host = h
			}
			ip := net.ParseIP(strings.Trim(host, "[]"))
			if !strings.EqualFold(host, "localhost") && (ip == nil || !ip.IsLoopback()) {
				http.Error(w, "untrusted admin host", http.StatusForbidden)
				return
			}
		}
		if strings.HasPrefix(r.URL.Path, "/api/") {
			if r.Header.Get("Sec-Fetch-Site") == "cross-site" {
				http.Error(w, "cross-site admin request", http.StatusForbidden)
				return
			}
			if raw := r.Header.Get("Origin"); raw != "" {
				origin, err := url.Parse(raw)
				if err != nil || (origin.Scheme != "http" && origin.Scheme != "https") || !strings.EqualFold(origin.Host, r.Host) {
					http.Error(w, "untrusted admin origin", http.StatusForbidden)
					return
				}
			}
		}
		next.ServeHTTP(w, r)
	})
}
