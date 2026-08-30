// Package metrics defines the Prometheus instrumentation (§13.1).
package metrics

import (
	"net/http"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"
	"github.com/prometheus/client_golang/prometheus/promhttp"

	"github.com/StevenGann/ShareDirStat/internal/version"
)

// Namespace prefixes every metric name.
const Namespace = "sharedirstat"

// Metrics bundles the registry and the collectors other packages update.
type Metrics struct {
	Registry *prometheus.Registry

	HTTPRequests      *prometheus.CounterVec
	HTTPDuration      *prometheus.HistogramVec
	SharesConfigured  prometheus.Gauge
	ShareState        *prometheus.GaugeVec
	ScanRunning       *prometheus.GaugeVec
	ScansTotal        *prometheus.CounterVec
	ScanDuration      prometheus.Histogram
	ScanErrors        *prometheus.CounterVec
	DeleteOps         *prometheus.CounterVec
	DeleteBytesFreed  *prometheus.CounterVec
	DownloadBytes     *prometheus.CounterVec
	DownloadsInFlight prometheus.Gauge

	ShareNodes            *prometheus.GaugeVec
	ShareBytes            *prometheus.GaugeVec
	ShareGenerationTime   *prometheus.GaugeVec
	SnapshotBytes         *prometheus.GaugeVec
	SnapshotWriteDuration prometheus.Histogram
	SSEClients            prometheus.Gauge
}

// New creates and registers all collectors on a fresh registry.
func New() *Metrics {
	reg := prometheus.NewRegistry()
	m := &Metrics{
		Registry: reg,
		HTTPRequests: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: Namespace, Name: "http_requests_total", Help: "HTTP requests by route, method and status.",
		}, []string{"route", "method", "status"}),
		HTTPDuration: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Namespace: Namespace, Name: "http_request_duration_seconds", Help: "HTTP request latency by route.",
			Buckets: []float64{.001, .005, .01, .025, .05, .1, .25, .5, 1, 2.5, 5, 10},
		}, []string{"route"}),
		SharesConfigured: prometheus.NewGauge(prometheus.GaugeOpts{
			Namespace: Namespace, Name: "shares_configured", Help: "Number of configured shares.",
		}),
		ShareState: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Namespace: Namespace, Name: "share_state", Help: "1 for the current state of each share, 0 otherwise.",
		}, []string{"share", "state"}),
		ScanRunning: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Namespace: Namespace, Name: "scan_running", Help: "1 while a scan of the share is running.",
		}, []string{"share"}),
		ScansTotal: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: Namespace, Name: "scans_total", Help: "Completed scans by outcome.",
		}, []string{"share", "outcome"}),
		ScanDuration: prometheus.NewHistogram(prometheus.HistogramOpts{
			Namespace: Namespace, Name: "scan_duration_seconds", Help: "Duration of completed scans.",
			Buckets: prometheus.ExponentialBuckets(1, 2, 16),
		}),
		ScanErrors: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: Namespace, Name: "scan_errors_total", Help: "Filesystem errors encountered while scanning.",
		}, []string{"share"}),
		ShareNodes: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Namespace: Namespace, Name: "share_nodes_total", Help: "Nodes in the current generation by kind.",
		}, []string{"share", "kind"}),
		ShareBytes: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Namespace: Namespace, Name: "share_bytes", Help: "Bytes accounted for in the current generation.",
		}, []string{"share", "basis"}),
		ShareGenerationTime: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Namespace: Namespace, Name: "share_generation_timestamp_seconds", Help: "Completion time of the current generation.",
		}, []string{"share"}),
		SnapshotBytes: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Namespace: Namespace, Name: "snapshot_bytes", Help: "Size on disk of the most recent snapshot.",
		}, []string{"share"}),
		SnapshotWriteDuration: prometheus.NewHistogram(prometheus.HistogramOpts{
			Namespace: Namespace, Name: "snapshot_write_duration_seconds", Help: "Time taken to persist a snapshot.",
			Buckets: prometheus.ExponentialBuckets(0.01, 2, 12),
		}),
		SSEClients: prometheus.NewGauge(prometheus.GaugeOpts{
			Namespace: Namespace, Name: "sse_clients", Help: "Connected Server-Sent Events clients.",
		}),
		DeleteOps: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: Namespace, Name: "delete_operations_total", Help: "Delete operations by outcome.",
		}, []string{"share", "outcome"}),
		DeleteBytesFreed: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: Namespace, Name: "delete_bytes_freed_total", Help: "Bytes freed by deletes.",
		}, []string{"share"}),
		DownloadBytes: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: Namespace, Name: "download_bytes_total", Help: "Bytes served by downloads.",
		}, []string{"share", "type"}),
		DownloadsInFlight: prometheus.NewGauge(prometheus.GaugeOpts{
			Namespace: Namespace, Name: "downloads_in_flight", Help: "Downloads currently streaming.",
		}),
	}
	buildInfo := prometheus.NewGauge(prometheus.GaugeOpts{
		Namespace: Namespace, Name: "build_info", Help: "Build metadata; always 1.",
		ConstLabels: prometheus.Labels{"version": version.Version, "commit": version.Commit},
	})
	buildInfo.Set(1)

	reg.MustRegister(
		collectors.NewGoCollector(),
		collectors.NewProcessCollector(collectors.ProcessCollectorOpts{}),
		buildInfo,
		m.HTTPRequests, m.HTTPDuration, m.SharesConfigured, m.ShareState,
		m.ScanRunning, m.ScansTotal, m.ScanDuration, m.ScanErrors,
		m.ShareNodes, m.ShareBytes, m.ShareGenerationTime,
		m.SnapshotBytes, m.SnapshotWriteDuration, m.SSEClients,
		m.DeleteOps, m.DeleteBytesFreed, m.DownloadBytes, m.DownloadsInFlight,
	)
	return m
}

// Handler serves the exposition endpoint.
func (m *Metrics) Handler() http.Handler {
	return promhttp.HandlerFor(m.Registry, promhttp.HandlerOpts{})
}

// SetShareState records the state of a share, clearing other states.
func (m *Metrics) SetShareState(share, state string) {
	for _, s := range []string{"unavailable", "never-scanned", "scanning", "ready", "ready-stale", "error"} {
		v := 0.0
		if s == state {
			v = 1
		}
		m.ShareState.WithLabelValues(share, s).Set(v)
	}
}
