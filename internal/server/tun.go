package server

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/netip"
	"net/url"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"

	"chijie/pkg/relay"

	"github.com/gorilla/websocket"
	sblog "github.com/sagernet/sing-box/log"
	tun "github.com/sagernet/sing-tun"
	"github.com/sagernet/sing/common/buf"
	"github.com/sagernet/sing/common/control"
	M "github.com/sagernet/sing/common/metadata"
	N "github.com/sagernet/sing/common/network"
)

// TUNConfig captures outbound TCP and UDP from the Linux host in a client deployment.
type TUNConfig struct {
	Enabled     bool          `yaml:"enabled" json:"enabled"`
	Interface   string        `yaml:"interface" json:"interface"`
	IPv4        string        `yaml:"ipv4" json:"ipv4"`
	IPv6        string        `yaml:"ipv6" json:"ipv6"`
	IPv6Enabled *bool         `yaml:"ipv6_enabled" json:"ipv6_enabled,omitempty"`
	MTU         uint32        `yaml:"mtu" json:"mtu"`
	BypassCIDRs []string      `yaml:"bypass_cidrs" json:"bypass_cidrs"`
	Egress      EgressOptions `yaml:"egress" json:"egress"`
}

type tunRuntime struct {
	cancel           context.CancelFunc
	stack            tun.Stack
	device           tun.Tun
	interfaceMonitor tun.DefaultInterfaceMonitor
	networkMonitor   tun.NetworkUpdateMonitor
}

func (r *tunRuntime) Close() error {
	if r == nil {
		return nil
	}
	if r.cancel != nil {
		r.cancel()
	}
	var failures []error
	if r.stack != nil {
		failures = append(failures, r.stack.Close())
	}
	if r.device != nil {
		failures = append(failures, r.device.Close())
	}
	if r.interfaceMonitor != nil {
		failures = append(failures, r.interfaceMonitor.Close())
	}
	if r.networkMonitor != nil {
		failures = append(failures, r.networkMonitor.Close())
	}
	return errors.Join(failures...)
}

func validateTUNConfig(cfg TUNConfig, upstream string) (TUNConfig, error) {
	parsed, err := url.Parse(upstream)
	if err != nil || parsed.Scheme != "wss" || parsed.Hostname() == "" || parsed.Path != "/relay/v1" {
		return cfg, fmt.Errorf("TUN mode requires socks.upstream_url using wss://.../relay/v1")
	}
	if err := validateRelayEgress(cfg.Egress); err != nil {
		return cfg, err
	}
	if cfg.Interface == "" {
		cfg.Interface = "chijie0"
	}
	if len(cfg.Interface) > 15 || strings.ContainsAny(cfg.Interface, " /\\\t\r\n") {
		return cfg, fmt.Errorf("invalid TUN interface name")
	}
	if cfg.IPv4 == "" {
		cfg.IPv4 = "198.18.0.1/30"
	}
	if cfg.IPv6Enabled != nil && !*cfg.IPv6Enabled {
		if cfg.IPv6 != "" {
			return cfg, fmt.Errorf("TUN IPv6 address conflicts with ipv6_enabled=false")
		}
	} else if cfg.IPv6 == "" {
		cfg.IPv6 = "fd7a:115c:a1e0::1/126"
	}
	if cfg.MTU == 0 {
		cfg.MTU = 1500
	}
	if cfg.MTU < 1280 || cfg.MTU > 9000 {
		return cfg, fmt.Errorf("TUN MTU must be 1280-9000")
	}
	ipv4, err := netip.ParsePrefix(cfg.IPv4)
	if err != nil || !ipv4.Addr().Is4() || !tun.HasNextAddress(ipv4, 1) {
		return cfg, fmt.Errorf("invalid TUN IPv4 prefix")
	}
	if cfg.IPv6 != "" {
		ipv6, err := netip.ParsePrefix(cfg.IPv6)
		if err != nil || !ipv6.Addr().Is6() || !tun.HasNextAddress(ipv6, 1) {
			return cfg, fmt.Errorf("invalid TUN IPv6 prefix")
		}
	}
	for _, value := range cfg.BypassCIDRs {
		if _, err := netip.ParsePrefix(value); err != nil {
			return cfg, fmt.Errorf("invalid TUN bypass CIDR: %w", err)
		}
	}
	return cfg, nil
}

// StartTUN routes host outbound TCP/UDP through an authenticated remote WSS relay.
// Its own remote WSS address is pinned and excluded from TUN routing.
func (s *Server) StartTUN(raw TUNConfig) (startErr error) {
	if !raw.Enabled {
		return nil
	}
	if runtime.GOOS != "linux" {
		return fmt.Errorf("TUN mode is supported on Linux only")
	}
	config, err := validateTUNConfig(raw, s.relayUpstreamURL)
	if err != nil {
		return err
	}
	if s.relayUpstreamToken == "" {
		return fmt.Errorf("TUN mode requires socks.upstream_token")
	}
	parsed, _ := url.Parse(s.relayUpstreamURL)
	ctx, cancel := context.WithCancel(context.Background())
	active := &tunRuntime{cancel: cancel}
	defer func() {
		if startErr != nil {
			_ = active.Close()
		}
	}()
	addresses, err := net.DefaultResolver.LookupNetIP(ctx, "ip", parsed.Hostname())
	if err != nil {
		return fmt.Errorf("resolve TUN upstream: %w", err)
	}
	if len(addresses) == 0 {
		return fmt.Errorf("TUN upstream has no IP addresses")
	}
	pinned := addresses[0]
	for _, address := range addresses {
		if address.Is4() {
			pinned = address
			break
		}
	}
	port := parsed.Port()
	if port == "" {
		port = "443"
	}
	portNumber, portErr := strconv.Atoi(port)
	if portErr != nil || portNumber < 1 || portNumber > 65535 {
		return fmt.Errorf("invalid TUN upstream port")
	}
	pinnedAddress := net.JoinHostPort(pinned.String(), port)
	pinnedDialer := &websocket.Dialer{HandshakeTimeout: 15 * time.Second, EnableCompression: false, NetDialContext: func(ctx context.Context, network, _ string) (net.Conn, error) {
		return (&net.Dialer{Timeout: 15 * time.Second}).DialContext(ctx, "tcp", pinnedAddress)
	}}
	ipv4, _ := netip.ParsePrefix(config.IPv4)
	var ipv6Prefixes []netip.Prefix
	if config.IPv6 != "" {
		ipv6, _ := netip.ParsePrefix(config.IPv6)
		ipv6Prefixes = []netip.Prefix{ipv6}
	}
	logger := sblog.NewNOPFactory().Logger()
	networkMonitor, err := tun.NewNetworkUpdateMonitor(logger)
	if err != nil {
		return err
	}
	active.networkMonitor = networkMonitor
	finder := control.NewDefaultInterfaceFinder()
	interfaceMonitor, err := tun.NewDefaultInterfaceMonitor(networkMonitor, logger, tun.DefaultInterfaceMonitorOptions{InterfaceFinder: finder})
	if err != nil {
		return err
	}
	active.interfaceMonitor = interfaceMonitor
	if err = networkMonitor.Start(); err != nil {
		return err
	}
	if err = interfaceMonitor.Start(); err != nil {
		return err
	}
	options := tun.Options{Name: config.Interface, Inet4Address: []netip.Prefix{ipv4}, Inet6Address: ipv6Prefixes, MTU: config.MTU, AutoRoute: true, EXP_DisableDNSHijack: true, InterfaceMonitor: interfaceMonitor, Logger: logger}
	for _, rawPrefix := range config.BypassCIDRs {
		prefix, _ := netip.ParsePrefix(rawPrefix)
		if prefix.Addr().Is4() {
			options.Inet4RouteExcludeAddress = append(options.Inet4RouteExcludeAddress, prefix)
		} else {
			options.Inet6RouteExcludeAddress = append(options.Inet6RouteExcludeAddress, prefix)
		}
	}
	remotePrefix := netip.PrefixFrom(pinned, pinned.BitLen())
	if pinned.Is4() {
		options.Inet4RouteExcludeAddress = append(options.Inet4RouteExcludeAddress, remotePrefix)
	} else {
		options.Inet6RouteExcludeAddress = append(options.Inet6RouteExcludeAddress, remotePrefix)
	}
	device, err := tun.New(options)
	if err != nil {
		return err
	}
	active.device = device
	handler := &tunRelayHandler{server: s, egress: config.Egress, upstreamDialer: pinnedDialer}
	stack, err := tun.NewStack("system", tun.StackOptions{Context: ctx, Tun: device, TunOptions: options, UDPTimeout: 2 * time.Minute, Handler: handler, Logger: logger})
	if err != nil {
		return err
	}
	active.stack = stack
	if err = stack.Start(); err != nil {
		return err
	}
	if err = device.Start(); err != nil {
		return err
	}
	s.upstreamDialer = pinnedDialer
	s.tunRuntime = active
	return nil
}

type tunRelayHandler struct {
	server         *Server
	egress         EgressOptions
	upstreamDialer *websocket.Dialer
}

func (h *tunRelayHandler) PrepareConnection(network string, source M.Socksaddr, destination M.Socksaddr, routeContext tun.DirectRouteContext, timeout time.Duration) (tun.DirectRouteDestination, error) {
	if network != N.NetworkTCP && network != N.NetworkUDP {
		return nil, tun.ErrDrop
	}
	return nil, nil
}

func (h *tunRelayHandler) NewConnectionEx(ctx context.Context, client net.Conn, source M.Socksaddr, destination M.Socksaddr, onClose N.CloseHandlerFunc) {
	defer client.Close()
	ctx, finish, accepted := h.server.sessions.track(ctx, client)
	if !accepted {
		return
	}
	defer finish()
	if onClose != nil {
		defer onClose(nil)
	}
	request := relay.OpenRequest{Version: 1, Network: "tcp", Address: destination.String(), Egress: h.egress}
	upstream, _, err := h.server.openClientTCP(ctx, request, h.upstreamDialer, "tun")
	if err != nil {
		return
	}
	defer upstream.Close()
	done := make(chan struct{}, 2)
	go func() { _, _ = io.Copy(upstream, client); done <- struct{}{} }()
	go func() { _, _ = io.Copy(client, upstream); done <- struct{}{} }()
	<-done
	_ = upstream.Close()
	_ = client.Close()
	<-done
}

func (h *tunRelayHandler) NewPacketConnectionEx(ctx context.Context, client N.PacketConn, source M.Socksaddr, destination M.Socksaddr, onClose N.CloseHandlerFunc) {
	ctx, finish, accepted := h.server.sessions.track(ctx, client)
	if !accepted {
		return
	}
	defer finish()

	if onClose != nil {
		defer onClose(nil)
	}
	var mu sync.Mutex
	var responses sync.WaitGroup
	sessions := make(map[string]packetSession)
	frontHeadroom := N.CalculateFrontHeadroom(client)
	defer func() {
		_ = client.Close()
		mu.Lock()
		active := make([]packetSession, 0, len(sessions))
		for _, session := range sessions {
			active = append(active, session)
		}
		mu.Unlock()
		for _, session := range active {
			_ = session.Close()
		}
		responses.Wait()
	}()
	for {
		packet := buf.NewSize(relay.MaxDatagram)
		target, err := client.ReadPacket(packet)
		if err != nil {
			packet.Release()
			return
		}
		payload := bytes.Clone(packet.Bytes())
		packet.Release()
		key := target.String()
		mu.Lock()
		session := sessions[key]
		mu.Unlock()
		if session == nil {
			mu.Lock()
			if len(sessions) >= 256 {
				mu.Unlock()
				continue
			}
			mu.Unlock()
			request := relay.OpenRequest{Version: 1, Network: "udp", Address: key, Egress: h.egress}
			opened, _, openErr := h.server.openClientUDP(ctx, request, h.upstreamDialer, "tun")
			if openErr != nil {
				continue
			}
			session = opened
			mu.Lock()
			sessions[key] = session
			mu.Unlock()
			responses.Add(1)
			go func(destination M.Socksaddr, active packetSession) {
				defer responses.Done()
				for {
					data, receiveErr := active.Receive()
					if receiveErr != nil {
						break
					}
					mu.Lock()
					packet := buf.NewSize(frontHeadroom + len(data))
					packet.Resize(frontHeadroom, 0)
					_, _ = packet.Write(data)
					writeErr := client.WritePacket(packet, destination)
					mu.Unlock()
					if writeErr != nil {
						break
					}
				}
				mu.Lock()
				if sessions[destination.String()] == active {
					delete(sessions, destination.String())
				}
				mu.Unlock()
				_ = active.Close()
			}(target, session)
		}
		if err := session.Send(payload); err != nil {
			mu.Lock()
			if sessions[key] == session {
				delete(sessions, key)
			}
			mu.Unlock()
			_ = session.Close()
		}
	}
}

var _ tun.Handler = (*tunRelayHandler)(nil)
