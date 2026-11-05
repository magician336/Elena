# Elena

**Append-only embedded storage engine in the Bitcask model, written in pure
Go — fsync tiers, merge, and kill -9 recovery in CI, plus trajectory,
session-TTL, and checkpoint semantics for durable agent state.**

Elena is an embedded state store built on its own
[Bitcask-model](https://github.com/basho/bitcask/blob/develop/doc/bitcask-intro.pdf)
storage engine: no external database, no daemon, one directory. Under the
default durability tier every append is fdatasync'ed before it is
acknowledged, and a kill -9 between any two acknowledged events loses
nothing — byte-exact on reopen, a claim CI tests. Above the
engine sits a semantic layer that models state the way long-running
processes use it: sessions grouped by namespace, each owning an append-only
trajectory of numbered events; whole-session TTLs; checkpoints; point-in-time
reads. Agent runtimes that append events and resume from checkpoints are the
workload it is built for; underneath them it is a plain, measurable storage
engine, evidence first.

## Evidence

A new store has no adoption history to point at, so this project points at
measurements instead — every claim below is a number someone else can
regenerate:

- **[Benchmarks: measured, declared, reproducible](docs/benchmarks/README.md)** —
  P50/P99/P999 latency (means never appear), YCSB A/B/C across five value
  sizes from 64 B to 16 KB, durability tiers aligned per engine, against
  bbolt, badger, and rosedb — one declared machine, one measurement path,
  raw JSON and regenerate commands checked in beside the report.
- **kill -9 fault injection runs in CI** — a suite kills the store mid-write
  and requires every acknowledged event to survive the reopen with its
  exact payload. A rig of the same construction, run by data-size tiers in
  the bench module, measures recovery: 166–176 ms to the first byte-exact
  read at 256 MB, 7.8–10.6 s at 4 GiB ([numbers](#benchmarks)).
- **Coverage gate ≥ 80% with the race detector** — library and benchmark
  harness alike; the kill -9 suite is part of the covered suite.
- **The harness is anchored to an official tool** — against `bbolt bench`
  on the same load, this harness's bbolt driver agrees within about
  4 percent (111 vs 107 ops/sec).

The report also carries the directions elena loses — large-value random
reads lose to bbolt's mmap clearly, stated with the structural reason in
[Benchmarks](#benchmarks) — because a comparison that only reports wins is
advertising, not evidence.

## The engine

A Bitcask-model store written from scratch in this repository, in pure Go,
with zero external dependencies in the library's `go.mod`. Every capability
below says why it holds, not just that it holds:

- **One-seek appends** — a write is one encoded record appended to the
  active data file plus one fdatasync. Writes are sequential I/O; no record
  is ever modified in place.
- **In-memory index, positioned reads** — the index maps every key to its
  position in the data files, so a read is one `pread` plus a copy of the
  payload. Measured: 75.3 bytes of index memory per live event, and
  ~1.0M read ops/sec at 64 B values.
- **Hint files** — Close writes hint files beside the data files; a
  following Open rebuilds the index from them without scanning the data.
  The measured pair on a 273.7 MB store: Open in 295.6 ms with hints,
  469.7 ms with the no-hint full data-file scan — the gap grows with cold
  caches and larger stores.
- **Merge** — live entries are rewritten into fresh data files and dead
  bytes (expired and superseded entries) are left out; that omission is the
  reclamation. Measured: write amplification 1.03 before a merge, 2.06 after
  one full rewrite of the live set — and append latency stays flat through
  it (P50 4.18–4.28 ms before, during, and after a 195 ms merge; the load
  never stops).
- **Three durability tiers, fixed at Open** — every-append fdatasync (the
  default, `WithSyncAlways`), group commit on a period (`WithSyncInterval`),
  or none (`WithSyncNone`); each option's doc comment states its loss
  window.
- **Bounded recovery** — data files are append-only and CRC-checked:
  recovery verifies the torn tail of one file, rebuilds the index (from
  hints or by scan), and reads. The cost is bounded by the tail plus the
  index rebuild — which is exactly what the kill -9 tiers measure.

## The semantic layer

The engine stores keys; the layer above stores state in the terms
long-running processes use. All five capabilities are native — none of them
is caller-side assembly over flat keys:

| capability | what it is |
| --- | --- |
| **Namespace** | the isolation and organization layer: sessions live in namespaces. |
| **Session + TTL** | one boundary unit owning exactly one trajectory. Perpetual, or timed: metadata and every event share one deadline, and once it elapses the whole session reads as if never written — immediately on the read path, the bytes returned by the background sweep's merge. |
| **Trajectory** | an append-only sequence of immutable events. Every append hands out the next sequence number — hole-free, strictly increasing, starting at 1 — and the sequence number and append timestamp travel with the payload on every read. |
| **Checkpoint** | a manifest of the whole store — every trajectory's reached sequence number, every session's deadline — written as one record, listable and restorable across close and reopen. A batch can commit an event group and its checkpoint as one atomic unit: one append, one fdatasync. |
| **Point-in-time read** | `EventsUpTo(seq)` returns the trajectory exactly as of a sequence number — the trajectory a checkpoint saw, or any consistent point. |

Sessions of this shape suit agent runtimes — one appended event per
exchange, a checkpoint at each consistent point, resume after any crash.
The CLI speaks exactly this layer: `status`, `dump`, `merge`, `checkpoint`,
`restore` are thin orchestrations of the same public API.

## How it compares

Elena sits between two groups: general-purpose embedded engines (bbolt,
badger, rosedb — same-measurement numbers in [Benchmarks](#benchmarks)), and
agent-state products assembled on top of someone else's storage. The table
lists four neighbors: the same-family engine, and the three nearest products
of that second group. Competitor cells are Yes/No read from each project's
own README (checked November 2026) — scattered capabilities are written Yes
where they exist, because they do:

| | storage base | shape | numbered trajectory | session TTL | checkpoint | point-in-time read |
| --- | --- | --- | --- | --- | --- | --- |
| **elena** | own Bitcask-model engine | embedded library + CLI | Yes | Yes — one deadline covers the whole session | Yes — whole-store manifests | Yes |
| [**rosedb**](https://github.com/rosedblabs/rosedb) | own Bitcask-model engine | embedded library | No — flat keys, no sequence semantics | Yes — per-key expire | No | No |
| [**engram**](https://github.com/Gentleman-Programming/engram) | SQLite + FTS5 | single binary: CLI, HTTP, MCP, TUI | No — memory retrieval records | No | No | No |
| [**beads**](https://github.com/gastownhall/beads) | Dolt | distributed CLI issue tracker | No — task dependency graph | summarized, not expired | No | No |
| [**MuninnDB**](https://github.com/scrypster/muninndb) | Pebble | networked service (REST, gRPC, MCP) + cluster | No — cognitive memory items | Yes — decay is a core primitive, probabilistic | No | No |

- beads summarizes old closed tasks to save context window; summarization
  is not a session TTL.
- MuninnDB's forgetting is engine-native Ebbinghaus decay — probabilistic
  by design, the opposite register from elena's deterministic deadlines.
- The precise claim is scoped to this table: each capability exists
  somewhere as a scattered point (badger ships per-key TTL too); the
  combination — numbered trajectories under sessions with deadlines,
  whole-store checkpoints, point-in-time reads — is what no other row
  carries, on an engine built in the same repository with its measurements
  published.

When to choose something else — the honest rows:

- **bbolt** for read-heavy or range-scan workloads at larger values: its
  mmap read path beats elena's from 1 KB up ([numbers](#benchmarks)).
- **badger** for general-purpose KV that wants per-key TTL and versioned
  access on a mature LSM engine.
- **rosedb** for general-purpose KV with key watch and iterators on the
  same engine family.
- **engram** for semantic memory retrieval over what a coding agent said —
  search and recall, not durable event streams.
- **beads** for managing a coding agent's task dependency graph.
- **MuninnDB** for memory that fades with disuse, served over a network
  with clustering.

Elena's lane: append events under sessions, read trajectories back to
consistent points, resume from checkpoints — durability and deterministic
semantics first, and every performance claim regenerable.

## Quick start

From a clone of this repository. The library core is three calls — open a
store, create a session, append (fragment; complete programs in
[`examples/`](examples/)):

```go
import "elena"

store, _ := elena.Open(dir)                     // one directory, created if absent; every append
                                                // fdatasync'ed before it acknowledges (default tier)
sess, _ := store.CreateSession("runs", "alpha") // a perpetual session in namespace "runs"
seq, _ := sess.Append([]byte("event one"))      // seq 1 — hole-free, strictly increasing from here
past, _ := sess.EventsUpTo(seq)                 // point-in-time read: the trajectory as of seq
```

Error returns are omitted for brevity; every call has one. Write a demo
store, then inspect it with the CLI — every CLI command is a thin
orchestration of the same public API:

```sh
go run ./examples/session-trajectory -dir ./demo-store  # append events, read, point-in-time read
go run ./examples/checkpoint-restore -dir ./demo-store  # atomic event group + checkpoint, restore
go run ./examples/ttl-lifecycle                        # timed session expiry, sweep and merge
go run ./cmd/elena status ./demo-store                 # namespaces, sessions, dead ratio, write amplification
go run ./cmd/elena dump ./demo-store                   # every trajectory: sequence numbers, timestamps
go run ./cmd/elena checkpoint ./demo-store             # record one whole-store baseline, print its ID
go run ./cmd/elena restore ./demo-store                # restore the latest baseline
go run ./cmd/elena merge ./demo-store                  # reclaim dead bytes, stats before and after
rm -rf ./demo-store                                    # a store is one directory — the whole footprint
```

The static [demo page](web/index.html) narrates the same story in a browser
— a trajectory timeline, a checkpoint recovery run, the measured numbers —
no server, no build step. The suite behind the claims:

```sh
go test ./...    # full suite, kill -9 recovery included
```

## Benchmarks

Engine-side comparison against bbolt, badger, and rosedb: one harness
drives every engine through the same lifecycle, observes every operation,
and reports P50/P99/P999 — means never. Durability tiers are aligned per
engine and declared; cross-engine numbers only exist inside one tier.
Everything below is one machine, one run per cell, a declared environment
(NVMe SSD on btrfs, zipfian keys, seeds declared per table); the full
matrices, the raw JSON, and the regenerate commands are in
[the report](docs/benchmarks/README.md).

**Where elena wins.** Under the every-write-synchronized tier, elena
carries the highest update throughput of the four at every value size in
YCSB A — 473 ops/sec at 64 B values against bbolt's 122, badger's 261, and
rosedb's 252 — because a Bitcask append is one encoded write plus one
fdatasync, while the others pay a B+tree transaction or their value-log
structures per update. Read-only at 64 B values, elena and bbolt tie at the
top (~1.0M ops/sec, sub-microsecond P50s). On the group-commit tier
(10 ms flush period) — which only elena of the four can configure —
write-heavy mixes run at ~100k ops/sec at small values: 112,619 ops/sec in
YCSB A at 64 B, with the tail cost at larger values declared in the report.

**Where elena loses.** Large-value random reads. From 1 KB values up under
the read-only C mix, bbolt's mmap wins clearly: 960k vs elena's 198k
ops/sec at 1 KB (4.8×), 684k vs 101k at 16 KB (6.8×). The structure is the
reason: every elena read is a `pread` plus a copy out of the data file —
the in-memory index holds positions, not payloads — while bbolt's whole
B+tree lives in an mmap, so a random point read is a page-cache touch with
no syscall and no copy. The bigger the value, the more elena pays per read
and the less bbolt does. Both directions are in the report as measured.

**Steady state, not just peaks.** A 33-minute pure-append run under
every-write sync sustained 149.7 ops/sec flat as the store grew to ~98 MB —
no decay, no deferred debt to pay later. The report's long-run badger cell
documents a 1,521 ms maximum write stall from an LSM's deferred background
reorganization — a number no minutes-scale cell shows, and the reason the
report runs long cells at all.

**Recovery measured, not asserted.** The kill -9 rig in [`bench/`](bench/)
kills a writing child process and times the reopen from a fresh process;
every acknowledged event must read back byte-exact before a number counts:

| dataset | kill → first byte-exact read |
| --- | --- |
| 256 MB (4,096 × 64 KiB) | 166–176 ms |
| 1 GiB (16,384 × 64 KiB) | 0.58–0.70 s |
| 4 GiB (65,536 × 64 KiB) | 7.8–10.6 s |

**Methodology anchor.** Against the official `bbolt bench` tool on the same
load, this harness's bbolt driver agrees within about 4 percent — 111 vs
107 ops/sec, the filesystem's sync dominating both. The self-built numbers
have a trusted anchor.

## Limitations

Pick the right tool for the job — elena is no exception. Two lists: what is
deliberately out of scope, and what the structure itself costs.

Out of scope, by decision:

- **No HTTP service or network protocols** — elena is a library linked into
  one process; put a service in front if you need one.
- **No vector search and no full-text search** — no embeddings, no ranking.
- **No intelligent memory retrieval** — no LLM calls, no contradiction
  resolution, no forgetting policy. The semantics are deterministic: what
  you appended is what you read.
- **No Raft, replication, or distribution** — one store, one directory, one
  machine.
- **No multi-tenancy** — the isolation unit is the namespace, inside one
  process's trust boundary.
- **No cross-language SDK** — it is a Go library.
- **No encryption or access control** — the store trusts its host process.
- **No second storage model** — the engine is Bitcask-model; an LSM variant
  is out of scope for this codebase.

Structural costs, stated plainly:

- **The whole index lives in memory.** A Bitcask-family store maps every
  key to its position and holds that map in RAM — measured 75.3 B per live
  event. The live key space must fit memory; rosedb carries the same
  constraint and states it the same way.
- **One writer per store.** Appends serialize on a single write mutex with
  the fdatasync inside it; concurrent appenders queue rather than scale.
  The throughput-concurrency curve in the report is that statement as
  numbers.
- **Random reads of large values lose to mmap engines.** Every read is a
  `pread` plus a copy; from 1 KB values up, bbolt wins clearly — 4.8× at
  1 KB, 6.8× at 16 KB under the read-only mix ([numbers](#benchmarks)).
- **Dead bytes wait for the merge.** Superseded and expired entries hold
  their disk space until a merge rewrites the live set; between merges the
  directory only grows. Under append-heavy loads the data set grows
  monotonically — an acknowledged event is never overwritten away.

## Repository map

Every claim above resolves to something you can open, run, or regenerate:

- [`examples/`](examples/) — three runnable programs, each with its test:
  session and trajectory reads, checkpoint restore, TTL lifecycle.
- [`cmd/elena`](cmd/elena) — the diagnostic CLI: `status`, `dump`, `merge`,
  `checkpoint`, `restore` — thin orchestration of the public library API,
  no storage logic of its own.
- [`web/index.html`](web/index.html) — the static single-page demo:
  trajectory timeline, checkpoint recovery, the measured numbers —
  generated from the public API with a fixed seed, opens straight off the
  disk.
- [`docs/benchmarks/`](docs/benchmarks/) — the benchmark report:
  environment declaration, methodology, every table, and the raw JSON each
  table cites.
- [`bench/`](bench/) — the measurement harness (a separate Go module, so
  the library keeps zero dependencies) and the kill -9 recovery rig; every
  report number regenerates from here.
- [`GLOSSARY.md`](GLOSSARY.md) — the project's terms — trajectory, session,
  checkpoint, merge — and the words it deliberately avoids.

## License

Apache-2.0 — see [LICENSE](LICENSE).
