# Plan: "Learn Rust via quixiot-lb" — educational HTML site under `learn-rust/`

## Objective

A self-contained static HTML site that teaches Rust to a reader with **zero Rust
background but solid general software-engineering experience** (they know Go —
this repo is a Go codebase — plus the usual C-family concepts). Every concept is
anchored to **real code in this repository**, primarily the Rust load balancer in
`lb/` (crate `quixiot-lb`), with contrasts against the Go impairment proxy in
`internal/proxy/proxy.go`.

Tone: thorough but not verbose. Explain *why*, show *real* code, move on.
Second person ("you"), no fluff, no marketing voice. Assume the reader can read
Go and C-family code fluently; never explain what a function or a hash map is.

## Deliverables — file tree

```
learn-rust/
  index.html                     landing page + table of contents + codebase map
  01-toolchain.html              cargo, crates, the lb crate anatomy
  02-fundamentals.html           bindings, types, expressions, control flow
  03-ownership.html              ownership, moves, borrowing, slices
  04-enums-matching.html         enums, match, Option, if let / let else
  05-errors.html                 Result, ?, error design, main -> ExitCode
  06-structs-traits.html         structs, impl, traits, derive, generics
  07-collections-iterators.html  Vec, HashMap, iterator adapters
  08-concurrency.html            Send/Sync, Arc, Mutex, atomics
  09-async.html                  tokio, tasks, select!, Notify, cancellation
  10-parsing-macros.html         unsafe-free byte parsing + macro_rules!
  11-testing.html                unit/async tests, clippy, fmt — and the bug tests caught
  12-rust-vs-go.html             side-by-side retrospective + where to go next
  assets/style.css               single shared stylesheet
  assets/highlight.js            tiny self-contained Rust syntax highlighter
```

No other repo files may be created or modified. Do not touch `lb/`, `README.md`,
`Makefile`, or anything else.

## Hard technical constraints

- **Fully offline**: no CDN links, no web fonts, no external images, no
  analytics. `file://` opening must work (use relative hrefs only).
- Plain HTML5 + one CSS file + one small vanilla-JS file. No frameworks, no
  build step.
- `assets/highlight.js` — write a ~60-line regex tokenizer that wraps Rust
  keywords, types, strings, comments, lifetimes, macros, and attributes in
  `<span class="tok-...">` inside every `pre code` block on DOMContentLoaded.
  It must degrade gracefully: with JS off, code is still readable monospace.
- Dark and light themes via `prefers-color-scheme`; code blocks legible in both.
- Responsive: sidebar nav on wide screens, simple stacked layout under ~900px.
  Code blocks scroll horizontally inside their container, never the page.
- Every page: fixed/sticky sidebar with the full chapter list (current page
  highlighted), and prev/next links in a footer nav. `index.html` links to all.

## Design direction

**Invoke the `frontend-design` skill before writing any HTML** and follow it.
Beyond that: this is a *technical field guide*, so aim for a calm, print-like
reading experience — generous line-height, measure ~70ch, restrained accent
color used for links/active nav/callout borders. Avoid the generic
purple-gradient-SaaS look. System font stack for prose, real monospace stack
for code. Small caps or mono for the file-path captions above code blocks.

Recurring components (define once in CSS, reuse everywhere):
- **Code block with caption bar**: caption shows the source path, e.g.
  `lb/src/balancer.rs`. Excerpts must be copied *verbatim* from the repo files
  (trim to the relevant 5–25 lines, mark elisions with `// …`).
- **Callout boxes**, four kinds with distinct left-border colors and small
  labels: `Go contrast`, `Gotcha`, `War story` (real errors from building this
  crate), `Try it` (exercise).
- Inline code styling for identifiers.
- A simple two-column comparison layout for Rust-vs-Go snippets (stacks on
  mobile).

## Source material — read these files before writing

- `lb/src/main.rs`, `lb/src/config.rs`, `lb/src/strategy.rs`,
  `lb/src/backend.rs`, `lb/src/balancer.rs`, `lb/src/health.rs`,
  `lb/src/quic.rs`, `lb/src/metrics.rs`, `lb/src/log.rs`, `lb/src/net.rs`,
  `lb/Cargo.toml`, `lb/README.md`
- Go contrast: `internal/proxy/proxy.go` (session lifecycle, `sync.Once`,
  channels, goroutines), `cmd/server/main.go` (flag parsing, error returns)

All snippets must match the current file contents exactly. When you reference a
construct, name the function it lives in so the reader can find it.

## Page-by-page content spec

### index.html — "Learn Rust from a working load balancer"
- What this guide is: a Rust course where every example is production-shaped
  code from `lb/`, an L4 UDP load balancer fronting QUIC servers in this repo.
- One-paragraph description of what quixiot-lb does + a small ASCII/HTML diagram
  (clients → lb → 3 backends) — copy the idea from `lb/README.md`.
- "How to use this guide": read in order; run `make lb-test` and `make lb-demo`
  to see the code live; each chapter ends with a Try-it exercise against the
  real crate.
- Codebase map: table of the ten `lb/src/*.rs` files, one line each on role and
  which chapters use them.
- Full TOC with one-sentence summaries per chapter.

### 01-toolchain.html — Cargo and the anatomy of a crate
- rustup/cargo/rustc in one short paragraph each; `cargo build`, `run`, `test`,
  `clippy`, `fmt`, `--release` (and this repo's `make lb`, `make lb-test`).
- Walk `lb/Cargo.toml`: `[package]`, `edition`, `[[bin]]`, `[dependencies]`
  with feature flags on tokio, `[profile.release]`. Explain semver caret
  defaults and `Cargo.lock` (locked for binaries — this repo tracks it).
- Crates vs modules: `main.rs` `mod` declarations = the module tree; `use`
  paths; `crate::` prefix; visibility with `pub`. Show the `mod` block from
  `lb/src/main.rs`.
- Go contrast callout: `go.mod`/packages-by-directory vs explicit `mod`
  declarations; no circular imports in either.

### 02-fundamentals.html — bindings, types, expressions
- `let`, immutability by default, `mut`, shadowing; type inference and when it
  needs help. **War story #1** (see War stories section): the
  `{integer}`/`checked_add` inference error and the `let dcid_start: usize = 6;`
  fix in `lb/src/quic.rs`.
- Scalar types (`u8..u64`, `usize`, explicit casts with `as`), tuples, arrays
  vs `Vec`, `String` vs `&str` (preview — full story in ch. 3).
- Expressions vs statements: blocks yield values; `if` is an expression —
  show `let bind = if backend.addr().is_ipv4() { … } else { … }` pattern (the
  wildcard_for fn in `lb/src/net.rs`) and a match-as-expression from
  `lb/src/log.rs` (`Level::label`).
- Functions: `fn`, `->`, last-expression return (no `return` keyword needed);
  closures briefly (`let mut value = || -> Result<String, String> { … }` from
  `config.rs::parse` — note closures capture their environment).
- Loops: `loop`, `while`, `for x in iter`. Ranges `0..n`.
- `const` and `static`: `PROBE_VERSION`, `MAX_DATAGRAM`; statics with atomics
  preview (`MIN_LEVEL` in log.rs).

### 03-ownership.html — the chapter that makes Rust Rust
- The three rules: each value has one owner; moves on assignment/call for
  non-Copy types; scope-end drops. RAII: sockets in `lb/` close when the last
  owner drops — point at `Session`'s `upstream` socket lifecycle (no explicit
  close call anywhere — contrast with Go's `defer conn.Close()` in proxy.go).
- Copy vs move types (integers/`SocketAddr` are Copy; `String`/`Vec` move).
  `.clone()` as explicit cost.
- Borrowing: `&T` shared / `&mut T` exclusive; the aliasing XOR mutation rule
  and *why* (data races, iterator invalidation — connect to their SWE
  experience: this is the compiler enforcing what code review tries to catch).
- **War story #2**: `error[E0308]: expected &str, found String` from
  `parse_u64(&value()?, flag)` during this crate's development — the fix is
  passing `&flag`; explain deref coercion `&String -> &str` while you're at it.
- Slices: `&buf[..n]` in the balancer hot path — borrowing a window of a buffer
  with zero copies; `&str` as a string slice.
- Lifetimes: keep it light. Show `fn label(&self) -> &str` on `Backend` (the
  return borrows from self) and `&'static str` (`Strategy::label`). State that
  elision handles most cases; name the concept, don't drill it.
- Go contrast: GC vs ownership; escape analysis vs explicit moves; `defer` vs
  drop.

### 04-enums-matching.html — enums, match, Option
- Enums with data: `quic::Header` (unit-ish `Short` vs struct-like `Long {…}`)
  and plain `Strategy`. Enums are tagged unions done right.
- `match`: exhaustiveness as a refactoring superpower — add a `Strategy`
  variant and every `match` fails to compile until handled (`Selector::select`,
  `Strategy::label`, `Strategy::parse`).
- `Option<T>`: no null. `Selector::select` returns `Option<Arc<Backend>>` —
  "all backends down" is a type-level state the caller must handle. Show the
  caller in `balancer.rs::get_or_create`.
- Patterns beyond match: `if let`, `let … else` (`let Ok((n, peer)) = … else
  { return; }` in the balancer test helpers), `matches!` macro
  (`Header::is_initial`), `while let`.
- Option/Result combinators used in the crate: `ok_or_else`, `map_err`,
  `unwrap_or_default`, `.next()` on iterators returning Option
  (`config.rs::parse_addr`).
- Go contrast: `nil` checks and `ok`-idiom vs Option; no exhaustiveness check
  on Go type switches.

### 05-errors.html — Result and ?
- `Result<T, E>`; errors are values (they know this from Go — lean on it).
- `?`: early-return propagation. Trace one real path end-to-end:
  `main()` → `config::parse` → `parse_addr` → error string bubbling to
  `eprintln!` + `ExitCode::FAILURE`. Contrast the same flow in Go
  (`cmd/server/main.go: run(os.Args[1:])` returning `error`).
- Error types: this crate uses `String` for CLI errors (pragmatic for a small
  tool) and `std::io::Error` where I/O demands it (`dial_upstream`,
  `net::bind_udp`); mention `Box<dyn Error>` / thiserror-style enums as the
  scaling path without covering the crates in depth.
- `unwrap`/`expect` policy: fine in tests and for provably-infallible cases
  (`"127.0.0.1:4450".parse().unwrap()` on a literal); a smell elsewhere.
  `.lock().unwrap()` and mutex poisoning in one gotcha callout.
- Panics vs Results: when each is appropriate.

### 06-structs-traits.html — structs, impl, traits, derive
- Struct + `impl` blocks: `Backend` as the tour (fields private by default,
  getter methods, `&self` vs `&mut self` vs consuming `self` — note `Backend`
  needs only `&self` because interior mutability via atomics, forward-ref ch. 8).
- Associated functions (`Backend::new`) vs methods; `Self`.
- Traits = interfaces resolved at compile time by default. Show `Default`
  (`Metrics::default()`), operator/formatting traits by name (`Display`,
  `Debug`).
- `#[derive(...)]`: Clone/Copy/Debug/PartialEq on `Strategy`, `Level`, `Config`.
  **War story #3**: `error[E0277]: ParseOutcome doesn't implement Debug` —
  `Result::unwrap_err` requires `T: Debug`; one `#[derive(Debug)]` fixes it.
  Lesson: std APIs constrain by trait bounds, and the compiler tells you the
  missing bound.
- Generics + bounds: `pub fn parse<I: IntoIterator<Item = String>>(args: I)`
  from config.rs — accepts `Vec<String>`, `env::args().skip(1)`, anything
  iterable; monomorphization = zero-cost.
- Auto traits preview: `Send`/`Sync` are *derived from your fields* (full story
  ch. 8).

### 07-collections-iterators.html
- `Vec<T>` and `HashMap<K, V>`: the session table
  `Mutex<HashMap<SocketAddr, Arc<Session>>>` — entry lookup/insert in
  `get_or_create`, `retain` in `sweep_loop` (filter-in-place while collecting
  evictees).
- Iterator adapters are lazy until `collect`: real chains from the crate —
  `backends.iter().filter(|b| b.is_healthy()).collect()` (`Selector::select`),
  `min_by_key` (least-conn), `cfg.backends.iter().map(…).collect()` (main.rs),
  `s.split(',')` + trim in `splitSANs`-equivalent `parse_backends`.
- Turbofish and collect's target-type inference (`collect::<Vec<_>>()` vs
  annotating the binding).
- `String` building: `String::with_capacity` + `write!` into it
  (`metrics.rs::render`) — formatted output without an allocation per line.
- Go contrast: no lazy iterator chains in Go; `for … range` + manual slices.

### 08-concurrency.html — fearless concurrency
- The claim: data races are compile errors, not code review items.
- `Send`/`Sync` auto-derivation: `Backend` is `Sync` *because* every field is
  atomic; swap one for a plain `i64` and it stops compiling if shared across
  tasks.
- `Arc<T>`: shared ownership across tasks; cheap clone = refcount bump. Where
  the crate uses it: `Arc<Backend>`, `Arc<Vec<Arc<Backend>>>`, `Arc<Session>`,
  `Arc<Metrics>`, `Arc<self>` on `Balancer`.
- `Mutex<T>` *wraps the data, not the code section*: the session table; the
  pattern of cloning the Arc out and dropping the guard before any `.await`
  (call out the comment in `get_or_create` about not holding the mutex across
  await points).
- Atomics: counters in `metrics.rs`; flags in `backend.rs`; `Ordering::Relaxed`
  for counters vs `AcqRel` in `finish()`. Keep the memory-ordering treatment to
  two paragraphs: Relaxed = "just a number", stronger orderings synchronize —
  don't teach the whole memory model.
- **Exactly-once teardown**: `Session.closed` CAS in `balancer.rs::finish` —
  put side-by-side with Go's `sync.Once` + channel-close in
  `internal/proxy/proxy.go::(s *session) close()`. This is the flagship
  two-column comparison of the whole site.

### 09-async.html — tokio
- Mental model: `async fn` returns a state machine; nothing runs until awaited;
  the runtime (tokio) polls tasks on a thread pool. Tasks ≈ goroutines, but
  cooperative at `.await` points, not preemptive.
- The runtime builder in `main.rs` (2 worker threads, `enable_all`,
  `block_on`).
- `tokio::spawn` and detached tasks: the return task per session; the sweeper;
  the probe loops. `JoinHandle::abort` (sweeper shutdown).
- `tokio::select!`: "whichever completes first" — the accept loop
  (shutdown vs recv) and the return loop (shutdown vs upstream recv).
  Cancellation safety in one paragraph: the branch that loses is *dropped*.
- `Notify`: permit semantics — `notify_one` stores a permit so a wake can't be
  missed between loop iterations (comment in `main.rs::spawn_signal_handler`).
- `tokio::time`: `timeout` turning "no reply in 500 ms" into a Result branch
  (`health.rs::probe_once`); `interval` + `MissedTickBehavior::Skip`.
- **War story #4, the centerpiece**: the `try_send` readiness bug. Tell it
  chronologically: (1) hot path switched from `send().await` to `try_send` for
  drop-not-block; (2) new integration tests failed — first packet of every
  session vanished; (3) cause: tokio's `try_*` consult the reactor's *cached*
  readiness and a fresh socket hasn't seen its first writable event;
  (4) fix: prime once with `writable().await` (show both call sites);
  (5) morals: async runtimes have observable internals; live QUIC traffic
  masked the bug because clients retransmit — tests > demos.
- Go contrast: netpoller blocks a goroutine transparently; Rust makes the
  readiness machinery explicit.

### 10-parsing-macros.html — bytes without unsafe, and macro_rules!
- Parsing untrusted bytes safely: full walk of `quic.rs::parse_header` — every
  read via `slice.get(range)?` so truncated/hostile input yields `None`;
  `checked_add` against overflow; zero `unsafe` in the whole crate. Bit
  twiddling (`first & 0x80`, `(first & 0x30) >> 4`) reads like C but cannot
  read out of bounds.
- `u32::from_be_bytes` + `try_into` for fixed-size conversion.
- Declarative macros: why `info!`-style logging needs a macro (variadic format
  args, lazy evaluation). Walk `log.rs`: `macro_rules!`, `$($arg:tt)*`,
  `#[macro_export]`, `format_args!` avoiding intermediate Strings. Mention
  (one sentence each) that `derive`, `vec!`, `matches!`, `write!` are macros
  too; proc-macros exist — do not teach writing them.
- Gotcha: macros before modules — `#[macro_use] mod log;` ordering in main.rs.

### 11-testing.html — tests, clippy, fmt
- `#[cfg(test)] mod tests` inline with the code; `#[test]`; assert macros with
  format messages. Unit examples: `strategy.rs` (deterministic round-robin
  cycle test), `quic.rs` (truncated-packet), `config.rs` (error-path asserts
  with `unwrap_err`).
- Async tests: `#[tokio::test]`; the balancer integration tests that spin up
  real UDP echo backends *inside the test* (`spawn_tagged_echo`) — no mocks,
  real sockets on port 0.
- Test-driven bug discovery: reprise war story #4 in one paragraph with a link
  to ch. 9.
- Determinism tactics in these tests: bounded timeouts, polling with deadlines
  instead of sleeps, tagged replies to identify backends.
- Tooling culture: `cargo clippy` (this crate is clippy-clean; give one example
  category of lint), `cargo fmt` (mechanical style = zero bikeshedding — this
  repo's diffs showed fmt reflowing code post-hoc).

### 12-rust-vs-go.html — retrospective + where next
- A summary table mapping every concept to both languages: goroutine/task,
  channel/Notify+mpsc, `sync.Once`/CAS flag, `defer`/Drop, `error` return/
  `Result`+`?`, nil/Option, interface/trait, GC/ownership, `go vet`/clippy.
- Three honest "when Go was simpler" admissions (e.g., no readiness priming
  needed, GC removes lifetime thought, faster compile loop) and three "what
  Rust bought us" (data-race prevention at compile time, exhaustive matching,
  no-null + no-exception control flow). Keep it fair, not fanboy.
- Where next: The Rust Book, Rustlings, `std` docs habit (`Option`/`Result`/
  `Iterator` method lists), and **concrete exercises against this crate**:
  1. Add a `weighted` strategy (`--backends addr@3,addr@1`) — touches config,
     strategy enum, tests.
  2. Add `quixiot_lb_backend_health_transitions_total` metric — touches
     backend.rs, metrics.rs.
  3. Make idle sweep interval configurable — config plumbing end-to-end.
  4. Harder: per-session byte counters with a `/metrics` label per backend.

## War stories — exact material (use verbatim, prune for length)

These all actually happened while building `lb/` in this repo. Frame each as
error → diagnosis → fix → principle.

1. **Inference needs an anchor** (`quic.rs`):
   `error[E0689]: can't call method checked_add on ambiguous numeric type
   {integer}` on `let dcid_start = 6;` followed by `dcid_start.checked_add(…)`.
   Fix: `let dcid_start: usize = 6;`.
2. **Borrow at the call site** (`config.rs`):
   `error[E0308]: mismatched types — expected &str, found String` with the
   compiler's own suggestion `help: consider borrowing here: &flag`. Fix:
   `parse_u64(&value()?, &flag)?`. Teach deref coercion `&String → &str`.
3. **Trait bounds in std APIs** (`config.rs` tests):
   `error[E0277]: ParseOutcome doesn't implement Debug … required by
   Result::unwrap_err`. Fix: `#[derive(Debug)]` on the enum (and Config/Level).
4. **The try_send readiness bug** (`balancer.rs`): test failure
   `panicked at 'reply within 2s: Elapsed(())'`; root cause and fix as
   specified in ch. 9. The fix comments are in the source at `dial_upstream`
   and the top of `Balancer::run` — quote them.

## Writing rules

- Chapters should land roughly 150–300 lines of rendered prose+code each; the
  ownership, concurrency, and async chapters may run longer (they carry the
  most weight). If a topic isn't load-bearing for reading this codebase, cut it
  (skip: trait objects in depth, Rc/RefCell, Box patterns, proc macros, FFI,
  Pin internals — at most name-drop with one sentence).
- Every chapter: at least one verbatim snippet from the repo with a file-path
  caption, at least one callout, exactly one Try-it exercise.
- Cross-link chapters liberally (plain relative hrefs).
- Code snippets: verbatim from source. Never invent code that pretends to be in
  the repo; clearly separate hypothetical/counter-example snippets (caption
  them "counter-example — does not compile" etc.).
- US English, sentence-case headings.

## QA checklist (execute before reporting done)

1. `ls learn-rust` matches the file tree above; nothing else changed
   (`git status --short` shows only `learn-rust/` additions and this plan).
2. Link check: every `href` in every page resolves to an existing local file or
   anchor (write a quick shell/grep check).
3. Snippet fidelity: for at least 6 randomly-chosen snippets, `grep` the first
   line in the named source file to confirm verbatim copy.
4. Open `index.html` and two chapter pages in the browser (Browser pane /
   `file://` or a throwaway static server); confirm: sidebar nav, prev/next,
   dark + light rendering, code highlighting active, no horizontal page
   scroll at 375px width.
5. No external URLs anywhere except (optionally) clearly-marked "further
   reading" links to doc.rust-lang.org in ch. 12 — those must be plain links,
   never loaded resources.
6. Rust claims sanity pass: no statement that contradicts the actual source
   (e.g., orderings, method names, flag names). When in doubt, re-open the file.
