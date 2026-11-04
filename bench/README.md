# bench — benchmark harness

Throughput and latency harness for the elena store and the engines it
compares against. One lifecycle drives every engine — open a fresh
directory, execute single operations, close — while the harness times
each phase wall-clock and observes every single operation, so numbers
across engines come off one declared measurement path.

The harness lives in its own Go module: it requires the root elena
module through a local `replace` plus the competitor client packages,
which keeps the root module's zero-external-dependency guarantee
untouched. The elena driver imports the public `elena` library only —
no internal package is importable across a module boundary, so every
elena number is taken off the public surface an embedded host uses.

## Layout

| File | Role |
| --- | --- |
| `driver.go` | the `Driver` lifecycle, the sync tiers, and the name registry (`bench.For`, `bench.ForTier`) |
| `workload.go` | the operation language and the micro workload builder |
| `generator.go` | the seeded mixed-workload generator: declared read/write ratio, uniform or Zipf key distribution |
| `ycsb.go` | the YCSB A/B/C mixes and their two-phase load/run workloads |
| `metrics.go` | per-operation latency collection, the throughput math, and nearest-rank quantiles (P50/P99/P999) |
| `harness.go` | phase orchestration and per-phase result collection |
| `matrix.go` | the comparison matrix: plan, per-engine durability declarations, and the run |
| `report.go` | deterministic rendering of results: markdown report plus raw JSON |
| `matrix_report.go` | deterministic rendering of the matrix: declarations beside the numbers |
| `elena_driver.go` | elena through the public library API |
| `bbolt_driver.go` | bbolt through its official Go package |
| `badger_driver.go` | badger through its official Go package |
| `rosedb_driver.go` | rosedb through its official Go package |
| `cmd/bench` | the CLI: one engine, one micro workload, printed numbers |
| `cmd/matrix` | the CLI: the YCSB comparison matrix, one command, any subset |

## Measurement path

One `bench.Run` opens the driver in a fresh directory, then drives each
workload phase in order: the phase's wall-clock span times its
throughput, every single operation gets its own latency observation,
and the driver is closed even when an operation fails. Latency samples
stay in the collector in full resolution, and every summary reads the
original measurements: count, min, max, mean, and the P50/P99/P999
trio by the nearest-rank method (`bench.Quantile` ranks a private
ascending copy and returns the observed sample at rank `ceil(p*n)` —
never an interpolated value; samples too few for a rank to resolve
below the maximum degrade to the maximum, explicitly). Reports print
the percentile trio, not averages: tail latency is the claim.

The micro workload is one append phase over `count` fresh keys, then
one read phase over the same keys — sequential keys, 8-byte encodings,
deterministic values regenerated identically from the same arguments.
The shape mirrors the official `bbolt bench` tool's sequential write
and read modes, which is what lets the anchor below compare like with
like.

## Generated workloads

`bench.Generate` builds a mixed phase from a declared spec: operation
count, read share, key space, key distribution (uniform, or truncated
Zipf shaped by theta — YCSB's zipfian default is 0.99), value size,
and a seed. The generated stream carries exactly
`round(Ops*ReadRatio)` reads shuffled through the appends, keys whose
measured frequencies equal the declared distribution (held by
statistical pins at five sigmas of the binomial spread), and values
derived from key and write ordinal, so repeated writes of a key never
share a payload. One seed selects the whole sequence: the same spec
regenerates the identical workload every time.

A generated phase is a composition unit, not a runnable claim about
key coverage: a mixed phase may read a key before writing it, exactly
as YCSB separates a load phase (pure appends that populate the key
space) from a run phase (the declared mix). Callers compose the two
into one `Workload` — load phases first, run phases behind — and the
assembly runs through the same `bench.Run` lifecycle as the micro
workload.

## Reports

`bench.RenderMarkdown` and `bench.RenderJSON` turn result slices into
the two report forms: a markdown table — one row per phase, latency as
P50/P99/P999 plus min and max — and a raw JSON document with fixed
field names in fixed order and nanosecond durations, the shape two
runs diff line by line. Rendering is a pure function of the numbers:
no clocks, no engine knowledge, same input → same bytes, every time.
Neither form prints a mean.

### Durability alignment

Two tiers exist. The per-write tier aligns every engine at its
every-write-synchronized configuration; the group-commit tier aligns at
periodic synchronization, one flusher sync per period. Cross-engine
numbers are only produced inside one tier — a tier an engine cannot
configure is declared and produces no numbers for that engine, never a
mismatched comparison. Every report prints the standing engine by
engine:

- elena, per-write: every append fdatasync'ed before it acknowledges
  (`WithSyncAlways`, the store default); group-commit: appends
  acknowledge after the write, a background flusher fdatasyncs once
  per the declared period (`WithSyncInterval`) — the only engine of the
  four that exposes a periodic-sync configuration.
- bbolt: one write transaction per append under its default
  sync-on-commit (`NoSync` left false); no periodic-sync configuration
  exists, so the group-commit tier produces no bbolt numbers.
- badger: `SyncWrites` set to true — badger msyncs the write-ahead
  value log after every write, the tier its option documentation scopes
  as surviving hard reboots (the v4 default is false; set explicitly);
  no periodic-sync configuration, so the group-commit tier produces no
  badger numbers.
- rosedb: every append runs as one batch with `Sync=true`, whose commit
  fsyncs the write-ahead log before it acknowledges. Per the v2 source,
  this is the only per-write synchronized path that exists: the
  DB-level `Sync` flag governs a write-ahead path the batch operations
  never ride, `db.Put` runs its internal batch with sync off, and a
  DB-level `Sync=true` would disable the batch-level sync — so the
  driver keeps the DB-level flag false and rides the batch tier. The
  `BytesPerSync` option flushes by accumulated bytes, not by a time
  period, which is not the group-commit tier; it produces no rosedb
  numbers either.

Reads never sync in any driver.

## The comparison matrix

`bench.RunMatrix` runs the declared full comparison — every engine,
the three YCSB mixes (A 50/50 read/update, B 95/5, C read-only), the
five value-size steps 64B/256B/1KB/4KB/16KB — or any subset of engines,
mixes, and sizes, at the tiers each engine supports. Each workload is
the YCSB two-phase shape: a load phase appending exactly one value per
key in key order, then a run phase carrying the mix's declared ratio
over the same keys. One workload per mix and value size is generated
once per run and driven identically by every engine and tier — the
same seed selects the same operation sequence for everyone, so driver
differences never leak into workload semantics. Keys draw from YCSB's
zipfian (theta 0.99) by default, uniform on request.

Elena-side mapping, declared with the numbers: one key is one
trajectory (namespace "bench", session "traj-\<key\>"); an update
appends the trajectory's next event; a read is the single-event read of
the latest acknowledged event. The elena data set grows monotonically —
an update is a new event, never an in-place overwrite — while the KV
engines overwrite in place. That difference is elena's append-only
semantics, declared rather than hidden: the comparison's common
language is the workload mix, and the mapping statement travels with
every number.

The matrix renders two report forms, both deterministic functions of
the result: a markdown report and a raw JSON document whose phase
fields are the single-run raw document's — one phase shape across
every raw file this module emits. The declarations (workload
semantics, elena mapping, per-engine durability standings) are part of
both documents, ahead of and beside the numbers.

## Usage

```
cd bench
go run ./cmd/bench --driver elena --ops 1000 --value-size 1024 --dir <fresh-dir>
go run ./cmd/bench --driver bbolt  --ops 1000 --value-size 1024 --dir <fresh-dir>
```

Each run prints one line per phase — operation count, wall time,
throughput in ops/sec, latency min/max/mean. The directory must not
exist; a rerun over leftovers is not a micro workload.

The full comparison matrix, one command — every engine, every mix,
every value size, at the aligned tiers (subsets by flag):

```
go run ./cmd/matrix --root <fresh-root> \
  --keys 1000000 --run-ops 2000000 --seed 1 \
  --json-out matrix.json --md-out matrix.md
go run ./cmd/matrix --root <fresh-root> --engines elena,bbolt --mixes a --values 1024
```

Each run prints one line per produced cell — throughput and P99 per
phase — then a summary; the JSON and markdown reports carry the
declarations with the numbers. The root must not exist, and each cell
runs in its own fresh directory under it.

Gates (same commands locally as in CI, see the Makefile's `bench-*`
targets): the module's tests run with the race detector behind an 80%
coverage gate, and a seconds-scale smoke run drives every driver end
to end on every push — the matrix's own end-to-end test is that smoke
run in test form, every engine on a handful of operations.

## Methodology anchor

Before extending this harness toward wider matrices, its numbers need
an anchor against the engine vendor's own tool: run the official
`bbolt bench` command on the same load shape and compare it with this
harness's bbolt driver. Recorded run of 2026-11-04, environment as
declared below.

**Alignment.** 1000 appends of 1024-byte values over sequential
8-byte keys; official tool run as
`bbolt bench -count 1000 -value-size 1024 -key-size 8 -write-mode seq -read-mode seq`
with the batch size pinned to 1 — one write transaction per append,
the granularity this harness's driver uses per operation. Sync on in
both (no `-no-sync`).

| Path | Tool | Write throughput | Per-op write |
| --- | --- | --- | --- |
| per-append transaction | official `bbolt bench -batch-size 1` | 107 ops/sec | 9.31 ms |
| per-append transaction | this harness, bbolt driver | 111 ops/sec | 8.98 ms mean |
| whole run in one transaction | official `bbolt bench` default batch | 22,689 ops/sec | 44.1 µs |

**Conclusion.** At the aligned granularity the harness and the vendor
tool agree within about 4 percent — 111 against 107 ops/sec, the same
magnitude on a load the filesystem's own sync cost dominates (about
9 ms per synchronized append on this disk). The harness's measurement
path adds no visible distortion at that scale. The third row is the
lesson that makes the alignment matter: the official tool's default
batch is the whole count in one transaction, 200x the throughput of
the per-append regime on the identical load — transaction granularity,
not engine speed, is the first variable of any throughput number, and
cross-tool comparisons are only valid at a declared, matched
granularity. This harness drives every operation through one declared
lifecycle call for exactly that reason.

The read side differs structurally, by design, and is not compared
numerically: the official tool reads by cursor-scanning a bucket
inside one long-lived read transaction until a second elapses, while
this harness issues each read as its own call — one transaction per
operation, the shape a host application actually issues. Numbers are
only compared where the structures line up.

The elena driver on the same load, for orientation: 111 ops/sec
appends under its default write-synchronized regime — the durability
alignment with bbolt's per-transaction sync shows up as the same
order of throughput, with the filesystem's sync cost dominating both.

**Environment declaration** (numbers above are only meaningful
against it): NVMe SSD, btrfs with `compress=zstd:1`, Linux 6.19.10,
Go 1.27.3, bbolt v1.4.3, single-threaded driver, 2026-11-04. Note the
obvious trap this run caught: the same loads on a tmpfs `/tmp`
reported per-append throughput in the tens of thousands of ops/sec —
tmpfs makes sync a no-op, so any fsync-aligned benchmark must run on
the real disk it claims to measure.
