//! Just enough QUIC packet parsing to *observe* traffic — not to route on it.
//!
//! This load balancer sticks sessions to the client 4-tuple, which is correct
//! and migration-free for the local QuixIoT fleet (see README). But peeking at
//! the QUIC header lets us count new connections (Initial packets) for metrics,
//! and it's a compact demonstration of Rust doing untrusted, bounds-checked
//! bit-parsing without a single `unsafe`: every field read goes through
//! `get(range)?`, so a truncated or hostile datagram yields `None` instead of a
//! panic or an out-of-bounds read.
//!
//! Reference: RFC 9000 §17. Long header:
//!   byte0: 1 T T X X X X X   (0x80 form bit set)
//!   version: u32
//!   dcid_len: u8, dcid: [u8; dcid_len]
//!   scid_len: u8, scid: [u8; scid_len]
//! A `version` of 0 marks a Version Negotiation packet; otherwise for QUIC v1
//! the two T bits give the long-packet type.

#[derive(Debug, PartialEq, Eq)]
pub enum LongPacketType {
    Initial,
    ZeroRtt,
    Handshake,
    Retry,
}

#[derive(Debug, PartialEq, Eq)]
pub enum Header {
    /// Short header (1-RTT). The DCID length is implicit on the wire, so we
    /// can't extract it here — we only note the form.
    Short,
    Long {
        version: u32,
        packet_type: Option<LongPacketType>,
        dcid: Vec<u8>,
    },
}

impl Header {
    /// True when this packet begins a fresh QUIC connection.
    pub fn is_initial(&self) -> bool {
        matches!(
            self,
            Header::Long {
                packet_type: Some(LongPacketType::Initial),
                ..
            }
        )
    }
}

/// Parse the header of a UDP payload. Returns `None` if the bytes don't look
/// like QUIC or are truncated — the caller treats that as "just forward it".
pub fn parse_header(datagram: &[u8]) -> Option<Header> {
    let first = *datagram.first()?;
    let long = first & 0x80 != 0;
    if !long {
        return Some(Header::Short);
    }

    // Long header: 1 byte + 4-byte version + length-prefixed DCID and SCID.
    let version = u32::from_be_bytes(datagram.get(1..5)?.try_into().ok()?);

    let dcid_len = *datagram.get(5)? as usize;
    let dcid_start: usize = 6;
    let dcid_end = dcid_start.checked_add(dcid_len)?;
    let dcid = datagram.get(dcid_start..dcid_end)?.to_vec();

    // Confirm the SCID length byte and bytes are actually present, so a truncated
    // packet is rejected rather than reported as a valid long header.
    let scid_len = *datagram.get(dcid_end)? as usize;
    let scid_end = dcid_end.checked_add(1)?.checked_add(scid_len)?;
    let _ = datagram.get(dcid_end + 1..scid_end)?;

    let packet_type = if version == 0 {
        None // Version Negotiation packet.
    } else {
        Some(match (first & 0x30) >> 4 {
            0x00 => LongPacketType::Initial,
            0x01 => LongPacketType::ZeroRtt,
            0x02 => LongPacketType::Handshake,
            _ => LongPacketType::Retry,
        })
    };

    Some(Header::Long {
        version,
        packet_type,
        dcid,
    })
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn short_header() {
        // High bit clear -> short header.
        assert_eq!(parse_header(&[0x40, 0xAB, 0xCD]), Some(Header::Short));
    }

    #[test]
    fn long_header_initial() {
        // form+fixed bits, type=Initial(00); version 1; dcid len 2; scid len 1.
        let pkt = [
            0xC0, // 1100_0000 -> long, type 00 (Initial)
            0x00, 0x00, 0x00, 0x01, // version 1
            0x02, 0xAA, 0xBB, // dcid len 2 + dcid
            0x01, 0xCC, // scid len 1 + scid
            0x00, 0x00, // payload
        ];
        let h = parse_header(&pkt).unwrap();
        assert!(h.is_initial());
        match h {
            Header::Long { version, dcid, .. } => {
                assert_eq!(version, 1);
                assert_eq!(dcid, vec![0xAA, 0xBB]);
            }
            _ => panic!("expected long header"),
        }
    }

    #[test]
    fn version_negotiation_has_no_type() {
        let pkt = [
            0xC0, 0x00, 0x00, 0x00, 0x00, // version 0 -> VN
            0x00, // dcid len 0
            0x00, // scid len 0
        ];
        assert_eq!(
            parse_header(&pkt),
            Some(Header::Long {
                version: 0,
                packet_type: None,
                dcid: vec![]
            })
        );
    }

    #[test]
    fn truncated_long_header_is_none() {
        // Claims a 5-byte DCID but the packet ends early.
        let pkt = [0xC0, 0x00, 0x00, 0x00, 0x01, 0x05, 0xAA];
        assert_eq!(parse_header(&pkt), None);
    }

    #[test]
    fn empty_is_none() {
        assert_eq!(parse_header(&[]), None);
    }
}
