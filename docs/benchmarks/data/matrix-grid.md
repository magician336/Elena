# YCSB matrix report

## Declarations

workload: one seed declares every workload: each engine and tier drives the identical load-then-run operation sequence the seeded generator emits, so driver differences never leak into workload semantics

elena mapping: one key is one trajectory (namespace bench, session traj-<key>); an update appends the trajectory's next event; a read is the single-event read of the latest acknowledged event. The elena data set grows monotonically — an update is a new event, never an in-place overwrite — while the KV engines overwrite in place; that difference is elena's append-only semantics, declared here rather than hidden

durability, engine by engine:

- elena
  - per-write-sync (aligned): every append fdatasync'ed to the data file before it acknowledges (elena.WithSyncAlways, the store default)
  - group-commit (aligned): appends acknowledge after the write; a background flusher fdatasyncs the active data file once per 10ms (elena.WithSyncInterval; the final flush is forced at close and at every rotation)
- bbolt
  - per-write-sync (aligned): every append commits as its own write transaction under bbolt's default sync-on-commit (Options.NoSync left false)
  - group-commit (not aligned): bbolt exposes no periodic-sync configuration (NoSync only turns syncing off), so this tier produces no bbolt numbers
- badger
  - per-write-sync (aligned): SyncWrites set to true: badger msyncs the write-ahead value log after every write, the tier its option documentation scopes as surviving hard reboots (the v4 default is false; set explicitly)
  - group-commit (not aligned): badger exposes no periodic-sync configuration (SyncWrites false drops to plain mmap writes), so this tier produces no badger numbers
- rosedb
  - per-write-sync (aligned): every append runs as one batch with Sync=true, whose commit fsyncs the write-ahead log before it acknowledges (the DB-level Sync flag stays false — in rosedb v2 it governs a write-ahead path the batch operations never ride, and it disables the batch-level sync)
  - group-commit (not aligned): rosedb's BytesPerSync flushes by accumulated bytes rather than by a time period, which is not this tier, so it produces no rosedb numbers

## Results

| engine | sync tier | mix | value | phase | ops | elapsed | ops/sec | p50 | p99 | p999 | min | max |
| --- | --- | --- | --- | --- | --- | --- | --- | --- | --- | --- | --- | --- |
| elena | per-write-sync | ycsb-a | 64B | load | 10000 | 1m30.612323s | 110.4 | 8.806ms | 15.396ms | 17.744ms | 7.601ms | 56.453ms |
| elena | per-write-sync | ycsb-a | 64B | run | 10000 | 21.138122s | 473.1 | 78µs | 4.83ms | 11.069ms | 0s | 14.525ms |
| elena | per-write-sync | ycsb-a | 256B | load | 10000 | 1m30.502171s | 110.5 | 8.827ms | 14.338ms | 17.657ms | 7.717ms | 25.94ms |
| elena | per-write-sync | ycsb-a | 256B | run | 10000 | 21.312967s | 469.2 | 222µs | 4.862ms | 11.825ms | 1µs | 15.815ms |
| elena | per-write-sync | ycsb-a | 1024B | load | 10000 | 1m30.045804s | 111.1 | 8.801ms | 13.229ms | 17.902ms | 7.205ms | 24.141ms |
| elena | per-write-sync | ycsb-a | 1024B | run | 10000 | 34.206987s | 292.3 | 66µs | 12.003ms | 17.605ms | 1µs | 52.511ms |
| elena | per-write-sync | ycsb-a | 4096B | load | 10000 | 2m26.950618s | 68.1 | 14.197ms | 25.336ms | 51.291ms | 10.2ms | 783.029ms |
| elena | per-write-sync | ycsb-a | 4096B | run | 10000 | 35.822067s | 279.2 | 5.079ms | 9.821ms | 35.948ms | 3µs | 38.816ms |
| elena | per-write-sync | ycsb-a | 16384B | load | 10000 | 2m28.74069s | 67.2 | 14.501ms | 24.193ms | 46.996ms | 10.097ms | 77.113ms |
| elena | per-write-sync | ycsb-a | 16384B | run | 10000 | 37.518609s | 266.5 | 216µs | 8.964ms | 17.336ms | 9µs | 71.215ms |
| elena | per-write-sync | ycsb-b | 64B | load | 10000 | 2m28.584659s | 67.3 | 14.405ms | 25.986ms | 40.441ms | 12.8ms | 114.168ms |
| elena | per-write-sync | ycsb-b | 64B | run | 10000 | 3.755349s | 2662.9 | 2µs | 7.355ms | 13.448ms | 0s | 37.392ms |
| elena | per-write-sync | ycsb-b | 256B | load | 10000 | 2m38.862821s | 62.9 | 14.546ms | 45.554ms | 59.177ms | 12.797ms | 118.398ms |
| elena | per-write-sync | ycsb-b | 256B | run | 10000 | 3.616322s | 2765.2 | 3µs | 7.133ms | 12.985ms | 1µs | 38.767ms |
| elena | per-write-sync | ycsb-b | 1024B | load | 10000 | 2m39.209377s | 62.8 | 14.799ms | 45.521ms | 48.323ms | 12.899ms | 190.401ms |
| elena | per-write-sync | ycsb-b | 1024B | run | 10000 | 3.867724s | 2585.5 | 3µs | 7.507ms | 15.266ms | 0s | 27.634ms |
| elena | per-write-sync | ycsb-b | 4096B | load | 10000 | 2m37.131002s | 63.6 | 14.917ms | 32.964ms | 52.333ms | 12.893ms | 189.681ms |
| elena | per-write-sync | ycsb-b | 4096B | run | 10000 | 3.938382s | 2539.1 | 6µs | 7.802ms | 14.093ms | 1µs | 27.737ms |
| elena | per-write-sync | ycsb-b | 16384B | load | 10000 | 2m42.319842s | 61.6 | 14.959ms | 30.598ms | 104.03ms | 12.804ms | 880.819ms |
| elena | per-write-sync | ycsb-b | 16384B | run | 10000 | 4.164524s | 2401.2 | 16µs | 7.504ms | 13.155ms | 5µs | 19.03ms |
| elena | per-write-sync | ycsb-c | 64B | load | 10000 | 2m38.289862s | 63.2 | 14.732ms | 30.124ms | 57.801ms | 12.288ms | 464.284ms |
| elena | per-write-sync | ycsb-c | 64B | run | 10000 | 9.718ms | 1029050.9 | 1µs | 2µs | 8µs | 0s | 189µs |
| elena | per-write-sync | ycsb-c | 256B | load | 10000 | 2m40.964794s | 62.1 | 14.878ms | 45.175ms | 57.391ms | 12.487ms | 247.263ms |
| elena | per-write-sync | ycsb-c | 256B | run | 10000 | 12.444ms | 803626.5 | 1µs | 3µs | 9µs | 0s | 21µs |
| elena | per-write-sync | ycsb-c | 1024B | load | 10000 | 2m44.718088s | 60.7 | 14.891ms | 45.342ms | 155.009ms | 12.799ms | 846.02ms |
| elena | per-write-sync | ycsb-c | 1024B | run | 10000 | 50.41ms | 198373.9 | 1µs | 110µs | 359µs | 1µs | 2.779ms |
| elena | per-write-sync | ycsb-c | 4096B | load | 10000 | 2m43.992878s | 61.0 | 15.049ms | 31.087ms | 145.38ms | 12.698ms | 1.214076s |
| elena | per-write-sync | ycsb-c | 4096B | run | 10000 | 30.418ms | 328749.4 | 3µs | 9µs | 13µs | 1µs | 27µs |
| elena | per-write-sync | ycsb-c | 16384B | load | 10000 | 2m41.377998s | 62.0 | 15.295ms | 30.599ms | 60.795ms | 12.999ms | 171.356ms |
| elena | per-write-sync | ycsb-c | 16384B | run | 10000 | 98.704ms | 101313.1 | 9µs | 46µs | 73µs | 5µs | 345µs |
| elena | group-commit | ycsb-a | 64B | load | 10000 | 11.25871s | 888.2 | 290µs | 8.996ms | 13.863ms | 8µs | 19.02ms |
| elena | group-commit | ycsb-a | 64B | run | 10000 | 88.795ms | 112618.9 | 2µs | 6µs | 22µs | 0s | 8.471ms |
| elena | group-commit | ycsb-a | 256B | load | 10000 | 8.357907s | 1196.5 | 221µs | 8.487ms | 13.587ms | 6µs | 43.417ms |
| elena | group-commit | ycsb-a | 256B | run | 10000 | 95.348ms | 104879.4 | 2µs | 8µs | 22µs | 0s | 16.068ms |
| elena | group-commit | ycsb-a | 1024B | load | 10000 | 12.859557s | 777.6 | 304µs | 9.58ms | 15.601ms | 7µs | 72.304ms |
| elena | group-commit | ycsb-a | 1024B | run | 10000 | 97.013ms | 103078.7 | 3µs | 11µs | 31µs | 1µs | 14.614ms |
| elena | group-commit | ycsb-a | 4096B | load | 10000 | 11.237962s | 889.8 | 254µs | 9.423ms | 17.327ms | 14µs | 103.913ms |
| elena | group-commit | ycsb-a | 4096B | run | 10000 | 221.927ms | 45059.8 | 6µs | 19µs | 8.704ms | 1µs | 17.219ms |
| elena | group-commit | ycsb-a | 16384B | load | 10000 | 12.425771s | 804.8 | 277µs | 9.471ms | 15.994ms | 21µs | 61.49ms |
| elena | group-commit | ycsb-a | 16384B | run | 10000 | 471.76ms | 21197.2 | 13µs | 40µs | 12.95ms | 6µs | 56.936ms |
| elena | group-commit | ycsb-b | 64B | load | 10000 | 12.168739s | 821.8 | 283µs | 9.318ms | 15.724ms | 5µs | 29.757ms |
| elena | group-commit | ycsb-b | 64B | run | 10000 | 79.529ms | 125740.2 | 1µs | 5µs | 18µs | 1µs | 8.251ms |
| elena | group-commit | ycsb-b | 256B | load | 10000 | 12.079289s | 827.9 | 330µs | 8.861ms | 13.959ms | 5µs | 158.236ms |
| elena | group-commit | ycsb-b | 256B | run | 10000 | 97.564ms | 102497.3 | 2µs | 8µs | 20µs | 1µs | 13.237ms |
| elena | group-commit | ycsb-b | 1024B | load | 10000 | 11.438033s | 874.3 | 268µs | 9.183ms | 14.099ms | 5µs | 18.383ms |
| elena | group-commit | ycsb-b | 1024B | run | 10000 | 101.502ms | 98520.1 | 2µs | 11µs | 32µs | 1µs | 14.497ms |
| elena | group-commit | ycsb-b | 4096B | load | 10000 | 11.309079s | 884.2 | 268µs | 9.027ms | 14.89ms | 10µs | 70.408ms |
| elena | group-commit | ycsb-b | 4096B | run | 10000 | 249.975ms | 40004.0 | 5µs | 18µs | 7.856ms | 2µs | 13.715ms |
| elena | group-commit | ycsb-b | 16384B | load | 10000 | 13.389798s | 746.8 | 292µs | 9.562ms | 16.044ms | 18µs | 69.848ms |
| elena | group-commit | ycsb-b | 16384B | run | 10000 | 499.167ms | 20033.4 | 13µs | 34µs | 9.218ms | 6µs | 26.975ms |
| elena | group-commit | ycsb-c | 64B | load | 10000 | 11.740272s | 851.8 | 272µs | 9.198ms | 14.015ms | 8µs | 20.51ms |
| elena | group-commit | ycsb-c | 64B | run | 10000 | 8.659ms | 1154816.6 | 1µs | 2µs | 5µs | 0s | 14µs |
| elena | group-commit | ycsb-c | 256B | load | 10000 | 10.308921s | 970.0 | 250µs | 8.925ms | 14.827ms | 5µs | 92.863ms |
| elena | group-commit | ycsb-c | 256B | run | 10000 | 25.043ms | 399307.9 | 1µs | 7µs | 334µs | 0s | 1.957ms |
| elena | group-commit | ycsb-c | 1024B | load | 10000 | 11.159611s | 896.1 | 274µs | 9.084ms | 13.801ms | 5µs | 26.448ms |
| elena | group-commit | ycsb-c | 1024B | run | 10000 | 11.545ms | 866172.8 | 1µs | 3µs | 10µs | 1µs | 13µs |
| elena | group-commit | ycsb-c | 4096B | load | 10000 | 11.818597s | 846.1 | 307µs | 9.243ms | 14.427ms | 12µs | 82.391ms |
| elena | group-commit | ycsb-c | 4096B | run | 10000 | 33.819ms | 295692.0 | 3µs | 11µs | 17µs | 1µs | 25µs |
| elena | group-commit | ycsb-c | 16384B | load | 10000 | 13.540193s | 738.5 | 293µs | 9.703ms | 16.993ms | 11µs | 91.268ms |
| elena | group-commit | ycsb-c | 16384B | run | 10000 | 286.792ms | 34868.5 | 9µs | 258µs | 488µs | 5µs | 2.139ms |
| bbolt | per-write-sync | ycsb-a | 64B | load | 10000 | 2m38.653063s | 63.0 | 15ms | 30.11ms | 53.059ms | 12.5ms | 91.215ms |
| bbolt | per-write-sync | ycsb-a | 64B | run | 10000 | 1m21.937625s | 122.0 | 601µs | 31.621ms | 49.179ms | 1µs | 108.104ms |
| bbolt | per-write-sync | ycsb-a | 256B | load | 10000 | 2m37.670287s | 63.4 | 14.595ms | 34.982ms | 61.206ms | 12.703ms | 509.725ms |
| bbolt | per-write-sync | ycsb-a | 256B | run | 10000 | 1m25.030156s | 117.6 | 2.42ms | 39.289ms | 60.06ms | 1µs | 2.039252s |
| bbolt | per-write-sync | ycsb-a | 1024B | load | 10000 | 2m43.737829s | 61.1 | 14.663ms | 45.866ms | 166.241ms | 10.495ms | 656.64ms |
| bbolt | per-write-sync | ycsb-a | 1024B | run | 10000 | 1m21.102764s | 123.3 | 2.053ms | 28.892ms | 46.561ms | 1µs | 165.122ms |
| bbolt | per-write-sync | ycsb-a | 4096B | load | 10000 | 2m45.992327s | 60.2 | 15.12ms | 37.652ms | 86.133ms | 13.178ms | 223.906ms |
| bbolt | per-write-sync | ycsb-a | 4096B | run | 10000 | 1m28.249018s | 113.3 | 3.181ms | 31.904ms | 51.581ms | 1µs | 295.033ms |
| bbolt | per-write-sync | ycsb-a | 16384B | load | 10000 | 2m44.433231s | 60.8 | 15.704ms | 29.466ms | 71.568ms | 13.406ms | 397.633ms |
| bbolt | per-write-sync | ycsb-a | 16384B | run | 10000 | 1m25.684147s | 116.7 | 1.72ms | 30.501ms | 47.011ms | 1µs | 207.504ms |
| bbolt | per-write-sync | ycsb-b | 64B | load | 10000 | 2m33.526766s | 65.1 | 14.8ms | 26.598ms | 45.302ms | 12.931ms | 147.089ms |
| bbolt | per-write-sync | ycsb-b | 64B | run | 10000 | 7.691102s | 1300.2 | 2µs | 15.658ms | 23.727ms | 0s | 57.287ms |
| bbolt | per-write-sync | ycsb-b | 256B | load | 10000 | 2m38.748225s | 63.0 | 14.594ms | 44.622ms | 60.265ms | 12.61ms | 542.137ms |
| bbolt | per-write-sync | ycsb-b | 256B | run | 10000 | 7.661654s | 1305.2 | 2µs | 15.247ms | 21.847ms | 0s | 27.912ms |
| bbolt | per-write-sync | ycsb-b | 1024B | load | 10000 | 2m34.520623s | 64.7 | 14.491ms | 44.256ms | 51.717ms | 12.599ms | 129.009ms |
| bbolt | per-write-sync | ycsb-b | 1024B | run | 10000 | 8.288297s | 1206.5 | 2µs | 16.88ms | 24.438ms | 0s | 31.588ms |
| bbolt | per-write-sync | ycsb-b | 4096B | load | 10000 | 2m39.154515s | 62.8 | 15.045ms | 29.678ms | 45.632ms | 12.772ms | 249.633ms |
| bbolt | per-write-sync | ycsb-b | 4096B | run | 10000 | 8.417001s | 1188.1 | 2µs | 16.641ms | 30.362ms | 0s | 53.295ms |
| bbolt | per-write-sync | ycsb-b | 16384B | load | 10000 | 2m48.938753s | 59.2 | 16.005ms | 31.581ms | 56.862ms | 12.812ms | 168.825ms |
| bbolt | per-write-sync | ycsb-b | 16384B | run | 10000 | 8.856685s | 1129.1 | 2µs | 17.118ms | 27.585ms | 0s | 120.332ms |
| bbolt | per-write-sync | ycsb-c | 64B | load | 10000 | 2m40.199215s | 62.4 | 15.1ms | 29.791ms | 38.631ms | 13.329ms | 179.896ms |
| bbolt | per-write-sync | ycsb-c | 64B | run | 10000 | 11.127ms | 898698.6 | 1µs | 2µs | 9µs | 0s | 19µs |
| bbolt | per-write-sync | ycsb-c | 256B | load | 10000 | 2m38.026904s | 63.3 | 14.804ms | 43.571ms | 51.426ms | 12.707ms | 202.933ms |
| bbolt | per-write-sync | ycsb-c | 256B | run | 10000 | 132.656ms | 75383.2 | 1µs | 248µs | 350µs | 0s | 477µs |
| bbolt | per-write-sync | ycsb-c | 1024B | load | 10000 | 2m39.478548s | 62.7 | 14.815ms | 43.218ms | 59.668ms | 12.899ms | 368.695ms |
| bbolt | per-write-sync | ycsb-c | 1024B | run | 10000 | 10.418ms | 959856.1 | 1µs | 6µs | 12µs | 0s | 17µs |
| bbolt | per-write-sync | ycsb-c | 4096B | load | 10000 | 2m38.71612s | 63.0 | 15.215ms | 29.499ms | 47.507ms | 13.017ms | 115.931ms |
| bbolt | per-write-sync | ycsb-c | 4096B | run | 10000 | 8.901ms | 1123429.3 | 1µs | 5µs | 10µs | 0s | 17µs |
| bbolt | per-write-sync | ycsb-c | 16384B | load | 10000 | 2m42.173709s | 61.7 | 15.792ms | 27.97ms | 40.226ms | 12.705ms | 229.394ms |
| bbolt | per-write-sync | ycsb-c | 16384B | run | 10000 | 14.612ms | 684355.7 | 1µs | 11µs | 16µs | 0s | 35µs |
| badger | per-write-sync | ycsb-a | 64B | load | 10000 | 1m18.663971s | 127.1 | 7.518ms | 14.641ms | 18.719ms | 6.405ms | 139.332ms |
| badger | per-write-sync | ycsb-a | 64B | run | 10000 | 38.303522s | 261.1 | 50µs | 9.399ms | 16.287ms | 1µs | 77.124ms |
| badger | per-write-sync | ycsb-a | 256B | load | 10000 | 1m21.715608s | 122.4 | 7.494ms | 36.772ms | 40.611ms | 6.472ms | 246.107ms |
| badger | per-write-sync | ycsb-a | 256B | run | 10000 | 39.889831s | 250.7 | 62µs | 14.862ms | 37.984ms | 1µs | 105.142ms |
| badger | per-write-sync | ycsb-a | 1024B | load | 10000 | 1m20.973313s | 123.5 | 7.211ms | 36.153ms | 43.851ms | 5.991ms | 268.743ms |
| badger | per-write-sync | ycsb-a | 1024B | run | 10000 | 39.603052s | 252.5 | 86µs | 14.828ms | 37.991ms | 1µs | 45.304ms |
| badger | per-write-sync | ycsb-a | 4096B | load | 10000 | 1m19.426877s | 125.9 | 7.437ms | 15.506ms | 32.794ms | 6.123ms | 222.643ms |
| badger | per-write-sync | ycsb-a | 4096B | run | 10000 | 40.370536s | 247.7 | 62µs | 14.894ms | 37.497ms | 1µs | 217.257ms |
| badger | per-write-sync | ycsb-a | 16384B | load | 10000 | 1m20.924129s | 123.6 | 7.502ms | 15.719ms | 37.981ms | 6.465ms | 153.315ms |
| badger | per-write-sync | ycsb-a | 16384B | run | 10000 | 41.2972s | 242.1 | 6.516ms | 15.062ms | 37.921ms | 5µs | 230.393ms |
| badger | per-write-sync | ycsb-b | 64B | load | 10000 | 1m21.392249s | 122.9 | 7.512ms | 15.922ms | 38.214ms | 6.402ms | 115.265ms |
| badger | per-write-sync | ycsb-b | 64B | run | 10000 | 4.031143s | 2480.7 | 2µs | 7.788ms | 14.38ms | 0s | 15.51ms |
| badger | per-write-sync | ycsb-b | 256B | load | 10000 | 1m21.42816s | 122.8 | 7.453ms | 18.906ms | 38.465ms | 6.186ms | 175.41ms |
| badger | per-write-sync | ycsb-b | 256B | run | 10000 | 4.03345s | 2479.3 | 3µs | 7.765ms | 14.391ms | 0s | 37.766ms |
| badger | per-write-sync | ycsb-b | 1024B | load | 10000 | 1m19.922667s | 125.1 | 7.25ms | 27.578ms | 43.959ms | 6.399ms | 62.81ms |
| badger | per-write-sync | ycsb-b | 1024B | run | 10000 | 3.921182s | 2550.3 | 3µs | 7.7ms | 13.979ms | 0s | 22.538ms |
| badger | per-write-sync | ycsb-b | 4096B | load | 10000 | 1m17.988278s | 128.2 | 7.366ms | 15.02ms | 36.839ms | 6.495ms | 178.209ms |
| badger | per-write-sync | ycsb-b | 4096B | run | 10000 | 4.617255s | 2165.8 | 5µs | 8.074ms | 37.652ms | 1µs | 45.353ms |
| badger | per-write-sync | ycsb-b | 16384B | load | 10000 | 1m19.31806s | 126.1 | 7.394ms | 15.249ms | 38.46ms | 6.402ms | 717.902ms |
| badger | per-write-sync | ycsb-b | 16384B | run | 10000 | 3.986084s | 2508.7 | 22µs | 7.574ms | 8.947ms | 4µs | 19.958ms |
| badger | per-write-sync | ycsb-c | 64B | load | 10000 | 1m19.231421s | 126.2 | 7.436ms | 15.58ms | 38.094ms | 6.389ms | 116.46ms |
| badger | per-write-sync | ycsb-c | 64B | run | 10000 | 19.072ms | 524328.7 | 1µs | 9µs | 24µs | 0s | 49µs |
| badger | per-write-sync | ycsb-c | 256B | load | 10000 | 1m23.607555s | 119.6 | 7.51ms | 36.954ms | 44.752ms | 6.485ms | 246.452ms |
| badger | per-write-sync | ycsb-c | 256B | run | 10000 | 21.964ms | 455282.3 | 2µs | 7µs | 15µs | 0s | 31µs |
| badger | per-write-sync | ycsb-c | 1024B | load | 10000 | 1m21.244764s | 123.1 | 7.247ms | 37.197ms | 44.881ms | 6.303ms | 137.998ms |
| badger | per-write-sync | ycsb-c | 1024B | run | 10000 | 15.665ms | 638358.4 | 1µs | 7µs | 12µs | 0s | 41µs |
| badger | per-write-sync | ycsb-c | 4096B | load | 10000 | 1m17.695095s | 128.7 | 7.298ms | 15.104ms | 37.331ms | 6.296ms | 206.265ms |
| badger | per-write-sync | ycsb-c | 4096B | run | 10000 | 36.26ms | 275783.8 | 3µs | 13µs | 25µs | 1µs | 269µs |
| badger | per-write-sync | ycsb-c | 16384B | load | 10000 | 1m18.889086s | 126.8 | 7.399ms | 15.207ms | 36.906ms | 6.22ms | 409.322ms |
| badger | per-write-sync | ycsb-c | 16384B | run | 10000 | 200.767ms | 49809.1 | 20µs | 54µs | 154µs | 5µs | 275µs |
| rosedb | per-write-sync | ycsb-a | 64B | load | 10000 | 1m18.224321s | 127.8 | 7.222ms | 16.448ms | 37.991ms | 6.286ms | 110.728ms |
| rosedb | per-write-sync | ycsb-a | 64B | run | 10000 | 39.66389s | 252.1 | 75µs | 14.348ms | 37.741ms | 1µs | 52.049ms |
| rosedb | per-write-sync | ycsb-a | 256B | load | 10000 | 1m20.797455s | 123.8 | 7.319ms | 17.296ms | 38.053ms | 6.392ms | 239.361ms |
| rosedb | per-write-sync | ycsb-a | 256B | run | 10000 | 38.91162s | 257.0 | 88µs | 14.242ms | 37.826ms | 1µs | 45.082ms |
| rosedb | per-write-sync | ycsb-a | 1024B | load | 10000 | 1m17.30193s | 129.4 | 7.217ms | 14.914ms | 29.999ms | 6.096ms | 106.288ms |
| rosedb | per-write-sync | ycsb-a | 1024B | run | 10000 | 40.435151s | 247.3 | 109µs | 14.985ms | 36.178ms | 1µs | 137.992ms |
| rosedb | per-write-sync | ycsb-a | 4096B | load | 10000 | 1m18.498133s | 127.4 | 7.393ms | 15.197ms | 23.01ms | 6.483ms | 101.697ms |
| rosedb | per-write-sync | ycsb-a | 4096B | run | 10000 | 38.935213s | 256.8 | 94µs | 10.058ms | 21.111ms | 4µs | 104.702ms |
| rosedb | per-write-sync | ycsb-a | 16384B | load | 10000 | 1m18.549309s | 127.3 | 7.398ms | 15.13ms | 38.322ms | 6.391ms | 45.073ms |
| rosedb | per-write-sync | ycsb-a | 16384B | run | 10000 | 39.502847s | 253.1 | 139µs | 13.971ms | 36.706ms | 6µs | 103.528ms |
| rosedb | per-write-sync | ycsb-b | 64B | load | 10000 | 1m16.615443s | 130.5 | 7.304ms | 14.635ms | 18.315ms | 6.296ms | 85.279ms |
| rosedb | per-write-sync | ycsb-b | 64B | run | 10000 | 3.847312s | 2599.2 | 6µs | 7.684ms | 9.093ms | 1µs | 18.151ms |
| rosedb | per-write-sync | ycsb-b | 256B | load | 10000 | 1m16.349102s | 131.0 | 7.302ms | 14.561ms | 18.609ms | 6.479ms | 126.511ms |
| rosedb | per-write-sync | ycsb-b | 256B | run | 10000 | 4.069402s | 2457.4 | 6µs | 8.037ms | 14.46ms | 1µs | 15.362ms |
| rosedb | per-write-sync | ycsb-b | 1024B | load | 10000 | 1m21.606151s | 122.5 | 7.395ms | 34.967ms | 38.989ms | 6.524ms | 112.349ms |
| rosedb | per-write-sync | ycsb-b | 1024B | run | 10000 | 3.701621s | 2701.5 | 8µs | 7.404ms | 8.612ms | 1µs | 16.301ms |
| rosedb | per-write-sync | ycsb-b | 4096B | load | 10000 | 1m17.489667s | 129.0 | 7.281ms | 15.285ms | 37.77ms | 6.398ms | 208.858ms |
| rosedb | per-write-sync | ycsb-b | 4096B | run | 10000 | 3.84633s | 2599.9 | 10µs | 8.108ms | 8.865ms | 3µs | 16.8ms |
| rosedb | per-write-sync | ycsb-b | 16384B | load | 10000 | 1m18.465407s | 127.4 | 7.439ms | 14.973ms | 19.625ms | 6.406ms | 82.415ms |
| rosedb | per-write-sync | ycsb-b | 16384B | run | 10000 | 4.424046s | 2260.4 | 36µs | 8.577ms | 14.303ms | 10µs | 19.924ms |
| rosedb | per-write-sync | ycsb-c | 64B | load | 10000 | 1m17.945223s | 128.3 | 7.318ms | 14.979ms | 19.212ms | 6.406ms | 84.438ms |
| rosedb | per-write-sync | ycsb-c | 64B | run | 10000 | 21.895ms | 456729.7 | 2µs | 7µs | 18µs | 1µs | 45µs |
| rosedb | per-write-sync | ycsb-c | 256B | load | 10000 | 1m18.850226s | 126.8 | 7.335ms | 15.671ms | 38.022ms | 6.292ms | 54.011ms |
| rosedb | per-write-sync | ycsb-c | 256B | run | 10000 | 15.354ms | 651298.2 | 1µs | 8µs | 16µs | 1µs | 41µs |
| rosedb | per-write-sync | ycsb-c | 1024B | load | 10000 | 1m20.137059s | 124.8 | 7.216ms | 33.555ms | 39.095ms | 6.403ms | 183.39ms |
| rosedb | per-write-sync | ycsb-c | 1024B | run | 10000 | 34.019ms | 293953.8 | 2µs | 16µs | 25µs | 1µs | 59µs |
| rosedb | per-write-sync | ycsb-c | 4096B | load | 10000 | 1m17.253583s | 129.4 | 7.14ms | 15.993ms | 37.776ms | 6.294ms | 120.974ms |
| rosedb | per-write-sync | ycsb-c | 4096B | run | 10000 | 53.938ms | 185397.0 | 4µs | 15µs | 25µs | 2µs | 253µs |
| rosedb | per-write-sync | ycsb-c | 16384B | load | 10000 | 1m18.923882s | 126.7 | 7.316ms | 15.484ms | 28.672ms | 6.312ms | 57.644ms |
| rosedb | per-write-sync | ycsb-c | 16384B | run | 10000 | 119.351ms | 83786.7 | 10µs | 32µs | 62µs | 4µs | 136µs |
