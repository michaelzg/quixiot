//! A minimal leveled logger — no `log`/`tracing`/`chrono` crates.
//!
//! Rust angle: the global level lives in a single `AtomicU8`, so logging is
//! lock-free and safe to call from any task on any thread. Levels are a plain
//! `enum` with an exhaustive `match`, so adding a level is a compile error until
//! every site handles it.

use std::sync::atomic::{AtomicU8, Ordering};
use std::time::{SystemTime, UNIX_EPOCH};

#[derive(Clone, Copy, Debug, PartialEq, Eq, PartialOrd, Ord)]
pub enum Level {
    Debug = 0,
    Info = 1,
    Warn = 2,
    Error = 3,
}

impl Level {
    fn label(self) -> &'static str {
        match self {
            Level::Debug => "DEBUG",
            Level::Info => "INFO",
            Level::Warn => "WARN",
            Level::Error => "ERROR",
        }
    }

    pub fn parse(s: &str) -> Option<Level> {
        match s.to_ascii_lowercase().as_str() {
            "debug" => Some(Level::Debug),
            "info" => Some(Level::Info),
            "warn" | "warning" => Some(Level::Warn),
            "error" => Some(Level::Error),
            _ => None,
        }
    }
}

static MIN_LEVEL: AtomicU8 = AtomicU8::new(Level::Info as u8);

pub fn set_level(level: Level) {
    MIN_LEVEL.store(level as u8, Ordering::Relaxed);
}

fn enabled(level: Level) -> bool {
    level as u8 >= MIN_LEVEL.load(Ordering::Relaxed)
}

/// Format the current wall-clock time as `HH:MM:SS.mmm` in UTC. Computing the
/// calendar date without a crate is fiddly, but time-of-day is just modular
/// arithmetic on the Unix epoch and is all a local log needs.
fn timestamp() -> String {
    let now = SystemTime::now()
        .duration_since(UNIX_EPOCH)
        .unwrap_or_default();
    let millis = now.subsec_millis();
    let secs = now.as_secs();
    let (h, m, s) = ((secs / 3600) % 24, (secs / 60) % 60, secs % 60);
    format!("{h:02}:{m:02}:{s:02}.{millis:03}")
}

/// Emit a line. Called by the `info!`/`warn!`/… macros; not usually direct.
pub fn emit(level: Level, msg: std::fmt::Arguments<'_>) {
    if !enabled(level) {
        return;
    }
    eprintln!("{} {:<5} lb: {}", timestamp(), level.label(), msg);
}

#[macro_export]
macro_rules! log_at {
    ($lvl:expr, $($arg:tt)*) => {
        $crate::log::emit($lvl, format_args!($($arg)*))
    };
}

#[macro_export]
macro_rules! debug {
    ($($arg:tt)*) => { $crate::log_at!($crate::log::Level::Debug, $($arg)*) };
}
#[macro_export]
macro_rules! info {
    ($($arg:tt)*) => { $crate::log_at!($crate::log::Level::Info, $($arg)*) };
}
#[macro_export]
macro_rules! warn {
    ($($arg:tt)*) => { $crate::log_at!($crate::log::Level::Warn, $($arg)*) };
}
#[macro_export]
macro_rules! error {
    ($($arg:tt)*) => { $crate::log_at!($crate::log::Level::Error, $($arg)*) };
}
