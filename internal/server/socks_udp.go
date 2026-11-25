package server

import (
	"bytes"
	"context"
	"net"
	"time"

	"chijie/pkg/relay"
)

func (s *Server) registerSOCKSAssociation(assoc *socksAssociation) bool {
	key := assoc.ip.String()
	s.udpMu.Lock()
	defer s.udpMu.Unlock()
	for _, existing := range s.udpAssociations[key] {
		if existing.port == assoc.port {
			return false
		}
	}
	s.udpAssociations[key] = append(s.udpAssociations[key], assoc)
	return true
}

func (s *Server) unregisterSOCKSAssociation(assoc *socksAssociation) {
	key := assoc.ip.String()
	s.udpMu.Lock()
	for i, existing := range s.udpAssociations[key] {
		if existing == assoc {
			s.udpAssociations[key] = append(s.udpAssociations[key][:i], s.udpAssociations[key][i+1:]...)
			break
		}
	}
	close(assoc.done)
	s.udpMu.Unlock()
	assoc.mu.Lock()
	for _, session := range assoc.sessions {
		_ = session.transport.Close()
	}
	assoc.sessions = nil
	assoc.mu.Unlock()
	assoc.wg.Wait()
}

func (s *Server) serveSOCKSUDP(conn *net.UDPConn) {
	buf := make([]byte, 65535)
	for {
		n, sender, err := conn.ReadFromUDP(buf)
		if err != nil {
			return
		}
		if n < 7 || buf[0] != 0 || buf[1] != 0 || buf[2] != 0 {
			continue
		}
		reader := bytes.NewReader(buf[4:n])
		address, err := readSOCKSAddress(reader, buf[3])
		if err != nil || reader.Len() > relay.MaxDatagram {
			continue
		}
		assoc := s.findSOCKSAssociation(sender)
		if assoc == nil {
			continue
		}
		packet := socksPacket{address: address, data: bytes.Clone(buf[n-reader.Len() : n]), client: sender}
		select {
		case assoc.packets <- packet:
		default:
		}
	}
}

func (s *Server) findSOCKSAssociation(sender *net.UDPAddr) *socksAssociation {
	s.udpMu.Lock()
	defer s.udpMu.Unlock()
	for _, assoc := range s.udpAssociations[sender.IP.String()] {
		if assoc.port == sender.Port {
			return assoc
		}
	}
	for _, assoc := range s.udpAssociations[sender.IP.String()] {
		if assoc.port == 0 {
			assoc.port = sender.Port
			return assoc
		}
	}
	return nil
}

func (s *Server) runSOCKSAssociation(assoc *socksAssociation) {
	for {
		select {
		case <-assoc.done:
			return
		case packet := <-assoc.packets:
			s.handleSOCKSDatagram(assoc, packet)
		}
	}
}

func (s *Server) handleSOCKSDatagram(assoc *socksAssociation, packet socksPacket) {
	assoc.mu.Lock()
	for address, session := range assoc.sessions {
		if time.Since(session.lastUsed) > 2*time.Minute {
			_ = session.transport.Close()
			delete(assoc.sessions, address)
		}
	}
	session := assoc.sessions[packet.address]
	assoc.mu.Unlock()
	if session == nil {
		assoc.mu.Lock()
		if len(assoc.sessions) >= 256 {
			assoc.mu.Unlock()
			return
		}
		assoc.mu.Unlock()
		ctx := assoc.ctx
		if ctx == nil {
			ctx = context.Background()
		}
		ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
		defer cancel()
		request := s.socksOpenRequest(assoc.account, "udp", packet.address)
		transport, _, err := s.openClientUDP(ctx, request, s.upstreamDialer, "socks")
		if err != nil {
			return
		}
		session = &socksUDPSession{transport: transport, lastUsed: time.Now()}
		assoc.mu.Lock()
		if assoc.sessions == nil {
			assoc.mu.Unlock()
			transport.Close()
			return
		}
		assoc.sessions[packet.address] = session
		assoc.mu.Unlock()
		assoc.wg.Add(1)
		go func() { defer assoc.wg.Done(); s.socksUDPResponses(assoc, packet.address, packet.client, session) }()
	}
	assoc.mu.Lock()
	session.lastUsed = time.Now()
	assoc.mu.Unlock()
	if err := session.transport.Send(packet.data); err != nil {
		assoc.mu.Lock()
		if assoc.sessions[packet.address] == session {
			delete(assoc.sessions, packet.address)
		}
		assoc.mu.Unlock()
		_ = session.transport.Close()
	}
}

func (s *Server) socksUDPResponses(assoc *socksAssociation, target string, client *net.UDPAddr, session *socksUDPSession) {
	defer func() {
		assoc.mu.Lock()
		if assoc.sessions[target] == session {
			delete(assoc.sessions, target)
		}
		assoc.mu.Unlock()
		_ = session.transport.Close()
	}()
	header := append([]byte{0, 0, 0}, socksAddressBytes(target)...)
	for {
		packet, err := session.transport.Receive()
		if err != nil {
			return
		}
		payload := make([]byte, 0, len(header)+len(packet))
		payload = append(payload, header...)
		payload = append(payload, packet...)
		if _, err := s.socksUDPListener.WriteToUDP(payload, client); err != nil {
			return
		}
	}
}
