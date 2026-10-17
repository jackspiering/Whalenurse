package rules_test

import (
	"bufio"
	"bytes"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/jackspiering/whalenurse/internal/docker"
)

// fixtureDir is the repository's scrubbed M0 captures.
const fixtureDir = "../../testdata"

// loadInspect decodes one scrubbed `docker inspect` fixture through the real
// moby types.
func loadInspect(t testing.TB, name string) docker.Container {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(fixtureDir, "inspect", name+".json"))
	if err != nil {
		t.Fatal(err)
	}
	c, err := docker.ParseInspect(raw)
	if err != nil {
		t.Fatalf("%s: %v", name, err)
	}
	return c
}

// listFixtures returns the base names of the files in testdata/<kind>.
func listFixtures(t testing.TB, kind, ext string) []string {
	t.Helper()
	files, err := filepath.Glob(filepath.Join(fixtureDir, kind, "*"+ext))
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, f := range files {
		names = append(names, strings.TrimSuffix(filepath.Base(f), ext))
	}
	sort.Strings(names)
	return names
}

// loadEvents decodes a scrubbed events fixture, in file order. Times in the
// fixtures are rebased, so order is by line.
func loadEvents(t testing.TB, name string) []docker.Event {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(fixtureDir, "events", name+".ndjson"))
	if err != nil {
		t.Fatal(err)
	}
	var out []docker.Event
	sc := bufio.NewScanner(bytes.NewReader(raw))
	sc.Buffer(make([]byte, 1<<20), 1<<20)
	for sc.Scan() {
		if len(bytes.TrimSpace(sc.Bytes())) == 0 {
			continue
		}
		ev, err := docker.ParseEvent(sc.Bytes())
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		out = append(out, ev)
	}
	if err := sc.Err(); err != nil {
		t.Fatal(err)
	}
	return out
}
