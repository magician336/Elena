// webgen generates the demo page's data file: two library-API stories
// (a trajectory timeline and a checkpoint recovery) run against fresh
// temporary store directories with a fixed seed and an injected clock,
// plus the benchmark figures distilled from the checked-in raw JSON —
// rendered as one script-includable const declaration for web/.
//
// Run from the repository root:
//
//	go run ./cmd/webgen [-seed 1] [-data docs/benchmarks/data] [-out web/data.js]
package main

import (
	"flag"
	"fmt"
	"os"
)

// main wires the flags to run; failures exit non-zero.
func main() {
	seed := flag.Uint64("seed", 1, "seed driving the story payloads")
	dataDir := flag.String("data", "docs/benchmarks/data", "directory holding the checked-in raw benchmark JSON documents")
	out := flag.String("out", "web/data.js", "output path of the generated page data file")
	flag.Parse()
	if err := run(*out, *dataDir, *seed); err != nil {
		_, _ = fmt.Fprintln(os.Stderr, "webgen failed:", err)
		os.Exit(1)
	}
}

// run generates the page data and writes it to outPath.
func run(outPath, dataDir string, seed uint64) error {
	out, err := Generate(GenerateInput{Seed: seed, DataDir: dataDir})
	if err != nil {
		return err
	}
	if err := os.WriteFile(outPath, out, 0o644); err != nil {
		return fmt.Errorf("write %s: %w", outPath, err)
	}
	return nil
}
