package exporter

import (
	"fmt"
	"sort"

	"github.com/prometheus/client_golang/prometheus"
)

// validateConstLabelConflicts rejects constant label keys that would cause a
// Prometheus panic due to duplicate label names between const and variable labels.
//
// This can happen when a user passes `--label key=value` (or PG_EXPORTER_LABEL)
// where `key` equals one of a query's metric label names (after rename).
//
// When intro metrics are enabled, it also rejects keys that collide with the
// exporter internal dynamic metric labels (currently: datname, query).
func validateConstLabelConflicts(constLabels prometheus.Labels, queries map[string]*Query, disableIntro bool) error {
	if len(constLabels) == 0 {
		return nil
	}

	// Exporter internal dynamic metrics use these variable labels.
	if !disableIntro {
		for _, reserved := range []string{"datname", "query"} {
			if _, exists := constLabels[reserved]; exists {
				return fmt.Errorf("const label %q conflicts with built-in exporter metric label %q", reserved, reserved)
			}
		}
	}

	if len(queries) == 0 {
		return nil
	}

	// Stable iteration order for deterministic error messages.
	branches := make([]string, 0, len(queries))
	for b := range queries {
		branches = append(branches, b)
	}
	sort.Strings(branches)

	for _, branch := range branches {
		q := queries[branch]
		if q == nil {
			continue
		}
		if _, exists := constLabels["le"]; exists && q.HasHistogram() {
			return fmt.Errorf("const label %q conflicts with query %q (name=%q) generated Histogram bucket label %q", "le", branch, q.Name, "le")
		}
		for _, lbl := range q.LabelList() {
			if _, exists := constLabels[lbl]; exists {
				return fmt.Errorf("const label %q conflicts with query %q (name=%q) label %q", lbl, branch, q.Name, lbl)
			}
		}
	}

	return nil
}
