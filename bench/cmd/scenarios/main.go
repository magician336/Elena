// Command scenarios is the scenario-load runner: it drives elena's
// three scenario workloads — pure append, append plus point-in-time
// reads, append plus TTL expiry — plus the append throughput-
// concurrency curve, through the public library API, and writes the
// raw numbers. Scenario loads are elena's own workload language:
// trajectories, sessions, and events, driven the way a host
// application drives them.
package main

import "os"

func main() {
	os.Exit(Run(os.Args[1:], os.Stdout, os.Stderr))
}
