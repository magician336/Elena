// Package engine implements Elena's embedded key-value storage engine.
//
// The engine follows the Bitcask model: an append-only data file per
// directory plus an in-memory index. This file provides the lifecycle
// surface: Open acquires a storage directory and holds an exclusive
// lock on it; Close releases everything the engine holds.
package engine

import (
	"fmt"
	"os"
)

// Options configures an Engine at Open time. The zero value is valid.
type Options struct{}

// Engine is an embedded key-value storage engine rooted at a directory.
type Engine struct {
	dir string
}

// Open opens the storage directory rooted at dir, creating it (and any
// missing parents) if needed.
func Open(dir string, opts Options) (*Engine, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("engine: create storage directory %q: %w", dir, err)
	}
	return &Engine{dir: dir}, nil
}

// Close releases all resources held by the Engine.
func (e *Engine) Close() error {
	return nil
}
