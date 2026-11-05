package bench

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/magician336/elena"
)

// The two open modes the startup scenario times. The engine writes one
// hint file next to each data file once the file's contents stop
// changing, and Open consults those hints to build its index without
// scanning the data files; with no hint, Open rebuilds the same index
// by scanning each data file in full. The scenario constructs the
// no-hint condition by deleting the hint files from the dataset
// directory after Close — environment preparation on the store's own
// on-disk layout, not an API behavior. The bench module cannot import
// the engine's internal packages, so the layout fact travels as this
// documented knowledge: hint files carry the ".hint" extension beside
// their data files. The scenario guards the assumption at runtime — a
// Close that leaves no hint files behind fails the run instead of
// silently timing two hint hits.
const (
	// StartupModeHint opens over a directory whose hint files are in
	// place, the accelerated mode.
	StartupModeHint = "hint"

	// StartupModeNoHint opens the same directory with its hint files
	// removed: index rebuild by full data-file scan.
	StartupModeNoHint = "no-hint"
)

// hintFileExt is the extension of hint files in a store directory, the
// one layout fact this module needs for its environment preparation.
const hintFileExt = ".hint"

// StartupPairConfig declares the dataset both open modes share:
// EventsPerSession events of ValueSize bytes appended to each of
// Sessions trajectories, then Close. All fields must be positive.
type StartupPairConfig struct {
	Sessions         int
	EventsPerSession int
	ValueSize        int
}

// StartupTiming is one open mode's numbers: the timed Open of the
// shared dataset, the timed first verified read after it, and the
// dataset facts the pair is only comparable against.
type StartupTiming struct {
	// Mode is one of the startup mode constants.
	Mode string `json:"mode"`

	// OpenNS is the wall time of elena.Open over the dataset — the
	// index build included.
	OpenNS int64 `json:"open_ns"`

	// FirstReadNS is the wall time of the first read after Open: one
	// session handle plus one single-event read, verified against what
	// the seeding wrote.
	FirstReadNS int64 `json:"first_read_ns"`

	// LiveEntries and DataBytes are the Stats facts of the dataset each
	// mode opened; the pair is comparable because these are equal.
	LiveEntries uint64 `json:"live_entries"`
	DataBytes   uint64 `json:"data_bytes"`
}

// StartupPairResult is the paired evidence: the same dataset opened
// once in each mode, two numbers one comparison wide.
type StartupPairResult struct {
	Hint   StartupTiming `json:"hint"`
	NoHint StartupTiming `json:"no_hint"`
}

// RunStartupPair seeds the declared dataset into a fresh store under
// dir — which must not exist; the store creates it — closes it, then
// times two opens of the same directory: once with the hint files
// Close wrote, once with those files removed. Both opens verify the
// same known event before closing, so a mode that cannot serve the
// data it just indexed fails the run rather than posting a time.
func RunStartupPair(dir string, cfg StartupPairConfig) (StartupPairResult, error) {
	if cfg.Sessions <= 0 {
		return StartupPairResult{}, fmt.Errorf("startup scenario: sessions %d must be positive", cfg.Sessions)
	}
	if cfg.EventsPerSession <= 0 {
		return StartupPairResult{}, fmt.Errorf("startup scenario: events per session %d must be positive", cfg.EventsPerSession)
	}
	if cfg.ValueSize <= 0 {
		return StartupPairResult{}, fmt.Errorf("startup scenario: value size %d must be positive", cfg.ValueSize)
	}
	if err := os.Mkdir(dir, 0o755); err != nil {
		return StartupPairResult{}, fmt.Errorf("startup scenario: create %s: %w", dir, err)
	}
	if err := seedAndClose(dir, cfg); err != nil {
		return StartupPairResult{}, err
	}

	hints, err := hintFiles(dir)
	if err != nil {
		return StartupPairResult{}, err
	}
	if len(hints) == 0 {
		return StartupPairResult{}, fmt.Errorf("startup scenario: no %s files after Close under %s: the on-disk hint layout changed, the no-hint mode would not force a scan", hintFileExt, dir)
	}

	hintTiming, err := timeStartupOpen(dir, StartupModeHint, cfg)
	if err != nil {
		return StartupPairResult{}, err
	}
	removed, err := removeHintFiles(dir)
	if err != nil {
		return StartupPairResult{}, fmt.Errorf("startup scenario: prepare no-hint mode: %w", err)
	}
	if removed == 0 {
		return StartupPairResult{}, fmt.Errorf("startup scenario: no %s files found to remove under %s", hintFileExt, dir)
	}
	noHintTiming, err := timeStartupOpen(dir, StartupModeNoHint, cfg)
	if err != nil {
		return StartupPairResult{}, err
	}
	return StartupPairResult{Hint: hintTiming, NoHint: noHintTiming}, nil
}

// seedAndClose seeds the dataset and closes the store, leaving the
// directory in the state Close leaves it in — hint files included.
func seedAndClose(dir string, cfg StartupPairConfig) error {
	store, err := elena.Open(dir, elena.WithSweepDisabled())
	if err != nil {
		return err
	}
	runErr := seedStore(store, cfg.Sessions, cfg.EventsPerSession, cfg.ValueSize)
	closeErr := store.Close()
	if runErr != nil {
		return runErr
	}
	return closeErr
}

// timeStartupOpen opens the dataset once in the named mode, times the
// Open and the first verified read, records the dataset facts, and
// closes.
func timeStartupOpen(dir, mode string, cfg StartupPairConfig) (StartupTiming, error) {
	openStart := time.Now()
	store, err := elena.Open(dir, elena.WithSweepDisabled())
	if err != nil {
		return StartupTiming{}, err
	}
	openNS := time.Since(openStart)

	timing, readErr := verifyFirstRead(store, cfg, StartupTiming{Mode: mode, OpenNS: int64(openNS)})
	closeErr := store.Close()
	if readErr != nil {
		return StartupTiming{}, readErr
	}
	if closeErr != nil {
		return StartupTiming{}, closeErr
	}
	return timing, nil
}

// verifyFirstRead performs and times the first read after Open: one
// session handle plus the single-event read of the first trajectory's
// last event, checked against what the seeding wrote — the store is
// usable, not merely open.
func verifyFirstRead(store *elena.Store, cfg StartupPairConfig, timing StartupTiming) (StartupTiming, error) {
	readStart := time.Now()
	sess, err := store.CreateSession(elenaNamespace, elenaSessionName(0))
	if err != nil {
		return StartupTiming{}, err
	}
	event, err := sess.Event(uint64(cfg.EventsPerSession))
	if err != nil {
		return StartupTiming{}, fmt.Errorf("startup scenario %s: first read: %w", timing.Mode, err)
	}
	timing.FirstReadNS = int64(time.Since(readStart))
	if event.Seq != uint64(cfg.EventsPerSession) || len(event.Payload) != cfg.ValueSize {
		return StartupTiming{}, fmt.Errorf("startup scenario %s: first read returned seq %d payload %d bytes, want seq %d payload %d bytes",
			timing.Mode, event.Seq, len(event.Payload), cfg.EventsPerSession, cfg.ValueSize)
	}
	stats := store.Stats()
	timing.LiveEntries = stats.LiveEntries
	timing.DataBytes = stats.DataBytes
	return timing, nil
}

// hintFiles lists the hint file names under dir.
func hintFiles(dir string) ([]string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, fmt.Errorf("startup scenario: list %s: %w", dir, err)
	}
	var names []string
	for _, entry := range entries {
		if strings.HasSuffix(entry.Name(), hintFileExt) {
			names = append(names, entry.Name())
		}
	}
	return names, nil
}

// removeHintFiles deletes every hint file under dir and returns how
// many it removed — the no-hint mode's environment preparation.
func removeHintFiles(dir string) (int, error) {
	names, err := hintFiles(dir)
	if err != nil {
		return 0, err
	}
	for _, name := range names {
		if err := os.Remove(filepath.Join(dir, name)); err != nil {
			return 0, fmt.Errorf("startup scenario: remove %s: %w", name, err)
		}
	}
	return len(names), nil
}
