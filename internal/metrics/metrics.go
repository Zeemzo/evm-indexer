// Package metrics defines the Prometheus instrumentation for the indexer.
package metrics

import (
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

type Metrics struct {
	LastIndexedBlock prometheus.Gauge
	ChainHeadBlock   prometheus.Gauge
	BlocksBehind     prometheus.Gauge
	EventsIndexed    *prometheus.CounterVec
	RPCCalls         *prometheus.CounterVec
	RPCDuration      *prometheus.HistogramVec
	ReorgsDetected   prometheus.Counter
	CycleDuration    prometheus.Histogram
	WSSubscribers    prometheus.Gauge
}

func New(reg prometheus.Registerer) *Metrics {
	factory := promauto.With(reg)
	return &Metrics{
		LastIndexedBlock: factory.NewGauge(prometheus.GaugeOpts{
			Name: "indexer_last_indexed_block",
			Help: "Highest block number durably indexed.",
		}),
		ChainHeadBlock: factory.NewGauge(prometheus.GaugeOpts{
			Name: "indexer_chain_head_block",
			Help: "Latest block number reported by the RPC node.",
		}),
		BlocksBehind: factory.NewGauge(prometheus.GaugeOpts{
			Name: "indexer_blocks_behind",
			Help: "Distance between the safe head and the last indexed block.",
		}),
		EventsIndexed: factory.NewCounterVec(prometheus.CounterOpts{
			Name: "indexer_events_indexed_total",
			Help: "Decoded events written to storage, by type.",
		}, []string{"type"}),
		RPCCalls: factory.NewCounterVec(prometheus.CounterOpts{
			Name: "indexer_rpc_calls_total",
			Help: "JSON-RPC calls issued, by method and outcome.",
		}, []string{"method", "outcome"}),
		RPCDuration: factory.NewHistogramVec(prometheus.HistogramOpts{
			Name:    "indexer_rpc_duration_seconds",
			Help:    "JSON-RPC call latency by method.",
			Buckets: prometheus.DefBuckets,
		}, []string{"method"}),
		ReorgsDetected: factory.NewCounter(prometheus.CounterOpts{
			Name: "indexer_reorgs_detected_total",
			Help: "Chain reorganizations detected via checkpoint hash mismatch.",
		}),
		CycleDuration: factory.NewHistogram(prometheus.HistogramOpts{
			Name:    "indexer_cycle_duration_seconds",
			Help:    "Duration of a full poll cycle (fetch, decode, store).",
			Buckets: []float64{0.1, 0.25, 0.5, 1, 2.5, 5, 10, 30, 60},
		}),
		WSSubscribers: factory.NewGauge(prometheus.GaugeOpts{
			Name: "indexer_websocket_subscribers",
			Help: "Currently connected WebSocket subscribers.",
		}),
	}
}
