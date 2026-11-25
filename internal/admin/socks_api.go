package admin

import (
	"encoding/json"
	"net/http"
	"net/url"
	"strings"

	proxyserver "chijie/internal/server"
)

type socksAccountInput struct {
	Account  proxyserver.SocksAccount `json:"account"`
	Password string                   `json:"password,omitempty"`
}

func (s *Server) handleSOCKSAccounts(w http.ResponseWriter, r *http.Request) {
	if s.socksAccounts == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"error": "SOCKS account store unavailable"})
		return
	}
	switch r.Method {
	case http.MethodGet:
		writeJSON(w, http.StatusOK, map[string]any{"accounts": s.socksAccounts.List(), "advertise_address": s.socksAdvertise})
	case http.MethodPost:
		var input socksAccountInput
		if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]any{"error": "invalid request body"})
			return
		}
		password, err := s.socksAccounts.Create(input.Account, input.Password)
		if err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]any{"error": err.Error()})
			return
		}
		result := map[string]any{"account": input.Account, "password": password}
		if s.socksAdvertise != "" {
			result["connection"] = (&url.URL{Scheme: "socks5h", User: url.UserPassword(input.Account.Username, password), Host: s.socksAdvertise}).String()
		}
		writeJSON(w, http.StatusCreated, result)
	default:
		w.Header().Set("Allow", "GET, POST")
		writeJSON(w, http.StatusMethodNotAllowed, map[string]any{"error": "method not allowed"})
	}
}

func (s *Server) handleSOCKSAccount(w http.ResponseWriter, r *http.Request) {
	if s.socksAccounts == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"error": "SOCKS account store unavailable"})
		return
	}
	username, err := url.PathUnescape(strings.TrimPrefix(r.URL.Path, "/api/socks/accounts/"))
	if err != nil || username == "" || strings.Contains(username, "/") {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "invalid account name"})
		return
	}
	switch r.Method {
	case http.MethodPut:
		var input socksAccountInput
		if err := json.NewDecoder(r.Body).Decode(&input); err != nil || input.Account.Username != username {
			writeJSON(w, http.StatusBadRequest, map[string]any{"error": "invalid account body"})
			return
		}
		if err := s.socksAccounts.Update(input.Account, input.Password); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]any{"error": err.Error()})
			return
		}
		result := map[string]any{"account": input.Account}
		if input.Password != "" {
			result["password"] = input.Password
			if s.socksAdvertise != "" {
				result["connection"] = (&url.URL{Scheme: "socks5h", User: url.UserPassword(username, input.Password), Host: s.socksAdvertise}).String()
			}
		}
		writeJSON(w, http.StatusOK, result)
	case http.MethodDelete:
		if err := s.socksAccounts.Delete(username); err != nil {
			writeJSON(w, http.StatusNotFound, map[string]any{"error": "account not found"})
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"deleted": username})
	default:
		w.Header().Set("Allow", "PUT, DELETE")
		writeJSON(w, http.StatusMethodNotAllowed, map[string]any{"error": "method not allowed"})
	}
}
