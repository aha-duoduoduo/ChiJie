package server

import (
	"context"
	"fmt"
	"net/url"
	"strings"
	"time"

	"chijie/internal/dialer"
	"chijie/internal/netguard"
	"chijie/internal/pool"
	"chijie/pkg/relay"
)

// EgressOptions 由调用方直接声明出口需求。
type EgressOptions = relay.EgressOptions

type egressRoute struct {
	Attempts       int
	RetryOutcome   string
	Direct         bool
	Any            bool
	Region         string
	Group          string
	Strategy       string
	Residential    bool
	Premium        bool
	TLSFingerprint string
	MaxLatencyMS   int
	Choice         *pool.EgressChoice
	Choices        []*pool.EgressChoice
}

func (s *Server) getDialer(route *egressRoute) (dialer.Dialer, error) {
	if route.Direct {
		base := s.direct()
		if s.allowPrivateTargets {
			return base, nil
		}
		return &guardedDialer{Dialer: base, allowPrivate: false}, nil
	}
	if route.Choice == nil || route.Choice.Dialer == nil {
		return nil, fmt.Errorf("no egress choice")
	}
	if dialer.IsDirect(route.Choice.Dialer) && !s.allowPrivateTargets {
		return &guardedDialer{Dialer: route.Choice.Dialer}, nil
	}
	return route.Choice.Dialer, nil
}

// validateTargetURL 校验调用方声明的目标 URL 协议合法且解析后的主机不命中私网黑名单。
// allow_private_targets=true 时跳过 IP 校验。
func (s *Server) validateTargetURL(ctx context.Context, rawURL string) error {
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return fmt.Errorf("parse url: %w", err)
	}
	switch strings.ToLower(parsed.Scheme) {
	case "http", "https", "ws", "wss":
	default:
		return fmt.Errorf("unsupported scheme: %s", parsed.Scheme)
	}
	if parsed.Host == "" {
		return fmt.Errorf("url has no host")
	}
	if s.allowPrivateTargets {
		return nil
	}
	host := parsed.Hostname()
	if err := netguard.CheckHost(ctx, host); err != nil {
		return err
	}
	return nil
}

func (s *Server) resolveEgress(options EgressOptions) (*egressRoute, error) {
	strategy := pool.NormalizeStrategy(options.Strategy)
	selector := pool.EgressSelector{Residential: options.Residential, Premium: options.Premium}
	fingerprintValue := strings.TrimSpace(options.TLSFingerprint)
	region := strings.TrimSpace(options.Region)
	maxLatencyMS := options.MaxLatencyMS
	if maxLatencyMS < 0 {
		maxLatencyMS = 0
	}

	if region == "" {
		if options.Any || options.Premium {
			choices, routeSelector, err := s.selectAnyEgressCandidates(strategy, selector, time.Duration(maxLatencyMS)*time.Millisecond)
			if err != nil {
				return nil, err
			}
			choice := choices[0]
			return &egressRoute{
				Any:            true,
				Region:         choice.Region,
				Group:          pool.AnyEgressGroupFor(routeSelector),
				Strategy:       strategy,
				Residential:    routeSelector.Residential,
				Premium:        routeSelector.Premium,
				TLSFingerprint: fingerprintValue,
				MaxLatencyMS:   maxLatencyMS,
				Choice:         choice,
				Choices:        choices,
			}, nil
		}
		return &egressRoute{
			Direct:         true,
			Group:          "DIRECT",
			Strategy:       strategy,
			TLSFingerprint: fingerprintValue,
		}, nil
	}

	if isAnyRegion(region) {
		choices, routeSelector, err := s.selectAnyEgressCandidates(strategy, selector, time.Duration(maxLatencyMS)*time.Millisecond)
		if err != nil {
			return nil, err
		}
		choice := choices[0]
		return &egressRoute{
			Any:            true,
			Region:         choice.Region,
			Group:          pool.AnyEgressGroupFor(routeSelector),
			Strategy:       strategy,
			Residential:    routeSelector.Residential,
			Premium:        routeSelector.Premium,
			TLSFingerprint: fingerprintValue,
			MaxLatencyMS:   maxLatencyMS,
			Choice:         choice,
			Choices:        choices,
		}, nil
	}

	region = pool.NormalizeRegionCode(region)
	if region == "" {
		return nil, errInvalidRegion
	}

	settings := s.ProxySettings()
	var choices []*pool.EgressChoice
	var err error
	if settings.TemplateFallbackAfterAttempts {
		choices, err = s.poolManager.SelectEgressCandidatesWithTemplateFallbackFor(region, strategy, selector)
	} else {
		choices, err = s.poolManager.SelectEgressCandidatesFor(region, strategy, selector)
	}
	if err != nil {
		return nil, err
	}
	choice := choices[0]
	routeResidential := choice.Residential
	routePremium := selector.Premium
	routeGroup := choice.Group
	if routeGroup == "" {
		routeGroup = pool.EgressGroupFor(region, pool.EgressSelector{Residential: routeResidential})
	}

	return &egressRoute{
		Region:         region,
		Group:          routeGroup,
		Strategy:       strategy,
		Residential:    routeResidential,
		Premium:        routePremium,
		TLSFingerprint: fingerprintValue,
		Choice:         choice,
		Choices:        choices,
	}, nil
}

func (s *Server) selectAnyEgressCandidates(strategy string, selector pool.EgressSelector, maxLatency time.Duration) ([]*pool.EgressChoice, pool.EgressSelector, error) {
	choices, err := s.poolManager.SelectAnyEgressCandidatesFor(strategy, selector, maxLatency)
	if err == nil {
		if selector.Premium && !selector.Residential {
			fallback := selector
			fallback.Residential = true
			if fallbackChoices, fallbackErr := s.poolManager.SelectAnyEgressCandidatesFor(strategy, fallback, maxLatency); fallbackErr == nil {
				choices = append(choices, fallbackChoices...)
			}
		}
		return choices, selector, nil
	}
	if !selector.Residential {
		fallback := selector
		fallback.Residential = true
		if fallbackChoices, fallbackErr := s.poolManager.SelectAnyEgressCandidatesFor(strategy, fallback, maxLatency); fallbackErr == nil {
			return fallbackChoices, fallback, nil
		}
	}
	return nil, selector, err
}

func isAnyRegion(region string) bool {
	switch strings.ToUpper(strings.TrimSpace(region)) {
	case "*", "ANY", "AUTO":
		return true
	default:
		return false
	}
}

func (s *Server) direct() *dialer.Managed {
	s.directOnce.Do(func() { s.directDialer = dialer.Manage(dialer.NewDirectDialer()) })
	return s.directDialer
}
