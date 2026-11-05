package main

import (
	"os"

	"github.com/magician336/elena/bench/recovery"
)

// main is wiring only, with one contract ahead of the wiring: Measure
// reexeces this very binary as the rig's writer child, so the
// writer-child environment switch comes before anything else. After
// that, arguments and the process writers flow into Run, whose exit
// code becomes the process's.
func main() {
	if recovery.IsWriterChild() {
		os.Exit(recovery.RunWriterChild(os.Stdin, os.Stdout, os.Stderr))
	}
	os.Exit(Run(os.Args[1:], os.Stdout, os.Stderr, recovery.Measure))
}
