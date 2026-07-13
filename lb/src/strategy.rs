//! Backend-selection strategies.
//!
//! Rust angle: the set of strategies is a closed `enum`, so `select` uses an
//! exhaustive `match` — add a variant and the compiler forces you to handle it
//! here. Selection only ever considers *healthy* backends, and returns
//! `Option<Arc<Backend>>` so "every backend is down" is a value the caller must
//! handle, not a null that panics later.

use std::net::IpAddr;
use std::sync::atomic::{AtomicU64, AtomicUsize, Ordering};
use std::sync::Arc;

use crate::backend::Backend;

#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub enum Strategy {
    RoundRobin,
    LeastConnections,
    Random,
    IpHash,
}

impl Strategy {
    pub fn parse(s: &str) -> Option<Strategy> {
        match s.to_ascii_lowercase().as_str() {
            "round-robin" | "roundrobin" | "rr" => Some(Strategy::RoundRobin),
            "least-conn" | "least-connections" | "leastconn" | "lc" => {
                Some(Strategy::LeastConnections)
            }
            "random" | "rand" => Some(Strategy::Random),
            "ip-hash" | "iphash" | "hash" => Some(Strategy::IpHash),
            _ => None,
        }
    }

    pub fn label(self) -> &'static str {
        match self {
            Strategy::RoundRobin => "round-robin",
            Strategy::LeastConnections => "least-conn",
            Strategy::Random => "random",
            Strategy::IpHash => "ip-hash",
        }
    }
}

/// Mutable bits a `Strategy` needs between calls: a round-robin cursor and an
/// xorshift RNG seed. Both are atomics, so `select` takes `&self` and stays
/// callable from the hot receive path without a lock.
pub struct Selector {
    strategy: Strategy,
    rr_cursor: AtomicUsize,
    rng: AtomicU64,
}

impl Selector {
    pub fn new(strategy: Strategy, seed: u64) -> Self {
        Selector {
            strategy,
            rr_cursor: AtomicUsize::new(0),
            // xorshift must never be seeded with 0.
            rng: AtomicU64::new(seed | 1),
        }
    }

    /// Pick a healthy backend for a new session from `client`, or `None` if all
    /// backends are currently unhealthy.
    pub fn select(&self, backends: &[Arc<Backend>], client: IpAddr) -> Option<Arc<Backend>> {
        // Collect the healthy subset once; every strategy operates on it so an
        // unhealthy backend is never handed a new session.
        let healthy: Vec<&Arc<Backend>> = backends.iter().filter(|b| b.is_healthy()).collect();
        if healthy.is_empty() {
            return None;
        }

        let chosen = match self.strategy {
            Strategy::RoundRobin => {
                // fetch_add wraps; modulo maps it onto the healthy subset. Note
                // the cursor counts *attempts*, not backends, so as backends flap
                // healthy/unhealthy the distribution stays roughly even.
                let n = self.rr_cursor.fetch_add(1, Ordering::Relaxed);
                healthy[n % healthy.len()]
            }
            Strategy::LeastConnections => healthy
                .iter()
                .min_by_key(|b| b.active_sessions())
                .copied()
                .expect("healthy is non-empty"),
            Strategy::Random => {
                let r = self.next_rand() as usize;
                healthy[r % healthy.len()]
            }
            Strategy::IpHash => {
                // Deterministic per client IP: the same device maps to the same
                // backend as long as that backend stays healthy.
                let h = hash_ip(client) as usize;
                healthy[h % healthy.len()]
            }
        };
        Some(Arc::clone(chosen))
    }

    /// xorshift64* — a tiny, fast, dependency-free PRNG. Good enough to spread
    /// sessions across backends; not for anything cryptographic.
    fn next_rand(&self) -> u64 {
        let mut x = self.rng.load(Ordering::Relaxed);
        x ^= x >> 12;
        x ^= x << 25;
        x ^= x >> 27;
        self.rng.store(x, Ordering::Relaxed);
        x.wrapping_mul(0x2545_F491_4F6C_DD1D)
    }
}

/// 64-bit FNV-1a. Hand-rolled so we pull in no hashing crate and the algorithm
/// stays visible.
fn fnv1a(bytes: &[u8]) -> u64 {
    let mut hash: u64 = 0xcbf2_9ce4_8422_2325;
    for &b in bytes {
        hash ^= b as u64;
        hash = hash.wrapping_mul(0x0000_0100_0000_01b3);
    }
    hash
}

/// Hash an IP without allocating: `octets()` returns a fixed-size array on the
/// stack for both families, so this is heap-free on the selection path.
fn hash_ip(ip: IpAddr) -> u64 {
    match ip {
        IpAddr::V4(v4) => fnv1a(&v4.octets()),
        IpAddr::V6(v6) => fnv1a(&v6.octets()),
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use std::net::SocketAddr;

    fn backend(port: u16) -> Arc<Backend> {
        let addr: SocketAddr = format!("127.0.0.1:{port}").parse().unwrap();
        Arc::new(Backend::new(addr))
    }

    fn ip() -> IpAddr {
        "127.0.0.1".parse().unwrap()
    }

    #[test]
    fn round_robin_cycles() {
        let backends = vec![backend(1), backend(2), backend(3)];
        let sel = Selector::new(Strategy::RoundRobin, 1);
        let picks: Vec<_> = (0..6)
            .map(|_| sel.select(&backends, ip()).unwrap().addr().port())
            .collect();
        assert_eq!(picks, vec![1, 2, 3, 1, 2, 3]);
    }

    #[test]
    fn round_robin_skips_unhealthy() {
        let backends = vec![backend(1), backend(2), backend(3)];
        backends[1].mark_down(); // take b2 out
        let sel = Selector::new(Strategy::RoundRobin, 1);
        let ports: Vec<_> = (0..10)
            .map(|_| sel.select(&backends, ip()).unwrap().addr().port())
            .collect();
        assert!(!ports.contains(&2), "unhealthy backend must not be picked");
    }

    #[test]
    fn least_connections_prefers_idle() {
        let backends = vec![backend(1), backend(2)];
        backends[0].inc_sessions();
        backends[0].inc_sessions();
        let sel = Selector::new(Strategy::LeastConnections, 1);
        assert_eq!(sel.select(&backends, ip()).unwrap().addr().port(), 2);
    }

    #[test]
    fn ip_hash_is_stable() {
        let backends = vec![backend(1), backend(2), backend(3)];
        let sel = Selector::new(Strategy::IpHash, 1);
        let a = sel.select(&backends, ip()).unwrap().addr().port();
        let b = sel.select(&backends, ip()).unwrap().addr().port();
        assert_eq!(a, b, "same IP must map to the same backend");
    }

    #[test]
    fn none_when_all_down() {
        let backends = vec![backend(1), backend(2)];
        for b in &backends {
            b.mark_down();
        }
        let sel = Selector::new(Strategy::RoundRobin, 1);
        assert!(sel.select(&backends, ip()).is_none());
    }
}
