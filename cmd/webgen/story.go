package main

// The generated page data's shape. Every field is a value type or a
// slice — no maps — so encoding order is the field order and the
// rendered bytes are a pure function of the values.

// StoryEvent is one event as the page shows it: the sequence number
// the append handed out, the injected clock's reading at that append,
// and the payload.
type StoryEvent struct {
	Seq     uint64 `json:"seq"`
	At      string `json:"at"`
	Payload string `json:"payload"`
}

// PointInTimeView is one point-in-time read as the page shows it: the
// sequence bound, the clock reading, and how many events the read
// returned.
type PointInTimeView struct {
	Seq      uint64 `json:"seq"`
	At       string `json:"at"`
	Returned int    `json:"returned"`
}

// TrajectoryStory narrates the first page act: a perpetual session
// created in a namespace, six seeded events appended, and one
// point-in-time read to a known consistent sequence number.
type TrajectoryStory struct {
	Namespace   string          `json:"namespace"`
	Session     string          `json:"session"`
	CreatedAt   string          `json:"created_at"`
	Events      []StoryEvent    `json:"events"`
	PointInTime PointInTimeView `json:"point_in_time"`
}

// BatchView is the atomic commit as the page shows it: the group's
// events, the checkpoint id it committed alongside them, and one
// clock reading for the whole unit.
type BatchView struct {
	Events       []StoryEvent `json:"events"`
	CheckpointID uint64       `json:"checkpoint_id"`
	CommittedAt  string       `json:"committed_at"`
}

// InterruptView records the close that stands in for the interruption
// in the story's second act.
type InterruptView struct {
	ClosedAt string `json:"closed_at"`
}

// CheckpointView is one listed checkpoint: its id and the injected
// clock's reading at the commit that created it.
type CheckpointView struct {
	ID        uint64 `json:"id"`
	CreatedAt string `json:"created_at"`
}

// ReopenView narrates the reopen after the interruption: one clock
// reading and every checkpoint the reopened store lists.
type ReopenView struct {
	ReopenedAt  string           `json:"reopened_at"`
	Checkpoints []CheckpointView `json:"checkpoints"`
}

// RestoreView narrates the restore: which checkpoint, and the baseline
// sequence number the runtime rolled its counters back to.
type RestoreView struct {
	RestoredAt   string `json:"restored_at"`
	CheckpointID uint64 `json:"checkpoint_id"`
	BaselineSeq  uint64 `json:"baseline_seq"`
}

// ResumeView narrates the resumed append: its sequence number is the
// baseline's next one, overwriting whatever that number held on disk
// before the restore — last write wins.
type ResumeView struct {
	Event         StoryEvent `json:"event"`
	OverwritesSeq uint64     `json:"overwrites_seq"`
}

// FullReadView is the full trajectory read after the resume.
type FullReadView struct {
	At    string `json:"at"`
	Count int    `json:"count"`
}

// AfterView is the act's closing contrast: the point-in-time read
// stops at the baseline; the full read still sees everything on disk.
type AfterView struct {
	PointInTime PointInTimeView `json:"point_in_time"`
	FullRead    FullReadView    `json:"full_read"`
}

// CheckpointStory narrates the second page act: appends, an atomic
// event-group-plus-checkpoint batch, more appends, a close standing
// in for the interruption, a reopen, a restore to the baseline, and a
// resumed append.
type CheckpointStory struct {
	Namespace    string         `json:"namespace"`
	Session      string         `json:"session"`
	CreatedAt    string         `json:"created_at"`
	PreBaseline  []StoryEvent   `json:"pre_baseline"`
	Batch        BatchView      `json:"batch"`
	PostBaseline []StoryEvent   `json:"post_baseline"`
	Interruption InterruptView  `json:"interruption"`
	Reopen       ReopenView     `json:"reopen"`
	Restore      RestoreView    `json:"restore"`
	Resume       ResumeView     `json:"resume"`
	After        AfterView      `json:"after"`
}
