package exporter

import (
	"strings"
	"testing"

	"github.com/prometheus/client_golang/prometheus"
)

func TestParseConfigHistogramOnlyQuery(t *testing.T) {
	config := `
activity_hist:
  name: pg_activity_hist
  query: SELECT 'app' AS datname, 0.5 AS duration
  metrics:
    - datname:
        usage: label
        rename: database
    - duration:
        usage: histogram
        rename: query_duration_seconds
        bucket: [0.1, 1, 10]
        scale: "2"
        default: "0.25"
        description: Current query duration snapshot
`

	queries, err := ParseConfig([]byte(config))
	if err != nil {
		t.Fatalf("ParseConfig returned error: %v", err)
	}
	q := queries["activity_hist"]
	if q == nil {
		t.Fatal("activity_hist query not found")
	}
	c := q.Columns["duration"]
	if c == nil {
		t.Fatal("duration column not found")
	}
	if c.Usage != HISTOGRAM {
		t.Fatalf("usage = %q, want %q", c.Usage, HISTOGRAM)
	}
	if !ColumnUsage[HISTOGRAM] {
		t.Fatal("HISTOGRAM should be classified as a metric column")
	}
	if len(q.MetricNames) != 1 || q.MetricNames[0] != "duration" {
		t.Fatalf("MetricNames = %#v, want [duration]", q.MetricNames)
	}
	if !q.HasHistogram() {
		t.Fatal("HasHistogram returned false for Histogram-only query")
	}
	if len(c.Bucket) != 3 || c.Bucket[0] != 0.1 || c.Bucket[2] != 10 {
		t.Fatalf("Bucket = %#v, want [0.1 1 10]", c.Bucket)
	}
	if !c.hasScale || c.scaleFactor != 2 {
		t.Fatalf("parsed scale = (%v, %v), want (true, 2)", c.hasScale, c.scaleFactor)
	}
	if !c.hasDefault || c.defaultValue != 0.25 {
		t.Fatalf("parsed default = (%v, %v), want (true, 0.25)", c.hasDefault, c.defaultValue)
	}

	metrics := q.MetricList()
	if len(metrics) != 1 {
		t.Fatalf("MetricList len = %d, want 1 logical Histogram", len(metrics))
	}
	if got := metrics[0].Name; got != "pg_activity_hist_query_duration_seconds{database}" {
		t.Fatalf("logical Histogram metric name = %q", got)
	}

	explain := q.Explain()
	if !strings.Contains(explain, "Bucket [0.1 1 10]") {
		t.Fatalf("Explain does not show Histogram buckets:\n%s", explain)
	}
	marshaled := q.MarshalYAML()
	if !strings.Contains(marshaled, "bucket:") || !strings.Contains(marshaled, "- 0.1") {
		t.Fatalf("MarshalYAML does not preserve Histogram buckets:\n%s", marshaled)
	}
}

func TestParseConfigRejectsInvalidHistogramBuckets(t *testing.T) {
	tests := []struct {
		name   string
		bucket string
		want   string
	}{
		{name: "missing", bucket: "", want: "at least one bucket"},
		{name: "empty", bucket: "bucket: []", want: "at least one bucket"},
		{name: "unsorted", bucket: "bucket: [1, 0.1]", want: "strictly increasing"},
		{name: "duplicate", bucket: "bucket: [0.1, 0.1]", want: "strictly increasing"},
		{name: "nan", bucket: "bucket: [.nan]", want: "must be finite"},
		{name: "positive infinity", bucket: "bucket: [.inf]", want: "must be finite"},
		{name: "negative infinity", bucket: "bucket: [-.inf]", want: "must be finite"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			config := `
bad_hist:
  query: SELECT 1 AS value
  metrics:
    - value:
        usage: HISTOGRAM
` + indentHistogramTestYAML(tt.bucket, 8)
			_, err := ParseConfig([]byte(config))
			if err == nil {
				t.Fatal("ParseConfig should reject invalid Histogram buckets")
			}
			if !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("error %q does not contain %q", err, tt.want)
			}
		})
	}
}

func TestParseConfigRejectsHistogramLabelLEAfterRename(t *testing.T) {
	tests := []struct {
		name  string
		label string
	}{
		{name: "direct", label: "le"},
		{name: "renamed", label: "upper_bound"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rename := ""
			if tt.name == "renamed" {
				rename = "        rename: le\n"
			}
			config := `
bad_hist:
  query: SELECT 'x' AS ` + tt.label + `, 1 AS value
  metrics:
    - ` + tt.label + `:
        usage: LABEL
` + rename + `    - value:
        usage: HISTOGRAM
        bucket: [1]
`
			_, err := ParseConfig([]byte(config))
			if err == nil {
				t.Fatal("ParseConfig should reject le as a Histogram query label")
			}
			if !strings.Contains(err.Error(), `label "le"`) {
				t.Fatalf("unexpected error: %v", err)
			}
		})
	}

	// le is not globally reserved for scalar-only queries.
	scalar := `
scalar:
  query: SELECT 'x' AS le, 1 AS value
  metrics:
    - le: {usage: LABEL}
    - value: {usage: GAUGE}
`
	if _, err := ParseConfig([]byte(scalar)); err != nil {
		t.Fatalf("scalar-only query should allow label le: %v", err)
	}
}

func TestParseConfigRejectsHistogramDerivedNameCollisions(t *testing.T) {
	tests := []struct {
		name        string
		otherUsage  string
		otherRename string
	}{
		{name: "bucket scalar", otherUsage: GAUGE, otherRename: "latency_bucket"},
		{name: "count scalar", otherUsage: COUNTER, otherRename: "latency_count"},
		{name: "sum scalar", otherUsage: GAUGE, otherRename: "latency_sum"},
		{name: "duplicate scalar base", otherUsage: GAUGE, otherRename: "latency"},
		{name: "derived Histogram base", otherUsage: HISTOGRAM, otherRename: "latency_bucket"},
		{name: "duplicate logical base", otherUsage: HISTOGRAM, otherRename: "latency"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			otherBucket := ""
			if tt.otherUsage == HISTOGRAM {
				otherBucket = "\n        bucket: [1, 2]"
			}
			config := `
collision:
  name: pg_collision
  query: SELECT 1 AS duration, 2 AS other
  metrics:
    - duration:
        usage: HISTOGRAM
        rename: latency
        bucket: [0.1, 1]
    - other:
        usage: ` + tt.otherUsage + `
        rename: ` + tt.otherRename + otherBucket + `
`
			_, err := ParseConfig([]byte(config))
			if err == nil {
				t.Fatal("ParseConfig should reject Histogram family collision")
			}
			if !strings.Contains(err.Error(), "conflicts") {
				t.Fatalf("unexpected error: %v", err)
			}
		})
	}
}

func TestValidateConstLabelLEOnlyConflictsWithHistogram(t *testing.T) {
	histogramConfig := `
hist:
  query: SELECT 1 AS value
  metrics:
    - value:
        usage: HISTOGRAM
        bucket: [1]
`
	histograms, err := ParseConfig([]byte(histogramConfig))
	if err != nil {
		t.Fatalf("ParseConfig Histogram failed: %v", err)
	}
	labels := prometheus.Labels{"le": "site"}
	if err := validateConstLabelConflicts(labels, histograms, true); err == nil {
		t.Fatal("expected const label le to conflict with Histogram query")
	} else if !strings.Contains(err.Error(), "generated Histogram bucket label") {
		t.Fatalf("unexpected error: %v", err)
	}

	scalarConfig := `
scalar:
  query: SELECT 1 AS value
  metrics:
    - value: {usage: GAUGE}
`
	scalars, err := ParseConfig([]byte(scalarConfig))
	if err != nil {
		t.Fatalf("ParseConfig scalar failed: %v", err)
	}
	if err := validateConstLabelConflicts(labels, scalars, true); err != nil {
		t.Fatalf("const label le should be allowed without Histogram queries: %v", err)
	}
}

func indentHistogramTestYAML(value string, spaces int) string {
	if value == "" {
		return ""
	}
	return strings.Repeat(" ", spaces) + value + "\n"
}
