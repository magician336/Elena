package bench

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// reportPrecision is the duration resolution reports print: whole
// microseconds, the resolution a single synchronized operation on a
// real disk resolves to. Raw JSON keeps full nanosecond resolution.
const reportPrecision = time.Microsecond

// RenderMarkdown renders results as the human report: one table row
// per phase, latency as the P50/P99/P999 trio plus min and max, no
// mean — averages flatten the tail the report exists to show. The
// rendering is a pure function of its input: same results, same
// bytes, every time, so a rerun of a rerun diffs clean.
func RenderMarkdown(results []Result) string {
	var b strings.Builder
	b.WriteString("# Bench report\n\n")
	b.WriteString("| driver | workload | phase | ops | elapsed | ops/sec | p50 | p99 | p999 | min | max |\n")
	b.WriteString("| --- | --- | --- | --- | --- | --- | --- | --- | --- | --- | --- |\n")
	for _, res := range results {
		for _, pr := range res.Phases {
			lat := pr.Latency
			fmt.Fprintf(&b, "| %s | %s | %s | %d | %s | %.1f | %s | %s | %s | %s | %s |\n",
				res.Driver, res.Workload, pr.Phase, pr.Ops,
				pr.Elapsed.Round(reportPrecision), pr.Throughput,
				lat.P50.Round(reportPrecision), lat.P99.Round(reportPrecision), lat.P999.Round(reportPrecision),
				lat.Min.Round(reportPrecision), lat.Max.Round(reportPrecision))
		}
	}
	return b.String()
}

// RenderJSON renders results as the raw-numbers document: fixed field
// names in fixed order, durations as nanosecond integers, throughput
// as ops per second. Structure is stable by construction — struct
// fields, no maps — so two runs of the same input produce identical
// bytes and two different runs diff line by line.
func RenderJSON(results []Result) ([]byte, error) {
	doc := jsonReport{Results: make([]jsonResult, 0, len(results))}
	for _, res := range results {
		jr := jsonResult{
			Driver:   res.Driver,
			Workload: res.Workload,
			Phases:   make([]jsonPhase, 0, len(res.Phases)),
		}
		for _, pr := range res.Phases {
			jr.Phases = append(jr.Phases, jsonPhase{
				Phase:      pr.Phase,
				Ops:        pr.Ops,
				ElapsedNS:  int64(pr.Elapsed),
				Throughput: pr.Throughput,
				Latency: jsonLatency{
					Count:  pr.Latency.Count,
					MinNS:  int64(pr.Latency.Min),
					P50NS:  int64(pr.Latency.P50),
					P99NS:  int64(pr.Latency.P99),
					P999NS: int64(pr.Latency.P999),
					MaxNS:  int64(pr.Latency.Max),
				},
			})
		}
		doc.Results = append(doc.Results, jr)
	}
	out, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("render json report: %w", err)
	}
	return append(out, '\n'), nil
}

// jsonReport is the top level of the raw document.
type jsonReport struct {
	Results []jsonResult `json:"results"`
}

// jsonResult is one driver's run inside the raw document.
type jsonResult struct {
	Driver   string      `json:"driver"`
	Workload string      `json:"workload"`
	Phases   []jsonPhase `json:"phases"`
}

// jsonPhase is one phase's numbers: counts, spans, rates, and the
// latency summary as the percentile trio with min and max.
type jsonPhase struct {
	Phase      string      `json:"phase"`
	Ops        int         `json:"ops"`
	ElapsedNS  int64       `json:"elapsed_ns"`
	Throughput float64     `json:"throughput_ops_sec"`
	Latency    jsonLatency `json:"latency"`
}

// jsonLatency is the latency summary in nanoseconds.
type jsonLatency struct {
	Count  int   `json:"count"`
	MinNS  int64 `json:"min_ns"`
	P50NS  int64 `json:"p50_ns"`
	P99NS  int64 `json:"p99_ns"`
	P999NS int64 `json:"p999_ns"`
	MaxNS  int64 `json:"max_ns"`
}
