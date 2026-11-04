// Command focused is the focused-metrics runner: it drives the four
// single-figure scenarios — latency across one explicit merge, the
// paired startup timings with and without hint files, per-entry index
// memory, and the write-amplification summary — and writes the raw
// numbers behind each claim.
package main

import "os"

func main() {
	os.Exit(Run(os.Args[1:], os.Stdout, os.Stderr))
}
