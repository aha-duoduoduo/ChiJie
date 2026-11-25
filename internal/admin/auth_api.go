package admin

import (
	"crypto/subtle"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"chijie/internal/auth"

	"github.com/golang-jwt/jwt/v5"
)

// LoginRequest 登录请求
type LoginRequest struct {
	Password string `json:"password"`
}

// LoginResponse 登录响应
type LoginResponse struct {
	Token     string `json:"token"`
	ExpiresIn int64  `json:"expires_in"`
}

// ProxyTokenRequest 代理调用 token 生成请求
type ProxyTokenRequest struct {
	Name     string `json:"name"`
	Duration string `json:"duration"`
}

// ProxyTokenResponse 代理调用 token 生成响应
type ProxyTokenResponse struct {
	ID        string `json:"id,omitempty"`
	Token     string `json:"token"`
	ExpiresIn int64  `json:"expires_in"`
	ExpiresAt string `json:"expires_at"`
	Name      string `json:"name"`
}

// handleLogin 处理登录请求
func (s *Server) handleLogin(w http.ResponseWriter, r *http.Request) {
	if r.Method != "POST" {
		http.Error(w, `{"error":"method not allowed"}`, http.StatusMethodNotAllowed)
		return
	}

	clientIP := s.clientIP(r)
	if s.loginLimiter != nil {
		if allowed, retryAfter := s.loginLimiter.allow(clientIP); !allowed {
			seconds := int(retryAfter.Seconds()) + 1
			w.Header().Set("Retry-After", strconv.Itoa(seconds))
			writeJSON(w, http.StatusTooManyRequests, map[string]any{
				"error":       "too many failed attempts, try again later",
				"retry_after": seconds,
			})
			return
		}
	}

	var req LoginRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{
			"error": "invalid request body",
		})
		return
	}

	// 如果未配置密码，拒绝登录
	if s.password == "" {
		writeJSON(w, http.StatusForbidden, map[string]any{
			"error": "authentication not configured",
		})
		return
	}

	// 常量时间比较，防止时序侧信道
	if subtle.ConstantTimeCompare([]byte(req.Password), []byte(s.password)) != 1 {
		if s.loginLimiter != nil {
			s.loginLimiter.recordFailure(clientIP)
		}
		writeJSON(w, http.StatusUnauthorized, map[string]any{
			"error": "invalid password",
		})
		return
	}

	if s.loginLimiter != nil {
		s.loginLimiter.recordSuccess(clientIP)
	}

	// 生成 JWT token
	now := time.Now()
	claims := &auth.Claims{
		Admin: true,
		RegisteredClaims: jwt.RegisteredClaims{
			ExpiresAt: jwt.NewNumericDate(now.Add(s.jwtExpire)),
			IssuedAt:  jwt.NewNumericDate(now),
		},
	}

	token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	tokenString, err := token.SignedString([]byte(s.jwtSecret))
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{
			"error": "failed to generate token",
		})
		return
	}

	writeJSON(w, http.StatusOK, LoginResponse{
		Token:     tokenString,
		ExpiresIn: int64(s.jwtExpire.Seconds()),
	})
}

// handleProxyToken manages persistent proxy keys. Legacy signed JWTs remain valid until expiry.
func (s *Server) handleProxyToken(w http.ResponseWriter, r *http.Request) {
	if s.accessKeys == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"error": "access key store unavailable"})
		return
	}
	if r.Method == http.MethodGet {
		keys, err := s.accessKeys.List()
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]any{"error": "failed to read access keys"})
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"keys": keys})
		return
	}
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", "GET, POST")
		writeJSON(w, http.StatusMethodNotAllowed, map[string]any{"error": "method not allowed"})
		return
	}

	var req ProxyTokenRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{
			"error": "invalid request body",
		})
		return
	}

	var duration time.Duration
	if strings.EqualFold(strings.TrimSpace(req.Duration), "never") {
		duration = 0
	} else {
		var err error
		duration, err = parseTokenDuration(req.Duration, s.jwtExpire)
		if err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]any{"error": err.Error()})
			return
		}
	}
	key, err := s.accessKeys.Create(req.Name, duration)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
		return
	}
	result := ProxyTokenResponse{ID: key.ID, Token: key.Token, ExpiresIn: int64(duration.Seconds()), Name: key.Name}
	if key.ExpiresAt != nil {
		result.ExpiresAt = key.ExpiresAt.Format(time.RFC3339)
	}
	writeJSON(w, http.StatusCreated, result)
}

func (s *Server) handleProxyTokenByID(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodDelete {
		w.Header().Set("Allow", "DELETE")
		writeJSON(w, http.StatusMethodNotAllowed, map[string]any{"error": "method not allowed"})
		return
	}
	if s.accessKeys == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"error": "access key store unavailable"})
		return
	}
	id := strings.TrimPrefix(r.URL.Path, "/api/auth/proxy-token/")
	if id == "" || strings.Contains(id, "/") {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "invalid key id"})
		return
	}
	if err := s.accessKeys.Revoke(id); err != nil {
		writeJSON(w, http.StatusNotFound, map[string]any{"error": "key not found"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"revoked": id})
}

func parseTokenDuration(raw string, fallback time.Duration) (time.Duration, error) {
	value := strings.TrimSpace(strings.ToLower(raw))
	if value == "" {
		return fallback, nil
	}
	var duration time.Duration
	var err error
	if strings.HasSuffix(value, "d") {
		days, parseErr := strconv.Atoi(strings.TrimSuffix(value, "d"))
		if parseErr != nil || days < 1 || days > 365 {
			return 0, fmt.Errorf("invalid token duration")
		}
		duration = time.Duration(days) * 24 * time.Hour
	} else {
		duration, err = time.ParseDuration(value)
		if err != nil {
			return 0, fmt.Errorf("invalid token duration")
		}
	}
	if duration <= 0 {
		return 0, fmt.Errorf("token duration must be positive")
	}
	if duration > 365*24*time.Hour {
		return 0, fmt.Errorf("token duration must be 365d or less")
	}
	return duration, nil
}

// authMiddleware JWT 认证中间件
func (s *Server) authMiddleware(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		// 如果未配置密码，直接放行
		if s.password == "" {
			next(w, r)
			return
		}

		// 提取 Authorization header
		authHeader := r.Header.Get("Authorization")
		if authHeader == "" {
			writeJSON(w, http.StatusUnauthorized, map[string]any{
				"error": "missing authorization header",
			})
			return
		}

		// 验证 Bearer token
		parts := strings.Split(authHeader, " ")
		if len(parts) != 2 || parts[0] != "Bearer" {
			writeJSON(w, http.StatusUnauthorized, map[string]any{
				"error": "invalid authorization header format",
			})
			return
		}

		tokenString := parts[1]

		// 解析 JWT
		token, err := jwt.ParseWithClaims(tokenString, &auth.Claims{}, func(token *jwt.Token) (interface{}, error) {
			// 验证签名方法
			if _, ok := token.Method.(*jwt.SigningMethodHMAC); !ok {
				return nil, fmt.Errorf("unexpected signing method: %v", token.Header["alg"])
			}
			return []byte(s.jwtSecret), nil
		})

		if err != nil {
			writeJSON(w, http.StatusUnauthorized, map[string]any{
				"error": "invalid token: " + err.Error(),
			})
			return
		}

		// 验证 claims
		if claims, ok := token.Claims.(*auth.Claims); ok && token.Valid {
			if !claims.Admin {
				writeJSON(w, http.StatusForbidden, map[string]any{
					"error": "insufficient permissions",
				})
				return
			}
			// 验证通过，继续处理
			next(w, r)
		} else {
			writeJSON(w, http.StatusUnauthorized, map[string]any{
				"error": "invalid token claims",
			})
		}
	}
}
