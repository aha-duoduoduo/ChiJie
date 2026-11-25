package server

import (
	"context"
	"encoding/binary"
	"fmt"
	"io"
	"log"
	"net"
	"strconv"
	"sync"
	"time"

	"chijie/pkg/relay"
)

const (
	socksVersion      = 5
	socksConnect      = 1
	socksUDPAssociate = 3
)

type socksAssociation struct {
	wg       sync.WaitGroup
	ctx      context.Context
	account  SocksAccount
	ip       net.IP
	port     int
	packets  chan socksPacket
	done     chan struct{}
	mu       sync.Mutex
	sessions map[string]*socksUDPSession
}

type socksUDPSession struct {
	transport packetSession
	lastUsed  time.Time
}

type socksPacket struct {
	address string
	data    []byte
	client  *net.UDPAddr
}

func (s *Server) SetSocksAccounts(accounts *SocksAccounts) { s.socksAccounts = accounts }

func (s *Server) ConfigureSOCKS(listen, advertise, upstreamURL, upstreamToken string) {
	s.socksListen = listen
	s.socksAdvertise = advertise
	s.relayUpstreamURL = upstreamURL
	s.relayUpstreamToken = upstreamToken
}

// StartSOCKS starts TCP and UDP on the same numeric port. A missing account denies all access.
func (s *Server) StartSOCKS() error {
	if s.socksListen == "" {
		return nil
	}
	listener, err := net.Listen("tcp", s.socksListen)
	if err != nil {
		return err
	}
	udpAddr, err := net.ResolveUDPAddr("udp", listener.Addr().String())
	if err != nil {
		listener.Close()
		return err
	}
	udp, err := net.ListenUDP("udp", udpAddr)
	if err != nil {
		listener.Close()
		return err
	}
	s.socksTCPListener = listener
	s.socksUDPListener = udp
	s.udpAssociations = make(map[string][]*socksAssociation)
	go s.serveSOCKSUDP(udp)
	go s.serveSOCKSTCP(listener)
	log.Printf("SOCKS5 listening on %s", listener.Addr())
	return nil
}

func (s *Server) SOCKSAddress() string {
	if s.socksTCPListener == nil {
		return ""
	}
	return s.socksTCPListener.Addr().String()
}

func (s *Server) serveSOCKSTCP(listener net.Listener) {
	limit := make(chan struct{}, 1024)
	for {
		conn, err := listener.Accept()
		if err != nil {
			return
		}
		select {
		case limit <- struct{}{}:
			go func() { defer func() { <-limit }(); s.handleSOCKS(conn) }()
		default:
			conn.Close()
		}
	}
}

func (s *Server) handleSOCKS(conn net.Conn) {
	defer conn.Close()
	ctx, finish, accepted := s.sessions.track(context.Background(), conn)
	if !accepted {
		return
	}
	defer finish()
	_ = conn.SetDeadline(time.Now().Add(20 * time.Second))
	var greeting [2]byte
	if _, err := io.ReadFull(conn, greeting[:]); err != nil || greeting[0] != socksVersion {
		return
	}
	methods := make([]byte, int(greeting[1]))
	if _, err := io.ReadFull(conn, methods); err != nil {
		return
	}
	method := byte(0xff)
	if s.socksAccounts != nil && len(s.socksAccounts.List()) > 0 {
		for _, offered := range methods {
			if offered == 2 {
				method = 2
				break
			}
		}
	}
	if _, err := conn.Write([]byte{5, method}); err != nil || method != 2 {
		return
	}
	account, ok := s.authenticateSOCKS(conn)
	if !ok {
		time.Sleep(200 * time.Millisecond)
		return
	}
	request, err := readSOCKSRequest(conn)
	if err != nil {
		return
	}
	_ = conn.SetDeadline(time.Time{})
	switch request.command {
	case socksConnect:
		s.handleSOCKSConnect(ctx, conn, account, request.address)
	case socksUDPAssociate:
		s.handleSOCKSUDPAssociate(ctx, conn, account, request.address)
	default:
		_ = writeSOCKSReply(conn, 7, conn.LocalAddr())
	}
}

func (s *Server) authenticateSOCKS(conn net.Conn) (SocksAccount, bool) {
	var header [2]byte
	if _, err := io.ReadFull(conn, header[:]); err != nil || header[0] != 1 || header[1] == 0 {
		return SocksAccount{}, false
	}
	username := make([]byte, int(header[1]))
	if _, err := io.ReadFull(conn, username); err != nil {
		return SocksAccount{}, false
	}
	var length [1]byte
	if _, err := io.ReadFull(conn, length[:]); err != nil || length[0] == 0 {
		return SocksAccount{}, false
	}
	password := make([]byte, int(length[0]))
	if _, err := io.ReadFull(conn, password); err != nil {
		return SocksAccount{}, false
	}
	account, ok := s.socksAccounts.Authenticate(string(username), string(password))
	status := byte(1)
	if ok {
		status = 0
	}
	_, _ = conn.Write([]byte{1, status})
	return account, ok
}

type socksRequest struct {
	command byte
	address string
}

func readSOCKSRequest(conn net.Conn) (socksRequest, error) {
	var header [4]byte
	if _, err := io.ReadFull(conn, header[:]); err != nil {
		return socksRequest{}, err
	}
	if header[0] != 5 || header[2] != 0 {
		return socksRequest{}, fmt.Errorf("invalid SOCKS request")
	}
	address, err := readSOCKSAddress(conn, header[3])
	return socksRequest{command: header[1], address: address}, err
}

func readSOCKSAddress(reader io.Reader, atyp byte) (string, error) {
	var host string
	switch atyp {
	case 1:
		buf := make([]byte, 4)
		if _, err := io.ReadFull(reader, buf); err != nil {
			return "", err
		}
		host = net.IP(buf).String()
	case 4:
		buf := make([]byte, 16)
		if _, err := io.ReadFull(reader, buf); err != nil {
			return "", err
		}
		host = net.IP(buf).String()
	case 3:
		var length [1]byte
		if _, err := io.ReadFull(reader, length[:]); err != nil || length[0] == 0 {
			return "", fmt.Errorf("invalid SOCKS domain")
		}
		buf := make([]byte, int(length[0]))
		if _, err := io.ReadFull(reader, buf); err != nil {
			return "", err
		}
		host = string(buf)
	default:
		return "", fmt.Errorf("unsupported SOCKS address type")
	}
	var port [2]byte
	if _, err := io.ReadFull(reader, port[:]); err != nil {
		return "", err
	}
	return net.JoinHostPort(host, strconv.Itoa(int(binary.BigEndian.Uint16(port[:])))), nil
}

func socksAddressBytes(address string) []byte {
	host, portText, err := net.SplitHostPort(address)
	if err != nil {
		return []byte{1, 0, 0, 0, 0, 0, 0}
	}
	port, _ := strconv.Atoi(portText)
	ip := net.ParseIP(host)
	var payload []byte
	if ip4 := ip.To4(); ip4 != nil {
		payload = append([]byte{1}, ip4...)
	} else if ip != nil {
		payload = append([]byte{4}, ip.To16()...)
	} else if len(host) <= 255 {
		payload = append([]byte{3, byte(len(host))}, []byte(host)...)
	} else {
		payload = []byte{1, 0, 0, 0, 0}
	}
	return binary.BigEndian.AppendUint16(payload, uint16(port))
}

func writeSOCKSReply(conn net.Conn, status byte, address net.Addr) error {
	return writeSOCKSReplyAddress(conn, status, address.String())
}
func writeSOCKSReplyAddress(conn net.Conn, status byte, address string) error {
	_, err := conn.Write(append([]byte{5, status, 0}, socksAddressBytes(address)...))
	return err
}

func (s *Server) socksOpenRequest(account SocksAccount, network, address string) relay.OpenRequest {
	return relay.OpenRequest{Version: 1, Network: network, Address: address, Egress: account.Egress, AffinityKey: "socks:" + account.Username, AffinityTTL: account.AffinityTTL}
}

func (s *Server) handleSOCKSConnect(parent context.Context, client net.Conn, account SocksAccount, address string) {
	ctx, cancel := context.WithTimeout(parent, 30*time.Second)
	defer cancel()
	req := s.socksOpenRequest(account, "tcp", address)
	upstream, _, err := s.openClientTCP(ctx, req, s.upstreamDialer, "socks")
	if err != nil {
		_ = writeSOCKSReply(client, 1, client.LocalAddr())
		return
	}
	defer upstream.Close()
	if err := writeSOCKSReply(client, 0, client.LocalAddr()); err != nil {
		return
	}
	done := make(chan struct{}, 2)
	go func() { _, _ = io.Copy(upstream, client); done <- struct{}{} }()
	go func() { _, _ = io.Copy(client, upstream); done <- struct{}{} }()
	<-done
	_ = upstream.Close()
	_ = client.Close()
	<-done
}

func (s *Server) handleSOCKSUDPAssociate(ctx context.Context, client net.Conn, account SocksAccount, address string) {
	if s.socksUDPListener == nil {
		_ = writeSOCKSReply(client, 1, client.LocalAddr())
		return
	}
	remote, ok := client.RemoteAddr().(*net.TCPAddr)
	if !ok {
		_ = writeSOCKSReply(client, 1, client.LocalAddr())
		return
	}
	_, portText, _ := net.SplitHostPort(address)
	port, _ := strconv.Atoi(portText)
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	assoc := &socksAssociation{ctx: ctx, account: account, ip: remote.IP, port: port, packets: make(chan socksPacket, 64), done: make(chan struct{}), sessions: make(map[string]*socksUDPSession)}
	if !s.registerSOCKSAssociation(assoc) {
		_ = writeSOCKSReply(client, 1, client.LocalAddr())
		return
	}
	defer func() { cancel(); s.unregisterSOCKSAssociation(assoc) }()
	advertise := s.socksAdvertise
	if advertise == "" {
		local := client.LocalAddr().(*net.TCPAddr)
		advertise = net.JoinHostPort(local.IP.String(), strconv.Itoa(s.socksUDPListener.LocalAddr().(*net.UDPAddr).Port))
	}
	if err := writeSOCKSReplyAddress(client, 0, advertise); err != nil {
		return
	}
	assoc.wg.Add(1)
	go func() { defer assoc.wg.Done(); s.runSOCKSAssociation(assoc) }()
	_, _ = io.Copy(io.Discard, client)
}
