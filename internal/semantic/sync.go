package semantic

import (
	"errors"

	"github.com/magician336/elena/internal/engine"
)

// SyncPolicy is the engine's durability tier, re-exported so the root
// facade can configure it without importing the engine directly: the
// semantic layer passes the choice through unchanged, so the tiers and
// their semantics have exactly one definition, the engine's (see the
// engine.SyncPolicy doc comment for the acknowledgment and loss-window
// semantics of each tier).
type SyncPolicy = engine.SyncPolicy

// The sync tiers the engine defines; SyncAlways is the zero value and
// therefore the default of Config.
const (
	// SyncAlways fdatasyncs every write before it acknowledges: an
	// acknowledged event is durable against any fault.
	SyncAlways = engine.SyncAlways

	// SyncInterval is group commit: a background flusher fdatasyncs the
	// active data file once per Config.SyncPeriod.
	SyncInterval = engine.SyncInterval

	// SyncNone never fdatasyncs the data files: the loss window is the
	// OS page cache.
	SyncNone = engine.SyncNone
)

// ErrInvalidSyncPeriod is returned by Open when the SyncInterval policy
// is configured with a period that cannot define a flush interval:
// zero or negative. The other tiers take no period.
var ErrInvalidSyncPeriod = errors.New("elena: sync period must be positive")
