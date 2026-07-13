//! A single upstream server and its live state.
//!
//! Rust angle: a `Backend` is shared by the receive loop, every session's
//! return task, and the health checker at once. All of its mutable state is
//! therefore atomics, and it is only ever handed around as `Arc<Backend>`.
//! `Send + Sync` are auto-derived because every field is `Send + Sync`, so the
//! compiler — not a code review — guarantees there is no data race.

use std::net::SocketAddr;
use std::sync::atomic::{AtomicBool, AtomicI64, AtomicU64, Ordering};

pub struct Backend {
    addr: SocketAddr,
    /// Label used in logs and metrics; the address string is stable and unique.
    label: String,
    healthy: AtomicBool,
    active_sessions: AtomicI64,
    selected_total: AtomicU64,
    /// Consecutive active-probe failures; reset to 0 on any success.
    health_failures: AtomicU64,
}

impl Backend {
    pub fn new(addr: SocketAddr) -> Self {
        Backend {
            addr,
            label: addr.to_string(),
            // Optimistic: assume up until a probe or a live error says otherwise.
            healthy: AtomicBool::new(true),
            active_sessions: AtomicI64::new(0),
            selected_total: AtomicU64::new(0),
            health_failures: AtomicU64::new(0),
        }
    }

    pub fn addr(&self) -> SocketAddr {
        self.addr
    }

    pub fn label(&self) -> &str {
        &self.label
    }

    pub fn is_healthy(&self) -> bool {
        self.healthy.load(Ordering::Relaxed)
    }

    pub fn active_sessions(&self) -> i64 {
        self.active_sessions.load(Ordering::Relaxed)
    }

    pub fn selected_total(&self) -> u64 {
        self.selected_total.load(Ordering::Relaxed)
    }

    pub fn inc_sessions(&self) {
        self.active_sessions.fetch_add(1, Ordering::Relaxed);
    }

    pub fn dec_sessions(&self) {
        self.active_sessions.fetch_sub(1, Ordering::Relaxed);
    }

    pub fn record_selected(&self) {
        self.selected_total.fetch_add(1, Ordering::Relaxed);
    }

    /// Mark healthy and clear the failure streak. Logs only on the down→up edge
    /// so steady-state probing stays quiet.
    pub fn mark_up(&self) {
        self.health_failures.store(0, Ordering::Relaxed);
        if !self.healthy.swap(true, Ordering::Relaxed) {
            info!("backend {} recovered -> healthy", self.label);
        }
    }

    /// Mark unhealthy immediately. Logs only on the up→down edge.
    pub fn mark_down(&self) {
        if self.healthy.swap(false, Ordering::Relaxed) {
            warn!("backend {} marked unhealthy", self.label);
        }
    }

    /// Record one active-probe failure. Once the streak reaches `threshold` the
    /// backend flips unhealthy. Returns the new streak length.
    pub fn record_probe_failure(&self, threshold: u32) -> u64 {
        let failures = self.health_failures.fetch_add(1, Ordering::Relaxed) + 1;
        if failures >= threshold as u64 {
            self.mark_down();
        }
        failures
    }
}
