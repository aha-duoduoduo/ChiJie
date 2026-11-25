package server

import (
	"context"
	"io"
	"net/http"
	"time"

	"chijie/pkg/relay"

	"github.com/gorilla/websocket"
)

func (s *Server) handleRelay(w http.ResponseWriter, r *http.Request) {
	if !s.auth.VerifyRequest(r) {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	ws, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		return
	}
	defer ws.Close()
	ctx, finish, accepted := s.sessions.track(r.Context(), ws)
	if !accepted {
		return
	}
	defer finish()
	r = r.WithContext(ctx)
	ws.SetReadLimit(relay.MaxMessageSize)
	_ = ws.SetReadDeadline(time.Now().Add(15 * time.Second))
	var open relay.OpenRequest
	if err := ws.ReadJSON(&open); err != nil {
		_ = ws.WriteJSON(relay.OpenResponse{Status: "error", Error: "invalid open request"})
		return
	}
	_ = ws.SetReadDeadline(time.Time{})
	if open.Version != 1 || (open.Network != "tcp" && open.Network != "udp") {
		_ = ws.WriteJSON(relay.OpenResponse{Status: "error", Error: "unsupported relay version or network"})
		return
	}
	if err := validateRelayEgress(open.Egress); err != nil {
		_ = ws.WriteJSON(relay.OpenResponse{Status: "error", Error: err.Error()})
		return
	}
	stopPing := make(chan struct{})
	defer close(stopPing)
	go func() {
		ticker := time.NewTicker(25 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-stopPing:
				return
			case <-ticker.C:
				if err := ws.WriteControl(websocket.PingMessage, nil, time.Now().Add(5*time.Second)); err != nil {
					return
				}
			}
		}
	}()
	if open.Network == "tcp" {
		s.serveRelayTCP(r.Context(), ws, open)
	} else {
		s.serveRelayUDP(r.Context(), ws, open)
	}
}

func (s *Server) serveRelayTCP(ctx context.Context, ws *websocket.Conn, open relay.OpenRequest) {
	conn, result, err := s.openRelayTCP(ctx, open)
	if err != nil {
		_ = ws.WriteJSON(relay.OpenResponse{Status: "error", Error: err.Error()})
		return
	}
	defer conn.Close()
	if err := ws.WriteJSON(result); err != nil {
		return
	}
	stream := relay.NewStream(ws)
	done := make(chan struct{}, 2)
	go func() { _, _ = io.Copy(conn, stream); done <- struct{}{} }()
	go func() { _, _ = io.Copy(stream, conn); done <- struct{}{} }()
	<-done
	_ = conn.Close()
	_ = ws.Close()
	<-done
}

func (s *Server) serveRelayUDP(ctx context.Context, ws *websocket.Conn, open relay.OpenRequest) {
	session, result, err := s.openRelayUDP(ctx, open)
	if err != nil {
		_ = ws.WriteJSON(relay.OpenResponse{Status: "error", Error: err.Error()})
		return
	}
	defer session.Close()
	if err := ws.WriteJSON(result); err != nil {
		return
	}
	transport := relay.NewDatagram(ws)
	done := make(chan struct{}, 2)
	go func() {
		for {
			packet, err := transport.Receive()
			if err != nil || session.Send(packet) != nil {
				break
			}
		}
		done <- struct{}{}
	}()
	go func() {
		for {
			packet, err := session.Receive()
			if err != nil || transport.Send(packet) != nil {
				break
			}
		}
		done <- struct{}{}
	}()
	<-done
	_ = session.Close()
	_ = ws.Close()
	<-done
}
