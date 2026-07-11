package exporter

import (
	"os"
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
