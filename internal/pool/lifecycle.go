package pool

import (
	"chijie/internal/dialer"
	"fmt"
)

func (p *Pool) closeExcept(retained map[dialer.Dialer]bool) {
	p.mu.Lock()
	p.retired = true
	var exits []dialer.Dialer
	for _, entry := range p.Entries {
		if !retained[entry.Dialer] {
			exits = append(exits, entry.Dialer)
		}
	}
	for _, exit := range p.templateDialers {
		if !retained[exit] {
			exits = append(exits, exit)
		}
	}
	p.templateDialers = nil
	p.mu.Unlock()
	for _, exit := range exits {
		_ = dialer.Close(exit)
	}
}

func closePoolsExcept(old, current map[string]*Pool) {
	retained := make(map[dialer.Dialer]bool)
	for _, p := range current {
		p.mu.RLock()
		for _, entry := range p.Entries {
			retained[entry.Dialer] = true
		}
		for _, exit := range p.templateDialers {
			retained[exit] = true
		}
		p.mu.RUnlock()
	}
	for name, p := range old {
		if current[name] != p {
			p.closeExcept(retained)
		}
	}
}

func (m *Manager) Close() {
	m.StopSubscriptionUpdater()
	m.reloadMu.Lock()
	defer m.reloadMu.Unlock()
	m.mu.Lock()
	old := m.pools
	m.pools = make(map[string]*Pool)
	m.mu.Unlock()
	closePoolsExcept(old, nil)
}

func (m *Manager) templateDialer(p *Pool, region string) (dialer.Dialer, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.retired {
		return nil, fmt.Errorf("pool is retired")
	}
	if d := p.templateDialers[region]; d != nil {
		return d, nil
	}
	d, err := m.buildTemplateDialer(p.Name, p.Config, region)
	if err != nil {
		return nil, err
	}
	if p.templateDialers == nil {
		p.templateDialers = make(map[string]dialer.Dialer)
	}
	if len(p.templateDialers) >= 128 {
		for key, old := range p.templateDialers {
			delete(p.templateDialers, key)
			_ = dialer.Close(old)
			break
		}
	}
	p.templateDialers[region] = d
	return d, nil
}
