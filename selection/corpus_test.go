package selection

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// corpusCase is one recorded release and the files it must resolve to.
type corpusCase struct {
	file, repo string
	kind       ItemKind
	topics     []string
	assets     []Asset
	// expect maps "x64/installers" and the like to a file name, "none" for no pick.
	expect map[string]string
}

var corpusTargets = []struct {
	name   string
	policy Policy
}{
	{"x64/installers", x64Windows},
	{"x64/portable", portable(x64Windows)},
	{"arm64/installers", arm64Windows},
	{"arm64/portable", portable(arm64Windows)},
}

// parseCorpus reads the blocks of one corpus file; its format is described in
// testdata/corpus/README.md.
func parseCorpus(t *testing.T, path string) []*corpusCase {
	t.Helper()
	f, err := os.Open(filepath.Clean(path))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()
	var cases []*corpusCase
	var current *corpusCase
	lineNo := 0
	fail := func(format string, args ...any) {
		t.Fatalf("%s:%d: %s", filepath.Base(path), lineNo, fmt.Sprintf(format, args...))
	}
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		lineNo++
		line := strings.TrimSpace(scanner.Text())
		switch {
		case line == "" || strings.HasPrefix(line, "#"):
		case strings.HasPrefix(line, "== "):
			fields := strings.Fields(line[3:])
			current = &corpusCase{file: filepath.Base(path), repo: fields[0], expect: map[string]string{}}
			for _, field := range fields[1:] {
				key, value, _ := strings.Cut(field, "=")
				switch key {
				case "kind":
					current.kind = ItemKind(value)
				case "topics":
					current.topics = strings.Split(value, ",")
				default:
					fail("unknown header field %q", field)
				}
			}
			cases = append(cases, current)
		case current == nil:
			fail("content before the first block")
		case strings.HasPrefix(line, "-> "):
			target, name, ok := strings.Cut(line[3:], ": ")
			if !ok {
				fail("malformed expectation %q", line)
			}
			for _, combo := range corpusTargets {
				if target == "all" || target == combo.name || strings.HasPrefix(combo.name, target+"/") ||
					strings.HasSuffix(combo.name, "/"+target) {
					current.expect[combo.name] = name
				}
			}
		default:
			name, size, _ := strings.Cut(line, " ")
			n, _ := strconv.ParseInt(size, 10, 64)
			current.assets = append(current.assets, Asset{Name: name, Size: n})
		}
	}
	if err := scanner.Err(); err != nil {
		t.Fatal(err)
	}
	return cases
}

func TestGoldenCorpus(t *testing.T) {
	paths, err := filepath.Glob(filepath.Join("testdata", "corpus", "*.txt"))
	if err != nil || len(paths) == 0 {
		t.Fatalf("no corpus files: %v", err)
	}
	total := 0
	seen := map[string]bool{}
	for _, path := range paths {
		for _, c := range parseCorpus(t, path) {
			total++
			if seen[c.repo] {
				t.Errorf("%s: %s is recorded twice", c.file, c.repo)
			}
			seen[c.repo] = true
			repo := c.repo[strings.Index(c.repo, "/")+1:]
			if got := GuessKind(c.assets, c.topics, repo); got != c.kind {
				t.Errorf("%s: guessed kind %q, want %q", c.repo, got, c.kind)
			}
			for _, target := range corpusTargets {
				want, ok := c.expect[target.name]
				if !ok {
					t.Errorf("%s: no expectation for %s", c.repo, target.name)
					continue
				}
				res := Resolve(Request{Assets: c.assets, Repo: repo, Kind: c.kind, Policy: target.policy})
				got := pickName(t, res)
				if got == "" {
					got = "none"
				}
				if got != want {
					t.Errorf("%s (%s): picked %s, want %s\n%s", c.repo, target.name, got, want, res.Explain())
				}
			}
		}
	}
	t.Logf("%d recorded releases", total)
}
