//go:build unix

package engine_test

import (
	"fmt"
	"os"
	"strconv"
	"testing"

	"elena/internal/engine"
)

// TestMain is the reexec entry of the fault-injection harness: when
// the writer-child environment variable is set, this process is the
// child writer and never runs any test — it writes, acks on stdout,
// and exits (or gets killed). See faultinject_test.go for why the
// harness kills a real process.
func TestMain(m *testing.M) {
	if os.Getenv(writerChildModeEnv) != "" {
		os.Exit(runWriterChild())
	}
	os.Exit(m.Run())
}

// runWriterChild is the writer-child main: open the storage directory
// from the environment, put every dataset entry through the public
// engine API, and print one ack line to stdout per Put that has
// already returned. The ack is strictly line-buffered and unordered
// with nothing: when the parent reads the line, the Put is durable per
// the engine's always-sync policy. All failures go to stderr with a
// non-zero exit so the parent reports the child stderr verbatim.
func runWriterChild() int {
	fail := func(err error) int {
		_, _ = fmt.Fprintln(os.Stderr, "writer child:", err)
		return 1
	}

	spec, err := writerSpecFromEnv()
	if err != nil {
		return fail(err)
	}

	eng, err := engine.Open(spec.Dir, engine.Options{})
	if err != nil {
		return fail(fmt.Errorf("open %q: %w", spec.Dir, err))
	}
	for i, w := range WriterDataset(spec) {
		if err := eng.Put([]byte(w.Key), w.Value); err != nil {
			return fail(fmt.Errorf("put %q: %w", w.Key, err))
		}
		// Ack only after Put has returned.
		if _, err := fmt.Fprintln(os.Stdout, w.Key); err != nil {
			return fail(fmt.Errorf("ack %q: %w", w.Key, err))
		}
		if spec.RotateEvery > 0 && (i+1)%spec.RotateEvery == 0 && i+1 < spec.Writes {
			if err := eng.Close(); err != nil {
				return fail(fmt.Errorf("close for rotation: %w", err))
			}
			if err := duplicateNewestDataFile(spec.Dir); err != nil {
				return fail(fmt.Errorf("rotate: %w", err))
			}
			eng, err = engine.Open(spec.Dir, engine.Options{})
			if err != nil {
				return fail(fmt.Errorf("reopen after rotation: %w", err))
			}
		}
	}
	if err := eng.Close(); err != nil {
		return fail(fmt.Errorf("close: %w", err))
	}
	return 0
}

// writerSpecFromEnv decodes the WriterSpec the parent encoded into the
// child's environment.
func writerSpecFromEnv() (WriterSpec, error) {
	spec := WriterSpec{Dir: os.Getenv(writerDirEnv)}
	var err error
	if spec.Writes, err = strconv.Atoi(os.Getenv(writerWritesEnv)); err != nil {
		return spec, fmt.Errorf("bad %s: %w", writerWritesEnv, err)
	}
	seed, err := strconv.ParseInt(os.Getenv(writerSeedEnv), 10, 64)
	if err != nil {
		return spec, fmt.Errorf("bad %s: %w", writerSeedEnv, err)
	}
	spec.Seed = seed
	if spec.ValueSize, err = strconv.Atoi(os.Getenv(writerValueSizeEnv)); err != nil {
		return spec, fmt.Errorf("bad %s: %w", writerValueSizeEnv, err)
	}
	if spec.RotateEvery, err = strconv.Atoi(os.Getenv(writerRotateEveryEnv)); err != nil {
		return spec, fmt.Errorf("bad %s: %w", writerRotateEveryEnv, err)
	}
	if spec.Dir == "" || spec.Writes <= 0 || spec.ValueSize <= 0 {
		return spec, fmt.Errorf("incomplete writer spec: %+v", spec)
	}
	return spec, nil
}
