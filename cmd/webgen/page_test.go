package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// webDir is the page's directory from this package: ../../web.
var webDir = filepath.Join("..", "..", "web")

// readWebFile reads one checked-in page file.
func readWebFile(t *testing.T, name string) string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(webDir, name))
	if err != nil {
		t.Fatalf("read %s: %v (generate it with: go run ./cmd/webgen)", name, err)
	}
	return string(raw)
}

// TestPagePresentsAllThreeActs checks the page's structure: the three
// acts' sections exist, the data and rendering scripts are pulled in
// by plain script tags, and nothing in the page tries to load a
// network resource — the page must work opened directly from the
// filesystem, where cross-origin fetches fail.
func TestPagePresentsAllThreeActs(t *testing.T) {
	page := readWebFile(t, "index.html")
	for _, want := range []string{
		`id="act-trajectory"`,
		`id="act-checkpoint"`,
		`id="act-benchmarks"`,
		`<script src="data.js"></script>`,
		`<script src="demo.js"></script>`,
	} {
		if !strings.Contains(page, want) {
			t.Fatalf("index.html lacks %q", want)
		}
	}
	for _, banned := range []string{"fetch(", "XMLHttpRequest", "http://", "https://"} {
		if strings.Contains(page, banned) || strings.Contains(readWebFile(t, "demo.js"), banned) {
			t.Fatalf("page files must not load network resources (%q found); file:// requires fully local data",
				banned)
		}
	}
}

// TestRendererConsumesEveryAct ensures the rendering script actually
// renders all three acts from the data file — a page that silently
// dropped an act would otherwise still open cleanly.
func TestRendererConsumesEveryAct(t *testing.T) {
	renderer := readWebFile(t, "demo.js")
	for _, want := range []string{"DATA.trajectory", "DATA.checkpoint", "DATA.benchmarks"} {
		if !strings.Contains(renderer, want) {
			t.Fatalf("demo.js never renders %s", want)
		}
	}
	dataFile := readWebFile(t, "data.js")
	if !strings.HasPrefix(dataFile, "const DATA = ") {
		t.Fatalf("data.js must open with the const DATA declaration")
	}
}

// TestPageFirstSignalWord pins the wording discipline on the page's
// opening line: the first word of the tagline is a system signal
// word, and the agent wording appears only inside the scene lines —
// never as the leading term of any heading or the tagline.
func TestPageFirstSignalWord(t *testing.T) {
	page := readWebFile(t, "index.html")
	taglineStart := strings.Index(page, `<p class="tagline">`)
	if taglineStart < 0 {
		t.Fatalf("index.html lacks the tagline paragraph")
	}
	tagline := page[taglineStart+len(`<p class="tagline">`):]
	if !strings.HasPrefix(tagline, "Append-only") {
		t.Fatalf("tagline must open with a system signal word, got %q", firstWords(tagline, 3))
	}
	for _, heading := range []string{"<h1>", "<h2>"} {
		for _, at := range headingPositions(page, heading) {
			text := firstWords(page[at:], 4)
			if strings.EqualFold(text, "agent") || strings.HasPrefix(strings.ToLower(text), "agent ") {
				t.Fatalf("heading opens with agent wording: %q", text)
			}
		}
	}
}

// headingPositions returns the byte offsets just after each occurrence
// of the given heading tag.
func headingPositions(page, tag string) []int {
	var positions []int
	for from := 0; ; {
		at := strings.Index(page[from:], tag)
		if at < 0 {
			return positions
		}
		positions = append(positions, from+at+len(tag))
		from = from + at + len(tag)
	}
}

// firstWords returns the first n space-separated tokens of a snippet.
func firstWords(snippet string, n int) string {
	fields := strings.Fields(snippet)
	if len(fields) > n {
		fields = fields[:n]
	}
	return strings.Join(fields, " ")
}
