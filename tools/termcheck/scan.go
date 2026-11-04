package main

import (
	"bytes"
	"regexp"
	"sort"
	"strings"
)

// File is one Git-tracked file to check: its repository-relative path
// and its bytes.
type File struct {
	Path    string
	Content []byte
}

// Violation is one finding: where it is, which rule fired, the matched
// text, and the suggested fix. Line is 1-based; 0 marks a finding in
// the path itself.
type Violation struct {
	Path string
	Line int
	Rule string
	Term string
	Fix  string
}

// checkFile applies the wording rules to one file and returns its
// violations, sorted by line and rule.
func checkFile(f File) []Violation {
	if excludedPath(f.Path) {
		return nil
	}
	p := profileFor(f.Path)
	var vs []Violation
	vs = append(vs, checkPath(f.Path, p)...)
	if p.contentScanned && !bytes.ContainsRune(f.Content, 0) {
		vs = append(vs, checkContent(f.Path, f.Content, p)...)
	}
	sortViolations(vs)
	return vs
}

func excludedPath(path string) bool {
	for _, ex := range excludedPaths {
		if path == ex || strings.HasPrefix(path, ex) {
			return true
		}
	}
	return false
}

// checkPath applies the rules to the path itself: import paths and
// document names are public surface, and a banned term in a file name
// is a renaming in its own right. Path segments are matched as tokens
// (separators are any non-alphanumerics, so snapshot_test.go counts),
// and files whose content is not scanned get the narrative profile for
// their name — the name is all that is public about them.
func checkPath(path string, p profile) []Violation {
	if !p.contentScanned {
		p = profile{narrative: true}
	}
	normalized := pathTokenizer.ReplaceAllString(path, " ")
	var vs []Violation
	for _, r := range rules {
		if proseActive(r, p) {
			if m := r.prose.FindString(normalized); m != "" {
				vs = append(vs, Violation{Path: path, Line: 0, Rule: r.id, Term: m, Fix: r.fix})
				continue
			}
		}
		if r.ident != nil {
			if m := r.ident.FindString(path); m != "" {
				vs = append(vs, Violation{Path: path, Line: 0, Rule: r.id, Term: m, Fix: r.fix})
			}
		}
	}
	return vs
}

// pathTokenizer turns separators into spaces so whole-word prose
// patterns match file names like snapshot_test.go.
var pathTokenizer = regexp.MustCompile(`[^A-Za-z0-9]+`)

func checkContent(path string, content []byte, p profile) []Violation {
	var vs []Violation
	for i, line := range strings.Split(string(content), "\n") {
		line = strings.TrimSuffix(line, "\r")
		vs = append(vs, checkLine(path, i+1, line, p)...)
	}
	return vs
}

func checkLine(path string, n int, line string, p profile) []Violation {
	var vs []Violation
	for _, r := range rules {
		if proseActive(r, p) {
			if m := r.prose.FindString(line); m != "" {
				vs = append(vs, Violation{Path: path, Line: n, Rule: r.id, Term: m, Fix: r.fix})
				continue
			}
		}
		if p.goFile && r.ident != nil {
			if m := r.ident.FindString(line); m != "" {
				vs = append(vs, Violation{Path: path, Line: n, Rule: r.id, Term: m, Fix: r.fix})
			}
		}
	}
	if m := gcRule.pattern.FindString(line); m != "" {
		lower := strings.ToLower(line)
		if !strings.Contains(lower, "runtime") && !strings.Contains(lower, "garbage") {
			vs = append(vs, Violation{Path: path, Line: n, Rule: gcRule.id, Term: m, Fix: gcRule.fix})
		}
	}
	for _, r := range attributionRules {
		if m := r.prose.FindString(line); m != "" {
			vs = append(vs, Violation{Path: path, Line: n, Rule: r.id, Term: m, Fix: r.fix})
		}
	}
	return vs
}

// proseActive reports whether a rule's prose matcher participates for
// this file: narrative files run every prose rule (prefix included),
// public-surface Go files likewise, and engine Go files only the rules
// whose term has no generic-engine register.
func proseActive(r rule, p profile) bool {
	if r.id == "glossary/namespace-prefix" {
		return p.narrative
	}
	if p.narrative {
		return true
	}
	if p.engineGo {
		return !r.genericRegister
	}
	return p.goFile
}

func sortViolations(vs []Violation) {
	sort.Slice(vs, func(i, j int) bool {
		if vs[i].Line != vs[j].Line {
			return vs[i].Line < vs[j].Line
		}
		if vs[i].Rule != vs[j].Rule {
			return vs[i].Rule < vs[j].Rule
		}
		return vs[i].Term < vs[j].Term
	})
}
