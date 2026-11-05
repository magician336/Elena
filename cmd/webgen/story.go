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
	Namespace   string           `json:"namespace"`
	Session     string           `json:"session"`
	CreatedAt   string           `json:"created_at"`
	Events      []StoryEvent     `json:"events"`
	PointInTime PointInTimeView  `json:"point_in_time"`
}
