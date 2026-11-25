package server

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"chijie/internal/dialer"
	"chijie/internal/fingerprint"
	"chijie/internal/pool"
	"chijie/internal/util"
)

type proxyAttemptError struct {
	err error
}

func (e *proxyAttemptError) Error() string {
	return e.err.Error()
}

func (e *proxyAttemptError) Unwrap() error {
	return e.err
}

// ProxyRequest 客户端发来的代理请求
type ProxyRequest struct {
	URL             string            `json:"url"`
	Method          string            `json:"method"`
	Headers         map[string]string `json:"headers"`
	Payload         string            `json:"payload"`
	FollowRedirects bool              `json:"follow_redirects"`
	Egress          EgressOptions     `json:"egress"`
	Hop             int               `json:"-"`
}

type proxyResponse struct {
	Body                 []byte
	ContentType          string
	Location             string
	SetCookies           []string
	StatusCode           int
	RetryAfter           string
	Attempts             int
	FinalURL             string
	Redirects            []proxyRedirect
	RedirectMax          int
	RedirectDetails      bool
	RedirectLimitReached bool
}

func proxyAttemptRoutes(route *egressRoute, settings ProxySettings) []*egressRoute {
	if route == nil {
		return nil
	}
	if len(route.Choices) == 0 {
		return []*egressRoute{route}
	}
	settings, err := ValidateProxySettings(settings)
	if err != nil {
		settings = DefaultProxySettings()
	}
	hasNodeChoice := false
	for _, choice := range route.Choices {
		if choice != nil && !choice.Template {
			hasNodeChoice = true
			break
		}
	}

	routes := make([]*egressRoute, 0, len(route.Choices))
	nodeAttempts := 0
	for _, choice := range route.Choices {
		if choice == nil {
			continue
		}
		if choice.Template {
			if hasNodeChoice && !settings.TemplateFallbackAfterAttempts && !(route.Premium && choice.Premium) {
				continue
			}
		} else {
			if nodeAttempts >= settings.MaxAttempts {
				continue
			}
			nodeAttempts++
		}
		attemptRoute := *route
		attemptRoute.Choice = choice
		attemptRoute.Region = choice.Region
		attemptRoute.Group = choice.Group
		attemptRoute.Residential = choice.Residential
		routes = append(routes, &attemptRoute)
	}
	return routes
}

func (s *Server) markAttemptNodeUnavailable(route *egressRoute) {
	if s == nil || s.poolManager == nil || route == nil || route.Choice == nil || route.Choice.Template {
		return
	}
	if ok := s.poolManager.MarkNodeUnavailable(route.Choice.PoolName, route.Choice.NodeName); ok {
		util.Warnf("[egress] marked %s unavailable after proxy attempt failure", routeAttemptName(route))
	}
}

func routeAttemptName(route *egressRoute) string {
	if route == nil {
		return "unknown"
	}
	if route.Choice != nil {
		if route.Choice.NodeName != "" {
			return route.Choice.NodeName
		}
		if route.Choice.PoolName != "" {
			return route.Choice.PoolName
		}
	}
	return route.Group
}

func isRetryableProxyAttempt(ctx context.Context, err error) bool {
	if ctx != nil && ctx.Err() != nil {
		return false
	}
	if isProxyRedirectError(err) {
		return false
	}
	var attemptErr *proxyAttemptError
	return errors.As(err, &attemptErr)
}

func (s *Server) doProxy(ctx context.Context, req *ProxyRequest, route *egressRoute) (*proxyResponse, error) {
	if isChijieTemplateRoute(route) {
		return s.doRemoteChijieProxy(ctx, req, route)
	}

	d, err := s.getDialer(route)
	if err != nil {
		return nil, fmt.Errorf("get dialer: %w", err)
	}

	var bodyReader io.Reader
	if req.Payload != "" && req.Method != "GET" && req.Method != "HEAD" {
		bodyReader = strings.NewReader(req.Payload)
	}

	httpReq, err := http.NewRequestWithContext(ctx, req.Method, req.URL, bodyReader)
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}

	for k, v := range req.Headers {
		httpReq.Header.Set(k, v)
	}

	var transport *http.Transport
	if route.TLSFingerprint == "" {
		var release func()
		transport, release, err = dialer.Transport(d, s.proxyResponseHeaderTimeout().String(), s.applyProxyResponseHeaderTimeout)
		if err != nil {
			return nil, err
		}
		defer release()
	} else {
		transport = d.GetHTTPTransport()
		s.applyProxyResponseHeaderTimeout(transport)
		defer transport.CloseIdleConnections()
	}
	if route.TLSFingerprint != "" && s.fpManager != nil {
		fpConfig, err := s.fpManager.ConfigFromValue(route.TLSFingerprint)
		if err != nil {
			return nil, fmt.Errorf("build tls fingerprint: %w", err)
		}
		helloID, spec, err := fingerprint.BuildSpecFromConfig(fpConfig)
		if err != nil {
			return nil, fmt.Errorf("build tls fingerprint: %w", err)
		}
		fpConfig.ApplyRequestDefaults(httpReq)
		if fpConfig.WantsHTTP2() {
			transport = nil
		} else {
			fingerprint.WrapTransportWithDialContext(transport, helloID, spec, "", d.DialContext)
		}

		if fpConfig.WantsHTTP2() {
			client := &http.Client{
				Transport: fingerprint.NewHTTP2RoundTripperWithResponseLimitAndHeaderTimeout(helloID, spec, "", d.DialContext, fpConfig, MaxProxyResponseBytes, s.proxyResponseHeaderTimeout()),
				Timeout:   s.proxyTotalTimeout(),
			}
			redirects := s.newProxyRedirectTracker(req)
			client.CheckRedirect = redirects.CheckRedirect

			startTime := time.Now()
			resp, err := client.Do(httpReq)
			elapsed := time.Since(startTime)
			if err != nil {
				return nil, &proxyAttemptError{err: fmt.Errorf("do request via %s: %w", d.Name(), err)}
			}
			defer resp.Body.Close()

			proxyResp, err := buildProxyResponse(resp)
			if err != nil {
				return nil, fmt.Errorf("read response: %w", err)
			}
			redirects.Apply(proxyResp, resp)

			util.Debugf("[egress] %dms via %s → %d (%d bytes)", elapsed.Milliseconds(), d.Name(), resp.StatusCode, len(proxyResp.Body))
			return proxyResp, nil
		}
	}

	client := &http.Client{
		Transport: transport,
		Timeout:   s.proxyTotalTimeout(),
	}
	redirects := s.newProxyRedirectTracker(req)
	client.CheckRedirect = redirects.CheckRedirect

	startTime := time.Now()
	resp, err := client.Do(httpReq)
	elapsed := time.Since(startTime)
	if err != nil {
		return nil, &proxyAttemptError{err: fmt.Errorf("do request via %s: %w", d.Name(), err)}
	}
	defer resp.Body.Close()

	proxyResp, err := buildProxyResponse(resp)
	if err != nil {
		return nil, fmt.Errorf("read response: %w", err)
	}
	redirects.Apply(proxyResp, resp)

	util.Debugf("[egress] %dms via %s → %d (%d bytes)", elapsed.Milliseconds(), d.Name(), resp.StatusCode, len(proxyResp.Body))
	return proxyResp, nil
}

func isChijieTemplateRoute(route *egressRoute) bool {
	return route != nil && route.Choice != nil && route.Choice.Template && pool.NormalizeTemplateType(route.Choice.TemplateType) == "chijie"
}

func (s *Server) doRemoteChijieProxy(ctx context.Context, req *ProxyRequest, route *egressRoute) (*proxyResponse, error) {
	if req == nil || route == nil || route.Choice == nil {
		return nil, fmt.Errorf("remote chijie route is incomplete")
	}
	if strings.TrimSpace(route.Choice.BearerToken) == "" {
		return nil, &proxyAttemptError{err: fmt.Errorf("remote chijie %s has no bearer token", routeAttemptName(route))}
	}
	proxyURL, err := pool.ChijieProxyURL(route.Choice.Endpoint, 0)
	if err != nil {
		return nil, &proxyAttemptError{err: fmt.Errorf("remote chijie %s endpoint: %w", routeAttemptName(route), err)}
	}

	forwardReq := *req
	if route.Region != "" {
		forwardReq.Egress.Region = route.Region
	}
	if route.Strategy != "" {
		forwardReq.Egress.Strategy = route.Strategy
	}
	forwardReq.Egress.Residential = route.Residential || (route.Choice != nil && route.Choice.Residential)
	forwardReq.Egress.Premium = route.Premium || (route.Choice != nil && route.Choice.Premium)
	if route.TLSFingerprint != "" {
		forwardReq.Egress.TLSFingerprint = route.TLSFingerprint
	}
	body, err := json.Marshal(&forwardReq)
	if err != nil {
		return nil, fmt.Errorf("marshal remote chijie request: %w", err)
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, proxyURL, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("create remote chijie request: %w", err)
	}
	httpReq.Header.Set("Authorization", "Bearer "+strings.TrimSpace(route.Choice.BearerToken))
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Accept", "*/*")
	httpReq.Header.Set(chijieHopHeader, strconv.Itoa(req.Hop+1))

	client := s.remoteChijieClient
	if client == nil {
		client, err = s.newRemoteChijieClient()
		if err != nil {
			return nil, err
		}
	}

	startTime := time.Now()
	resp, err := client.Do(httpReq)
	elapsed := time.Since(startTime)
	if err != nil {
		return nil, &proxyAttemptError{err: fmt.Errorf("do request via remote chijie %s: %w", routeAttemptName(route), err)}
	}
	defer resp.Body.Close()

	proxyResp, err := buildProxyResponse(resp)
	if err != nil {
		return nil, fmt.Errorf("read remote chijie response: %w", err)
	}
	applyRedirectHeadersFromRemote(proxyResp, resp.Header)
	if resp.Header.Get(chijieErrorHeader) != "" {
		detail := strings.TrimSpace(string(proxyResp.Body))
		if len(detail) > 240 {
			detail = detail[:240] + "..."
		}
		return nil, &proxyAttemptError{err: fmt.Errorf("remote chijie %s returned gateway error %d: %s", routeAttemptName(route), resp.StatusCode, detail)}
	}

	util.Debugf("[egress] %dms via remote chijie %s → %d (%d bytes)", elapsed.Milliseconds(), routeAttemptName(route), resp.StatusCode, len(proxyResp.Body))
	return proxyResp, nil
}

func buildProxyResponse(resp *http.Response) (*proxyResponse, error) {
	respBody, err := readProxyResponseBody(resp, MaxProxyResponseBytes)
	if err != nil {
		return nil, err
	}
	return &proxyResponse{
		Body:        respBody,
		ContentType: resp.Header.Get("Content-Type"),
		Location:    resp.Header.Get("Location"),
		SetCookies:  append([]string(nil), resp.Header.Values("Set-Cookie")...),
		StatusCode:  resp.StatusCode,
		RetryAfter:  resp.Header.Get("Retry-After"),
	}, nil
}

func readProxyResponseBody(resp *http.Response, limit int64) ([]byte, error) {
	if resp == nil || resp.Body == nil {
		return nil, nil
	}
	if limit <= 0 {
		return io.ReadAll(resp.Body)
	}
	if resp.ContentLength > limit {
		return nil, errProxyResponseTooLarge
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(body)) > limit {
		return nil, errProxyResponseTooLarge
	}
	return body, nil
}
