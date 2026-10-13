package api

import (
	"net/http"
	"time"

	"github.com/gorilla/websocket"

	"github.com/zeemzo/evm-indexer/internal/events"
)

var upgrader = websocket.Upgrader{
	ReadBufferSize:  1024,
	WriteBufferSize: 4096,
	// Streaming public, read-only indexed data: allow any origin.
	CheckOrigin: func(*http.Request) bool { return true },
}

const (
	writeTimeout = 10 * time.Second
	pingInterval = 30 * time.Second
	// pongWait bounds how long a silent peer stays connected. It must
	// comfortably exceed pingInterval: each pong pushes the read deadline
	// forward, so a client that misses two pings in a row is dropped
	// instead of holding its goroutines and broker subscription forever.
	pongWait = 75 * time.Second
)

// handleWebSocket streams newly indexed events to the client as JSON.
// An optional ?type=erc20_transfer|swap query filters the stream.
// Each connection gets its own broker subscription and two goroutines:
// a writer pumping events and pings, and a reader whose only job is to
// detect the client going away.
func (s *Server) handleWebSocket(w http.ResponseWriter, r *http.Request) {
	typeFilter := events.Type(r.URL.Query().Get("type"))

	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		return // Upgrade already wrote the error response.
	}

	sub, cancel := s.broker.Subscribe()
	s.metrics.WSSubscribers.Set(float64(s.broker.SubscriberCount()))
	s.log.Info("websocket connected", "remote", r.RemoteAddr, "typeFilter", typeFilter)

	defer func() {
		cancel()
		conn.Close()
		s.metrics.WSSubscribers.Set(float64(s.broker.SubscriberCount()))
		s.log.Info("websocket disconnected", "remote", r.RemoteAddr)
	}()

	// Dead-peer detection: the read deadline only advances when the
	// client answers our pings, so a half-open connection fails the read
	// within pongWait instead of leaking the subscription.
	_ = conn.SetReadDeadline(time.Now().Add(pongWait))
	conn.SetPongHandler(func(string) error {
		return conn.SetReadDeadline(time.Now().Add(pongWait))
	})

	// Reader: discard client messages; returns when the peer closes or
	// the read deadline expires.
	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			if _, _, err := conn.ReadMessage(); err != nil {
				return
			}
		}
	}()

	ping := time.NewTicker(pingInterval)
	defer ping.Stop()

	for {
		select {
		case <-done:
			return
		case <-ping.C:
			_ = conn.SetWriteDeadline(time.Now().Add(writeTimeout))
			if err := conn.WriteMessage(websocket.PingMessage, nil); err != nil {
				return
			}
		case evt, ok := <-sub:
			if !ok {
				return // Broker shut down.
			}
			if typeFilter != "" && evt.Type != typeFilter {
				continue
			}
			_ = conn.SetWriteDeadline(time.Now().Add(writeTimeout))
			if err := conn.WriteJSON(evt); err != nil {
				return
			}
		}
	}
}
