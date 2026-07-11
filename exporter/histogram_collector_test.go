package exporter

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"io"
	"math"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"
)

type histogramTestConnector struct {
	rowsFactory func() driver.Rows
	queryCount  *int
	queryErr    error
}

func (c histogramTestConnector) Connect(context.Context) (driver.Conn, error) {
	return &histogramTestConn{rowsFactory: c.rowsFactory, queryCount: c.queryCount, queryErr: c.queryErr}, nil
}

func (c histogramTestConnector) Driver() driver.Driver { return histogramTestDriver{} }

type histogramTestDriver struct{}

func (histogramTestDriver) Open(string) (driver.Conn, error) {
	return nil, errors.New("histogram test driver must be opened through its connector")
}

type histogramTestConn struct {
	rowsFactory func() driver.Rows
	queryCount  *int
	queryErr    error
}

func (c *histogramTestConn) Prepare(string) (driver.Stmt, error) {
	return nil, errors.New("prepare not supported")
}
func (c *histogramTestConn) Close() error { return nil }
func (c *histogramTestConn) Begin() (driver.Tx, error) {
	return nil, errors.New("transactions not supported")
}
func (c *histogramTestConn) QueryContext(context.Context, string, []driver.NamedValue) (driver.Rows, error) {
	if c.queryCount != nil {
		*c.queryCount++
	}
	if c.queryErr != nil {
		return nil, c.queryErr
	}
	return c.rowsFactory(), nil
}

type histogramTestRows struct {
	columns []string
	values  [][]driver.Value
	index   int
}

func (r *histogramTestRows) Columns() []string { return r.columns }
func (r *histogramTestRows) Close() error      { return nil }
func (r *histogramTestRows) Next(dest []driver.Value) error {
	if r.index >= len(r.values) {
		return io.EOF
	}
	copy(dest, r.values[r.index])
	r.index++
	return nil
}

func histogramTestQuery(name string, labels []string, metrics map[string]*Column, metricNames []string) *Query {
	columns := make(map[string]*Column, len(labels)+len(metrics))
	columnNames := make([]string, 0, len(labels)+len(metrics))
	for _, label := range labels {
		columns[label] = &Column{Name: label, Usage: LABEL}
		columnNames = append(columnNames, label)
	}
	for _, metricName := range metricNames {
		column := metrics[metricName]
		columns[metricName] = column
		columnNames = append(columnNames, metricName)
		if err := column.parseNumbers(); err != nil {
			panic(err)
		}
	}
	return &Query{
		Name:        name,
		Branch:      name,
		SQL:         "SELECT histogram test data",
		Columns:     columns,
		ColumnNames: columnNames,
		LabelNames:  append([]string(nil), labels...),
		MetricNames: append([]string(nil), metricNames...),
	}
}

func newHistogramTestCollector(t *testing.T, query *Query, factory func() driver.Rows, queryCount *int) *Collector {
	t.Helper()
	db := sql.OpenDB(histogramTestConnector{rowsFactory: factory, queryCount: queryCount})
	t.Cleanup(func() { _ = db.Close() })
	server := &Server{
		DB:       db,
		Database: "postgres",
		labels:   prometheus.Labels{"cluster": "c1"},
	}
	return NewCollector(query, server)
}

func newHistogramQueryErrorCollector(t *testing.T, query *Query, queryErr error) *Collector {
	t.Helper()
	db := sql.OpenDB(histogramTestConnector{queryErr: queryErr})
	t.Cleanup(func() { _ = db.Close() })
	server := &Server{
		DB:       db,
		Database: "postgres",
		labels:   prometheus.Labels{"cluster": "c1"},
	}
	return NewCollector(query, server)
}

func gatherHistogramSamples(t *testing.T, collector *Collector) (map[string]float64, map[string]*dto.MetricFamily) {
	t.Helper()
	collector.TTL = 3600
	collector.Server.scrapeBegin = time.Now()
	collector.lastScrape = collector.Server.scrapeBegin

	registry := prometheus.NewRegistry()
	if err := registry.Register(collector); err != nil {
		t.Fatalf("register collector: %v", err)
	}
	families, err := registry.Gather()
	if err != nil {
		t.Fatalf("gather collector: %v", err)
	}

	samples := make(map[string]float64)
	familyMap := make(map[string]*dto.MetricFamily, len(families))
	for _, family := range families {
		familyMap[family.GetName()] = family
		if family.GetType() != dto.MetricType_GAUGE {
			t.Fatalf("family %s type = %s, want GAUGE", family.GetName(), family.GetType())
		}
		for _, metric := range family.GetMetric() {
			labels := make(map[string]string, len(metric.GetLabel()))
			for _, pair := range metric.GetLabel() {
				labels[pair.GetName()] = pair.GetValue()
			}
			samples[histogramSampleKey(family.GetName(), labels)] = metric.GetGauge().GetValue()
		}
	}
	return samples, familyMap
}

func histogramSampleKey(name string, labels map[string]string) string {
	keys := make([]string, 0, len(labels))
	for key := range labels {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	var b strings.Builder
	b.WriteString(name)
	for _, key := range keys {
		b.WriteByte('|')
		b.WriteString(key)
		b.WriteByte('=')
		b.WriteString(labels[key])
	}
	return b.String()
}

func requireHistogramSample(t *testing.T, samples map[string]float64, name string, labels map[string]string, want float64) {
	t.Helper()
	key := histogramSampleKey(name, labels)
	got, found := samples[key]
	if !found {
		t.Fatalf("missing sample %s; got %v", key, samples)
	}
	if math.Abs(got-want) > 1e-12 {
		t.Fatalf("sample %s = %v, want %v", key, got, want)
	}
}

func TestHistogramCollectorSnapshotAggregation(t *testing.T) {
	query := histogramTestQuery(
		"test_hist",
		[]string{"left", "right"},
		map[string]*Column{
			"duration": {
				Name:   "duration",
				Usage:  HISTOGRAM,
				Rename: "latency_seconds",
				Bucket: []float64{0.001, 1, 10},
				Scale:  "2",
				Desc:   "Current duration",
			},
		},
		[]string{"duration"},
	)
	values := [][]driver.Value{
		{"a", "bc", float64(0.0005)}, // scaled to exact 0.001 boundary
		{"a", "bc", float64(0.5)},    // scaled to exact 1 boundary
		{"a", "bc", float64(2)},
		{"a", "bc", float64(20)},
		{"a", "bc", nil},           // NULL without default is absent
		{"ab", "c", float64(0.25)}, // collision check versus ["a", "bc"]
	}
	collector := newHistogramTestCollector(t, query, func() driver.Rows {
		return &histogramTestRows{columns: []string{"left", "right", "duration"}, values: values}
	}, nil)
	collector.scrapeBegin = time.Now()
	collector.execute()
	if err := collector.Error(); err != nil {
		t.Fatalf("execute histogram query: %v", err)
	}
	// Three finite buckets + +Inf + count + sum for each of two groups.
	if got := collector.ResultSize(); got != 12 {
		t.Fatalf("result size = %d, want 12", got)
	}

	// Within every group, runtime output is finite buckets, +Inf, count, sum.
	for i, suffix := range []string{"_bucket", "_bucket", "_bucket", "_bucket", "_count", "_sum"} {
		if got := collector.result[i].Desc().String(); !strings.Contains(got, `fqName: "test_hist_latency_seconds`+suffix+`"`) {
			t.Fatalf("metric %d descriptor = %s, want suffix %s", i, got, suffix)
		}
	}

	samples, families := gatherHistogramSamples(t, collector)
	if _, found := families["test_hist_latency_seconds"]; found {
		t.Fatal("histogram collector emitted an unwanted bare base metric")
	}
	if got := families["test_hist_latency_seconds_bucket"].GetHelp(); got != "Current duration (cumulative bucket)" {
		t.Fatalf("bucket HELP = %q", got)
	}
	if got := families["test_hist_latency_seconds_count"].GetHelp(); got != "Current duration (observation count)" {
		t.Fatalf("count HELP = %q", got)
	}
	if got := families["test_hist_latency_seconds_sum"].GetHelp(); got != "Current duration (observation sum)" {
		t.Fatalf("sum HELP = %q", got)
	}

	base := map[string]string{"cluster": "c1", "left": "a", "right": "bc"}
	for le, want := range map[string]float64{"0.001": 1, "1": 2, "10": 3, "+Inf": 4} {
		labels := map[string]string{"cluster": base["cluster"], "left": base["left"], "right": base["right"], "le": le}
		requireHistogramSample(t, samples, "test_hist_latency_seconds_bucket", labels, want)
	}
	requireHistogramSample(t, samples, "test_hist_latency_seconds_count", base, 4)
	requireHistogramSample(t, samples, "test_hist_latency_seconds_sum", base, 45.001)

	second := map[string]string{"cluster": "c1", "left": "ab", "right": "c"}
	requireHistogramSample(t, samples, "test_hist_latency_seconds_count", second, 1)
	requireHistogramSample(t, samples, "test_hist_latency_seconds_sum", second, 0.5)
}

func TestHistogramCollectorDefaultThenScale(t *testing.T) {
	query := histogramTestQuery(
		"test_default",
		[]string{"datname"},
		map[string]*Column{
			"duration": {
				Name:    "duration",
				Usage:   HISTOGRAM,
				Bucket:  []float64{2, 6},
				Scale:   "2",
				Default: "3",
			},
		},
		[]string{"duration"},
	)
	collector := newHistogramTestCollector(t, query, func() driver.Rows {
		return &histogramTestRows{
			columns: []string{"datname", "duration"},
			values:  [][]driver.Value{{"db", nil}, {"db", "1"}},
		}
	}, nil)
	collector.scrapeBegin = time.Now()
	collector.execute()
	if err := collector.Error(); err != nil {
		t.Fatalf("execute histogram query: %v", err)
	}
	samples, _ := gatherHistogramSamples(t, collector)
	base := map[string]string{"cluster": "c1", "datname": "db"}
	requireHistogramSample(t, samples, "test_default_duration_bucket", map[string]string{"cluster": "c1", "datname": "db", "le": "2"}, 1)
	requireHistogramSample(t, samples, "test_default_duration_bucket", map[string]string{"cluster": "c1", "datname": "db", "le": "6"}, 2)
	requireHistogramSample(t, samples, "test_default_duration_count", base, 2)
	requireHistogramSample(t, samples, "test_default_duration_sum", base, 8)
}

func TestHistogramCollectorInvalidObservationIsAtomic(t *testing.T) {
	for _, invalid := range []driver.Value{"bad", math.NaN(), math.Inf(1), math.Inf(-1)} {
		t.Run(strings.ReplaceAll(strings.TrimSpace(strings.ReplaceAll(toTestString(invalid), "/", "_")), " ", "_"), func(t *testing.T) {
			query := histogramTestQuery(
				"test_atomic",
				[]string{"datname"},
				map[string]*Column{
					"current":  {Name: "current", Usage: GAUGE},
					"duration": {Name: "duration", Usage: HISTOGRAM, Bucket: []float64{1}},
				},
				[]string{"current", "duration"},
			)
			collector := newHistogramTestCollector(t, query, func() driver.Rows {
				return &histogramTestRows{
					columns: []string{"datname", "current", "duration"},
					values:  [][]driver.Value{{"db", int64(1), float64(0.5)}, {"db", int64(2), invalid}},
				}
			}, nil)
			collector.result = []prometheus.Metric{
				prometheus.MustNewConstMetric(collector.descriptors["current"], prometheus.GaugeValue, 99, "db"),
			}
			collector.scrapeBegin = time.Now()
			collector.execute()
			if collector.Error() == nil {
				t.Fatalf("invalid observation %v did not fail query", invalid)
			}
			if got := collector.ResultSize(); got != 0 {
				t.Fatalf("invalid observation published %d partial/stale metrics", got)
			}
		})
	}
}

func toTestString(value driver.Value) string {
	if f, ok := value.(float64); ok {
		switch {
		case math.IsNaN(f):
			return "nan"
		case math.IsInf(f, 1):
			return "pos_inf"
		case math.IsInf(f, -1):
			return "neg_inf"
		}
	}
	return "invalid_string"
}

func TestHistogramCollectorMissingColumnIsError(t *testing.T) {
	query := histogramTestQuery(
		"test_missing",
		[]string{"datname"},
		map[string]*Column{"duration": {Name: "duration", Usage: HISTOGRAM, Bucket: []float64{1}}},
		[]string{"duration"},
	)
	collector := newHistogramTestCollector(t, query, func() driver.Rows {
		return &histogramTestRows{columns: []string{"datname"}, values: [][]driver.Value{{"db"}}}
	}, nil)
	collector.scrapeBegin = time.Now()
	collector.execute()
	if err := collector.Error(); err == nil || !strings.Contains(err.Error(), "missing histogram column") {
		t.Fatalf("missing histogram column error = %v", err)
	}
	if got := collector.ResultSize(); got != 0 {
		t.Fatalf("missing histogram column published %d metrics", got)
	}
}

func TestHistogramCollectorDoesNotAccumulateAcrossExecutionsOrCacheHits(t *testing.T) {
	query := histogramTestQuery(
		"test_refresh",
		[]string{"datname"},
		map[string]*Column{"duration": {Name: "duration", Usage: HISTOGRAM, Bucket: []float64{1}}},
		[]string{"duration"},
	)
	queryCount := 0
	collector := newHistogramTestCollector(t, query, func() driver.Rows {
		var values [][]driver.Value
		switch queryCount {
		case 1:
			values = [][]driver.Value{{"db1", float64(0.5)}, {"db1", float64(2)}, {"db2", float64(0.5)}}
		case 2:
			values = [][]driver.Value{{"db1", float64(2)}}
		}
		return &histogramTestRows{
			columns: []string{"datname", "duration"},
			values:  values,
		}
	}, &queryCount)
	collector.Server.DisableCache = true

	firstScrape := time.Now()
	collector.Server.scrapeBegin = firstScrape
	first := make(chan prometheus.Metric, 8)
	collector.Collect(first)
	if queryCount != 1 || len(first) != 8 {
		t.Fatalf("first collect queries=%d metrics=%d, want 1 and 8", queryCount, len(first))
	}

	collector.Server.scrapeBegin = firstScrape.Add(time.Second)
	second := make(chan prometheus.Metric, 8)
	collector.Collect(second)
	if queryCount != 2 || len(second) != 4 {
		t.Fatalf("second collect queries=%d metrics=%d, want 2 and 4", queryCount, len(second))
	}
	secondMetrics := make([]prometheus.Metric, 0, len(second))
	for len(second) > 0 {
		secondMetrics = append(secondMetrics, <-second)
	}
	var firstFiniteBucket dto.Metric
	if err := secondMetrics[0].Write(&firstFiniteBucket); err != nil {
		t.Fatalf("write finite bucket: %v", err)
	}
	if got := firstFiniteBucket.GetGauge().GetValue(); got != 0 {
		t.Fatalf("refreshed finite bucket = %v, want 0 (no cross-execution accumulation)", got)
	}
	secondSequence := histogramAcceptanceMetricSequence(t, secondMetrics)
	for _, sample := range secondSequence {
		if strings.Contains(sample, "datname=db2") {
			t.Fatalf("refreshed snapshot retained disappeared label tuple: %s", sample)
		}
	}

	collector.Server.DisableCache = false
	collector.TTL = 3600
	collector.Server.scrapeBegin = collector.lastScrape.Add(time.Second)
	cached := make(chan prometheus.Metric, 8)
	collector.Collect(cached)
	if queryCount != 2 || len(cached) != 4 {
		t.Fatalf("cache hit queries=%d metrics=%d, want 2 and 4", queryCount, len(cached))
	}
	cachedMetrics := make([]prometheus.Metric, 0, len(cached))
	for len(cached) > 0 {
		cachedMetrics = append(cachedMetrics, <-cached)
	}
	if cachedSequence := histogramAcceptanceMetricSequence(t, cachedMetrics); !reflect.DeepEqual(cachedSequence, secondSequence) {
		t.Fatalf("cache hit changed snapshot:\nreal:   %v\ncached: %v", secondSequence, cachedSequence)
	}

	collector.Server.DisableCache = true
	collector.Server.scrapeBegin = collector.lastScrape.Add(2 * time.Second)
	empty := make(chan prometheus.Metric, 8)
	collector.Collect(empty)
	if queryCount != 3 || len(empty) != 0 || collector.ResultSize() != 0 {
		t.Fatalf("empty refresh queries=%d metrics=%d result=%d, want 3, 0, 0", queryCount, len(empty), collector.ResultSize())
	}
}
