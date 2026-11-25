package pool

import (
	"fmt"
	"math/rand"
	"sort"
	"strings"
	"time"
)

// SelectEgress 按地区、策略和家宽要求选择出口。普通节点优先，模板节点作为兜底。
func (m *Manager) SelectEgress(region string, strategy string, residential bool) (*EgressChoice, error) {
	return m.SelectEgressFor(region, strategy, EgressSelector{Residential: residential})
}

// SelectEgressFor 按地区、策略、家宽类别和高端偏好选择出口。
func (m *Manager) SelectEgressFor(region string, strategy string, selector EgressSelector) (*EgressChoice, error) {
	choices, err := m.SelectEgressCandidatesFor(region, strategy, selector)
	if err != nil {
		return nil, err
	}
	return choices[0], nil
}

// SelectEgressCandidates 返回按策略排序后的出口候选。请求普通出口时，如果普通节点和模板都不可用，
// 会降级到同地区家宽节点或家宽模板。
func (m *Manager) SelectEgressCandidates(region string, strategy string, residential bool) ([]*EgressChoice, error) {
	return m.SelectEgressCandidatesFor(region, strategy, EgressSelector{Residential: residential})
}

// SelectEgressCandidatesFor 返回按策略、家宽类别和高端偏好排序后的出口候选。
func (m *Manager) SelectEgressCandidatesFor(region string, strategy string, selector EgressSelector) ([]*EgressChoice, error) {
	region = NormalizeRegionCode(region)
	if region == "" {
		return nil, fmt.Errorf("region must be a two-letter region code")
	}
	strategy = NormalizeStrategy(strategy)

	if choices := m.egressCandidateGroup(region, strategy, selector, false); len(choices) > 0 {
		choices = m.appendPremiumResidentialFallback(region, strategy, selector, choices, false)
		return choices, nil
	}

	if !selector.Residential {
		fallback := selector
		fallback.Residential = true
		if choices := m.egressCandidateGroup(region, strategy, fallback, false); len(choices) > 0 {
			return choices, nil
		}
	}

	return nil, noEgressChoicesError(region, selector)
}

// SelectEgressCandidatesWithTemplateFallback 返回显式地区出口候选，并在可用节点候选之后追加同类型模板。
// /proxy 会限制普通节点尝试数量；模板候选只在这些节点都失败后继续使用。
func (m *Manager) SelectEgressCandidatesWithTemplateFallback(region string, strategy string, residential bool) ([]*EgressChoice, error) {
	return m.SelectEgressCandidatesWithTemplateFallbackFor(region, strategy, EgressSelector{Residential: residential})
}

// SelectEgressCandidatesWithTemplateFallbackFor 返回显式地区出口候选，并在可用节点候选之后追加同类型模板。
func (m *Manager) SelectEgressCandidatesWithTemplateFallbackFor(region string, strategy string, selector EgressSelector) ([]*EgressChoice, error) {
	region = NormalizeRegionCode(region)
	if region == "" {
		return nil, fmt.Errorf("region must be a two-letter region code")
	}
	strategy = NormalizeStrategy(strategy)

	if choices := m.egressCandidateGroup(region, strategy, selector, true); len(choices) > 0 {
		choices = m.appendPremiumResidentialFallback(region, strategy, selector, choices, true)
		return choices, nil
	}

	if !selector.Residential {
		fallback := selector
		fallback.Residential = true
		if choices := m.egressCandidateGroup(region, strategy, fallback, true); len(choices) > 0 {
			return choices, nil
		}
	}

	return nil, noEgressChoicesError(region, selector)
}

func (m *Manager) egressCandidateGroup(region string, strategy string, selector EgressSelector, includeTemplatesAfterEntries bool) []*EgressChoice {
	group := EgressGroupFor(region, selector)
	entries := m.orderEgressChoices(m.entryChoices(region, selector), strategy, group)
	offlineEntries := m.orderEgressChoices(m.tryOfflineEntryChoices(region, selector), strategy, group)
	templates := m.templateChoices(region, selector)

	if selector.Premium {
		if choices := premiumCandidateGroup(entries, offlineEntries, templates, includeTemplatesAfterEntries); len(choices) > 0 {
			return choices
		}
	}

	if len(entries) > 0 {
		if includeTemplatesAfterEntries {
			return append(entries, templates...)
		}
		return entries
	}
	if len(offlineEntries) > 0 {
		if includeTemplatesAfterEntries {
			return append(offlineEntries, templates...)
		}
		return offlineEntries
	}
	return templates
}

func (m *Manager) appendPremiumResidentialFallback(region string, strategy string, selector EgressSelector, choices []*EgressChoice, includeTemplatesAfterEntries bool) []*EgressChoice {
	if !selector.Premium || selector.Residential || len(choices) == 0 {
		return choices
	}
	fallback := selector
	fallback.Residential = true
	fallbackChoices := m.egressCandidateGroup(region, strategy, fallback, includeTemplatesAfterEntries)
	if len(fallbackChoices) == 0 {
		return choices
	}
	return append(choices, fallbackChoices...)
}

// SelectAnyEgress 在不指定地区时选择一个非直连出口。maxLatency 为 0 时不限制延迟。
func (m *Manager) SelectAnyEgress(strategy string, residential bool, maxLatency time.Duration) (*EgressChoice, error) {
	return m.SelectAnyEgressFor(strategy, EgressSelector{Residential: residential}, maxLatency)
}

// SelectAnyEgressFor 在不指定地区时按家宽类别和高端偏好选择一个非直连出口。
func (m *Manager) SelectAnyEgressFor(strategy string, selector EgressSelector, maxLatency time.Duration) (*EgressChoice, error) {
	choices, err := m.SelectAnyEgressCandidatesFor(strategy, selector, maxLatency)
	if err != nil {
		return nil, err
	}
	return choices[0], nil
}

// SelectAnyEgressCandidates 在不指定地区时返回按策略排序后的非直连候选。maxLatency 为 0 时不限制延迟。
func (m *Manager) SelectAnyEgressCandidates(strategy string, residential bool, maxLatency time.Duration) ([]*EgressChoice, error) {
	return m.SelectAnyEgressCandidatesFor(strategy, EgressSelector{Residential: residential}, maxLatency)
}

// SelectAnyEgressCandidatesFor 在不指定地区时返回按策略、家宽类别和高端偏好排序后的非直连候选。
func (m *Manager) SelectAnyEgressCandidatesFor(strategy string, selector EgressSelector, maxLatency time.Duration) ([]*EgressChoice, error) {
	strategy = NormalizeStrategy(strategy)
	group := AnyEgressGroupFor(selector)
	if choices := prioritizePremiumChoices(m.orderEgressChoices(m.anyEntryChoices(selector, maxLatency), strategy, group), selector.Premium); len(choices) > 0 {
		return choices, nil
	}

	if maxLatency > 0 {
		return nil, fmt.Errorf("no %s nodes available under %dms", group, maxLatency.Milliseconds())
	}
	return nil, fmt.Errorf("no %s nodes available", group)
}

func (m *Manager) entryChoices(region string, selector EgressSelector) []*EgressChoice {
	m.mu.RLock()
	defer m.mu.RUnlock()

	choices := make([]*EgressChoice, 0)
	for poolName, pool := range m.pools {
		if pool.Config.Source != "static" && pool.Config.Source != "subscription" {
			continue
		}
		if !poolEnabled(pool.Config) {
			continue
		}

		pool.mu.RLock()
		for _, entry := range pool.Entries {
			if !entry.Enabled || !entry.Alive {
				continue
			}
			if entry.Region != region || !entryMatchesSelector(entry, selector) {
				continue
			}
			choices = append(choices, &EgressChoice{
				Dialer:      entry.Dialer,
				PoolName:    poolName,
				NodeName:    entry.Node.Name,
				Source:      pool.Config.Source,
				Region:      region,
				Group:       EgressGroupFor(region, selector),
				Residential: entry.Residential,
				Premium:     entry.Premium,
				Latency:     entry.Latency,
			})
		}
		pool.mu.RUnlock()
	}
	sort.Slice(choices, func(i, j int) bool {
		if choices[i].PoolName == choices[j].PoolName {
			return choices[i].NodeName < choices[j].NodeName
		}
		return choices[i].PoolName < choices[j].PoolName
	})
	return choices
}

func (m *Manager) tryOfflineEntryChoices(region string, selector EgressSelector) []*EgressChoice {
	m.mu.RLock()
	defer m.mu.RUnlock()

	var allMatching int
	var fallback *EgressChoice
	for poolName, pool := range m.pools {
		if pool.Config == nil || (pool.Config.Source != "static" && pool.Config.Source != "subscription") {
			continue
		}
		if !poolEnabled(pool.Config) {
			continue
		}

		pool.mu.RLock()
		for _, entry := range pool.Entries {
			if entry == nil || entry.Node == nil || !entry.Enabled {
				continue
			}
			if entry.Region != region || !entryMatchesSelector(entry, selector) {
				continue
			}
			allMatching++
			if pool.Config.Source == "subscription" && pool.Config.TryOffline && !entry.Alive {
				fallback = &EgressChoice{
					Dialer:      entry.Dialer,
					PoolName:    poolName,
					NodeName:    entry.Node.Name,
					Source:      pool.Config.Source,
					Region:      region,
					Group:       EgressGroupFor(region, selector),
					Residential: entry.Residential,
					Premium:     entry.Premium,
					Latency:     entry.Latency,
				}
			}
		}
		pool.mu.RUnlock()
	}
	if allMatching == 1 && fallback != nil {
		return []*EgressChoice{fallback}
	}
	return nil
}

func (m *Manager) anyEntryChoices(selector EgressSelector, maxLatency time.Duration) []*EgressChoice {
	m.mu.RLock()
	defer m.mu.RUnlock()

	group := AnyEgressGroupFor(selector)
	choices := make([]*EgressChoice, 0)
	for poolName, pool := range m.pools {
		if pool.Config == nil || (pool.Config.Source != "static" && pool.Config.Source != "subscription") {
			continue
		}
		if !poolEnabled(pool.Config) {
			continue
		}

		pool.mu.RLock()
		for _, entry := range pool.Entries {
			if !entry.Enabled || !entry.Alive {
				continue
			}
			if entry.Node != nil && strings.EqualFold(entry.Node.Type, "direct") {
				continue
			}
			if !entryMatchesSelector(entry, selector) {
				continue
			}
			if maxLatency > 0 && (entry.Latency <= 0 || entry.Latency > maxLatency) {
				continue
			}
			choices = append(choices, &EgressChoice{
				Dialer:      entry.Dialer,
				PoolName:    poolName,
				NodeName:    entry.Node.Name,
				Source:      pool.Config.Source,
				Region:      entry.Region,
				Group:       group,
				Residential: entry.Residential,
				Premium:     entry.Premium,
				Latency:     entry.Latency,
			})
		}
		pool.mu.RUnlock()
	}
	sort.Slice(choices, func(i, j int) bool {
		if choices[i].Latency != choices[j].Latency {
			if choices[i].Latency == 0 {
				return false
			}
			if choices[j].Latency == 0 {
				return true
			}
			return choices[i].Latency < choices[j].Latency
		}
		if choices[i].PoolName == choices[j].PoolName {
			return choices[i].NodeName < choices[j].NodeName
		}
		return choices[i].PoolName < choices[j].PoolName
	})
	return choices
}

func (m *Manager) orderEgressChoices(choices []*EgressChoice, strategy string, rrKey string) []*EgressChoice {
	if len(choices) == 0 {
		return nil
	}
	ordered := append([]*EgressChoice(nil), choices...)

	switch NormalizeStrategy(strategy) {
	case "round-robin":
		m.rrMu.Lock()
		idx := m.rrByGroup[rrKey] % len(ordered)
		m.rrByGroup[rrKey]++
		m.rrMu.Unlock()
		if idx > 0 {
			ordered = append(ordered[idx:], ordered[:idx]...)
		}
	case "least-latency":
		sort.SliceStable(ordered, func(i, j int) bool {
			if ordered[i].Latency == ordered[j].Latency {
				if ordered[i].PoolName == ordered[j].PoolName {
					return ordered[i].NodeName < ordered[j].NodeName
				}
				return ordered[i].PoolName < ordered[j].PoolName
			}
			if ordered[i].Latency == 0 {
				return false
			}
			if ordered[j].Latency == 0 {
				return true
			}
			return ordered[i].Latency < ordered[j].Latency
		})
	default:
		rand.Shuffle(len(ordered), func(i, j int) {
			ordered[i], ordered[j] = ordered[j], ordered[i]
		})
	}
	return ordered
}

func prioritizePremiumChoices(choices []*EgressChoice, prefer bool) []*EgressChoice {
	if !prefer || len(choices) == 0 {
		return choices
	}
	premium := make([]*EgressChoice, 0, len(choices))
	ordinary := make([]*EgressChoice, 0, len(choices))
	for _, choice := range choices {
		if choice != nil && choice.Premium {
			premium = append(premium, choice)
		} else {
			ordinary = append(ordinary, choice)
		}
	}
	if len(premium) == 0 {
		return choices
	}
	return append(premium, ordinary...)
}

func premiumCandidateGroup(entries []*EgressChoice, offlineEntries []*EgressChoice, templates []*EgressChoice, includeTemplateFallback bool) []*EgressChoice {
	premiumEntries := filterChoicesByPremium(entries, true)
	premiumOfflineEntries := filterChoicesByPremium(offlineEntries, true)
	premiumTemplates := filterChoicesByPremium(templates, true)
	if len(premiumEntries)+len(premiumOfflineEntries)+len(premiumTemplates) == 0 {
		return nil
	}

	choices := make([]*EgressChoice, 0, len(entries)+len(offlineEntries)+len(templates))
	switch {
	case len(premiumEntries) > 0:
		choices = append(choices, premiumEntries...)
	case len(premiumOfflineEntries) > 0:
		choices = append(choices, premiumOfflineEntries...)
	default:
		choices = append(choices, premiumTemplates...)
	}
	if len(premiumEntries) > 0 || len(premiumOfflineEntries) > 0 {
		choices = append(choices, premiumTemplates...)
	}
	choices = append(choices, filterChoicesByPremium(entries, false)...)
	choices = append(choices, filterChoicesByPremium(offlineEntries, false)...)
	if includeTemplateFallback {
		choices = append(choices, filterChoicesByPremium(templates, false)...)
	}
	return choices
}

func filterChoicesByPremium(choices []*EgressChoice, premium bool) []*EgressChoice {
	filtered := make([]*EgressChoice, 0, len(choices))
	for _, choice := range choices {
		if choice != nil && choice.Premium == premium {
			filtered = append(filtered, choice)
		}
	}
	return filtered
}
