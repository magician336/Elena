package main

import "os"

// main is wiring only: arguments and the process writers flow into
// Run, and its exit code becomes the process's.
func main() {
	os.Exit(Run(os.Args[1:], os.Stdout, os.Stderr))
}
