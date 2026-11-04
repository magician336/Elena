package bench

import "fmt"

// For returns a fresh driver by its report name — the one place new
// engines join the harness: add the driver, add its case, and every
// entry point (CLI, tests, later matrix runners) reaches it by name.
// An unknown name is an error, not a default.
func For(name string) (Driver, error) {
	switch name {
	case "elena":
		return &elenaDriver{}, nil
	case "bbolt":
		return &bboltDriver{}, nil
	default:
		return nil, fmt.Errorf("unknown driver %q", name)
	}
}

// Driver is one engine the harness can drive, behind a single
// lifecycle: open a fresh store under one directory, execute single
// operations, close. Drivers map the harness's logical operations onto
// their engine's own addressing; the elena driver goes through the
// public elena library only, competitor drivers through their official
// client packages.
type Driver interface {
	// Name identifies the driver in reports.
	Name() string

	// Open prepares a fresh store under dir, which exists.
	Open(dir string) error

	// Do executes one operation synchronously; returning marks the
	// operation complete — for writes, acknowledged as durable under
	// the driver's declared sync regime.
	Do(op Op) error

	// Close releases the store and its resources.
	Close() error
}
