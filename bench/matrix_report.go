package bench

import (
	"encoding/json"
	"fmt"
	"strings"
)

// The two statements every matrix report carries beside its numbers:
// what the workload language is, and how it maps onto elena's own
// model. A matrix number without these declarations is unreadable —
// the declarations are part of the report, not commentary on it.
const (
	// workloadDeclaration states the same-sequence contract.
	workloadDeclaration = "one seed declares every workload: each engine and tier drives the identical load-then-run operation sequence the seeded generator emits, so driver differences never leak into workload semantics"

	// elenaMappingDeclaration states the elena-side correspondence and
	// the data-set difference it implies.
	elenaMappingDeclaration = "one key is one trajectory (namespace bench, session traj-<key>); an update appends the trajectory's next event; a read is the single-event read of the latest acknowledged event. " +
		"The elena data set grows monotonically — an update is a new event, never an in-place overwrite — while the KV engines overwrite in place; " +
		"that difference is elena's append-only semantics, declared here rather than hidden"
)

// RenderMatrixMarkdown renders the matrix result as the human report:
// the declarations ahead of the numbers — workload semantics, the
// elena mapping, the durability standings engine by engine — then one
// table row per phase, latency as the P50/P99/P999 trio, and the
// failures listed by cell. The rendering is a pure function of its
// input: same result, same bytes, every time.
func RenderMatrixMarkdown(res MatrixResult) string {
	var b strings.Builder
	b.WriteString("# YCSB matrix report\n\n")
	b.WriteString("## Declarations\n\n")
	fmt.Fprintf(&b, "workload: %s\n\n", workloadDeclaration)
	fmt.Fprintf(&b, "elena mapping: %s\n\n", elenaMappingDeclaration)
	b.WriteString("durability, engine by engine:\n\n")
	for _, a := range res.Alignment {
		fmt.Fprintf(&b, "- %s\n", a.Engine)
		for _, td := range a.Tiers {
			standing := "aligned"
			if !td.Aligned {
				standing = "not aligned"
			}
			fmt.Fprintf(&b, "  - %s (%s): %s\n", td.Tier, standing, td.Configuration)
		}
	}
	b.WriteString("\n## Results\n\n")
	b.WriteString("| engine | sync tier | mix | value | phase | ops | elapsed | ops/sec | p50 | p99 | p999 | min | max |\n")
	b.WriteString("| --- | --- | --- | --- | --- | --- | --- | --- | --- | --- | --- | --- | --- |\n")
	for _, cr := range res.Cells {
		for _, pr := range cr.Result.Phases {
			lat := pr.Latency
			fmt.Fprintf(&b, "| %s | %s | %s | %dB | %s | %d | %s | %.1f | %s | %s | %s | %s | %s |\n",
				cr.Cell.Engine, cr.Cell.Tier, cr.Cell.Mix, cr.Cell.ValueSize, pr.Phase, pr.Ops,
				pr.Elapsed.Round(reportPrecision), pr.Throughput,
				lat.P50.Round(reportPrecision), lat.P99.Round(reportPrecision), lat.P999.Round(reportPrecision),
				lat.Min.Round(reportPrecision), lat.Max.Round(reportPrecision))
		}
	}
	if len(res.Failures) > 0 {
		b.WriteString("\n## Failures\n\n")
		for _, f := range res.Failures {
			fmt.Fprintf(&b, "- %s: %v\n", f.Cell.DirName(), f.Err)
		}
	}
	return b.String()
}

// RenderMatrixJSON renders the matrix result as the raw-numbers
// document: the declaration block — workload semantics, the elena
// mapping, the engine-by-engine durability statements — beside one
// result object per produced cell, whose phases carry the same field
// names the single-run raw document uses. Fixed field names in fixed
// order, durations as nanosecond integers: two runs of the same result
// produce identical bytes.
func RenderMatrixJSON(res MatrixResult) ([]byte, error) {
	doc := jsonMatrixDoc{
		Declaration: jsonMatrixDeclaration{
			Workload:     workloadDeclaration,
			ElenaMapping: elenaMappingDeclaration,
			Sync:         make([]jsonEngineSync, 0, len(res.Alignment)),
		},
		Results:  make([]jsonMatrixResult, 0, len(res.Cells)),
		Failures: make([]jsonCellFailure, 0, len(res.Failures)),
	}
	for _, a := range res.Alignment {
		entry := jsonEngineSync{Engine: a.Engine, Tiers: make([]jsonTierSync, 0, len(a.Tiers))}
		for _, td := range a.Tiers {
			entry.Tiers = append(entry.Tiers, jsonTierSync{
				Tier:          td.Tier.String(),
				Aligned:       td.Aligned,
				Configuration: td.Configuration,
			})
		}
		doc.Declaration.Sync = append(doc.Declaration.Sync, entry)
	}
	for _, cr := range res.Cells {
		doc.Results = append(doc.Results, jsonMatrixResult{
			Engine:    cr.Cell.Engine,
			SyncTier:  cr.Cell.Tier.String(),
			Mix:       cr.Cell.Mix.String(),
			ValueSize: cr.Cell.ValueSize,
			Phases:    jsonPhases(cr.Result.Phases),
		})
	}
	for _, f := range res.Failures {
		doc.Failures = append(doc.Failures, jsonCellFailure{
			Engine:    f.Cell.Engine,
			SyncTier:  f.Cell.Tier.String(),
			Mix:       f.Cell.Mix.String(),
			ValueSize: f.Cell.ValueSize,
			Error:     f.Err.Error(),
		})
	}
	out, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("render matrix json report: %w", err)
	}
	return append(out, '\n'), nil
}

// jsonMatrixDoc is the top level of the matrix raw document: the
// declarations the numbers travel with, the produced cells' numbers,
// and the recorded failures.
type jsonMatrixDoc struct {
	Declaration jsonMatrixDeclaration `json:"declaration"`
	Results     []jsonMatrixResult    `json:"results"`
	Failures    []jsonCellFailure     `json:"failures"`
}

// jsonMatrixDeclaration is the declaration block: the workload
// semantics, the elena mapping, and the durability standings engine by
// engine.
type jsonMatrixDeclaration struct {
	Workload     string           `json:"workload"`
	ElenaMapping string           `json:"elena_mapping"`
	Sync         []jsonEngineSync `json:"sync"`
}

// jsonEngineSync declares one engine's standings.
type jsonEngineSync struct {
	Engine string         `json:"engine"`
	Tiers  []jsonTierSync `json:"tiers"`
}

// jsonTierSync declares one engine×tier standing: the configuration
// sentence when aligned, the reason no numbers exist when not.
type jsonTierSync struct {
	Tier          string `json:"tier"`
	Aligned       bool   `json:"aligned"`
	Configuration string `json:"configuration"`
}

// jsonMatrixResult is one produced cell: its four dimensions plus the
// phase numbers in the single-run raw document's shape.
type jsonMatrixResult struct {
	Engine    string      `json:"engine"`
	SyncTier  string      `json:"sync_tier"`
	Mix       string      `json:"mix"`
	ValueSize int         `json:"value_size"`
	Phases    []jsonPhase `json:"phases"`
}

// jsonCellFailure is one cell that produced no numbers, with its
// dimensions and the reason.
type jsonCellFailure struct {
	Engine    string `json:"engine"`
	SyncTier  string `json:"sync_tier"`
	Mix       string `json:"mix"`
	ValueSize int    `json:"value_size"`
	Error     string `json:"error"`
}
