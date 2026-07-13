//! Lock-free counters plus a hand-written Prometheus text endpoint.
//!
//! Rust angle: every counter is an atomic, so the hot forwarding path records
//! traffic with a single `fetch_add` and no lock. The `/metrics` HTTP server is
//! written directly against a `tokio::net::TcpListener` — no `hyper`, no
//! `prometheus` crate — because the exposition format is just text and this
//! keeps the whole crate at one dependency. It fits the repo's existing
//! Prometheus scraping (server :9103, proxy :9104, client :9105, lb :9106).

use std::fmt::Write as _;
use std::net::SocketAddr;
use std::sync::atomic::{AtomicI64, AtomicU64, Ordering};
use std::sync::Arc;

use tokio::io::{AsyncReadExt, AsyncWriteExt};
use tokio::net::TcpListener;

use crate::backend::Backend;
use crate::strategy::Strategy;

#[derive(Default)]
pub struct Metrics {
    pub packets_to_server: AtomicU64,
    pub packets_to_client: AtomicU64,
    pub bytes_to_server: AtomicU64,
    pub bytes_to_client: AtomicU64,
    pub dropped_to_server: AtomicU64,
    pub dropped_to_client: AtomicU64,
    pub sessions_active: AtomicI64,
    pub sessions_total: AtomicU64,
    pub sessions_rejected: AtomicU64,
    pub quic_initials: AtomicU64,
}

impl Metrics {
    pub fn add_to_server(&self, bytes: usize) {
        self.packets_to_server.fetch_add(1, Ordering::Relaxed);
        self.bytes_to_server
            .fetch_add(bytes as u64, Ordering::Relaxed);
    }

    pub fn add_to_client(&self, bytes: usize) {
        self.packets_to_client.fetch_add(1, Ordering::Relaxed);
        self.bytes_to_client
            .fetch_add(bytes as u64, Ordering::Relaxed);
    }

    pub fn drop_to_server(&self) {
        self.dropped_to_server.fetch_add(1, Ordering::Relaxed);
    }

    pub fn drop_to_client(&self) {
        self.dropped_to_client.fetch_add(1, Ordering::Relaxed);
    }

    pub fn session_opened(&self) {
        self.sessions_active.fetch_add(1, Ordering::Relaxed);
        self.sessions_total.fetch_add(1, Ordering::Relaxed);
    }

    pub fn session_closed(&self) {
        self.sessions_active.fetch_sub(1, Ordering::Relaxed);
    }

    pub fn session_rejected(&self) {
        self.sessions_rejected.fetch_add(1, Ordering::Relaxed);
    }

    pub fn quic_initial(&self) {
        self.quic_initials.fetch_add(1, Ordering::Relaxed);
    }
}

/// Render the Prometheus text exposition for the whole load balancer.
pub fn render(metrics: &Metrics, backends: &[Arc<Backend>], strategy: Strategy) -> String {
    let mut out = String::with_capacity(2048);

    metric(
        &mut out,
        "quixiot_lb_packets_total",
        "counter",
        "Datagrams accepted for forwarding, by direction (drops counted separately).",
        &[
            (
                &[("direction", "to_server")],
                metrics.packets_to_server.load(Ordering::Relaxed) as f64,
            ),
            (
                &[("direction", "to_client")],
                metrics.packets_to_client.load(Ordering::Relaxed) as f64,
            ),
        ],
    );
    metric(
        &mut out,
        "quixiot_lb_bytes_total",
        "counter",
        "Bytes forwarded, by direction.",
        &[
            (
                &[("direction", "to_server")],
                metrics.bytes_to_server.load(Ordering::Relaxed) as f64,
            ),
            (
                &[("direction", "to_client")],
                metrics.bytes_to_client.load(Ordering::Relaxed) as f64,
            ),
        ],
    );
    metric(
        &mut out,
        "quixiot_lb_packets_dropped_total",
        "counter",
        "Datagrams dropped because a socket send buffer was full (drop-not-block).",
        &[
            (
                &[("direction", "to_server")],
                metrics.dropped_to_server.load(Ordering::Relaxed) as f64,
            ),
            (
                &[("direction", "to_client")],
                metrics.dropped_to_client.load(Ordering::Relaxed) as f64,
            ),
        ],
    );
    metric(
        &mut out,
        "quixiot_lb_sessions_active",
        "gauge",
        "Currently active client sessions.",
        &[(&[], metrics.sessions_active.load(Ordering::Relaxed) as f64)],
    );
    metric(
        &mut out,
        "quixiot_lb_sessions_total",
        "counter",
        "Client sessions opened since start.",
        &[(&[], metrics.sessions_total.load(Ordering::Relaxed) as f64)],
    );
    metric(
        &mut out,
        "quixiot_lb_sessions_rejected_total",
        "counter",
        "New sessions dropped because no backend was healthy.",
        &[(
            &[],
            metrics.sessions_rejected.load(Ordering::Relaxed) as f64,
        )],
    );
    metric(
        &mut out,
        "quixiot_lb_quic_initials_total",
        "counter",
        "QUIC Initial packets observed (new-connection attempts).",
        &[(&[], metrics.quic_initials.load(Ordering::Relaxed) as f64)],
    );

    // Per-backend series share their metric name and vary by the `backend` label.
    let up: Vec<_> = backends
        .iter()
        .map(|b| {
            (
                vec![("backend", b.label())],
                if b.is_healthy() { 1.0 } else { 0.0 },
            )
        })
        .collect();
    labeled(
        &mut out,
        "quixiot_lb_backend_up",
        "gauge",
        "1 if the backend is healthy, else 0.",
        &up,
    );

    let sess: Vec<_> = backends
        .iter()
        .map(|b| (vec![("backend", b.label())], b.active_sessions() as f64))
        .collect();
    labeled(
        &mut out,
        "quixiot_lb_backend_sessions_active",
        "gauge",
        "Active sessions per backend.",
        &sess,
    );

    let sel: Vec<_> = backends
        .iter()
        .map(|b| (vec![("backend", b.label())], b.selected_total() as f64))
        .collect();
    labeled(
        &mut out,
        "quixiot_lb_backend_selected_total",
        "counter",
        "Times each backend was chosen for a new session.",
        &sel,
    );

    metric(
        &mut out,
        "quixiot_lb_strategy_info",
        "gauge",
        "Active balancing strategy (label carries the name).",
        &[(&[("strategy", strategy.label())], 1.0)],
    );

    out
}

/// Emit `# HELP` / `# TYPE` and one line per sample, for a metric whose label
/// sets are known at the call site (fixed `&str` slices).
fn metric(
    out: &mut String,
    name: &str,
    kind: &str,
    help: &str,
    samples: &[(&[(&str, &str)], f64)],
) {
    let _ = writeln!(out, "# HELP {name} {help}");
    let _ = writeln!(out, "# TYPE {name} {kind}");
    for (labels, value) in samples {
        write_sample(out, name, labels, *value);
    }
}

/// Same as `metric` but for series built at runtime (per-backend), where labels
/// are owned `Vec`s.
fn labeled(
    out: &mut String,
    name: &str,
    kind: &str,
    help: &str,
    samples: &[(Vec<(&str, &str)>, f64)],
) {
    let _ = writeln!(out, "# HELP {name} {help}");
    let _ = writeln!(out, "# TYPE {name} {kind}");
    for (labels, value) in samples {
        write_sample(out, name, labels, *value);
    }
}

fn write_sample(out: &mut String, name: &str, labels: &[(&str, &str)], value: f64) {
    if labels.is_empty() {
        let _ = writeln!(out, "{name} {value}");
        return;
    }
    let _ = write!(out, "{name}{{");
    for (i, (k, v)) in labels.iter().enumerate() {
        if i > 0 {
            let _ = write!(out, ",");
        }
        let _ = write!(out, "{k}=\"{}\"", escape(v));
    }
    let _ = writeln!(out, "}} {value}");
}

/// Escape a Prometheus label value (backslash, double-quote, newline).
fn escape(v: &str) -> String {
    v.replace('\\', "\\\\")
        .replace('"', "\\\"")
        .replace('\n', "\\n")
}

/// Serve `/metrics` forever on `addr`. One connection at a time is plenty for a
/// scrape endpoint; each is read to the blank line and answered with the text.
pub async fn serve(
    addr: SocketAddr,
    metrics: Arc<Metrics>,
    backends: Arc<Vec<Arc<Backend>>>,
    strategy: Strategy,
) {
    let listener = match TcpListener::bind(addr).await {
        Ok(l) => l,
        Err(e) => {
            error!("metrics endpoint failed to bind {addr}: {e}");
            return;
        }
    };
    info!("metrics endpoint listening on http://{addr}/metrics");

    loop {
        let (mut sock, _peer) = match listener.accept().await {
            Ok(pair) => pair,
            Err(e) => {
                error!("metrics accept failed: {e}");
                continue;
            }
        };
        let body = render(&metrics, &backends, strategy);
        tokio::spawn(async move {
            // Drain the request line(s); we ignore the path and always serve
            // metrics, which is all a Prometheus scrape needs.
            let mut scratch = [0u8; 1024];
            let _ = sock.read(&mut scratch).await;
            let response = format!(
                "HTTP/1.1 200 OK\r\nContent-Type: text/plain; version=0.0.4\r\nContent-Length: {}\r\nConnection: close\r\n\r\n{}",
                body.len(),
                body
            );
            let _ = sock.write_all(response.as_bytes()).await;
            let _ = sock.shutdown().await;
        });
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn renders_expected_families() {
        let metrics = Metrics::default();
        metrics.add_to_server(100);
        metrics.drop_to_client();
        metrics.session_opened();
        let backends = vec![Arc::new(Backend::new("127.0.0.1:4444".parse().unwrap()))];
        backends[0].record_selected();
        let text = render(&metrics, &backends, Strategy::RoundRobin);

        assert!(text.contains("quixiot_lb_packets_total{direction=\"to_server\"} 1"));
        assert!(text.contains("quixiot_lb_bytes_total{direction=\"to_server\"} 100"));
        assert!(text.contains("quixiot_lb_packets_dropped_total{direction=\"to_client\"} 1"));
        assert!(text.contains("quixiot_lb_sessions_active 1"));
        assert!(text.contains("quixiot_lb_backend_up{backend=\"127.0.0.1:4444\"} 1"));
        assert!(text.contains("quixiot_lb_backend_selected_total{backend=\"127.0.0.1:4444\"} 1"));
        assert!(text.contains("quixiot_lb_strategy_info{strategy=\"round-robin\"} 1"));
    }
}
