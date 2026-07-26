// Package api exposes the read side of the indexer: REST queries over
// indexed data, live event streaming over WebSocket, health/readiness
// probes, and Prometheus metrics.
package api

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"

	"github.com/zeemzo/evm-indexer/internal/events"
	"github.com/zeemzo/evm-indexer/internal/indexer"
	"github.com/zeemzo/evm-indexer/internal/metrics"
	"github.com/zeemzo/evm-indexer/internal/pubsub"
	"github.com/zeemzo/evm-indexer/internal/storage"
)

type Server struct {
	store   *storage.Store
	idx     *indexer.Indexer
	broker  *pubsub.Broker[events.Event]
	metrics *metrics.Metrics
	chainID uint64
	log     *slog.Logger
	http    *http.Server
}

func NewServer(addr string, store *storage.Store, idx *indexer.Indexer, broker *pubsub.Broker[events.Event], m *metrics.Metrics, registry *prometheus.Registry, chainID uint64, log *slog.Logger) *Server {
	s := &Server{
		store:   store,
		idx:     idx,
		broker:  broker,
		metrics: m,
		chainID: chainID,
		log:     log,
	}

	r := chi.NewRouter()
	r.Use(middleware.RequestID)
	r.Use(middleware.Recoverer)
	r.Use(middleware.Timeout(30 * time.Second))

	r.Get("/health", s.handleHealth)
	r.Get("/status", s.handleStatus)
	r.Get("/stats", s.handleStats)
	r.Get("/transfers", s.handleTransfers)
	r.Get("/swaps", s.handleSwaps)
	r.Get("/ws", s.handleWebSocket)
	r.Handle("/metrics", promhttp.HandlerFor(registry, promhttp.HandlerOpts{}))

	s.http = &http.Server{
		Addr:              addr,
		Handler:           r,
		ReadHeaderTimeout: 10 * time.Second,
	}
	return s
}

// ListenAndServe blocks until the server stops.
func (s *Server) ListenAndServe() error {
	return s.http.ListenAndServe()
}

// Shutdown drains in-flight requests.
func (s *Server) Shutdown(ctx context.Context) error {
	return s.http.Shutdown(ctx)
}

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
	defer cancel()

	dbOK := s.store.Ping(ctx) == nil
	status := s.idx.Status()
	healthy := dbOK && status.Running

	code := http.StatusOK
	if !healthy {
		code = http.StatusServiceUnavailable
	}
	writeJSON(w, code, map[string]any{
		"healthy":  healthy,
		"database": dbOK,
		"indexer":  status.Running,
		"caughtUp": status.CaughtUp,
	})
}

func (s *Server) handleStatus(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, s.idx.Status())
}

func (s *Server) handleStats(w http.ResponseWriter, r *http.Request) {
	stats, err := s.store.Stats(r.Context(), s.chainID)
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, stats)
}

// handleTransfers serves GET /transfers?token=&from=&to=&beforeBlock=&limit=
func (s *Server) handleTransfers(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	transfers, err := s.store.Transfers(r.Context(), storage.TransferFilter{
		Token:       q.Get("token"),
		From:        q.Get("from"),
		To:          q.Get("to"),
		BeforeBlock: parseUint(q.Get("beforeBlock")),
		Limit:       int(parseUint(q.Get("limit"))),
	})
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"transfers": transfers, "count": len(transfers)})
}

// handleSwaps serves GET /swaps?pool=&venue=&beforeBlock=&limit=
func (s *Server) handleSwaps(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	swaps, err := s.store.Swaps(r.Context(), storage.SwapFilter{
		Pool:        q.Get("pool"),
		Venue:       q.Get("venue"),
		BeforeBlock: parseUint(q.Get("beforeBlock")),
		Limit:       int(parseUint(q.Get("limit"))),
	})
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"swaps": swaps, "count": len(swaps)})
}

func (s *Server) serverError(w http.ResponseWriter, r *http.Request, err error) {
	s.log.Error("request failed", "path", r.URL.Path, "error", err)
	writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "internal server error"})
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

func parseUint(s string) uint64 {
	if s == "" {
		return 0
	}
	n, err := strconv.ParseUint(s, 10, 64)
	if err != nil {
		return 0
	}
	return n
}
