package server

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"chijie/internal/dialer"
	"chijie/internal/util"
	"golang.org/x/net/http/httpguts"
)

func (s *Server) doProxyWithRetry(ctx context.Context, req *ProxyRequest, route *egressRoute) (*proxyResponse, *egressRoute, error) {
	for name, value := range req.Headers {
		if !httpguts.ValidHeaderFieldName(name) || !httpguts.ValidHeaderFieldValue(value) {
			return nil, route, fmt.Errorf("invalid header: %q", name)
		}
	}
	settings := s.ProxySettings()
	ctx, cancel := context.WithTimeout(ctx, settings.TotalTimeout)
	defer cancel()
	rule := domainRetryRule(req, settings)
	attempts := domainAttemptRoutes(route, settings, rule)
	var attemptErrors []string
	var lastRoute *egressRoute
	var lastErr error
	for idx, attemptRoute := range attempts {
		if ctx.Err() != nil {
			lastErr = ctx.Err()
			break
		}
		lastRoute = attemptRoute
		attemptRoute.Attempts = idx + 1
		resp, err := s.doProxy(ctx, req, attemptRoute)
		if resp != nil {
			resp.Attempts = idx + 1
		}
		if err == nil {
			switch {
			case rule == nil:
				if resp.StatusCode >= 400 {
					attemptRoute.RetryOutcome = "no_matching_rule"
				}
			case !slices.Contains(rule.StatusCodes, resp.StatusCode):
				attemptRoute.RetryOutcome = "status_not_retryable"
				if resp.StatusCode < 400 {
					attemptRoute.RetryOutcome = "not_needed"
					if idx > 0 {
						attemptRoute.RetryOutcome = "recovered"
					}
				}
			case !retryResponseDomain(req, resp, settings):
				attemptRoute.RetryOutcome = "redirected_to_other_domain"
			case idx+1 == len(attempts):
				attemptRoute.RetryOutcome = exhaustedRetryOutcome(rule, idx+1)
			}
			if rule == nil || attemptRoute.RetryOutcome != "" {
				return resp, attemptRoute, nil
			}
			// The response body has already been consumed and closed by doProxy.
			// A target's 429/5xx says nothing about the node's global health.
			// Alternate exits are tried immediately. Retry-After never delays
			// gateway retries; preserve it only on the final upstream response.
			if !waitProxyRetry(ctx, domainRetryDelay(rule, idx)) {
				attemptRoute.RetryOutcome = interruptedRetryOutcome(ctx)
				return resp, attemptRoute, nil
			}
			util.Warnf("[egress] HTTP %d via %s; domain retry %d/%d", resp.StatusCode, routeAttemptName(attemptRoute), idx+1, rule.MaxRetries)
			continue
		}
		lastErr = err
		attemptErrors = append(attemptErrors, fmt.Sprintf("%s: %v", routeAttemptName(attemptRoute), err))
		var endpointErr *dialer.EndpointError
		if isRetryableProxyAttempt(ctx, err) && errors.As(err, &endpointErr) {
			s.markAttemptNodeUnavailable(attemptRoute)
		}
		if idx+1 >= len(attempts) || !isRetryableProxyAttempt(ctx, err) {
			break
		}
		if rule != nil && !waitProxyRetry(ctx, domainRetryDelay(rule, idx)) {
			attemptRoute.RetryOutcome = interruptedRetryOutcome(ctx)
			break
		}
		util.Warnf("[egress] attempt via %s failed, retrying: %v", routeAttemptName(attemptRoute), err)
	}
	if rule != nil && lastRoute != nil && lastRoute.RetryOutcome == "" {
		switch {
		case ctx.Err() != nil:
			lastRoute.RetryOutcome = interruptedRetryOutcome(ctx)
		case !isRetryableProxyAttempt(ctx, lastErr):
			lastRoute.RetryOutcome = "non_retryable_error"
		default:
			lastRoute.RetryOutcome = exhaustedRetryOutcome(rule, lastRoute.Attempts)
		}
	}
	if len(attemptErrors) > 1 {
		return nil, lastRoute, errors.New(strings.Join(attemptErrors, "; "))
	}
	if lastErr != nil {
		return nil, lastRoute, lastErr
	}
	return nil, lastRoute, fmt.Errorf("proxy request failed")
}

func domainAttemptRoutes(route *egressRoute, settings ProxySettings, rule *DomainRetryRule) []*egressRoute {
	if rule != nil {
		settings.MaxAttempts = rule.MaxRetries + 1
	}
	if rule != nil && rule.RetryOtherNodes && route != nil {
		clone := *route
		clone.Choices = nil
		seen := make(map[string]bool)
		for _, choice := range route.Choices {
			if choice == nil {
				continue
			}
			key := choice.PoolName + "\x00" + choice.NodeName
			if !seen[key] {
				clone.Choices = append(clone.Choices, choice)
				seen[key] = true
			}
		}
		route = &clone
	}
	attempts := proxyAttemptRoutes(route, settings)
	if rule == nil || len(attempts) == 0 {
		return attempts
	}
	limit := rule.MaxRetries + 1
	if !rule.RetryOtherNodes {
		first := attempts[0]
		attempts = make([]*egressRoute, 0, limit)
		for range limit {
			clone := *first
			attempts = append(attempts, &clone)
		}
		return attempts
	}
	// Each configured exit is used at most once. Keep the original selector's
	// region, premium/residential constraints and template fallback ordering.
	unique := make([]*egressRoute, 0, min(limit, len(attempts)))
	seen := make(map[string]bool)
	for _, attempt := range attempts {
		key := "direct"
		if c := attempt.Choice; c != nil {
			key = c.PoolName + "\x00" + c.NodeName
		}
		if seen[key] {
			continue
		}
		seen[key] = true
		unique = append(unique, attempt)
		if len(unique) == limit {
			break
		}
	}
	return unique
}

func domainRetryDelay(rule *DomainRetryRule, attempt int) time.Duration {
	if rule.RetryOtherNodes {
		return 0
	}
	return 250 * time.Millisecond * time.Duration(1<<min(attempt, 3))
}

func exhaustedRetryOutcome(rule *DomainRetryRule, attempts int) string {
	if attempts >= rule.MaxRetries+1 {
		return "retry_limit"
	}
	return "no_other_nodes"
}

func interruptedRetryOutcome(ctx context.Context) string {
	if errors.Is(ctx.Err(), context.Canceled) {
		return "request_cancelled"
	}
	return "total_timeout"
}

func waitProxyRetry(ctx context.Context, delay time.Duration) bool {
	if delay <= 0 {
		return ctx.Err() == nil
	}
	if deadline, ok := ctx.Deadline(); ok && time.Until(deadline) <= delay {
		return false
	}
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return ctx.Err() == nil
	}
}
