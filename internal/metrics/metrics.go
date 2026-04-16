package metrics

import (
	"net/http"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

const namespace = "quixiot"

type ServerMetrics struct {
	Registry *prometheus.Registry

	ConnectionsActive prometheus.Gauge
	ConnectionsTotal  prometheus.Counter
	HandshakeDuration prometheus.Histogram
	HTTPDuration      *prometheus.HistogramVec
	Bytes             *prometheus.CounterVec
	PubSubSubscribers *prometheus.GaugeVec
	PubSubDrops       *prometheus.CounterVec
	Streams           *prometheus.CounterVec
	UploadBytes       prometheus.Histogram
	UploadDuration    prometheus.Histogram
}

type ClientMetrics struct {
	Registry *prometheus.Registry

	Reconnects        prometheus.Counter
	HandshakeDuration prometheus.Histogram
	RTT               prometheus.Gauge
	Datagrams         *prometheus.CounterVec
	PublishLatency    prometheus.Histogram
	UploadDuration    prometheus.Histogram
	PollDuration      prometheus.Histogram
}

type ProxyMetrics struct {
	Registry *prometheus.Registry

	SessionsActive prometheus.Gauge
	SessionsTotal  prometheus.Counter
	Packets        *prometheus.CounterVec
	Bytes          *prometheus.CounterVec
	Drops          *prometheus.CounterVec
	Duplicates     *prometheus.CounterVec
	Reorders       *prometheus.CounterVec
	EnforcedDelay  *prometheus.HistogramVec
}

func NewServer() *ServerMetrics {
	reg := newRegistry()
	m := &ServerMetrics{
		Registry: reg,
		ConnectionsActive: prometheus.NewGauge(prometheus.GaugeOpts{
			Namespace: namespace,
			Name:      "server_connections_active",
			Help:      "Current number of active QUIC connections on the server.",
		}),
		ConnectionsTotal: prometheus.NewCounter(prometheus.CounterOpts{
			Namespace: namespace,
			Name:      "server_connections_total",
			Help:      "Total QUIC connections accepted by the server.",
		}),
		HandshakeDuration: prometheus.NewHistogram(prometheus.HistogramOpts{
			Namespace: namespace,
			Name:      "server_handshake_duration_seconds",
			Help:      "Observed QUIC handshake duration on the server side.",
			Buckets:   prometheus.DefBuckets,
		}),
		HTTPDuration: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Namespace: namespace,
			Name:      "server_http_request_duration_seconds",
			Help:      "HTTP/3 handler latency by method, path, and status.",
			Buckets:   prometheus.DefBuckets,
		}, []string{"method", "path", "status"}),
		Bytes: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: namespace,
			Name:      "server_bytes_total",
			Help:      "Bytes processed by the server, partitioned by surface and direction.",
		}, []string{"surface", "direction"}),
		PubSubSubscribers: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Namespace: namespace,
			Name:      "server_pubsub_subscribers",
			Help:      "Current pubsub subscriber count by topic.",
		}, []string{"topic"}),
		PubSubDrops: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: namespace,
			Name:      "server_pubsub_drops_total",
			Help:      "Dropped pubsub messages by surface, topic, and reason.",
		}, []string{"surface", "topic", "reason"}),
		Streams: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: namespace,
			Name:      "server_streams_total",
			Help:      "Count of pubsub control and delivery stream events by kind and direction.",
		}, []string{"kind", "direction"}),
		UploadBytes: prometheus.NewHistogram(prometheus.HistogramOpts{
			Namespace: namespace,
			Name:      "server_upload_bytes",
			Help:      "Observed upload payload sizes in bytes.",
			Buckets:   prometheus.ExponentialBuckets(256, 4, 8),
		}),
		UploadDuration: prometheus.NewHistogram(prometheus.HistogramOpts{
			Namespace: namespace,
			Name:      "server_upload_duration_seconds",
			Help:      "Observed upload handler duration in seconds.",
			Buckets:   prometheus.DefBuckets,
		}),
	}
	register(reg,
		m.ConnectionsActive,
		m.ConnectionsTotal,
		m.HandshakeDuration,
		m.HTTPDuration,
		m.Bytes,
		m.PubSubSubscribers,
		m.PubSubDrops,
		m.Streams,
		m.UploadBytes,
		m.UploadDuration,
	)
	return m
}

func NewClient() *ClientMetrics {
	reg := newRegistry()
	m := &ClientMetrics{
		Registry: reg,
		Reconnects: prometheus.NewCounter(prometheus.CounterOpts{
			Namespace: namespace,
			Name:      "client_reconnects_total",
			Help:      "Number of pubsub reconnect attempts performed by the client.",
		}),
		HandshakeDuration: prometheus.NewHistogram(prometheus.HistogramOpts{
			Namespace: namespace,
			Name:      "client_handshake_duration_seconds",
			Help:      "Observed QUIC handshake duration on the client side.",
			Buckets:   prometheus.DefBuckets,
		}),
		RTT: prometheus.NewGauge(prometheus.GaugeOpts{
			Namespace: namespace,
			Name:      "client_rtt_seconds",
			Help:      "Most recently observed smoothed RTT from the active QUIC connection.",
		}),
		Datagrams: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: namespace,
			Name:      "client_datagrams_total",
			Help:      "Datagrams sent or received by the client, partitioned by direction and topic.",
		}, []string{"direction", "topic"}),
		PublishLatency: prometheus.NewHistogram(prometheus.HistogramOpts{
			Namespace: namespace,
			Name:      "client_publish_latency_seconds",
			Help:      "Observed publish-to-receive latency for stamped pubsub payloads.",
			Buckets:   prometheus.DefBuckets,
		}),
		UploadDuration: prometheus.NewHistogram(prometheus.HistogramOpts{
			Namespace: namespace,
			Name:      "client_upload_duration_seconds",
			Help:      "Observed uploader role latency in seconds.",
			Buckets:   prometheus.DefBuckets,
		}),
		PollDuration: prometheus.NewHistogram(prometheus.HistogramOpts{
			Namespace: namespace,
			Name:      "client_poll_duration_seconds",
			Help:      "Observed poller role latency in seconds.",
			Buckets:   prometheus.DefBuckets,
		}),
	}
	register(reg,
		m.Reconnects,
		m.HandshakeDuration,
		m.RTT,
		m.Datagrams,
		m.PublishLatency,
		m.UploadDuration,
		m.PollDuration,
	)
	return m
}

func NewProxy() *ProxyMetrics {
	reg := newRegistry()
	m := &ProxyMetrics{
		Registry: reg,
		SessionsActive: prometheus.NewGauge(prometheus.GaugeOpts{
			Namespace: namespace,
			Name:      "proxy_sessions_active",
			Help:      "Current active proxy NAT sessions.",
		}),
		SessionsTotal: prometheus.NewCounter(prometheus.CounterOpts{
			Namespace: namespace,
			Name:      "proxy_sessions_total",
			Help:      "Total proxy sessions opened.",
		}),
		Packets: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: namespace,
			Name:      "proxy_packets_total",
			Help:      "Observed packets by proxy direction.",
		}, []string{"direction"}),
		Bytes: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: namespace,
			Name:      "proxy_bytes_total",
			Help:      "Observed bytes by proxy direction.",
		}, []string{"direction"}),
		Drops: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: namespace,
			Name:      "proxy_dropped_total",
			Help:      "Packets dropped by impairments by direction.",
		}, []string{"direction"}),
		Duplicates: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: namespace,
			Name:      "proxy_duplicated_total",
			Help:      "Packets duplicated by impairments by direction.",
		}, []string{"direction"}),
		Reorders: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: namespace,
			Name:      "proxy_reordered_total",
			Help:      "Packets reordered by impairments by direction.",
		}, []string{"direction"}),
		EnforcedDelay: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Namespace: namespace,
			Name:      "proxy_enforced_delay_seconds",
			Help:      "Enforced impairment delay in seconds by direction.",
			Buckets:   prometheus.DefBuckets,
		}, []string{"direction"}),
	}
	register(reg,
		m.SessionsActive,
		m.SessionsTotal,
		m.Packets,
		m.Bytes,
		m.Drops,
		m.Duplicates,
		m.Reorders,
		m.EnforcedDelay,
	)
	return m
}

func Handler(reg *prometheus.Registry) http.Handler {
	return promhttp.HandlerFor(reg, promhttp.HandlerOpts{Registry: reg})
}

func newRegistry() *prometheus.Registry {
	reg := prometheus.NewRegistry()
	reg.MustRegister(prometheus.NewGoCollector())
	reg.MustRegister(prometheus.NewProcessCollector(prometheus.ProcessCollectorOpts{}))
	return reg
}

func register(reg *prometheus.Registry, collectors ...prometheus.Collector) {
	reg.MustRegister(collectors...)
}
