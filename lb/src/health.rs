//! Active health checking via QUIC Version Negotiation.
//!
//! We can't TCP-connect to probe liveness — on this branch the QuixIoT server
//! is UDP/QUIC only. Instead we use a spec-defined trick (RFC 9000 §6.1): a
//! server that receives a long-header packet carrying a QUIC version it doesn't
//! recognize MUST reply with a Version Negotiation packet. So we send a
//! well-formed long-header packet with a deliberately reserved version; any
//! reply means "alive", silence means "probably down".
//!
//! This complements the *passive* detection in the balancer (a connected UDP
//! socket surfaces `ECONNREFUSED` the moment a backend dies): passive catches
//! failures instantly, active detects recovery so a backend can rejoin the pool.
//!
//! Rust angle: the whole checker is one `async` task that owns its probe
//! sockets; backends are shared as `Arc`, and `tokio::time::timeout` turns "no
//! reply within N ms" into an ordinary `Result` branch instead of a callback.

use std::sync::Arc;

use tokio::net::UdpSocket;
use tokio::time::{self, Duration};

use crate::backend::Backend;
use crate::config::Config;

/// A reserved QUIC version guaranteed not to be supported, so servers answer
/// with Version Negotiation. 0x?a?a?a?a values are the "GREASE" pattern from
/// RFC 9287 and are reserved to always be unsupported.
const PROBE_VERSION: u32 = 0x1a2a_3a4a;

/// QUIC forbids an Initial packet smaller than 1200 bytes; padding the probe to
/// the same size sidesteps any anti-amplification size checks on the server.
const PROBE_LEN: usize = 1200;

pub fn spawn(config: Arc<Config>, backends: Arc<Vec<Arc<Backend>>>) {
    if !config.health_active {
        info!("active health checks disabled; relying on passive failure detection only");
        return;
    }
    info!(
        "active health checks on: every {:?}, {:?} timeout, {} failures = down",
        config.health_interval, config.health_timeout, config.health_fail_threshold
    );
    for backend in backends.iter().cloned() {
        let config = Arc::clone(&config);
        tokio::spawn(probe_loop(config, backend));
    }
}

async fn probe_loop(config: Arc<Config>, backend: Arc<Backend>) {
    let mut ticker = time::interval(config.health_interval);
    // If a tick is missed (e.g. a slow probe), skip it rather than firing a burst.
    ticker.set_missed_tick_behavior(time::MissedTickBehavior::Skip);

    // One socket for the lifetime of the loop. UDP `connect` records the peer
    // address without any handshake, so it stays valid across backend restarts;
    // re-binding every 2s would churn ephemeral ports for nothing.
    let sock = match bind_probe_socket(backend.addr()).await {
        Ok(s) => s,
        Err(e) => {
            // Can't even create a local socket — probing is impossible; leave
            // the backend to passive detection rather than spin.
            crate::error!("health probe socket for {} failed: {e}", backend.label());
            return;
        }
    };

    loop {
        ticker.tick().await;
        match probe_once(&sock, config.health_timeout).await {
            Ok(()) => backend.mark_up(),
            Err(reason) => {
                let streak = backend.record_probe_failure(config.health_fail_threshold);
                debug!(
                    "backend {} probe failed ({reason}); streak={streak}",
                    backend.label()
                );
            }
        }
    }
}

async fn bind_probe_socket(peer: std::net::SocketAddr) -> std::io::Result<UdpSocket> {
    let sock = UdpSocket::bind(crate::net::wildcard_for(peer)).await?;
    sock.connect(peer).await?;
    Ok(sock)
}

/// Send one Version Negotiation probe on `sock` and wait for any reply.
async fn probe_once(sock: &UdpSocket, timeout: Duration) -> Result<(), String> {
    // Drain anything already queued (e.g. a late reply to the *previous*
    // probe), so a stale packet can't be credited to this round. try_recv
    // returns WouldBlock once empty; any other error will resurface below.
    let mut buf = [0u8; 2048];
    while sock.try_recv(&mut buf).is_ok() {}

    let packet = build_probe();
    sock.send(&packet).await.map_err(|e| format!("send: {e}"))?;

    match time::timeout(timeout, sock.recv(&mut buf)).await {
        Ok(Ok(_n)) => Ok(()),                    // any reply -> alive
        Ok(Err(e)) => Err(format!("recv: {e}")), // e.g. ECONNREFUSED -> dead
        Err(_) => Err("timeout".into()),
    }
}

/// Build a minimal long-header packet with a reserved version so the server
/// answers with Version Negotiation. Layout mirrors `quic::parse_header`.
fn build_probe() -> [u8; PROBE_LEN] {
    let mut pkt = [0u8; PROBE_LEN];
    pkt[0] = 0xC0; // long header, fixed bit set
    pkt[1..5].copy_from_slice(&PROBE_VERSION.to_be_bytes());
    // 8-byte DCID then 8-byte SCID, contents arbitrary. A fresh nonce each time
    // keeps the probe from looking like a replay to any stateful middlebox.
    let nonce = nonce_bytes();
    pkt[5] = 8;
    pkt[6..14].copy_from_slice(&nonce);
    pkt[14] = 8;
    pkt[15..23].copy_from_slice(&nonce);
    // pkt[23..] stays zero padding, bringing the datagram to PROBE_LEN.
    pkt
}

/// 8 pseudo-random bytes from the system clock — no `rand` crate needed; probe
/// connection IDs don't need cryptographic randomness.
fn nonce_bytes() -> [u8; 8] {
    let nanos = std::time::SystemTime::now()
        .duration_since(std::time::UNIX_EPOCH)
        .map(|d| d.as_nanos() as u64)
        .unwrap_or(0);
    // scramble a little so successive calls in the same nanosecond still differ
    nanos.wrapping_mul(0x9E37_79B9_7F4A_7C15).to_le_bytes()
}

#[cfg(test)]
mod tests {
    use super::*;

    /// A fake server that answers every datagram with a few bytes — good
    /// enough, since probe_once treats *any* reply as alive.
    async fn spawn_responder() -> std::net::SocketAddr {
        let sock = UdpSocket::bind("127.0.0.1:0").await.unwrap();
        let addr = sock.local_addr().unwrap();
        tokio::spawn(async move {
            let mut buf = [0u8; 2048];
            loop {
                let Ok((_, peer)) = sock.recv_from(&mut buf).await else {
                    return;
                };
                let _ = sock.send_to(b"vn", peer).await;
            }
        });
        addr
    }

    #[tokio::test]
    async fn probe_succeeds_against_a_responder() {
        let addr = spawn_responder().await;
        let sock = bind_probe_socket(addr).await.unwrap();
        probe_once(&sock, Duration::from_secs(2)).await.unwrap();
        // And again on the same socket — the reuse path.
        probe_once(&sock, Duration::from_secs(2)).await.unwrap();
    }

    #[tokio::test]
    async fn probe_fails_when_nobody_answers() {
        // A bound-but-silent socket never replies, which exercises the timeout
        // path. (An *unbound* port would give ECONNREFUSED instead, but ICMP
        // delivery timing makes that flaky to assert portably.)
        let silent = UdpSocket::bind("127.0.0.1:0").await.unwrap();
        let addr = silent.local_addr().unwrap();
        let sock = bind_probe_socket(addr).await.unwrap();
        let err = probe_once(&sock, Duration::from_millis(150))
            .await
            .unwrap_err();
        assert!(
            err.contains("timeout") || err.contains("recv"),
            "got: {err}"
        );
    }

    #[test]
    fn probe_packet_is_a_valid_long_header_with_grease_version() {
        // Our own parser must agree the probe is a long-header packet carrying
        // the GREASE version — i.e. exactly the shape RFC 9000 §6.1 says a
        // server must answer with Version Negotiation.
        let pkt = build_probe();
        assert_eq!(pkt.len(), PROBE_LEN);
        match crate::quic::parse_header(&pkt) {
            Some(crate::quic::Header::Long { version, dcid, .. }) => {
                assert_eq!(version, PROBE_VERSION);
                assert_eq!(dcid.len(), 8);
            }
            other => panic!("probe must parse as a long header, got {other:?}"),
        }
    }
}
