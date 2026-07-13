//! quixiot-lb — an educational L4 UDP load balancer for the QuixIoT QUIC servers.
//!
//! See README.md for the "strengths of Rust" tour. `main` just wires the pieces
//! together: parse config, bind the listen socket, build the backend pool, and
//! start the balancer, health checker, and metrics endpoint as tokio tasks.

#[macro_use]
mod log;
mod backend;
mod balancer;
mod config;
mod health;
mod metrics;
mod net;
mod quic;
mod strategy;

use std::process::ExitCode;
use std::sync::Arc;

use tokio::sync::Notify;

use backend::Backend;
use balancer::Balancer;
use config::{Config, ParseOutcome};
use metrics::Metrics;
use strategy::Selector;

fn main() -> ExitCode {
    // Parse before spinning up the async runtime, so `--help` and bad flags are
    // cheap and never touch the network.
    let cfg = match config::parse(std::env::args().skip(1)) {
        Ok(ParseOutcome::Run(cfg)) => cfg,
        Ok(ParseOutcome::Help) => {
            print!("{}", config::usage());
            return ExitCode::SUCCESS;
        }
        Err(msg) => {
            eprintln!("quixiot-lb: {msg}");
            return ExitCode::FAILURE;
        }
    };
    log::set_level(cfg.log_level);

    // A small multi-thread runtime; UDP forwarding is I/O-bound, so a couple of
    // workers keep the receive loop and return tasks moving without oversubscribing.
    let runtime = match tokio::runtime::Builder::new_multi_thread()
        .worker_threads(2)
        .enable_all()
        .build()
    {
        Ok(rt) => rt,
        Err(e) => {
            eprintln!("quixiot-lb: failed to start runtime: {e}");
            return ExitCode::FAILURE;
        }
    };

    match runtime.block_on(serve(cfg)) {
        Ok(()) => ExitCode::SUCCESS,
        Err(e) => {
            error!("{e}");
            ExitCode::FAILURE
        }
    }
}

async fn serve(cfg: Config) -> Result<(), String> {
    let cfg = Arc::new(cfg);

    // Sized kernel buffers on the client-facing socket — a whole fleet funnels
    // through this one fd, so the default (a few hundred KiB) drops bursts.
    let (listen, granted) = net::bind_udp(cfg.listen, net::BUFFER_BYTES)
        .map_err(|e| format!("bind listen socket {}: {e}", cfg.listen))?;
    if granted < net::BUFFER_BYTES {
        warn!(
            "kernel capped listen socket buffers at {granted} bytes (wanted {}); \
             on macOS raise kern.ipc.maxsockbuf — see README",
            net::BUFFER_BYTES
        );
    }

    let backends: Arc<Vec<Arc<Backend>>> = Arc::new(
        cfg.backends
            .iter()
            .map(|addr| Arc::new(Backend::new(*addr)))
            .collect(),
    );

    let metrics = Arc::new(Metrics::default());
    // Seed the strategy's RNG/cursor from the listen port so two LBs on one host
    // don't march in lockstep.
    let selector = Selector::new(cfg.strategy, cfg.listen.port() as u64);

    let backend_list: Vec<String> = cfg.backends.iter().map(|b| b.to_string()).collect();
    info!(
        "quixiot-lb listening on {} | strategy={} | backends=[{}]",
        cfg.listen,
        cfg.strategy.label(),
        backend_list.join(", ")
    );

    // Metrics endpoint (optional).
    if let Some(metrics_addr) = cfg.metrics_addr {
        tokio::spawn(metrics::serve(
            metrics_addr,
            Arc::clone(&metrics),
            Arc::clone(&backends),
            cfg.strategy,
        ));
    }

    // Active health probes (recovery detection); passive detection is inline.
    health::spawn(Arc::clone(&cfg), Arc::clone(&backends));

    let balancer = Balancer::new(
        listen,
        Arc::clone(&backends),
        selector,
        Arc::clone(&metrics),
        cfg.idle_timeout,
    );

    // Ctrl-C / SIGTERM trips the shutdown Notify, which the accept loop selects on.
    let shutdown = Arc::new(Notify::new());
    spawn_signal_handler(Arc::clone(&shutdown));

    balancer.run(shutdown).await;
    info!("quixiot-lb stopped");
    Ok(())
}

fn spawn_signal_handler(shutdown: Arc<Notify>) {
    tokio::spawn(async move {
        #[cfg(unix)]
        {
            use tokio::signal::unix::{signal, SignalKind};
            let mut term = match signal(SignalKind::terminate()) {
                Ok(s) => s,
                Err(e) => {
                    warn!("cannot install SIGTERM handler: {e}");
                    return;
                }
            };
            tokio::select! {
                _ = tokio::signal::ctrl_c() => info!("received Ctrl-C"),
                _ = term.recv() => info!("received SIGTERM"),
            }
        }
        #[cfg(not(unix))]
        {
            let _ = tokio::signal::ctrl_c().await;
            info!("received Ctrl-C");
        }
        // notify_one stores a permit if the accept loop isn't parked in select
        // at this instant, so the shutdown can't be missed between iterations.
        shutdown.notify_one();
    });
}
