package main

import (
	"encoding/json"
	"fmt"
	"os"
	"time"

	"github.com/magician336/elena"
)

// Meta records how the page data was produced, so anyone can
// regenerate it: the seed, the injected clock's constants, and the
// generator's identity.
type Meta struct {
	Seed       int    `json:"seed"`
	ClockStart string `json:"clock_start"`
	ClockStep  string `json:"clock_step"`
	Generator  string `json:"generator"`
}

// DemoData is the page's whole data file: the two library-API stories,
// the distilled benchmark figures, and the provenance meta.
type DemoData struct {
	Meta       Meta             `json:"meta"`
	Trajectory TrajectoryStory  `json:"trajectory"`
	Checkpoint CheckpointStory  `json:"checkpoint"`
	Benchmarks Benchmarks       `json:"benchmarks"`
}

// GenerateInput names what Generate consumes: the seed driving the
// story payloads and the directory holding the checked-in raw
// benchmark documents.
type GenerateInput struct {
	Seed    uint64
	DataDir string
}

// Generate runs both library-API stories against fresh temporary
// store directories and distills the benchmark figures, then renders
// the whole page data file. Same input, same bytes — the stories read
// their timestamps off a fresh injected clock each run and the store
// observables that could vary by machine never enter the output.
func Generate(in GenerateInput) ([]byte, error) {
	trajDir, err := os.MkdirTemp("", "elena-webgen-traj-")
	if err != nil {
		return nil, fmt.Errorf("trajectory store dir: %w", err)
	}
	defer func() { _ = os.RemoveAll(trajDir) }()
	cpDir, err := os.MkdirTemp("", "elena-webgen-cp-")
	if err != nil {
		return nil, fmt.Errorf("checkpoint store dir: %w", err)
	}
	defer func() { _ = os.RemoveAll(cpDir) }()

	store, err := elena.Open(trajDir)
	if err != nil {
		return nil, fmt.Errorf("open %q: %w", trajDir, err)
	}
	trajectory, err := trajectoryStory(store, newFakeClock(), in.Seed)
	if err != nil {
		return nil, err
	}
	if err := store.Close(); err != nil {
		return nil, fmt.Errorf("trajectory store close: %w", err)
	}

	checkpointClock := newFakeClock()
	checkpointClock.Advance(time.Hour) // the second act starts an hour after the first
	checkpoint, err := checkpointStory(cpDir, checkpointClock)
	if err != nil {
		return nil, err
	}

	benchmarks, err := distillBenchmarks(in.DataDir)
	if err != nil {
		return nil, err
	}
	return render(DemoData{
		Meta: Meta{
			Seed:       int(in.Seed),
			ClockStart: clockStart.UTC().Format(timestampFormat),
			ClockStep:  clockStep.String(),
			Generator:  "cmd/webgen: public library API, fixed seed, injected clock",
		},
		Trajectory: trajectory,
		Checkpoint: checkpoint,
		Benchmarks: benchmarks,
	})
}

// render emits the page's data file as one script-includable
// expression: a browser that opened the page straight off the disk
// (file://) cannot fetch data cross-origin, so the data ships as a
// const declaration the page's script tag pulls in directly.
func render(data DemoData) ([]byte, error) {
	body, err := json.Marshal(data)
	if err != nil {
		return nil, fmt.Errorf("marshal demo data: %w", err)
	}
	out := make([]byte, 0, len(body)+len("const DATA = ")+len(";\n"))
	out = append(out, "const DATA = "...)
	out = append(out, body...)
	out = append(out, ";\n"...)
	return out, nil
}
