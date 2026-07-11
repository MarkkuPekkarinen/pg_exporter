package exporter

import (
	"context"
	"database/sql/driver"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"
)

func TestHistogramAcceptanceMultipleColumnsAggregateIndependently(t *testing.T) {
	query := histogramTestQuery(
		"test_multi_hist",
		[]string{"datname"},
		map[string]*Column{
			"query_time": {Name: "query_time", Usage: HISTOGRAM, Bucket: []float64{1, 10}},
			"xid_age":    {Name: "xid_age", Usage: HISTOGRAM, Bucket: []float64{100, 1000}},
		},
		[]string{"query_time", "xid_age"},
	)
	collector := newHistogramTestCollector(t, query, func() driver.Rows {
		return &histogramTestRows{
			columns: []string{"datname", "query_time", "xid_age"},
			values: [][]driver.Value{
				{"db1", float64(0.5), float64(50)},
				{"db1", float64(5), float64(500)},
				{"db1", nil, float64(5000)},
				{"db2", float64(20), nil},
			},
		}
	}, nil)
	collector.scrapeBegin = time.Now()
	collector.execute()
	if err := collector.Error(); err != nil {
		t.Fatalf("execute two histogram columns: %v", err)
	}
	// query_time has two label groups and xid_age has one. Each two-bucket
	// histogram group emits two finite buckets, +Inf, count, and sum.
	if got := collector.ResultSize(); got != 15 {
		t.Fatalf("result size = %d, want 15", got)
	}

	samples, _ := gatherHistogramSamples(t, collector)
	db1 := map[string]string{"cluster": "c1", "datname": "db1"}
	requireHistogramSample(t, samples, "test_multi_hist_query_time_bucket", map[string]string{"cluster": "c1", "datname": "db1", "le": "1"}, 1)
	requireHistogramSample(t, samples, "test_multi_hist_query_time_bucket", map[string]string{"cluster": "c1", "datname": "db1", "le": "10"}, 2)
	requireHistogramSample(t, samples, "test_multi_hist_query_time_count", db1, 2)
	requireHistogramSample(t, samples, "test_multi_hist_query_time_sum", db1, 5.5)
	requireHistogramSample(t, samples, "test_multi_hist_xid_age_bucket", map[string]string{"cluster": "c1", "datname": "db1", "le": "100"}, 1)
	requireHistogramSample(t, samples, "test_multi_hist_xid_age_bucket", map[string]string{"cluster": "c1", "datname": "db1", "le": "1000"}, 2)
	requireHistogramSample(t, samples, "test_multi_hist_xid_age_count", db1, 3)
	requireHistogramSample(t, samples, "test_multi_hist_xid_age_sum", db1, 5550)
	requireHistogramSample(t, samples, "test_multi_hist_query_time_count", map[string]string{"cluster": "c1", "datname": "db2"}, 1)
}

func TestHistogramAcceptanceBelowFirstBucketAndAllNullTuple(t *testing.T) {
	query := histogramTestQuery(
		"test_absent_hist",
		[]string{"datname"},
		map[string]*Column{
			"value": {Name: "value", Usage: HISTOGRAM, Bucket: []float64{0, 1}},
		},
		[]string{"value"},
	)
	collector := newHistogramTestCollector(t, query, func() driver.Rows {
		return &histogramTestRows{
			columns: []string{"datname", "value"},
			values: [][]driver.Value{
				{"below", float64(-5)},
				{"all_null", nil},
				{"all_null", nil},
			},
		}
	}, nil)
	collector.scrapeBegin = time.Now()
	collector.execute()
	if err := collector.Error(); err != nil {
		t.Fatalf("execute histogram query: %v", err)
	}
	if got := collector.ResultSize(); got != 5 {
		t.Fatalf("result size = %d, want only one two-bucket group (5 metrics)", got)
	}

	samples, _ := gatherHistogramSamples(t, collector)
	requireHistogramSample(t, samples, "test_absent_hist_value_bucket", map[string]string{"cluster": "c1", "datname": "below", "le": "0"}, 1)
	requireHistogramSample(t, samples, "test_absent_hist_value_bucket", map[string]string{"cluster": "c1", "datname": "below", "le": "1"}, 1)
	requireHistogramSample(t, samples, "test_absent_hist_value_count", map[string]string{"cluster": "c1", "datname": "below"}, 1)
	for key := range samples {
		if strings.Contains(key, "datname=all_null") {
			t.Fatalf("all-NULL label tuple unexpectedly emitted sample %s", key)
		}
	}
}

func TestHistogramAcceptanceScientificNotationLEIsStable(t *testing.T) {
	query := histogramTestQuery(
		"test_large_boundary",
		[]string{"datname"},
		map[string]*Column{
			"age": {Name: "age", Usage: HISTOGRAM, Bucket: []float64{3000000}},
		},
		[]string{"age"},
	)
	collector := newHistogramTestCollector(t, query, func() driver.Rows {
		return &histogramTestRows{
			columns: []string{"datname", "age"},
			values:  [][]driver.Value{{"db", float64(1)}},
		}
	}, nil)
	collector.scrapeBegin = time.Now()
	collector.execute()
	if err := collector.Error(); err != nil {
		t.Fatalf("execute histogram query: %v", err)
	}

	samples, _ := gatherHistogramSamples(t, collector)
	requireHistogramSample(t, samples, "test_large_boundary_age_bucket", map[string]string{
		"cluster": "c1", "datname": "db", "le": "3e+06",
	}, 1)
}

func TestHistogramAcceptanceRenamedLabelOrderAndLELast(t *testing.T) {
	query := histogramTestQuery(
		"test_label_order",
		[]string{"first_source", "second_source"},
		map[string]*Column{
			"duration": {Name: "duration", Usage: HISTOGRAM, Bucket: []float64{1}},
		},
		[]string{"duration"},
	)
	query.Columns["first_source"].Rename = "zeta"
	query.Columns["second_source"].Rename = "alpha"
	collector := newHistogramTestCollector(t, query, func() driver.Rows {
		return &histogramTestRows{
			columns: []string{"first_source", "second_source", "duration"},
			values:  [][]driver.Value{{"Z", "A", float64(0.5)}},
		}
	}, nil)

	descriptors := collector.histogramDesc["duration"]
	if got := descriptors.bucket.String(); !strings.Contains(got, "variableLabels: {zeta,alpha,le}") {
		t.Fatalf("bucket descriptor label order = %s, want zeta,alpha,le", got)
	}
	for component, descriptor := range map[string]*prometheus.Desc{
		"count": descriptors.count,
		"sum":   descriptors.sum,
	} {
		if got := descriptor.String(); !strings.Contains(got, "variableLabels: {zeta,alpha}") {
			t.Fatalf("%s descriptor label order = %s, want zeta,alpha", component, got)
		}
	}

	collector.scrapeBegin = time.Now()
	collector.execute()
	if err := collector.Error(); err != nil {
		t.Fatalf("execute histogram query: %v", err)
	}
	samples, _ := gatherHistogramSamples(t, collector)
	requireHistogramSample(t, samples, "test_label_order_duration_bucket", map[string]string{
		"cluster": "c1", "zeta": "Z", "alpha": "A", "le": "1",
	}, 1)
}

func TestHistogramAcceptanceFailedRefreshCachesEmptySnapshot(t *testing.T) {
	query := histogramTestQuery(
		"test_failed_ttl",
		[]string{"datname"},
		map[string]*Column{
			"duration": {Name: "duration", Usage: HISTOGRAM, Bucket: []float64{1}},
		},
		[]string{"duration"},
	)
	query.TTL = 60
	queryCount := 0
	collector := newHistogramTestCollector(t, query, func() driver.Rows {
		value := driver.Value("invalid")
		if queryCount > 1 {
			value = float64(0.5)
		}
		return &histogramTestRows{
			columns: []string{"datname", "duration"},
			values:  [][]driver.Value{{"db", value}},
		}
	}, &queryCount)

	firstScrape := time.Now()
	collector.Server.scrapeBegin = firstScrape
	first := make(chan prometheus.Metric, 4)
	collector.Collect(first)
	if queryCount != 1 || len(first) != 0 || collector.Error() == nil {
		t.Fatalf("failed refresh: queries=%d metrics=%d err=%v, want 1, 0, non-nil", queryCount, len(first), collector.Error())
	}

	collector.Server.scrapeBegin = firstScrape.Add(30 * time.Second)
	second := make(chan prometheus.Metric, 4)
	collector.Collect(second)
	if queryCount != 1 {
		t.Fatalf("TTL cache retried failed execution: query count = %d, want 1", queryCount)
	}
	if len(second) != 0 || !collector.CacheHit() {
		t.Fatalf("cached failed snapshot: metrics=%d cacheHit=%v, want 0 and true", len(second), collector.CacheHit())
	}
	if collector.Error() == nil {
		t.Fatal("cached failed snapshot lost the query error before TTL expiry")
	}

	collector.Server.scrapeBegin = firstScrape.Add(61 * time.Second)
	third := make(chan prometheus.Metric, 4)
	collector.Collect(third)
	if queryCount != 2 || len(third) != 4 || collector.CacheHit() || collector.Error() != nil {
		t.Fatalf("post-TTL recovery: queries=%d metrics=%d cacheHit=%v err=%v, want 2, 4, false, nil",
			queryCount, len(third), collector.CacheHit(), collector.Error())
	}
}

func TestHistogramAcceptanceQueryStartErrorIsAtomic(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want string
	}{
		{name: "driver error", err: errors.New("query start failed"), want: "query start failed"},
		{name: "deadline", err: context.DeadlineExceeded, want: "timeout"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			query := histogramTestQuery(
				"test_query_error",
				[]string{"datname"},
				map[string]*Column{
					"current":  {Name: "current", Usage: GAUGE},
					"duration": {Name: "duration", Usage: HISTOGRAM, Bucket: []float64{1}},
				},
				[]string{"current", "duration"},
			)
			query.Timeout = 1
			collector := newHistogramQueryErrorCollector(t, query, tt.err)
			collector.result = []prometheus.Metric{
				prometheus.MustNewConstMetric(collector.descriptors["current"], prometheus.GaugeValue, 99, "db"),
				prometheus.MustNewConstMetric(collector.histogramDesc["duration"].count, prometheus.GaugeValue, 1, "db"),
			}
			collector.scrapeBegin = time.Now()
			collector.execute()
			if err := collector.Error(); err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("query start error = %v, want substring %q", err, tt.want)
			}
			if got := collector.ResultSize(); got != 0 {
				t.Fatalf("query start failure retained/published %d metrics", got)
			}
		})
	}
}

type histogramAcceptanceErrorRows struct {
	columns []string
	values  [][]driver.Value
	index   int
	err     error
}

func (r *histogramAcceptanceErrorRows) Columns() []string { return r.columns }
func (r *histogramAcceptanceErrorRows) Close() error      { return nil }
func (r *histogramAcceptanceErrorRows) Next(dest []driver.Value) error {
	if r.index < len(r.values) {
		copy(dest, r.values[r.index])
		r.index++
		return nil
	}
	if r.err == nil {
		return io.EOF
	}
	return r.err
}

func TestHistogramAcceptanceRowsErrorIsAtomic(t *testing.T) {
	query := histogramTestQuery(
		"test_rows_error",
		[]string{"datname"},
		map[string]*Column{
			"current":  {Name: "current", Usage: GAUGE},
			"duration": {Name: "duration", Usage: HISTOGRAM, Bucket: []float64{1}},
		},
		[]string{"current", "duration"},
	)
	iterationErr := errors.New("driver rows failed")
	collector := newHistogramTestCollector(t, query, func() driver.Rows {
		return &histogramAcceptanceErrorRows{
			columns: []string{"datname", "current", "duration"},
			values:  [][]driver.Value{{"db", int64(1), float64(0.5)}},
			err:     iterationErr,
		}
	}, nil)
	collector.result = []prometheus.Metric{
		prometheus.MustNewConstMetric(collector.descriptors["current"], prometheus.GaugeValue, 99, "db"),
	}
	collector.scrapeBegin = time.Now()
	collector.execute()
	if err := collector.Error(); err == nil || !strings.Contains(err.Error(), iterationErr.Error()) {
		t.Fatalf("rows.Err failure = %v, want wrapped %q", err, iterationErr)
	}
	if got := collector.ResultSize(); got != 0 {
		t.Fatalf("rows.Err published %d partial/stale metrics", got)
	}
}

func TestHistogramAcceptanceResultOrderIsDeterministic(t *testing.T) {
	query := histogramTestQuery(
		"test_order",
		[]string{"left", "right"},
		map[string]*Column{
			"second": {Name: "second", Usage: HISTOGRAM, Bucket: []float64{10}},
			"first":  {Name: "first", Usage: HISTOGRAM, Bucket: []float64{1}},
		},
		[]string{"second", "first"},
	)
	queryCount := 0
	collector := newHistogramTestCollector(t, query, func() driver.Rows {
		rows := [][]driver.Value{
			{"a", "bc", float64(5), float64(0.5)},
			{"ab", "c", float64(20), float64(2)},
		}
		if queryCount == 1 {
			rows[0], rows[1] = rows[1], rows[0]
		}
		return &histogramTestRows{
			columns: []string{"left", "right", "second", "first"},
			values:  rows,
		}
	}, &queryCount)

	collector.scrapeBegin = time.Now()
	collector.execute()
	if err := collector.Error(); err != nil {
		t.Fatalf("first execute: %v", err)
	}
	first := histogramAcceptanceMetricSequence(t, collector.result)

	collector.scrapeBegin = time.Now()
	collector.execute()
	if err := collector.Error(); err != nil {
		t.Fatalf("second execute: %v", err)
	}
	second := histogramAcceptanceMetricSequence(t, collector.result)
	if !reflect.DeepEqual(first, second) {
		t.Fatalf("metric order changed across equivalent row sets:\nfirst:  %v\nsecond: %v", first, second)
	}
}

func histogramAcceptanceMetricSequence(t *testing.T, metrics []prometheus.Metric) []string {
	t.Helper()
	result := make([]string, len(metrics))
	for i, metric := range metrics {
		var dtoMetric dto.Metric
		if err := metric.Write(&dtoMetric); err != nil {
			t.Fatalf("write metric %d: %v", i, err)
		}
		labels := make([]string, len(dtoMetric.GetLabel()))
		for j, pair := range dtoMetric.GetLabel() {
			labels[j] = pair.GetName() + "=" + pair.GetValue()
		}
		result[i] = fmt.Sprintf("%s|%s|%g", metric.Desc(), strings.Join(labels, ","), dtoMetric.GetGauge().GetValue())
	}
	return result
}

func TestHistogramAcceptanceConcurrentCollectIsRaceSafe(t *testing.T) {
	query := histogramTestQuery(
		"test_concurrent",
		[]string{"datname"},
		map[string]*Column{
			"duration": {Name: "duration", Usage: HISTOGRAM, Bucket: []float64{1, 10}},
		},
		[]string{"duration"},
	)
	collector := newHistogramTestCollector(t, query, func() driver.Rows {
		return &histogramTestRows{
			columns: []string{"datname", "duration"},
			values:  [][]driver.Value{{"db", float64(0.5)}, {"db", float64(5)}},
		}
	}, nil)
	collector.Server.DisableCache = true
	collector.Server.scrapeBegin = time.Now()

	const workers = 12
	errorsCh := make(chan error, workers)
	var wg sync.WaitGroup
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			metrics := make(chan prometheus.Metric, 5)
			collector.Collect(metrics)
			if got := len(metrics); got != 5 {
				errorsCh <- fmt.Errorf("concurrent collect emitted %d metrics, want 5", got)
			}
		}()
	}
	wg.Wait()
	close(errorsCh)
	for err := range errorsCh {
		t.Error(err)
	}
}

func TestHistogramAcceptanceReloadDiscardsCachedCollector(t *testing.T) {
	originalExporter := PgExporter
	t.Cleanup(func() { setCurrentExporter(originalExporter) })
	originalConfigPath := *configPath
	t.Cleanup(func() { *configPath = originalConfigPath })

	oldQuery := histogramTestQuery(
		"reload_hist",
		[]string{"datname"},
		map[string]*Column{
			"duration": {Name: "duration", Usage: HISTOGRAM, Bucket: []float64{1}},
		},
		[]string{"duration"},
	)
	server := NewServer("postgresql://u:p@localhost:5432/postgres")
	oldCollector := NewCollector(oldQuery, server)
	oldCollector.result = []prometheus.Metric{
		prometheus.MustNewConstMetric(
			oldCollector.histogramDesc["duration"].count,
			prometheus.GaugeValue,
			1,
			"db",
		),
	}
	server.queries = map[string]*Query{"reload_hist": oldQuery}
	server.Collectors = []*Collector{oldCollector}
	server.Planned = true
	server.ResetStats()

	exporter := &Exporter{
		server:  server,
		servers: map[string]*Server{},
		queries: server.queries,
	}
	setCurrentExporter(exporter)

	dir := t.TempDir()
	path := filepath.Join(dir, "pg_exporter.yml")
	content := []byte(`
reload_hist:
  query: SELECT 'db' AS datname, 0.5 AS duration
  metrics:
    - datname: {usage: LABEL}
    - duration:
        usage: HISTOGRAM
        bucket: [2, 4]
`)
	if err := os.WriteFile(path, content, 0o644); err != nil {
		t.Fatalf("write reload config: %v", err)
	}
	*configPath = path

	if err := Reload(); err != nil {
		t.Fatalf("reload Histogram config: %v", err)
	}
	if server.Planned {
		t.Fatal("reload left server planned")
	}
	if server.Collectors != nil {
		t.Fatal("reload retained old Histogram collector and cached snapshot")
	}

	server.Plan()
	if len(server.Collectors) != 1 {
		t.Fatalf("replanned collectors = %d, want 1", len(server.Collectors))
	}
	newCollector := server.Collectors[0]
	if newCollector == oldCollector {
		t.Fatal("reload reused the old Histogram collector")
	}
	if got := newCollector.ResultSize(); got != 0 {
		t.Fatalf("new Histogram collector inherited %d cached metrics", got)
	}
	if got := newCollector.Columns["duration"].Bucket; !reflect.DeepEqual(got, []float64{2, 4}) {
		t.Fatalf("reloaded buckets = %v, want [2 4]", got)
	}
}
