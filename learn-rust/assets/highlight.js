/*
 * Tiny Rust syntax highlighter.
 * Tokenizes every `pre code` block on DOMContentLoaded and wraps recognized
 * tokens in <span class="tok-...">. No dependencies, no network access.
 * If this script fails to load, code stays as plain (still legible) text.
 */
(function () {
  "use strict";

  var KEYWORDS = (
    "as async await break const continue crate dyn else enum extern false " +
    "fn for if impl in let loop match mod move mut pub ref return self Self " +
    "static struct super trait true type unsafe use where while union"
  ).split(" ");

  var TYPES = (
    "u8 u16 u32 u64 u128 usize i8 i16 i32 i64 i128 isize f32 f64 bool char " +
    "str String Vec HashMap HashSet Option Result Box Arc Mutex Rc RefCell " +
    "Duration Instant SocketAddr IpAddr UdpSocket TcpListener Ordering " +
    "AtomicBool AtomicU8 AtomicU64 AtomicI64 AtomicUsize AtomicU32 Notify " +
    "JoinHandle ExitCode OnceLock"
  ).split(" ");

  var KEYWORD_SET = toSet(KEYWORDS);
  var TYPE_SET = toSet(TYPES);

  function toSet(list) {
    var set = Object.create(null);
    for (var i = 0; i < list.length; i++) set[list[i]] = true;
    return set;
  }

  // Alternation order matters: longer/more specific patterns first so e.g. a
  // char literal ('a') wins over the bare-lifetime pattern ('a) where both
  // could otherwise start matching at the same position.
  var TOKEN_RE = new RegExp(
    [
      "(//[^\\n]*)", // 1 line comment
      "(/\\*[\\s\\S]*?\\*/)", // 2 block comment
      '("(?:\\\\.|[^"\\\\])*")', // 3 string
      "('(?:\\\\.|[^'\\\\]){1,4}')", // 4 char literal
      "('[a-zA-Z_]\\w*)", // 5 lifetime
      "(#!?\\[[^\\]]*\\])", // 6 attribute
      "(\\b[A-Za-z_]\\w*!)", // 7 macro invocation
      "(\\b0x[0-9a-fA-F_]+\\b|\\b\\d[\\d_]*(?:\\.\\d[\\d_]*)?\\b)", // 8 number
      "(\\b[A-Za-z_]\\w*\\b)", // 9 identifier
    ].join("|"),
    "g"
  );

  function escapeHtml(s) {
    return s
      .replace(/&/g, "&amp;")
      .replace(/</g, "&lt;")
      .replace(/>/g, "&gt;");
  }

  function classify(text) {
    if (KEYWORD_SET[text]) return "tok-keyword";
    if (TYPE_SET[text] || /^[A-Z]/.test(text)) return "tok-type";
    return null;
  }

  function highlight(src) {
    var out = "";
    var last = 0;
    var m;
    TOKEN_RE.lastIndex = 0;
    while ((m = TOKEN_RE.exec(src))) {
      out += escapeHtml(src.slice(last, m.index));
      var text = m[0];
      var cls = null;
      if (m[1] || m[2]) cls = "tok-comment";
      else if (m[3] || m[4]) cls = "tok-string";
      else if (m[5]) cls = "tok-lifetime";
      else if (m[6]) cls = "tok-attribute";
      else if (m[7]) cls = "tok-macro";
      else if (m[8]) cls = "tok-number";
      else if (m[9]) cls = classify(text);
      out += cls ? '<span class="' + cls + '">' + escapeHtml(text) + "</span>" : escapeHtml(text);
      last = TOKEN_RE.lastIndex;
    }
    out += escapeHtml(src.slice(last));
    return out;
  }

  function run() {
    var blocks = document.querySelectorAll("pre code");
    for (var i = 0; i < blocks.length; i++) {
      var block = blocks[i];
      block.innerHTML = highlight(block.textContent);
    }
  }

  if (document.readyState === "loading") {
    document.addEventListener("DOMContentLoaded", run);
  } else {
    run();
  }
})();
