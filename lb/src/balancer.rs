//! The forwarding core: one client-facing UDP socket, a sticky session table,
//! and a per-session upstream socket that carries replies back.
//!
//! Design (mirrors the Go impairment proxy in `internal/proxy`, so the two are
//! directly comparable):
//!   * One task owns the listen socket and demultiplexes by client address.
//!   * The first datagram from a new client picks a backend (via the strategy),
//!     opens a dedicated upstream socket `connect`ed to that backend, and spawns
//!     a *return task* that pumps replies back to the client.
//!   * Every later datagram from that client reuses the same session — so all of
//!     a QUIC connection's packets land on one backend, which QUIC requires.
//!   * An idle sweeper evicts sessions that go quiet.
//!
//! Rust angle: the session table is `Arc<Mutex<HashMap<..>>>` shared by the
//! receive loop, every return task, and the sweeper. Each `Session` is an `Arc`,
//! and teardown is made exactly-once by an `AtomicBool` compare-and-swap — so
//! the "who frees it?" question the Go version answers with `sync.Once` +
//! channel closes is answered here by ownership and one atomic flag. Cancelling
//! a return task uses `Notify` (permit-storing `notify_one`, so there is no
//! wake-up race).

use std::collections::HashMap;
use std::io::ErrorKind;
use std::net::SocketAddr;
use std::sync::atomic::{AtomicBool, AtomicI64, Ordering};
use std::sync::{Arc, Mutex};
use std::time::{Duration, Instant};

use tokio::net::UdpSocket;
use tokio::sync::Notify;

use crate::backend::Backend;
use crate::metrics::Metrics;
use crate::strategy::Selector;
use crate::{net, quic};

/// Max UDP datagram we will read in one go. Matches the Go proxy's 64 KiB, which
/// comfortably covers QUIC's ~1200–1500 byte packets plus any GSO batching.
const MAX_DATAGRAM: usize = 64 * 1024;
const SWEEP_INTERVAL: Duration = Duration::from_secs(30);

pub struct Balancer {
    listen: Arc<UdpSocket>,
    backends: Arc<Vec<Arc<Backend>>>,
    selector: Selector,
    metrics: Arc<Metrics>,
    idle_timeout: Duration,
    sessions: Mutex<HashMap<SocketAddr, Arc<Session>>>,
    /// True once we've warned that the whole pool is down; cleared when a
    /// session opens again. QUIC clients retransmit Initials aggressively, so
    /// without this a dead pool would log one warning per datagram.
    pool_down_warned: AtomicBool,
}

/// One client's flow. Owns the upstream socket; the return task holds an `Arc`
/// of this so the socket outlives the receive loop's view of it.
struct Session {
    client: SocketAddr,
    backend: Arc<Backend>,
    upstream: Arc<UdpSocket>,
    last_seen_millis: AtomicI64,
    shutdown: Notify,
    closed: AtomicBool,
}

impl Session {
    fn touch(&self) {
        self.last_seen_millis.store(now_millis(), Ordering::Relaxed);
    }

    fn idle_for(&self) -> Duration {
        let last = self.last_seen_millis.load(Ordering::Relaxed);
        let delta = now_millis().saturating_sub(last);
        Duration::from_millis(delta.max(0) as u64)
    }
}

impl Balancer {
    pub fn new(
        listen: UdpSocket,
        backends: Arc<Vec<Arc<Backend>>>,
        selector: Selector,
        metrics: Arc<Metrics>,
        idle_timeout: Duration,
    ) -> Arc<Self> {
        Arc::new(Balancer {
            listen: Arc::new(listen),
            backends,
            selector,
            metrics,
            idle_timeout,
            sessions: Mutex::new(HashMap::new()),
            pool_down_warned: AtomicBool::new(false),
        })
    }

    /// Run until the socket errors unrecoverably or `shutdown` fires.
    pub async fn run(self: Arc<Self>, shutdown: Arc<Notify>) {
        // Prime the listen socket's write readiness so the return tasks'
        // try_send_to doesn't spuriously drop the first replies (see
        // dial_upstream for the full story).
        if let Err(e) = self.listen.writable().await {
            error!("listen socket is unusable for writes: {e}");
            return;
        }
        let sweeper = tokio::spawn(Arc::clone(&self).sweep_loop());

        let mut buf = vec![0u8; MAX_DATAGRAM];
        loop {
            let recv = self.listen.recv_from(&mut buf);
            let (n, client) = tokio::select! {
                _ = shutdown.notified() => {
                    info!("shutdown requested; stopping accept loop");
                    break;
                }
                result = recv => match result {
                    Ok(pair) => pair,
                    Err(e) => {
                        error!("recv_from on listen socket failed: {e}");
                        break;
                    }
                },
            };

            self.observe_quic(&buf[..n]);

            let session = match self.get_or_create(client).await {
                Some(s) => s,
                None => continue, // no healthy backend; already counted+logged
            };
            session.touch();

            self.metrics.add_to_server(n);
            // try_send, not send().await: this loop serves *every* client, so it
            // must never park on one session's socket. If the send buffer is
            // full we drop the datagram — UDP semantics, QUIC recovers — and
            // count it, instead of stalling the whole balancer (drop-not-block).
            match session.upstream.try_send(&buf[..n]) {
                Ok(_) => {}
                Err(e) if e.kind() == ErrorKind::WouldBlock => self.metrics.drop_to_server(),
                Err(e) => self.handle_upstream_error(&session, e, "send to upstream"),
            }
        }

        sweeper.abort();
        self.close_all();
    }

    /// Count QUIC Initial packets so `quixiot_lb_quic_initials_total` tracks
    /// new-connection attempts. Parsing is best-effort; non-QUIC bytes are fine.
    fn observe_quic(&self, datagram: &[u8]) {
        if let Some(header) = quic::parse_header(datagram) {
            if header.is_initial() {
                self.metrics.quic_initial();
            }
        }
    }

    async fn get_or_create(self: &Arc<Self>, client: SocketAddr) -> Option<Arc<Session>> {
        // Fast path: existing session under a short lock.
        if let Some(session) = self.sessions.lock().unwrap().get(&client).cloned() {
            return Some(session);
        }

        // Choose a backend and open the upstream socket *before* taking the lock
        // again, so the await points don't hold the mutex across .await.
        let backend = match self.selector.select(&self.backends, client.ip()) {
            Some(b) => b,
            None => {
                self.metrics.session_rejected();
                // swap returns the previous value, so exactly one caller wins
                // the edge and warns; the rest stay at debug until recovery.
                if !self.pool_down_warned.swap(true, Ordering::Relaxed) {
                    warn!(
                        "no healthy backend for new client {client}; dropping packets \
                         (suppressing further warnings until the pool recovers)"
                    );
                } else {
                    debug!("no healthy backend for new client {client}; dropping packet");
                }
                return None;
            }
        };

        let upstream = match self.dial_upstream(&backend).await {
            Ok(sock) => Arc::new(sock),
            Err(e) => {
                // A failure to even open the socket is a strong down signal.
                backend.mark_down();
                self.metrics.session_rejected();
                warn!("failed to open upstream socket to {}: {e}", backend.label());
                return None;
            }
        };

        let session = Arc::new(Session {
            client,
            backend: Arc::clone(&backend),
            upstream,
            last_seen_millis: AtomicI64::new(now_millis()),
            shutdown: Notify::new(),
            closed: AtomicBool::new(false),
        });

        // Insert, guarding against a racing datagram that created the same
        // session first: if we lost the race, drop ours and use theirs.
        {
            let mut map = self.sessions.lock().unwrap();
            if let Some(existing) = map.get(&client).cloned() {
                drop(map);
                session.closed.store(true, Ordering::Relaxed); // ours never counted
                return Some(existing);
            }
            map.insert(client, Arc::clone(&session));
        }

        backend.inc_sessions();
        backend.record_selected();
        self.metrics.session_opened();
        // Pool is serving again; re-arm the all-backends-down warning.
        self.pool_down_warned.store(false, Ordering::Relaxed);
        info!(
            "opened session {} -> {} ({} active on backend)",
            client,
            backend.label(),
            backend.active_sessions()
        );

        tokio::spawn(Arc::clone(self).return_loop(Arc::clone(&session)));
        Some(session)
    }

    async fn dial_upstream(&self, backend: &Backend) -> std::io::Result<UdpSocket> {
        // Sized kernel buffers (like the Go proxy's 8 MiB) so return-path
        // bursts from the backend aren't dropped at the socket before we can
        // read them.
        let (sock, _granted) = net::bind_udp(net::wildcard_for(backend.addr()), net::BUFFER_BYTES)?;
        sock.connect(backend.addr()).await?;
        // Prime write readiness once. tokio's try_send consults the reactor's
        // *cached* readiness, and a freshly registered socket hasn't seen its
        // first writable event yet — without this await the session's first
        // try_send spuriously reports WouldBlock and we'd drop the packet.
        // After priming, WouldBlock from try_send means "buffer really full".
        sock.writable().await?;
        Ok(sock)
    }

    /// Pump replies from one backend back to the client until the session is
    /// cancelled or the upstream socket errors.
    async fn return_loop(self: Arc<Self>, session: Arc<Session>) {
        let mut buf = vec![0u8; MAX_DATAGRAM];
        loop {
            tokio::select! {
                _ = session.shutdown.notified() => break,
                result = session.upstream.recv(&mut buf) => match result {
                    Ok(n) => {
                        session.touch();
                        self.metrics.add_to_client(n);
                        // Same drop-not-block rationale as the accept loop: the
                        // listen socket is shared by every session's return
                        // task, so nobody gets to park on it.
                        match self.listen.try_send_to(&buf[..n], session.client) {
                            Ok(_) => {}
                            Err(e) if e.kind() == ErrorKind::WouldBlock => {
                                self.metrics.drop_to_client()
                            }
                            Err(e) => {
                                warn!("send to client {} failed: {e}", session.client);
                                break;
                            }
                        }
                    }
                    Err(e) => {
                        self.handle_upstream_error(&session, e, "recv from upstream");
                        break;
                    }
                },
            }
        }
        self.remove_session(&session);
    }

    /// Classify an upstream I/O error. `ConnectionRefused` (a connected UDP
    /// socket surfacing ICMP port-unreachable) means the backend is gone, so we
    /// mark it down at once — passive failure detection.
    fn handle_upstream_error(&self, session: &Arc<Session>, e: std::io::Error, ctx: &str) {
        if e.kind() == ErrorKind::ConnectionRefused {
            warn!(
                "{ctx} refused by {} — marking backend down (passive)",
                session.backend.label()
            );
            session.backend.mark_down();
        } else {
            debug!("{ctx} for {} failed: {e}", session.backend.label());
        }
        self.remove_session(session);
    }

    async fn sweep_loop(self: Arc<Self>) {
        let mut ticker = tokio::time::interval(SWEEP_INTERVAL);
        loop {
            ticker.tick().await;
            let mut expired = Vec::new();
            {
                let mut map = self.sessions.lock().unwrap();
                map.retain(|_, s| {
                    if s.idle_for() > self.idle_timeout {
                        expired.push(Arc::clone(s));
                        false
                    } else {
                        true
                    }
                });
            }
            for session in expired {
                info!(
                    "evicting idle session {} (idle {:?})",
                    session.client,
                    session.idle_for()
                );
                self.finish(&session);
            }
        }
    }

    /// Remove a session from the table (if it's still the current one) and tear
    /// it down. Safe to call from either the receive loop or a return task.
    fn remove_session(&self, session: &Arc<Session>) {
        {
            let mut map = self.sessions.lock().unwrap();
            if let Some(current) = map.get(&session.client) {
                if Arc::ptr_eq(current, session) {
                    map.remove(&session.client);
                }
            }
        }
        self.finish(session);
    }

    /// Exactly-once teardown: the first caller to flip `closed` wakes the return
    /// task and releases the accounting; later callers are no-ops.
    fn finish(&self, session: &Arc<Session>) {
        if session.closed.swap(true, Ordering::AcqRel) {
            return;
        }
        session.shutdown.notify_one();
        session.backend.dec_sessions();
        self.metrics.session_closed();
    }

    fn close_all(&self) {
        let sessions: Vec<Arc<Session>> = {
            let mut map = self.sessions.lock().unwrap();
            map.drain().map(|(_, s)| s).collect()
        };
        for session in sessions {
            self.finish(&session);
        }
    }
}

fn now_millis() -> i64 {
    // Anchored to a process-start `Instant` so the clock is monotonic — an NTP
    // step can't make a session look negatively idle.
    use std::sync::OnceLock;
    static START: OnceLock<Instant> = OnceLock::new();
    let start = START.get_or_init(Instant::now);
    start.elapsed().as_millis() as i64
}

#[cfg(test)]
mod tests {
    use super::*;
    use crate::strategy::Strategy;
    use tokio::time::timeout;

    /// Spawn a UDP echo server that prefixes every reply with `tag`, so a test
    /// client can tell which backend answered.
    async fn spawn_tagged_echo(tag: u8) -> SocketAddr {
        let sock = UdpSocket::bind("127.0.0.1:0").await.unwrap();
        let addr = sock.local_addr().unwrap();
        tokio::spawn(async move {
            let mut buf = [0u8; 2048];
            loop {
                let Ok((n, peer)) = sock.recv_from(&mut buf).await else {
                    return;
                };
                let mut reply = Vec::with_capacity(n + 1);
                reply.push(tag);
                reply.extend_from_slice(&buf[..n]);
                let _ = sock.send_to(&reply, peer).await;
            }
        });
        addr
    }

    struct Harness {
        lb_addr: SocketAddr,
        backends: Arc<Vec<Arc<Backend>>>,
        metrics: Arc<Metrics>,
        shutdown: Arc<Notify>,
    }

    async fn start_balancer(backend_addrs: &[SocketAddr], strategy: Strategy) -> Harness {
        let listen = UdpSocket::bind("127.0.0.1:0").await.unwrap();
        let lb_addr = listen.local_addr().unwrap();
        let backends: Arc<Vec<Arc<Backend>>> = Arc::new(
            backend_addrs
                .iter()
                .map(|a| Arc::new(Backend::new(*a)))
                .collect(),
        );
        let metrics = Arc::new(Metrics::default());
        let balancer = Balancer::new(
            listen,
            Arc::clone(&backends),
            Selector::new(strategy, 7),
            Arc::clone(&metrics),
            Duration::from_secs(60),
        );
        let shutdown = Arc::new(Notify::new());
        tokio::spawn(balancer.run(Arc::clone(&shutdown)));
        Harness {
            lb_addr,
            backends,
            metrics,
            shutdown,
        }
    }

    /// Send `payload` through the LB and return the tag byte of the reply.
    async fn round_trip(client: &UdpSocket, lb: SocketAddr, payload: &[u8]) -> u8 {
        client.send_to(payload, lb).await.unwrap();
        let mut buf = [0u8; 2048];
        let (n, _) = timeout(Duration::from_secs(2), client.recv_from(&mut buf))
            .await
            .expect("reply within 2s")
            .unwrap();
        assert_eq!(&buf[1..n], payload, "payload must round-trip unmodified");
        buf[0]
    }

    #[tokio::test]
    async fn sticky_sessions_and_round_robin_distribution() {
        let b1 = spawn_tagged_echo(b'A').await;
        let b2 = spawn_tagged_echo(b'B').await;
        let h = start_balancer(&[b1, b2], Strategy::RoundRobin).await;

        // Two clients, several packets each: every packet from one client must
        // hit the same backend (stickiness), and the two clients must land on
        // different backends (round-robin across new sessions).
        let c1 = UdpSocket::bind("127.0.0.1:0").await.unwrap();
        let c2 = UdpSocket::bind("127.0.0.1:0").await.unwrap();

        let c1_tags: Vec<u8> = [
            round_trip(&c1, h.lb_addr, b"one").await,
            round_trip(&c1, h.lb_addr, b"two").await,
            round_trip(&c1, h.lb_addr, b"three").await,
        ]
        .into();
        let c2_tag = round_trip(&c2, h.lb_addr, b"four").await;

        assert!(
            c1_tags.iter().all(|&t| t == c1_tags[0]),
            "client 1 must stick: {c1_tags:?}"
        );
        assert_ne!(
            c1_tags[0], c2_tag,
            "round-robin must spread two clients over two backends"
        );

        assert_eq!(h.metrics.sessions_total.load(Ordering::Relaxed), 2);
        assert_eq!(h.metrics.sessions_active.load(Ordering::Relaxed), 2);
        let selected: u64 = h.backends.iter().map(|b| b.selected_total()).sum();
        assert_eq!(selected, 2);
        h.shutdown.notify_one();
    }

    #[tokio::test]
    async fn rejects_new_sessions_when_pool_is_down() {
        let b1 = spawn_tagged_echo(b'A').await;
        let h = start_balancer(&[b1], Strategy::RoundRobin).await;
        h.backends[0].mark_down();

        let client = UdpSocket::bind("127.0.0.1:0").await.unwrap();
        client.send_to(b"hello", h.lb_addr).await.unwrap();
        client.send_to(b"hello", h.lb_addr).await.unwrap();

        // No reply should come back...
        let mut buf = [0u8; 64];
        let got = timeout(Duration::from_millis(300), client.recv_from(&mut buf)).await;
        assert!(got.is_err(), "packets to a down pool must be dropped");

        // ...and the drops must be visible in metrics, not silent.
        let deadline = Instant::now() + Duration::from_secs(2);
        loop {
            if h.metrics.sessions_rejected.load(Ordering::Relaxed) >= 2 {
                break;
            }
            assert!(Instant::now() < deadline, "rejections never counted");
            tokio::time::sleep(Duration::from_millis(20)).await;
        }
        assert_eq!(h.metrics.sessions_total.load(Ordering::Relaxed), 0);
        h.shutdown.notify_one();
    }

    #[tokio::test]
    async fn recovered_backend_receives_new_sessions() {
        let b1 = spawn_tagged_echo(b'A').await;
        let b2 = spawn_tagged_echo(b'B').await;
        let h = start_balancer(&[b1, b2], Strategy::RoundRobin).await;

        // With backend A down, both clients must land on B.
        h.backends[0].mark_down();
        let c1 = UdpSocket::bind("127.0.0.1:0").await.unwrap();
        let c2 = UdpSocket::bind("127.0.0.1:0").await.unwrap();
        assert_eq!(round_trip(&c1, h.lb_addr, b"x").await, b'B');
        assert_eq!(round_trip(&c2, h.lb_addr, b"y").await, b'B');

        // After recovery a fresh client can land on A again.
        h.backends[0].mark_up();
        let mut saw_a = false;
        for _ in 0..4 {
            let c = UdpSocket::bind("127.0.0.1:0").await.unwrap();
            if round_trip(&c, h.lb_addr, b"z").await == b'A' {
                saw_a = true;
                break;
            }
        }
        assert!(saw_a, "recovered backend never selected");
        h.shutdown.notify_one();
    }
}
