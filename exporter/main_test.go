package exporter

import (
	"os"
	"strings"
	"testing"
)

func TestClearLibPQEnvironment(t *testing.T) {
	cleared := []string{
		"PGSYSCONFDIR",
		"PGLOCALEDIR",
		"PGREALM",
		"PGSERVICEFILE",
		"PGSERVICE",
	}
	for _, name := range cleared {
		t.Setenv(name, "test-value")
	}
	t.Setenv("PGHOST", "preserved-host")

	clearLibPQEnvironment()

	for _, name := range cleared {
		if value, exists := os.LookupEnv(name); exists {
			t.Errorf("%s remains set to %q", name, value)
		}
	}
	if value := os.Getenv("PGHOST"); value != "preserved-host" {
		t.Errorf("PGHOST = %q, want %q", value, "preserved-host")
	}
}

func TestValidateTelemetryPath(t *testing.T) {
	tests := []struct {
		name    string
		path    string
		wantErr string
	}{
		{name: "default", path: "/metrics"},
		{name: "empty", path: "", wantErr: "must not be empty"},
		{name: "missing leading slash", path: "metrics", wantErr: "must start with '/'"},
		{name: "reserved endpoint", path: "/up", wantErr: "invalid or conflicting"},
		{name: "escaped reserved endpoint", path: "/%75p", wantErr: "invalid or conflicting"},
		{name: "root endpoint", path: "/", wantErr: "invalid or conflicting"},
		{name: "wildcard", path: "/{x...}", wantErr: "literal URL path"},
		{name: "end wildcard", path: "/{$}", wantErr: "literal URL path"},
		{name: "nested wildcard", path: "/metrics/{id}", wantErr: "literal URL path"},
		{name: "malformed escape", path: "/metrics/%zz", wantErr: "valid URL path"},
		{name: "query is not a path", path: "/metrics?format=text", wantErr: "valid URL path"},
		{name: "fragment is not a path", path: "/metrics#internal", wantErr: "valid URL path"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateTelemetryPath(tt.path)
			if tt.wantErr == "" {
				if err != nil {
					t.Fatalf("validateTelemetryPath(%q) returned unexpected error: %v", tt.path, err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("validateTelemetryPath(%q) error = %v, want substring %q", tt.path, err, tt.wantErr)
			}
		})
	}
}
