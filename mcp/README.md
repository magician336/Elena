# elena-mcp

A thin MCP (Model Context Protocol) interface over the elena library:
one store directory served as six tools over stdio, each a thin
orchestration of the same public API the CLI and the examples use. The
server is an interface, not a second product — every semantic below is
the library's own, and this module adds no storage logic. It is its own
Go module (the same isolation pattern as `bench/`), so the MCP SDK
dependency stays off the library's `go.mod`.

Agent runtimes that speak MCP connect the process over stdio and work
in the store's own terms: append events under sessions, read
trajectories back to any sequence number, checkpoint the whole store,
restore a baseline.

## Tools

| tool | arguments | what it returns |
| --- | --- | --- |
| `create_session` | `namespace`, `session`, optional `ttl_seconds` | the session's identity and its recorded deadline — absent for a perpetual session |
| `append_event` | `namespace`, `session`, `payload` | the appended event's sequence number, starting at 1; an absent session starts as a perpetual one |
| `read_trajectory` | `namespace`, `session`, optional `upto_seq` | the trajectory's events in sequence order; with `upto_seq` it is a point-in-time read — every event at or below that sequence number, endpoint included |
| `create_checkpoint` | — | the recorded checkpoint's ID |
| `restore_checkpoint` | `checkpoint_id` | the baseline the runtime view reset to: one line per trajectory with its recorded sequence number |
| `stats` | — | the store's observables — entry counts, byte counters, dead ratio, write amplification, the per-file view — forwarded unchanged from the engine |

Input schemas are derived from the argument structs by the SDK's
schema reflection and validated before each handler runs. A timed
session whose deadline has elapsed reads empty and its name is free to
start a fresh session — the library's own lifecycle, unchanged.

## Connecting an agent runtime

The process speaks MCP over stdio: frames on stdin and stdout, errors
and logs on stderr. The store directory comes from `-dir` or
`ELENA_MCP_DIR` and is created if absent.

```sh
cd mcp && go build -o elena-mcp ./cmd/elena-mcp
./elena-mcp -dir /tmp/elena-store
```

An agent runtime registers it under `mcpServers` — the shape Claude
Desktop, Cursor, and other MCP clients take:

```json
{
  "mcpServers": {
    "elena": {
      "command": "/absolute/path/to/elena-mcp",
      "args": ["-dir", "/absolute/path/to/store"]
    }
  }
}
```

One store directory holds one exclusive lock: one server process per
directory, and a CLI command inspecting the same directory needs the
server stopped first.

## Gates

The module carries the same gates as the rest of the repository, run
locally exactly as CI runs them:

```sh
make mcp-test   # race suite, the stdio end-to-end pass included
make mcp-cov    # coverage gate >= 80%
```

The end-to-end pass drives every tool over the byte transport a stdio
server uses — newline-delimited JSON frames between the server and an
MCP client — before it checks shutdown behavior.
