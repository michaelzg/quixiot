//! UDP socket construction with sized kernel buffers.
//!
//! The Go proxy in this repo sets 8 MiB SO_RCVBUF on every UDP socket because
//! the workload measures drop rates — a default-sized buffer drops packets
//! under fleet load and skews the numbers. std doesn't expose buffer sizing, so
//! we build the socket with `socket2` and hand it to tokio via `from_std`.
//!
//! Rust angle: the std → socket2 → std → tokio conversions are all zero-cost
//! ownership transfers of the same file descriptor — the type changes, the fd
//! doesn't. And where Go's proxy hard-fails when the sysctl cap is too low,
//! degrading is a *policy choice* made visible here: we halve the request until
//! the kernel accepts it and report what we actually got.

use std::net::SocketAddr;

use socket2::{Domain, Protocol, Socket, Type};
use tokio::net::UdpSocket;

/// Matches `ReadBufferSize` in the Go proxy (internal/proxy/proxy.go).
pub const BUFFER_BYTES: usize = 8 * 1024 * 1024;

/// Bind a nonblocking UDP socket at `addr` with send/recv buffers as close to
/// `buf_bytes` as the kernel allows (halving on rejection — macOS caps this via
/// `kern.ipc.maxsockbuf`). Returns the socket and the granted size.
pub fn bind_udp(addr: SocketAddr, buf_bytes: usize) -> std::io::Result<(UdpSocket, usize)> {
    let domain = if addr.is_ipv4() {
        Domain::IPV4
    } else {
        Domain::IPV6
    };
    let socket = Socket::new(domain, Type::DGRAM, Some(Protocol::UDP))?;

    let granted = size_buffers(&socket, buf_bytes);

    socket.bind(&addr.into())?;
    socket.set_nonblocking(true)?; // required before handing the fd to tokio
    let tokio_sock = UdpSocket::from_std(socket.into())?;
    Ok((tokio_sock, granted))
}

/// The local wildcard address in the same family as `peer`, for binding
/// upstream/probe sockets that will `connect` to it.
pub fn wildcard_for(peer: SocketAddr) -> SocketAddr {
    if peer.is_ipv4() {
        "0.0.0.0:0".parse().unwrap()
    } else {
        "[::]:0".parse().unwrap()
    }
}

/// Try `want` bytes for both buffers, halving until the kernel accepts, and
/// return the size that stuck (0 if even the smallest attempt failed, in which
/// case the kernel default remains — still functional, just smaller).
fn size_buffers(socket: &Socket, want: usize) -> usize {
    let mut size = want;
    while size >= 64 * 1024 {
        if socket.set_recv_buffer_size(size).is_ok() && socket.set_send_buffer_size(size).is_ok() {
            return size;
        }
        size /= 2;
    }
    0
}

#[cfg(test)]
mod tests {
    use super::*;

    #[tokio::test]
    async fn binds_with_sized_buffers() {
        let (sock, granted) = bind_udp("127.0.0.1:0".parse().unwrap(), BUFFER_BYTES).unwrap();
        assert!(sock.local_addr().unwrap().port() != 0);
        // The kernel may clamp below 8 MiB, but some sizing must have stuck.
        assert!(granted >= 64 * 1024, "granted {granted}");
    }

    #[test]
    fn wildcard_matches_family() {
        assert!(wildcard_for("127.0.0.1:1".parse().unwrap()).is_ipv4());
        assert!(wildcard_for("[::1]:1".parse().unwrap()).is_ipv6());
    }
}
