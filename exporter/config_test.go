package exporter

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestParseConfigUsageCaseInsensitive(t *testing.T) {
	config := `
test_query:
  query: SELECT 1 AS metric, 'db' AS datname
  metrics:
    - metric:
        usage: gauge
        description: metric value
    - datname:
        usage: label
        description: database name
`

	queries, err := ParseConfig([]byte(config))
	if err != nil {
		t.Fatalf("ParseConfig returned error: %v", err)
	}

	query, ok := queries["test_query"]
	if !ok {
		t.Fatalf("query test_query not found")
	}

	if got := query.Columns["metric"].Usage; got != GAUGE {
		t.Fatalf("metric usage = %s, want %s", got, GAUGE)
	}
	if got := query.Columns["datname"].Usage; got != LABEL {
		t.Fatalf("datname usage = %s, want %s", got, LABEL)
	}
}

func TestParseConfigInvalidUsage(t *testing.T) {
	config := `
bad_query:
  query: SELECT 1 AS metric
  metrics:
    - metric:
        usage: bad_usage
        description: metric value
`

	if _, err := ParseConfig([]byte(config)); err == nil {
		t.Fatal("ParseConfig should fail on unsupported usage")
	}
}

func TestParseConfigRejectsMultiColumnMetricsEntry(t *testing.T) {
	config := `
bad_query:
  query: SELECT 1 AS a, 2 AS b
  metrics:
    - a:
        usage: gauge
      b:
        usage: gauge
`
	if _, err := ParseConfig([]byte(config)); err == nil {
		t.Fatal("ParseConfig should fail when one metrics entry defines multiple columns")
	}
}

func TestParseQueryErrors(t *testing.T) {
	if _, err := ParseQuery(`{}`); err == nil {
		t.Fatal("ParseQuery should fail when no query is defined")
	}

	multi := `
q1:
  query: SELECT 1 AS metric
  metrics:
    - metric:
        usage: gauge
q2:
  query: SELECT 2 AS metric
  metrics:
    - metric:
        usage: gauge
`
	if _, err := ParseQuery(multi); err == nil {
		t.Fatal("ParseQuery should fail when multiple queries are defined")
	}
}

func TestLoadConfigDirectoryPriorityAndOverride(t *testing.T) {
	dir := t.TempDir()
	f1 := filepath.Join(dir, "0100-a.yml")
	f2 := filepath.Join(dir, "0200-b.yml")

	cfg1 := `
q_common:
  query: SELECT 1 AS metric
  metrics:
    - metric:
        usage: gauge
`
	cfg2 := `
q_common:
  query: SELECT 2 AS metric
  metrics:
    - metric:
        usage: gauge
q_extra:
  query: SELECT 3 AS metric
  metrics:
    - metric:
        usage: gauge
`
	if err := os.WriteFile(f1, []byte(cfg1), 0o644); err != nil {
		t.Fatalf("write config 1 failed: %v", err)
	}
	if err := os.WriteFile(f2, []byte(cfg2), 0o644); err != nil {
		t.Fatalf("write config 2 failed: %v", err)
	}

	queries, err := LoadConfig(dir)
	if err != nil {
		t.Fatalf("LoadConfig dir failed: %v", err)
	}
	if len(queries) != 2 {
		t.Fatalf("LoadConfig query count = %d, want 2", len(queries))
	}
	if queries["q_common"].SQL != "SELECT 2 AS metric" {
		t.Fatalf("q_common should be overridden by later file, got: %s", queries["q_common"].SQL)
	}
	// 2nd config file gets default priority 102.
	if queries["q_common"].Priority != 102 {
		t.Fatalf("q_common priority = %d, want 102", queries["q_common"].Priority)
	}
	if queries["q_extra"].Priority != 102 {
		t.Fatalf("q_extra priority = %d, want 102", queries["q_extra"].Priority)
	}
}

func TestLoadConfigDirectoryAllInvalidReturnsError(t *testing.T) {
	dir := t.TempDir()
	bad := `
q_bad:
  query: SELECT 1 AS metric
  metrics:
    - metric:
        usage: bad_usage
`
	if err := os.WriteFile(filepath.Join(dir, "0100-bad.yml"), []byte(bad), 0o644); err != nil {
		t.Fatalf("write config failed: %v", err)
	}

	if _, err := LoadConfig(dir); err == nil {
		t.Fatal("LoadConfig should fail when no valid queries are loaded from a config directory")
	} else if !strings.Contains(err.Error(), "unsupported usage: bad_usage") {
		t.Fatalf("LoadConfig error %q does not preserve the first file error", err)
	}
}

func TestLoadConfigDirectorySkipsInvalidFile(t *testing.T) {
	dir := t.TempDir()
	valid := `
q_valid:
  query: SELECT 1 AS metric
  metrics:
    - metric:
        usage: gauge
`
	invalid := `
q_invalid:
  query: SELECT 1 AS metric
  metrics:
    - metric:
        usage: bad_usage
`
	if err := os.WriteFile(filepath.Join(dir, "0100-invalid.yml"), []byte(invalid), 0o644); err != nil {
		t.Fatalf("write invalid config failed: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "0200-valid.yml"), []byte(valid), 0o644); err != nil {
		t.Fatalf("write valid config failed: %v", err)
	}

	queries, err := LoadConfig(dir)
	if err != nil {
		t.Fatalf("LoadConfig should skip the invalid file: %v", err)
	}
	if len(queries) != 1 {
		t.Fatalf("LoadConfig returned %d queries, want only the valid query", len(queries))
	}
	if _, ok := queries["q_valid"]; !ok {
		t.Fatal("LoadConfig did not return q_valid")
	}
	if _, ok := queries["q_invalid"]; ok {
		t.Fatal("LoadConfig returned q_invalid from the skipped file")
	}
	if queries["q_valid"].Priority != 101 {
		t.Fatalf("q_valid priority = %d, want 101 after one valid config", queries["q_valid"].Priority)
	}
}

func TestLoadConfigDirectoryAllowsDocumentationYAML(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "0000-doc.yml"), []byte("# documentation only\n"), 0o644); err != nil {
		t.Fatalf("write documentation config failed: %v", err)
	}
	valid := `
q_valid:
  query: SELECT 1 AS metric
  metrics:
    - metric:
        usage: gauge
`
	if err := os.WriteFile(filepath.Join(dir, "0100-valid.yml"), []byte(valid), 0o644); err != nil {
		t.Fatalf("write valid config failed: %v", err)
	}

	queries, err := LoadConfig(dir)
	if err != nil {
		t.Fatalf("LoadConfig failed with documentation and valid YAML: %v", err)
	}
	if _, ok := queries["q_valid"]; !ok {
		t.Fatal("LoadConfig did not return q_valid")
	}
}

func TestLoadConfigDirectoryNoQueries(t *testing.T) {
	t.Run("empty directory", func(t *testing.T) {
		queries, err := LoadConfig(t.TempDir())
		if err != nil {
			t.Fatalf("LoadConfig failed for an empty directory: %v", err)
		}
		if len(queries) != 0 {
			t.Fatalf("LoadConfig returned %d queries from an empty directory, want 0", len(queries))
		}
	})

	t.Run("only empty YAML", func(t *testing.T) {
		dir := t.TempDir()
		if err := os.WriteFile(filepath.Join(dir, "0000-empty.yml"), nil, 0o644); err != nil {
			t.Fatalf("write empty config failed: %v", err)
		}
		if err := os.WriteFile(filepath.Join(dir, "0001-comment.yaml"), []byte("# documentation only\n"), 0o644); err != nil {
			t.Fatalf("write comment-only config failed: %v", err)
		}

		if queries, err := LoadConfig(dir); err == nil {
			t.Fatalf("LoadConfig returned %d queries from empty YAML files, want error", len(queries))
		}
	})
}

func TestGetConfigPrecedence(t *testing.T) {
	originConfigPath := *configPath
	t.Cleanup(func() { *configPath = originConfigPath })

	originEnv := os.Getenv("PG_EXPORTER_CONFIG")
	t.Cleanup(func() { _ = os.Setenv("PG_EXPORTER_CONFIG", originEnv) })

	*configPath = "/tmp/from-cli.yml"
	_ = os.Setenv("PG_EXPORTER_CONFIG", "/tmp/from-env.yml")
	if got := GetConfig(); got != "/tmp/from-cli.yml" {
		t.Fatalf("GetConfig CLI precedence failed: got %s", got)
	}

	*configPath = ""
	if got := GetConfig(); got != "/tmp/from-env.yml" {
		t.Fatalf("GetConfig env fallback failed: got %s", got)
	}
}
