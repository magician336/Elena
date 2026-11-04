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
| `driver.go` | the `Driver` lifecycle and the name registry (`bench.For`) |
| `workload.go` | the operation language and the micro workload builder |
| `metrics.go` | per-operation latency collection and the throughput math |
| `harness.go` | phase orchestration and per-phase result collection |
| `elena_driver.go` | elena through the public library API |
| `bbolt_driver.go` | bbolt through its official Go package |
| `cmd/bench` | the CLI: one engine, one micro workload, printed numbers |

## Measurement path

One `bench.Run` opens the driver in a fresh directory, then drives each
workload phase in order: the phase's wall-clock span times its
throughput, every single operation gets its own latency observation,
and the driver is closed even when an operation fails. Latency samples
stay in the collector in full resolution — count, min, max, and mean
summarize them today, and percentile summaries (P50/P99/P999) will
derive from the same sample stream rather than a lossy aggregate.

The micro workload is one append phase over `count` fresh keys, then
one read phase over the same keys — sequential keys, 8-byte encodings,
deterministic values regenerated identically from the same arguments.
The shape mirrors the official `bbolt bench` tool's sequential write
and read modes, which is what lets the anchor below compare like with
like.

### Durability alignment

Both drivers ride the same regime: an append is complete only once it
is synchronized — elena's default write-synchronized appends, bbolt
committing one write transaction per append under its default
sync-on-commit. Reads never sync in either driver. One durability
level for every driver is the precondition for any cross-engine
number this harness prints.

## Usage

```
cd bench
go run ./cmd/bench --driver elena --ops 1000 --value-size 1024 --dir <fresh-dir>
go run ./cmd/bench --driver bbolt  --ops 1000 --value-size 1024 --dir <fresh-dir>
```

Each run prints one line per phase — operation count, wall time,
throughput in ops/sec, latency min/max/mean. The directory must not
exist; a rerun over leftovers is not a micro workload.

Gates (same commands locally as in CI, see the Makefile's `bench-*`
targets): the module's tests run with the race detector behind an 80%
coverage gate, and a seconds-scale smoke run drives every driver end
to end on every push.

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
