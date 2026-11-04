// Command termcheck is the repository's wording guard: it checks the
// files Git tracks for terminology consistency with GLOSSARY.md and
// for code-generation attribution trailers. It scans narrative files
// (docs, README, web pages, CI config), Go files on the public API
// surface (facade, semantic layer, CLI, examples), and — restricted to
// identifier names — the engine internals. Run it with `make terms`;
// CI runs the same command.
package main

import (
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, gitListFiles))
}

// run checks one repository tree and reports the exit status: 0 clean,
// 1 wording violations found, 2 operational failure. The file lister
// is injected so tests can drive run without Git.
func run(args []string, stdout io.Writer, listFiles func(root string) ([]string, error)) int {
	flags := flag.NewFlagSet("termcheck", flag.ContinueOnError)
	flags.SetOutput(stdout)
	root := flags.String("root", ".", "repository root, scanned via git ls-files")
	if err := flags.Parse(args); err != nil {
		return 2
	}

	paths, err := listFiles(*root)
	if err != nil {
		fmt.Fprintf(stdout, "termcheck: listing tracked files: %v\n", err)
		return 2
	}

	total := 0
	for _, rel := range paths {
		content, err := os.ReadFile(filepath.Join(*root, filepath.FromSlash(rel)))
		if err != nil {
			fmt.Fprintf(stdout, "termcheck: reading %s: %v\n", rel, err)
			return 2
		}
		for _, v := range checkFile(File{Path: rel, Content: content}) {
			fmt.Fprintf(stdout, "%s:%d: %s: %q (%s)\n", v.Path, v.Line, v.Rule, v.Term, v.Fix)
			total++
		}
	}
	if total > 0 {
		fmt.Fprintf(stdout, "termcheck: %d violation(s); fix the wording or record an exemption\n", total)
		return 1
	}
	fmt.Fprintf(stdout, "termcheck: %d tracked files clean\n", len(paths))
	return 0
}

// gitListFiles returns the repository-relative paths Git tracks under
// root, in Git's sorted order.
func gitListFiles(root string) ([]string, error) {
	out, err := exec.Command("git", "-C", root, "ls-files", "-z", "--cached").Output()
	if err != nil {
		return nil, err
	}
	var paths []string
	for _, p := range strings.Split(string(out), "\x00") {
		if p != "" {
			paths = append(paths, p)
		}
	}
	return paths, nil
}
