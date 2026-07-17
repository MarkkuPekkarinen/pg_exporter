package exporter

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

// loadConfigDirForTest loads a repo-relative config directory, skipping the
// test when the directory is absent (e.g. in stripped source archives).
func loadConfigDirForTest(t *testing.T, rel string) map[string]*Query {
	t.Helper()

	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	configDir := filepath.Clean(filepath.Join(filepath.Dir(thisFile), "..", filepath.FromSlash(rel)))
	if _, err := os.Stat(configDir); err != nil {
		t.Skipf("config dir not found: %s: %v", configDir, err)
	}

	queries, err := LoadConfig(configDir)
	if err != nil {
		t.Fatalf("LoadConfig(%s) failed: %v", configDir, err)
	}
	return queries
}

// branchesMutuallyExclusive reports whether overlapping branches for one
// collector are acceptable because they split on the engine-defined
// primary/replica tag pair. This is a tag-semantics check, deliberately not
// keyed on any collector name: the config is user-replaceable content.
func branchesMutuallyExclusive(appl []*Query) bool {
	if len(appl) != 2 {
		return false
	}
	return (appl[0].HasTag("primary") && appl[1].HasTag("replica")) ||
		(appl[0].HasTag("replica") && appl[1].HasTag("primary"))
}

// Ensure the repo-bundled config/ covers PG10..PG19 without version gaps for
// collectors that are supposed to work on PG10+. This is a cheap static check
// (no DB required) to catch off-by-one mistakes on min/max_version splits.
// It iterates whatever collectors the config defines and never assumes any
// specific collector exists: the config is user-replaceable content, not a fixture.
func TestConfigCoveragePG10To19(t *testing.T) {
	queries := loadConfigDirForTest(t, "config")

	byName := make(map[string][]*Query)
	for _, q := range queries {
		if q.HasTag("pgbouncer") { // PG and pgbouncer versions are in different namespaces.
			continue
		}
		byName[q.Name] = append(byName[q.Name], q)
	}

	for name, qs := range byName {
		minMin := 0
		for i, q := range qs {
			if i == 0 || q.MinVersion < minMin {
				minMin = q.MinVersion
			}
		}

		// Collectors introduced after PG10 are allowed to have gaps for PG10-.
		if minMin > 100000 {
			continue
		}

		for v := 100000; v <= 190000; v += 10000 { // PG10..PG19
			var appl []*Query
			for _, q := range qs {
				if q.MinVersion != 0 && v < q.MinVersion {
					continue
				}
				if q.MaxVersion != 0 && v >= q.MaxVersion { // exclude
					continue
				}
				appl = append(appl, q)
			}

			if len(appl) == 0 {
				t.Errorf("collector %q has no branch for server_version_num=%d", name, v)
				continue
			}

			if len(appl) > 1 && !branchesMutuallyExclusive(appl) {
				t.Errorf("collector %q has %d overlapping branches for server_version_num=%d: %v", name, len(appl), v, func() []string {
					out := make([]string, 0, len(appl))
					for _, q := range appl {
						out = append(out, q.Branch)
					}
					return out
				}())
			}
		}
	}
}
