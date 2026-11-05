// Package mcp is a thin MCP (Model Context Protocol) interface over the
// elena library: one store directory, six tools, each a thin
// orchestration of the library's public API. The server speaks stdio;
// agent runtimes connect it as an MCP server process.
package mcp

import (
	"context"
	"fmt"

	"github.com/magician336/elena"
	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

// ServerName is the MCP server implementation name the SDK advertises
// in its initialize handshake.
const ServerName = "elena-mcp"

// ServerVersion is the advertised server version.
const ServerVersion = "0.1.0"

// StoreOpener opens the elena store the server serves. It is injected
// so tests — and any other host — decide where the store lives; the
// server holds the returned store for its whole lifetime.
type StoreOpener func() (*elena.Store, error)

// Server exposes one elena store as MCP tools. Every tool is a thin
// orchestration of the store's public API; the server holds no storage
// logic of its own.
type Server struct {
	store *elena.Store
	sdk   *sdk.Server
}

// New opens the store via opener and builds the server with all tools
// registered.
func New(opener StoreOpener) (*Server, error) {
	if opener == nil {
		return nil, fmt.Errorf("mcp: nil store opener")
	}
	store, err := opener()
	if err != nil {
		return nil, fmt.Errorf("mcp: opening store: %w", err)
	}
	s := &Server{store: store}
	s.sdk = sdk.NewServer(&sdk.Implementation{Name: ServerName, Version: ServerVersion}, nil)
	s.registerTools()
	return s, nil
}

// SDK returns the underlying SDK server, for hosts and tests that
// connect their own transport.
func (s *Server) SDK() *sdk.Server {
	return s.sdk
}

// Serve runs the server over t until ctx is done. A stdio server is
// Serve(ctx, &sdk.StdioTransport{}); the transport is a parameter so
// the byte path is injectable in tests.
func (s *Server) Serve(ctx context.Context, t sdk.Transport) error {
	return s.sdk.Run(ctx, t)
}

// Close closes the store and releases its directory lock.
func (s *Server) Close() error {
	return s.store.Close()
}
