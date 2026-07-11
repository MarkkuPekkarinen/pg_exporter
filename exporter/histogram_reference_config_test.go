package exporter

import (
	"slices"
	"strings"
	"testing"
)

func TestHistogramReferenceSessionCollector(t *testing.T) {
	queries := loadRepoConfigForTest(t)
	query := queries["pg_session"]
	if query == nil {
		t.Fatal("reference pg_session collector is missing")
	}
	if query.Name != "pg_session" || query.TTL != 10 || query.MinVersion != 100000 || !slices.Equal(query.Tags, []string{"cluster"}) {
		t.Fatalf("pg_session controls = name:%s ttl:%v min:%d tags:%v", query.Name, query.TTL, query.MinVersion, query.Tags)
	}
	requireLabelNames(t, query, "datname", "state")
	if want := []string{"query_age_seconds", "xact_age_seconds", "age_seconds", "xid_age"}; !slices.Equal(query.MetricNames, want) {
		t.Fatalf("pg_session metric columns = %v, want %v", query.MetricNames, want)
	}

	timeBuckets := []float64{
		0.01, 0.03, 0.1, 0.3, 1, 3, 10, 30, 100, 300, 1000, 3000,
		10000, 30000, 100000, 300000, 1000000, 3000000,
	}
	for _, name := range []string{"query_age_seconds", "xact_age_seconds", "age_seconds"} {
		column := requireColumn(t, query, name, HISTOGRAM)
		if !slices.Equal(column.Bucket, timeBuckets) {
			t.Fatalf("pg_session %s buckets = %v, want %v", name, column.Bucket, timeBuckets)
		}
	}

	xidBuckets := []float64{
		1000000, 2000000, 5000000, 10000000, 20000000, 50000000,
		100000000, 200000000, 500000000, 1000000000, 2000000000,
	}
	if column := requireColumn(t, query, "xid_age", HISTOGRAM); !slices.Equal(column.Bucket, xidBuckets) {
		t.Fatalf("pg_session xid_age buckets = %v, want %v", column.Bucket, xidBuckets)
	}

	normalizedSQL := strings.Join(strings.Fields(query.SQL), " ")
	for _, fragment := range []string{
		"WHEN state = 'active' AND query_start IS NOT NULL",
		"greatest(0, extract(epoch FROM statement_timestamp() - query_start))",
		"backend_xid IS NOT NULL OR backend_xmin IS NOT NULL",
		"coalesce(age(backend_xid), 0)",
		"coalesce(age(backend_xmin), 0)",
		"pid <> pg_backend_pid()",
		"backend_type = 'client backend'",
		"datname IS NOT NULL",
	} {
		if !strings.Contains(normalizedSQL, fragment) {
			t.Fatalf("pg_session SQL is missing %q", fragment)
		}
	}
	if strings.Count(normalizedSQL, "greatest(") != 4 || strings.Contains(normalizedSQL, "least(") {
		t.Fatalf("pg_session SQL no longer clamps all four ages with greatest(): %s", normalizedSQL)
	}
	if hasCollector(queries, "pg_activity_hist") || hasCollector(queries, "pg_table_hist") {
		t.Fatal("superseded activity/table Histogram collector is still loaded")
	}
}
