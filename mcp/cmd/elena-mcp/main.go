// Command elena-mcp serves one elena store directory over the MCP
// stdio interface: six tools, each a thin orchestration of the elena
// library's public API. The directory comes from -dir or
// ELENA_MCP_DIR; agent runtimes start the process and speak MCP over
// its stdin and stdout.
package main

import (
	"context"
	"os"
	"os/signal"
	"syscall"
)

// main is wiring only: the process streams flow into Run, and its exit
// code becomes the process's.
func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	os.Exit(Run(ctx, os.Args[1:], os.Stdin, os.Stdout, os.Stderr))
}
