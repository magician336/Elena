package main

// Maintenance: extend this table whenever GLOSSARY.md gains a term or an
// _Avoid_ entry, and add a fixture line to main_test.go next to it.
// New public artifacts (README sections, web pages, benchmark reports)
// need no registration — anything Git tracks is scanned by default;
// a deliberate exception goes into excludedPaths or a context allow
// with a comment saying why, and the CI terms job is the merge gate.

import (
	"regexp"
	"strings"
)

// The wording rules derive from GLOSSARY.md: every domain term has an
// _Avoid_ list, and this table mechanizes those lists. Each rule has
// two matchers:
//
//   - prose: case-insensitive, whole-word forms. Active in narrative
//     files and in Go files outside internal/engine — everywhere a
//     sentence can rename a domain concept.
//   - ident: case-sensitive CamelCase forms, matched as substrings so
//     compounds like scanSnapshot are caught. Active in all Go files:
//     an identifier is a name, and a banned name is a violation even
//     deep inside the engine.
//
// Scope notes, kept explicit so the false-positive budget stays
// predictable:
//
//   - "compact" the adjective ("a compact key-to-position map") is not
//     the LSM process noun; only compaction forms are banned.
//   - "prefix" is banned in narrative files only: in code it names
//     byte-prefix scan bounds, length-prefixed encodings, and sequence
//     prefixes — none of them the namespace concept.
//   - internal/engine Go files get the ident matcher only: generic
//     storage-engine vocabulary there (an index snapshot under a lock,
//     replaying a data file at Open) is precise standard usage, not a
//     domain-term renaming.

type rule struct {
	id    string
	prose *regexp.Regexp
	ident *regexp.Regexp
	fix   string
	// genericRegister: the term also has a legitimate generic
	// storage-engine register (an index snapshot, replaying a data
	// file at Open, leaving no trace), so inside internal/engine the
	// prose matcher is off for it and only identifiers are checked.
	// Terms without that register (tenant, compaction, savepoint,
	// conversation) are concept names in every register and stay
	// prose-checked even in engine files.
	genericRegister bool
}

var rules = []rule{
	{
		id:              "glossary/trajectory",
		prose:           regexp.MustCompile(`(?i)\b(?:traces?|traced|tracing)\b`),
		ident:           regexp.MustCompile(`(?:Trace|Traces|Traced|Tracing)`),
		fix:             `say "trajectory"`,
		genericRegister: true,
	},
	{
		id:              "glossary/checkpoint",
		prose:           regexp.MustCompile(`(?i)\b(?:snapshots?|snapshotting|snapshotted)\b`),
		ident:           regexp.MustCompile(`(?:Snapshot|Snapshots)`),
		fix:             `say "checkpoint"`,
		genericRegister: true,
	},
	{
		id:    "glossary/checkpoint",
		prose: regexp.MustCompile(`(?i)\bsavepoints?\b`),
		ident: regexp.MustCompile(`(?:Savepoint|Savepoints)`),
		fix:   `say "checkpoint"`,
	},
	{
		id:    "glossary/merge",
		prose: regexp.MustCompile(`(?i)\bcompactions?\b`),
		ident: regexp.MustCompile(`(?:Compaction|Compactions)`),
		fix:   `say "merge"`,
	},
	{
		id:              "glossary/point-in-time",
		prose:           regexp.MustCompile(`(?i)\b(?:replays?|replayed|replaying)\b|回放`),
		ident:           regexp.MustCompile(`(?:Replay|Replays|Replayed|Replaying)`),
		fix:             `say "point-in-time read"`,
		genericRegister: true,
	},
	{
		id:    "glossary/namespace",
		prose: regexp.MustCompile(`(?i)\btenants?\b`),
		ident: regexp.MustCompile(`(?:Tenant|Tenants)`),
		fix:   `say "namespace"; multi-tenancy is out of scope`,
	},
	{
		id:    "glossary/namespace-prefix",
		prose: regexp.MustCompile(`(?i)\bprefix(?:es)?\b`),
		fix:   `say "namespace"`,
	},
	{
		id:    "glossary/session",
		prose: regexp.MustCompile(`(?i)\bconversations?\b`),
		ident: regexp.MustCompile(`(?:Conversation|Conversations)`),
		fix:   `say "session"`,
	},
}

// gcRule bans "GC" outside Go runtime contexts: the project's space
// reclamation process is the merge. Lines that speak of the runtime
// (runtime.GC, garbage collector pauses) are allowed through.
var gcRule = struct {
	pattern *regexp.Regexp
	id      string
	fix     string
}{
	pattern: regexp.MustCompile(`\bGC\b`),
	id:      "glossary/merge",
	fix:     `say "merge"; only Go runtime contexts may say GC`,
}

// attribution rules apply to every scanned file: trailers and phrasing
// that credit code-generation tools.
var attributionRules = []rule{
	{
		id:    "attribution/co-author",
		prose: regexp.MustCompile(`(?i)\bco-authored-by\b`),
		fix:   "remove the co-author trailer",
	},
	{
		id:    "attribution/generated-with",
		prose: regexp.MustCompile(`(?i)generated\s+(?:with|by)\b[^\n]{0,60}?\b(?:claude|copilot|chatgpt|gpt|gemini|codex|zcode)\b`),
		fix:   "remove the generated-with credit",
	},
}

// excludedPaths are skipped entirely, path included. GLOSSARY.md is the
// definition source of every rule above and must show the avoided
// words; the checker's own table and fixtures reference them by
// necessity.
var excludedPaths = []string{"GLOSSARY.md", "tools/termcheck/"}

// profile selects which matchers a file gets.
type profile struct {
	// narrative: prose matchers on, including the prefix rule.
	narrative bool
	// engineGo: inside internal/engine; prose matchers run only for
	// rules without the generic-engine register.
	engineGo bool
	// goFile: any Go file; ident matchers are on.
	goFile bool
	// contentScanned: content is checked at all (GC and attribution
	// included). Paths are always checked otherwise.
	contentScanned bool
}

func profileFor(path string) profile {
	if narrativeFile(path) {
		return profile{narrative: true, contentScanned: true}
	}
	if !goFile(path) {
		return profile{}
	}
	if strings.HasPrefix(path, "internal/engine/") {
		return profile{engineGo: true, goFile: true, contentScanned: true}
	}
	return profile{goFile: true, contentScanned: true}
}

var narrativeExts = map[string]bool{
	".md": true, ".markdown": true, ".yml": true, ".yaml": true,
	".html": true, ".htm": true, ".js": true, ".css": true,
	".json": true, ".txt": true, ".sh": true, ".mod": true,
}

var narrativeNames = map[string]bool{
	"Makefile": true, "LICENSE": true, ".gitignore": true, "Dockerfile": true,
}

func narrativeFile(path string) bool {
	base := path[strings.LastIndexByte(path, '/')+1:]
	if narrativeNames[base] || strings.HasPrefix(base, "Makefile") {
		return true
	}
	return narrativeExts[strings.ToLower(filepathExt(path))]
}

func goFile(path string) bool {
	return filepathExt(path) == ".go"
}

func filepathExt(path string) string {
	base := path[strings.LastIndexByte(path, '/')+1:]
	if i := strings.IndexByte(base, '.'); i >= 0 {
		return strings.ToLower(base[i:])
	}
	return ""
}
