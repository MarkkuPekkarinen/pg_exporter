package exporter

import (
	"testing"
)

// Ensure the legacy config (legacy/config) covers PG9.1..PG9.6 without version
// gaps for collectors that are supposed to work on legacy PG9.x.
func TestConfigCoveragePG9(t *testing.T) {
	queries := loadConfigDirForTest(t, "legacy/config")

	byName := make(map[string][]*Query)
	for _, q := range queries {
		if q.HasTag("pgbouncer") { // PG and pgbouncer versions are in different namespaces.
			continue
		}
		byName[q.Name] = append(byName[q.Name], q)
	}

	versions := []int{90100, 90200, 90300, 90400, 90500, 90600} // PG9.1..PG9.6
	for name, qs := range byName {
		minMin := 0
		for i, q := range qs {
			if i == 0 || q.MinVersion < minMin {
				minMin = q.MinVersion
			}
		}

		for _, v := range versions {
			// Collectors introduced after v are allowed to have gaps for older versions.
			if minMin != 0 && v < minMin {
				continue
			}

			var appl []*Query
			for _, q := range qs {
				if q.MinVersion != 0 && v < q.MinVersion {
					continue
				}
				if q.MaxVersion != 0 && v >= q.MaxVersion {
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
