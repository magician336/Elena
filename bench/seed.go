package bench

import (
	"fmt"

	"elena"
)

// seedStore seeds the focused-metric scenarios' shared dataset shape:
// one trajectory per logical key (the elena driver's own mapping —
// namespace "bench", session "traj-<key>"), eventsPerSession events of
// valueSize bytes each, values derived deterministically from key and
// event ordinal so every scenario regenerates the identical dataset.
func seedStore(store *elena.Store, sessions, eventsPerSession, valueSize int) error {
	for key := 0; key < sessions; key++ {
		sess, err := store.CreateSession(elenaNamespace, elenaSessionName(uint64(key)))
		if err != nil {
			return err
		}
		for event := 0; event < eventsPerSession; event++ {
			if _, err := sess.Append(genValue(uint64(key), event, valueSize)); err != nil {
				return fmt.Errorf("seed key %d event %d: %w", key, event, err)
			}
		}
	}
	return nil
}
