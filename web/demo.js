// Page rendering for the Elena demo page. Zero framework, zero build
// step: this script reads the global DATA constant that data.js
// declares (a plain script tag works even when the page is opened
// directly from the filesystem, where cross-origin fetches fail) and
// builds the three acts' DOM. Every number shown comes from DATA —
// nothing is typed in here.

"use strict";

(function () {
  // ---------- small helpers ----------

  const byId = (id) => document.getElementById(id);

  const escapeHtml = (s) =>
    String(s).replace(/&/g, "&amp;").replace(/</g, "&lt;").replace(/>/g, "&gt;");

  const intFmt = (n) => Math.round(n).toLocaleString("en-US");

  // The same register the distillation rounds to: whole ops/sec from
  // a thousand up, one decimal below.
  const opsFmt = (n) => (n >= 1000 ? intFmt(n) : n.toFixed(1));

  const msFmt = (n) => n.toFixed(1) + " ms";

  // ---------- Act 1: trajectory timeline ----------

  function renderTrajectory(story) {
    const bound = story.point_in_time.seq;
    const items = story.events.map((ev) => {
      const beyond = ev.seq > bound ? " beyond" : "";
      const note = ev.seq > bound
        ? '<span class="ev-note">beyond the read bound — not returned</span>'
        : "";
      return (
        '<li class="ev' + beyond + '">' +
        '<span class="seq">seq ' + ev.seq + "</span>" +
        '<span class="payload">' + escapeHtml(ev.payload) + "</span>" +
        '<span class="at">' + ev.at + "</span>" + note +
        "</li>"
      );
    });
    // The bound marker sits after the last event the read returns.
    items.splice(
      bound, 0,
      '<li class="bound">' +
      '<span class="bound-tag">point-in-time read</span>' +
      "<span>EventsUpTo(" + bound + ") at " + story.point_in_time.at +
      " — returns " + story.point_in_time.returned + " events," +
      " the trajectory as of one consistent point</span>" +
      "</li>"
    );
    byId("trajectory-body").innerHTML =
      '<ol class="timeline">' + items.join("") + "</ol>";
  }

  // ---------- Act 2: checkpoint recovery ----------

  function step(kind, title, detail) {
    return (
      '<li class="step ' + kind + '">' +
      '<span class="step-title">' + title + "</span>" +
      '<span class="step-detail">' + detail + "</span>" +
      "</li>"
    );
  }

  const listEvents = (events) =>
    events
    .map((ev) => "seq " + ev.seq + " " + escapeHtml(ev.payload))
    .join(" · ");

  function renderCheckpoint(story) {
    const steps = [
      step("create", "Session created",
        "namespace <code>" + escapeHtml(story.namespace) + "</code>, session <code>" +
        escapeHtml(story.session) + "</code>, perpetual lifecycle — " + story.created_at),
      step("append", "Pre-baseline appends", listEvents(story.pre_baseline)),
      step("commit", "Atomic batch commit",
        listEvents(story.batch.events) +
        " + checkpoint " + story.batch.checkpoint_id +
        " — one append, one fdatasync: the event group and its baseline survive as a whole, " +
        story.batch.committed_at),
      step("append", "Work continues past the baseline", listEvents(story.post_baseline)),
      step("fault", "Interruption",
        "the store closes — this story's stand-in for a crash; acknowledged events and the " +
        "checkpoint are already durable, " + story.interruption.closed_at),
      step("reopen", "Reopen",
        "same directory, same lock discipline; stored checkpoint " +
        story.reopen.checkpoints[0].id + " listed, " + story.reopen.reopened_at),
      step("restore", "Restore to the baseline",
        "checkpoint " + story.restore.checkpoint_id +
        " — sequence counters back at seq " + story.restore.baseline_seq +
        "; a baseline to recover to, not a time machine, " + story.restore.restored_at),
      step("resume", "Resume",
        "<code>" + escapeHtml(story.resume.event.payload) + "</code> takes seq " +
        story.resume.event.seq + ", overwriting the stored seq " + story.resume.overwrites_seq +
        " — last write wins, the overwritten bytes become dead data the next merge reclaims, " +
        story.resume.event.at),
    ];

    const after = story.after;
    const finalEvents = [];
    for (let seq = 1; seq <= after.full_read.count; seq++) {
      let badge = "";
      let cls = "";
      if (seq <= story.restore.baseline_seq) {
        badge = "inside the baseline";
      } else if (seq === story.resume.event.seq) {
        badge = "rewritten by the resume";
        cls = " rewritten";
      } else {
        badge = "still on disk, still visible";
        cls = " residue";
      }
      finalEvents.push(
        '<span class="final-ev' + cls + '">seq ' + seq + "<em>" + badge + "</em></span>"
      );
    }

    byId("checkpoint-body").innerHTML =
      '<ol class="steps">' + steps.join("") + "</ol>" +
      '<div class="contrast">' +
      '<div class="contrast-side">' +
      '<h3>Point-in-time read to seq ' + after.point_in_time.seq + "</h3>" +
      "<p>" + after.point_in_time.returned + " events — the trajectory as of the baseline</p>" +
      "</div>" +
      '<div class="contrast-side">' +
      "<h3>Full read</h3>" +
      "<p>" + after.full_read.count + " events — data past the baseline never left the disk</p>" +
      "</div>" +
      "</div>" +
      '<div class="final-strip">' + finalEvents.join("") + "</div>";
  }

  // ---------- Act 3: benchmark cards ----------

  function card(title, scope, body, foot) {
    return (
      '<article class="card">' +
      "<h3>" + title + "</h3>" +
      '<p class="scope">' + scope + "</p>" +
      '<div class="figure">' + body + "</div>" +
      '<p class="source">' + foot + "</p>" +
      "</article>"
    );
  }

  function bars(engines) {
    const max = Math.max(...engines.map((e) => e.ops_sec));
    return engines
      .map(
        (e) =>
        '<div class="bar-row' + (e.name === "elena" ? " self" : "") + '">' +
        '<span class="bar-name">' + e.name + "</span>" +
        '<span class="bar-track"><span class="bar-fill" style="width:' +
        Math.round((e.ops_sec / max) * 100) + '%"></span></span>' +
        '<span class="bar-val">' + opsFmt(e.ops_sec) + " ops/s</span>" +
        '<span class="bar-p99">P99 ' + msFmt(e.p99_ms) + "</span>" +
        "</div>"
      )
      .join("");
  }

  const bigPair = (from, to, unit, via) =>
    '<p class="big">' +
    "<span>" + opsFmt(from) + "</span>" +
    '<span class="arrow">' + via + "</span>" +
    "<span>" + opsFmt(to) + " " + unit + "</span>" +
    "</p>";

  function renderBenchmarks(b) {
    const gc = b.group_commit;
    const jump = Math.round(gc.group_commit_ops_sec / gc.per_write_ops_sec);
    const cards = [
      card(
        "Same-tier write throughput",
        "YCSB A · 1 KiB values · every write synchronized · run phase",
        bars(b.same_tier_write.engines),
        "An elena append is one encoded write plus one fdatasync; bbolt pays a B+tree " +
        "transaction per write, badger and rosedb their value-log/WAL paths. " +
        "Source: matrix-grid.json"),
      card(
        "Where elena loses",
        "YCSB C read-only · 16 KiB values · warm-cache random reads",
        '<div class="bar-row self"><span class="bar-name">elena</span>' +
        '<span class="bar-val">' + intFmt(b.read_only_loss.elena_ops_sec) + " ops/s</span></div>" +
        '<div class="bar-row"><span class="bar-name">bbolt</span>' +
        '<span class="bar-val">' + intFmt(b.read_only_loss.bbolt_ops_sec) + " ops/s</span></div>",
        "bbolt keeps its whole B+tree in an mmap — a page-cache touch per read — while every " +
        "elena read is one positioned read into the data file; the bigger the value, the more " +
        "the copy costs. Reported as measured, with the structural reason. Source: matrix-grid.json"),
      card(
        "Group commit",
        "YCSB A · 64 B values · elena alone exposes the tier",
        bigPair(gc.per_write_ops_sec, gc.group_commit_ops_sec, "ops/s", "→") +
        '<p class="sub-figure">×' + jump + " — the fsync amortizes across the period</p>",
        "Per-write: every append fdatasync'ed before it acknowledges. Group commit: one flush " +
        "per 10 ms period. The other three engines expose no periodic-sync configuration, so " +
        "this tier has no aligned comparison row. Source: matrix-grid.json"),
      card(
        "Merge under load",
        "mixed append+read load · one public Merge mid-load · 500 ms windows",
        '<p class="big"><span>' + msFmt(b.merge_window.wall_ms) + "</span> merge wall time</p>" +
        '<p class="sub-figure">append P50 ' + msFmt(b.merge_window.p50_low_ms) + "–" +
        msFmt(b.merge_window.p50_high_ms) + " across " + b.merge_window.windows +
        " windows, " + intFmt(b.merge_window.appends_observed) + " appends observed</p>",
        "The load never stops: append P50s are flat before, during, and after the merge " +
        "window — a merge is a bounded period, not a stop. Source: focused-merge-curve.json"),
      card(
        "Startup, hint pair",
        "one dataset opened twice",
        '<div class="bar-row self"><span class="bar-name">hint files</span>' +
        '<span class="bar-val">' + msFmt(b.startup_pair.hint_open_ms) + "</span></div>" +
        '<div class="bar-row"><span class="bar-name">hint removed</span>' +
        '<span class="bar-val">' + msFmt(b.startup_pair.no_hint_open_ms) + "</span></div>",
        "The hint file is an index accelerator: without it, Open rebuilds the index by " +
        "scanning the data files in full — " + intFmt(b.startup_pair.live_entries) +
        " entries, " + b.startup_pair.data_mb.toFixed(1) + " MB. Source: focused-startup-pair.json"),
      card(
        "Write amplification",
        "flushed bytes / logical bytes, from the public Stats",
        '<p class="big"><span>' + b.write_amplification.before.toFixed(2) +
        '</span><span class="arrow">→</span><span>' +
        b.write_amplification.after.toFixed(2) + "</span></p>" +
        '<p class="sub-figure">across one explicit merge</p>',
        "The step is the merge's rewrite: one full copy of the live set, counted as flushed — " +
        "the accounting is demonstrated as numbers. Source: focused-write-amp.json"),
      card(
        "One writer",
        "append throughput · concurrency curve · syncing disabled",
        bigPair(b.single_writer.one_worker_ops_sec, b.single_writer.two_workers_ops_sec,
          "ops/s", "→") +
        '<p class="sub-figure">' + b.single_writer.ratio.toFixed(2) +
        "x with the second concurrent appender</p>",
        "The store is single-writer by design — one write mutex; concurrent appenders queue " +
        "rather than scale. Reported as the structural fact it is. Source: curve-sync-none.json"),
      card(
        "Steady state, half an hour",
        "pure append · 256 B values · every write synchronized",
        '<p class="big"><span>' + opsFmt(b.long_run.ops_sec) + " ops/s</span> sustained</p>" +
        '<p class="sub-figure">' + intFmt(b.long_run.ops) + " appends over " +
        b.long_run.minutes.toFixed(1) + " minutes</p>",
        "Throughput holds flat as the directory grows — an append-only load has no deferred " +
        "debt to pay mid-run. Source: scenario-append.json"),
    ];
    byId("benchmarks-body").innerHTML =
      '<div class="cards">' + cards.join("") + "</div>";
  }

  // ---------- boot ----------

  renderTrajectory(DATA.trajectory);
  renderCheckpoint(DATA.checkpoint);
  renderBenchmarks(DATA.benchmarks);
})();
