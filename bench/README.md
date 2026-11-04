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
| `generator.go` | the seeded mixed-workload generator: declared read/write ratio, uniform or Zipf key distribution |
| `metrics.go` | per-operation latency collection, the throughput math, and nearest-rank quantiles (P50/P99/P999) |
| `harness.go` | phase orchestration and per-phase result collection |
| `report.go` | deterministic rendering of results: markdown report plus raw JSON |
| `elena_driver.go` | elena through the public library API |
| `bbolt_driver.go` | bbolt through its official Go package |
| `cmd/bench` | the CLI: one engine, one micro workload, printed numbers |

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
