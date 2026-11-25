// Package netguard 提供针对 SSRF 的目标地址校验与受控拨号包装。
//
// 默认拒绝拨向以下范围：回环、私有网段（10/8、172.16/12、192.168/16）、
// 链路本地（169.254/16）、CGNAT（100.64/10）、保留段，以及 IPv6 同类。
// 通过 AllowPrivate=true 可关闭防护。
package netguard

import (
	"context"
	"errors"
	"fmt"
	"net"
	"strings"

	"chijie/internal/dnsresolver"
)

// ErrBlockedAddress 表示目标命中私网/保留段黑名单。
var ErrBlockedAddress = errors.New("target address is in a blocked range (private, loopback or reserved)")

// IsBlockedIP 判断给定 IP 是否落在被禁止的地址范围内。
func IsBlockedIP(ip net.IP) bool {
	if ip == nil {
		return true
	}
	if ip.IsLoopback() || ip.IsUnspecified() || ip.IsLinkLocalUnicast() ||
		ip.IsLinkLocalMulticast() || ip.IsInterfaceLocalMulticast() ||
		ip.IsMulticast() || ip.IsPrivate() {
		return true
	}
	// CGNAT 100.64.0.0/10
	if v4 := ip.To4(); v4 != nil {
		if v4[0] == 100 && v4[1]&0xC0 == 64 {
			return true
		}
		// 0.0.0.0/8 保留
		if v4[0] == 0 {
			return true
		}
		// 240.0.0.0/4 保留
		if v4[0] >= 240 {
			return true
		}
	}
	return false
}

// ResolvePublicHost returns the exact addresses permitted for a connection.
// Callers must dial these addresses rather than resolving the hostname again.
func ResolvePublicHost(ctx context.Context, host string) ([]net.IPAddr, error) {
	host = strings.TrimSpace(host)
	if host == "" {
		return nil, fmt.Errorf("host is empty")
	}
	var addrs []net.IPAddr
	if ip := net.ParseIP(host); ip != nil {
		addrs = []net.IPAddr{{IP: ip}}
	} else {
		var err error
		addrs, err = dnsresolver.Resolver().LookupIPAddr(ctx, host)
		if err != nil {
			return nil, fmt.Errorf("resolve host: %w", err)
		}
	}
	if len(addrs) == 0 {
		return nil, fmt.Errorf("no addresses for host: %s", host)
	}
	for _, addr := range addrs {
		if IsBlockedIP(addr.IP) {
			return nil, ErrBlockedAddress
		}
	}
	return addrs, nil
}

// CheckHost performs a preflight check; Guard also pins the actual dial address.
func CheckHost(ctx context.Context, host string) error {
	_, err := ResolvePublicHost(ctx, host)
	return err
}

// DialContextFunc 是 net.Dialer.DialContext 的兼容签名。
type DialContextFunc func(ctx context.Context, network, addr string) (net.Conn, error)

// Guard validates and pins addresses, preserving the caller's URL and TLS SNI.
func Guard(base DialContextFunc, allowPrivate bool) DialContextFunc {
	if base == nil {
		base = (&net.Dialer{}).DialContext
	}
	if allowPrivate {
		return base
	}
	return func(ctx context.Context, network, addr string) (net.Conn, error) {
		host, port, err := net.SplitHostPort(addr)
		if err != nil {
			return nil, err
		}
		addresses, err := ResolvePublicHost(ctx, host)
		if err != nil {
			return nil, err
		}
		var failures []error
		for _, address := range addresses {
			if network == "tcp4" && address.IP.To4() == nil || network == "tcp6" && address.IP.To4() != nil {
				continue
			}
			conn, err := base(ctx, network, net.JoinHostPort(address.IP.String(), port))
			if err == nil {
				return conn, nil
			}
			failures = append(failures, err)
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
		}
		if len(failures) == 0 {
			return nil, fmt.Errorf("no compatible target addresses")
		}
		return nil, errors.Join(failures...)
	}
}
