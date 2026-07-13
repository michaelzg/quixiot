//! Command-line configuration.
//!
//! Rust angle: parsing argv by hand (rather than reaching for `clap`) keeps the
//! dependency surface tiny and puts the language front-and-center — a `match`
//! over flag names, `Result<Config, String>` for fallible parsing, and the `?`
//! operator to short-circuit the first bad value. Invalid input never becomes a
//! silent default: it returns an `Err` the caller prints before exiting.

use std::net::{SocketAddr, ToSocketAddrs};
use std::time::Duration;

use crate::log::Level;
use crate::strategy::Strategy;

#[derive(Clone, Debug)]
pub struct Config {
    pub listen: SocketAddr,
    pub backends: Vec<SocketAddr>,
    pub strategy: Strategy,
    pub metrics_addr: Option<SocketAddr>,
    pub idle_timeout: Duration,
    pub health_active: bool,
    pub health_interval: Duration,
    pub health_timeout: Duration,
    pub health_fail_threshold: u32,
    pub log_level: Level,
}

impl Config {
    fn default_listen() -> SocketAddr {
        "127.0.0.1:4450".parse().unwrap()
    }
}

const USAGE: &str = "\
quixiot-lb — an educational L4 UDP load balancer for the QuixIoT QUIC servers

USAGE:
    quixiot-lb --backends <addr,addr,...> [options]

OPTIONS:
    --listen <host:port>          client-facing UDP listen address (default 127.0.0.1:4450)
    --backends <a,b,c>            comma-separated upstream UDP addresses (required)
    --strategy <name>             round-robin | least-conn | random | ip-hash (default round-robin)
    --metrics-addr <host:port>    Prometheus text endpoint; \"off\" disables (default 127.0.0.1:9106)
    --idle-timeout <secs>         evict a session after this many idle seconds (default 300)
    --health-active <bool>        send active QUIC version-negotiation probes (default true)
    --health-interval <secs>      seconds between active health probes (default 2)
    --health-timeout <ms>         milliseconds to wait for a probe reply (default 500)
    --health-fail-threshold <n>   consecutive failures before a backend is marked down (default 2)
    --log-level <level>           debug | info | warn | error (default info)
    -h, --help                    print this help and exit

EXAMPLE:
    quixiot-lb --listen 127.0.0.1:4450 \\
        --backends 127.0.0.1:4444,127.0.0.1:4445,127.0.0.1:4446 \\
        --strategy least-conn
";

/// Outcome of parsing argv: either run with a `Config`, or print help/usage and
/// exit with the given code. Modeling "print help and stop" as a value rather
/// than calling `process::exit` inside the parser keeps `main` in charge of exit.
#[derive(Debug)]
pub enum ParseOutcome {
    Run(Config),
    Help,
}

pub fn parse<I: IntoIterator<Item = String>>(args: I) -> Result<ParseOutcome, String> {
    let mut listen = Config::default_listen();
    let mut backends_raw: Option<String> = None;
    let mut strategy = Strategy::RoundRobin;
    let mut metrics_addr: Option<SocketAddr> = Some("127.0.0.1:9106".parse().unwrap());
    let mut idle_timeout = Duration::from_secs(300);
    let mut health_active = true;
    let mut health_interval = Duration::from_secs(2);
    let mut health_timeout = Duration::from_millis(500);
    let mut health_fail_threshold: u32 = 2;
    let mut log_level = Level::Info;

    let mut it = args.into_iter();
    while let Some(flag) = it.next() {
        // A tiny helper closure would need to borrow `it` mutably alongside the
        // match; instead we pull the value inline where each flag needs one.
        let mut value = || -> Result<String, String> {
            it.next()
                .ok_or_else(|| format!("flag {flag} requires a value"))
        };
        match flag.as_str() {
            "-h" | "--help" => return Ok(ParseOutcome::Help),
            "--listen" => listen = parse_addr(&value()?)?,
            "--backends" => backends_raw = Some(value()?),
            "--strategy" => {
                let v = value()?;
                strategy = Strategy::parse(&v)
                    .ok_or_else(|| format!("unknown strategy {v:?} (see --help)"))?;
            }
            "--metrics-addr" => {
                let v = value()?;
                metrics_addr = if v.eq_ignore_ascii_case("off") {
                    None
                } else {
                    Some(parse_addr(&v)?)
                };
            }
            "--idle-timeout" => idle_timeout = Duration::from_secs(parse_u64(&value()?, &flag)?),
            "--health-active" => health_active = parse_bool(&value()?, &flag)?,
            "--health-interval" => {
                health_interval = Duration::from_secs(parse_u64(&value()?, &flag)?)
            }
            "--health-timeout" => {
                health_timeout = Duration::from_millis(parse_u64(&value()?, &flag)?)
            }
            "--health-fail-threshold" => {
                health_fail_threshold = parse_u64(&value()?, &flag)? as u32
            }
            "--log-level" => {
                let v = value()?;
                log_level = Level::parse(&v).ok_or_else(|| format!("unknown log level {v:?}"))?;
            }
            other => return Err(format!("unknown flag {other:?} (see --help)")),
        }
    }

    let backends = parse_backends(backends_raw.as_deref())?;
    if idle_timeout.is_zero() {
        // 0 would evict every session on every sweep, silently breaking all
        // connections; better to refuse than to half-work.
        return Err("--idle-timeout must be > 0".into());
    }
    if health_interval.is_zero() {
        return Err("--health-interval must be > 0".into());
    }
    if health_fail_threshold == 0 {
        return Err("--health-fail-threshold must be >= 1".into());
    }

    Ok(ParseOutcome::Run(Config {
        listen,
        backends,
        strategy,
        metrics_addr,
        idle_timeout,
        health_active,
        health_interval,
        health_timeout,
        health_fail_threshold,
        log_level,
    }))
}

pub fn usage() -> &'static str {
    USAGE
}

fn parse_backends(raw: Option<&str>) -> Result<Vec<SocketAddr>, String> {
    let raw = raw.ok_or("--backends is required (see --help)")?;
    let mut out = Vec::new();
    for token in raw.split(',') {
        let token = token.trim();
        if token.is_empty() {
            continue;
        }
        out.push(parse_addr(token)?);
    }
    if out.is_empty() {
        return Err("--backends listed no usable addresses".into());
    }
    // A duplicate backend would emit two Prometheus series with identical
    // labels (invalid exposition) and double that backend's share of traffic.
    // If weighting is ever wanted, it should be explicit, not an accident.
    for (i, addr) in out.iter().enumerate() {
        if out[..i].contains(addr) {
            return Err(format!("--backends lists {addr} more than once"));
        }
    }
    Ok(out)
}

/// Resolve `host:port` to exactly one `SocketAddr`. Names like `localhost` are
/// allowed; we take the first resolved address.
fn parse_addr(s: &str) -> Result<SocketAddr, String> {
    s.to_socket_addrs()
        .map_err(|e| format!("bad address {s:?}: {e}"))?
        .next()
        .ok_or_else(|| format!("address {s:?} resolved to nothing"))
}

fn parse_u64(s: &str, flag: &str) -> Result<u64, String> {
    s.parse::<u64>()
        .map_err(|_| format!("flag {flag} expects an integer, got {s:?}"))
}

fn parse_bool(s: &str, flag: &str) -> Result<bool, String> {
    match s.to_ascii_lowercase().as_str() {
        "true" | "1" | "yes" | "on" => Ok(true),
        "false" | "0" | "no" | "off" => Ok(false),
        _ => Err(format!("flag {flag} expects a bool, got {s:?}")),
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    fn args(list: &[&str]) -> Vec<String> {
        list.iter().map(|s| s.to_string()).collect()
    }

    #[test]
    fn requires_backends() {
        let err = parse(args(&[])).unwrap_err();
        assert!(err.contains("--backends"));
    }

    #[test]
    fn parses_backends_and_strategy() {
        let out = parse(args(&[
            "--backends",
            "127.0.0.1:1,127.0.0.1:2",
            "--strategy",
            "least-conn",
        ]))
        .unwrap();
        match out {
            ParseOutcome::Run(cfg) => {
                assert_eq!(cfg.backends.len(), 2);
                assert!(matches!(cfg.strategy, Strategy::LeastConnections));
            }
            ParseOutcome::Help => panic!("expected Run"),
        }
    }

    #[test]
    fn metrics_off() {
        let out = parse(args(&[
            "--backends",
            "127.0.0.1:1",
            "--metrics-addr",
            "off",
        ]))
        .unwrap();
        match out {
            ParseOutcome::Run(cfg) => assert!(cfg.metrics_addr.is_none()),
            ParseOutcome::Help => panic!("expected Run"),
        }
    }

    #[test]
    fn rejects_unknown_flag() {
        assert!(parse(args(&["--nope"])).is_err());
    }

    #[test]
    fn rejects_duplicate_backends() {
        let err = parse(args(&["--backends", "127.0.0.1:1,127.0.0.1:2,127.0.0.1:1"])).unwrap_err();
        assert!(err.contains("more than once"), "got: {err}");
    }

    #[test]
    fn rejects_zero_idle_timeout() {
        let err = parse(args(&["--backends", "127.0.0.1:1", "--idle-timeout", "0"])).unwrap_err();
        assert!(err.contains("--idle-timeout"), "got: {err}");
    }

    #[test]
    fn help_short_circuits() {
        assert!(matches!(
            parse(args(&["--help"])).unwrap(),
            ParseOutcome::Help
        ));
    }
}
