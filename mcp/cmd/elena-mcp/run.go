package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"

	"github.com/magician336/elena"
	mcp "github.com/magician336/elena/mcp"
	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

// usageText is the process's help surface: one line per decision a
// host has to make — where the store lives and how the server speaks.
const usageText = `usage: elena-mcp [-dir <store-directory>]

serves one elena store over the MCP stdio interface: sessions,
trajectories with point-in-time reads, checkpoints, and stats

store:   -dir flag or ELENA_MCP_DIR; the directory is created if absent
stdio:   MCP frames on stdin and stdout; logs and errors on stderr
`

// closableWriter adapts an io.Writer to the io.WriteCloser the SDK's
// IOTransport takes; closing the process's writers belongs to the
// process, not the transport.
type closableWriter struct{ io.Writer }

// Close is a no-op: the adapted writer stays open.
func (closableWriter) Close() error { return nil }

// Run serves the store until ctx is done and returns the process exit
// code: 0 on a clean end, 2 on a usage problem, 1 on a store or server
// failure.
func Run(ctx context.Context, args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("elena-mcp", flag.ContinueOnError)
	flags.SetOutput(stderr)
	dir := flags.String("dir", "", "store directory served over MCP, created if absent (env ELENA_MCP_DIR)")
	if err := flags.Parse(args); err != nil {
		return 2
	}
	if flags.NArg() > 0 {
		_, _ = fmt.Fprintf(stderr, "elena-mcp: unexpected argument %q\n\n%s", flags.Arg(0), usageText)
		return 2
	}
	storeDir := *dir
	if storeDir == "" {
		storeDir = os.Getenv("ELENA_MCP_DIR")
	}
	if storeDir == "" {
		_, _ = fmt.Fprintf(stderr, "elena-mcp: no store directory: pass -dir or set ELENA_MCP_DIR\n\n%s", usageText)
		return 2
	}

	srv, err := mcp.New(func() (*elena.Store, error) { return elena.Open(storeDir) })
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "elena-mcp: %v\n", err)
		return 1
	}
	transport := &sdk.IOTransport{
		Reader: io.NopCloser(stdin),
		Writer: closableWriter{stdout},
	}
	if err := srv.Serve(ctx, transport); err != nil {
		_ = srv.Close()
		_, _ = fmt.Fprintf(stderr, "elena-mcp: %v\n", err)
		return 1
	}
	if err := srv.Close(); err != nil {
		_, _ = fmt.Fprintf(stderr, "elena-mcp: %v\n", err)
		return 1
	}
	return 0
}
