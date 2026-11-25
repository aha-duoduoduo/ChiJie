package server

import (
	"container/list"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"net"
	"net/url"
	"strconv"
	"strings"
	"time"

	"chijie/internal/dialer"
	"chijie/internal/dnsresolver"
	"chijie/internal/netguard"
	"chijie/internal/pool"
	"chijie/pkg/relay"

	M "github.com/sagernet/sing/common/metadata"
)

type relayAffinity struct {
	choice  string
	until   time.Time
	element *list.Element
}

type packetSession interface {
	Send([]byte) error
	Receive() ([]byte, error)
	Close() error
}

type localPacketSession struct {
	conn net.PacketConn
	addr net.Addr
}

func (p *localPacketSession) Send(data []byte) error {
	_, err := p.conn.WriteTo(data, p.addr)
	return err
}
func (p *localPacketSession) Receive() ([]byte, error) {
	buf := make([]byte, relay.MaxDatagram)
	n, _, err := p.conn.ReadFrom(buf)
	return buf[:n], err
}
func (p *localPacketSession) Close() error { return p.conn.Close() }

func validateRelayEgress(value EgressOptions) error {
	if value.Gateway != "" && (value.Pool != "" || value.Node != "") {
		return fmt.Errorf("gateway and node cannot be combined")
	}
	if (value.Pool == "") != (value.Node == "") {
		return fmt.Errorf("pool and node must be set together")
	}
	if value.Direct && (value.Region != "" || value.Any || value.Pool != "") {
		return fmt.Errorf("direct cannot be combined with a region or node")
	}
	if len(value.Gateway) > 128 || len(value.Pool) > 128 || len(value.Node) > 128 {
		return fmt.Errorf("egress name too long")
	}
	if value.MaxLatencyMS < 0 {
		return fmt.Errorf("max_latency_ms must be nonnegative")
	}
	return nil
}

func validateRelayAddress(ctx context.Context, address string, allowPrivate bool) error {
	host, port, err := net.SplitHostPort(address)
	if err != nil || host == "" {
		return fmt.Errorf("address must be host:port")
	}
	portNumber, err := strconv.Atoi(port)
	if err != nil || portNumber < 1 || portNumber > 65535 {
		return fmt.Errorf("invalid destination port")
	}
	if strings.ContainsAny(host, " /\\\x00\r\n") || len(host) > 253 {
		return fmt.Errorf("invalid destination host")
	}
	if !allowPrivate {
		return netguard.CheckHost(ctx, host)
	}
	return nil
}

func (s *Server) relayChoices(options EgressOptions) ([]*pool.EgressChoice, error) {
	if err := validateRelayEgress(options); err != nil {
		return nil, err
	}
	if options.Gateway != "" {
		choice, err := s.poolManager.SelectGateway(options.Gateway)
		if err != nil {
			return nil, err
		}
		return []*pool.EgressChoice{choice}, nil
	}
	if options.Pool != "" {
		choice, err := s.poolManager.SelectNamedNode(options.Pool, options.Node)
		if err != nil {
			return nil, err
		}
		return []*pool.EgressChoice{choice}, nil
	}
	if options.Direct {
		return []*pool.EgressChoice{{Dialer: s.direct(), PoolName: "DIRECT", NodeName: "direct"}}, nil
	}
	route, err := s.resolveEgress(options)
	if err != nil {
		return nil, err
	}
	if route.Direct {
		return []*pool.EgressChoice{{Dialer: s.direct(), PoolName: "DIRECT", NodeName: "direct"}}, nil
	}
	return route.Choices, nil
}

func (s *Server) orderedRelayChoices(options EgressOptions, key string, ttl time.Duration) ([]*pool.EgressChoice, error) {
	if len(key) > 256 {
		return nil, fmt.Errorf("affinity_key exceeds 256 bytes")
	}
	choices, err := s.relayChoices(options)
	if err != nil {
		return nil, err
	}
	if key == "" || ttl <= 0 {
		return choices, nil
	}
	identity := relayAffinityIdentity(key, options)
	s.affinityMu.Lock()
	stored := s.affinities[identity]
	if time.Now().After(stored.until) {
		delete(s.affinities, identity)
		if stored.element != nil {
			s.affinityOrder.Remove(stored.element)
		}
		stored = relayAffinity{}
	}
	if stored.element != nil {
		s.affinityOrder.MoveToFront(stored.element)
	}
	s.affinityMu.Unlock()
	if stored.choice == "" {
		return choices, nil
	}
	for i, choice := range choices {
		if choice.PoolName+"/"+choice.NodeName == stored.choice {
			return append([]*pool.EgressChoice{choice}, append(choices[:i], choices[i+1:]...)...), nil
		}
	}
	return choices, nil
}

const maxRelayAffinities = 10000

func relayAffinityIdentity(key string, options EgressOptions) string {
	return fmt.Sprintf("%x", sha256.Sum256([]byte(fmt.Sprintf("%s|%+v", key, options))))
}
func (s *Server) rememberRelayChoice(options EgressOptions, key string, ttl time.Duration, choice *pool.EgressChoice) {
	if key == "" || len(key) > 256 || ttl <= 0 || choice == nil {
		return
	}
	identity := relayAffinityIdentity(key, options)
	s.affinityMu.Lock()
	defer s.affinityMu.Unlock()
	if stored, ok := s.affinities[identity]; ok {
		s.affinityOrder.Remove(stored.element)
		delete(s.affinities, identity)
	}
	if len(s.affinities) >= maxRelayAffinities {
		if oldest := s.affinityOrder.Back(); oldest != nil {
			delete(s.affinities, oldest.Value.(string))
			s.affinityOrder.Remove(oldest)
		}
	}
	s.affinities[identity] = relayAffinity{choice: choice.PoolName + "/" + choice.NodeName, until: time.Now().Add(ttl), element: s.affinityOrder.PushFront(identity)}
}

func relayEndpoint(proxyURL string) (string, error) {
	parsed, err := url.Parse(proxyURL)
	if err != nil {
		return "", err
	}
	if parsed.Scheme != "https" {
		return "", fmt.Errorf("gateway relay requires HTTPS")
	}
	parsed.Scheme = "wss"
	parsed.Path = strings.TrimSuffix(parsed.Path, "/proxy") + "/relay/v1"
	return parsed.String(), nil
}

func (s *Server) openRelayTCP(ctx context.Context, req relay.OpenRequest) (connection net.Conn, result relay.OpenResponse, failure error) {
	req.Network = "tcp"
	started := time.Now()
	defer func() {
		t := s.sessionTraffic(ctx, req, result, started, failure)
		if failure == nil && connection != nil {
			connection = &meteredConn{Conn: connection, traffic: t}
		}
	}()
	if req.Hop < 0 || req.Hop >= MaxChijieProxyHops {
		return nil, result, fmt.Errorf("relay hop limit exceeded")
	}
	if err := validateRelayAddress(ctx, req.Address, s.allowPrivateTargets); err != nil {
		return nil, result, err
	}
	ttl, err := parseRelayTTL(req.AffinityTTL)
	if err != nil {
		return nil, result, err
	}
	choices, err := s.orderedRelayChoices(req.Egress, req.AffinityKey, ttl)
	if err != nil {
		return nil, result, err
	}
	var failures []error
	for _, choice := range choices {
		if choice.TemplateType == "chijie" {
			endpoint, endpointErr := relayEndpoint(choice.Endpoint)
			if endpointErr != nil {
				failures = append(failures, endpointErr)
				continue
			}
			forwarded := req
			forwarded.Hop++
			forwarded.Egress.Gateway = ""
			conn, remote, dialErr := relay.DialTCPWithDialer(ctx, s.relayDialer, endpoint, choice.BearerToken, forwarded)
			if dialErr == nil {
				s.rememberRelayChoice(req.Egress, req.AffinityKey, ttl, choice)
				return conn, relay.OpenResponse{Status: "connected", Pool: choice.PoolName, Node: remote.Node}, nil
			}
			failures = append(failures, dialErr)
			continue
		}
		if choice.Dialer == nil {
			continue
		}
		conn, dialErr := netguard.Guard(choice.Dialer.DialContext, s.allowPrivateTargets)(ctx, "tcp", req.Address)
		if dialErr == nil {
			s.rememberRelayChoice(req.Egress, req.AffinityKey, ttl, choice)
			return conn, relay.OpenResponse{Status: "connected", Pool: choice.PoolName, Node: choice.NodeName}, nil
		}
		failures = append(failures, dialErr)
	}
	if len(failures) == 0 {
		return nil, result, fmt.Errorf("no usable relay exits")
	}
	return nil, result, errors.Join(failures...)
}

func (s *Server) openRelayUDP(ctx context.Context, req relay.OpenRequest) (connection packetSession, result relay.OpenResponse, failure error) {
	req.Network = "udp"
	started := time.Now()
	defer func() {
		t := s.sessionTraffic(ctx, req, result, started, failure)
		if failure == nil && connection != nil {
			connection = &meteredPackets{packetSession: connection, traffic: t}
		}
	}()
	if req.Hop < 0 || req.Hop >= MaxChijieProxyHops {
		return nil, result, fmt.Errorf("relay hop limit exceeded")
	}
	if err := validateRelayAddress(ctx, req.Address, s.allowPrivateTargets); err != nil {
		return nil, result, err
	}
	ttl, err := parseRelayTTL(req.AffinityTTL)
	if err != nil {
		return nil, result, err
	}
	choices, err := s.orderedRelayChoices(req.Egress, req.AffinityKey, ttl)
	if err != nil {
		return nil, result, err
	}
	var failures []error
	for _, choice := range choices {
		if choice.TemplateType == "chijie" {
			endpoint, endpointErr := relayEndpoint(choice.Endpoint)
			if endpointErr != nil {
				failures = append(failures, endpointErr)
				continue
			}
			forwarded := req
			forwarded.Hop++
			forwarded.Egress.Gateway = ""
			conn, remote, dialErr := relay.DialUDPWithDialer(ctx, s.relayDialer, endpoint, choice.BearerToken, forwarded)
			if dialErr == nil {
				s.rememberRelayChoice(req.Egress, req.AffinityKey, ttl, choice)
				return conn, relay.OpenResponse{Status: "connected", Pool: choice.PoolName, Node: remote.Node}, nil
			}
			failures = append(failures, dialErr)
			continue
		}
		packetDialer, ok := choice.Dialer.(dialer.PacketDialer)
		if !ok {
			failures = append(failures, fmt.Errorf("%s/%s has no UDP support", choice.PoolName, choice.NodeName))
			continue
		}
		conn, listenErr := packetDialer.ListenPacket(ctx, req.Address)
		if listenErr != nil {
			failures = append(failures, listenErr)
			continue
		}
		var destination net.Addr = M.ParseSocksaddr(req.Address)
		if dialer.IsDirect(choice.Dialer) {
			destination, err = resolveDirectUDPAddress(ctx, req.Address, s.allowPrivateTargets)
			if err != nil {
				conn.Close()
				failures = append(failures, err)
				continue
			}
		}
		s.rememberRelayChoice(req.Egress, req.AffinityKey, ttl, choice)
		return &localPacketSession{conn: conn, addr: destination}, relay.OpenResponse{Status: "connected", Pool: choice.PoolName, Node: choice.NodeName}, nil
	}
	if len(failures) == 0 {
		return nil, result, fmt.Errorf("no usable relay exits")
	}
	return nil, result, errors.Join(failures...)
}

func resolveDirectUDPAddress(ctx context.Context, address string, allowPrivate bool) (net.Addr, error) {
	host, portText, err := net.SplitHostPort(address)
	if err != nil {
		return nil, err
	}
	port, err := strconv.Atoi(portText)
	if err != nil {
		return nil, err
	}
	ip := net.ParseIP(host)
	if ip == nil {
		addresses, lookupErr := dnsresolver.Resolver().LookupIPAddr(ctx, host)
		if lookupErr != nil {
			return nil, lookupErr
		}
		if len(addresses) == 0 {
			return nil, fmt.Errorf("no addresses for %s", host)
		}
		for _, address := range addresses {
			if !allowPrivate && netguard.IsBlockedIP(address.IP) {
				return nil, netguard.ErrBlockedAddress
			}
		}
		ip = addresses[0].IP
	}
	if !allowPrivate && netguard.IsBlockedIP(ip) {
		return nil, netguard.ErrBlockedAddress
	}
	return &net.UDPAddr{IP: ip, Port: port}, nil
}

func parseRelayTTL(raw string) (time.Duration, error) {
	if raw == "" {
		return 0, nil
	}
	ttl, err := time.ParseDuration(raw)
	if err != nil || ttl < 0 || ttl > 30*24*time.Hour {
		return 0, fmt.Errorf("invalid affinity_ttl")
	}
	return ttl, nil
}
