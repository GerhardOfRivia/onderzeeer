package config

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"
)

func TestDatabaseMaintenanceConfiguration(t *testing.T) {
	t.Parallel()
	for _, managed := range []bool{false, true} {
		t.Run(fmt.Sprint(managed), func(t *testing.T) {
			path := writeConfig(t, `database:
  output_retention: 720h
  retention_include_failed: true
  warn_size_bytes: 123456
  warn_free_percent: 5.5
watches:
  - name: input
    path: ./input
    pipeline: [{name: step, program: unused}]
`)
			load := Load
			if managed {
				load = LoadManaged
			}
			cfg, err := load(path)
			if err != nil {
				t.Fatal(err)
			}
			encoded, err := json.Marshal(cfg)
			if err != nil {
				t.Fatal(err)
			}
			var restored Config
			if err := json.Unmarshal(encoded, &restored); err != nil {
				t.Fatal(err)
			}
			size, free := restored.Database.WarningThresholds()
			if restored.Database.RetentionAge() != 720*time.Hour || !restored.Database.RetentionIncludeFailed || size != 123456 || free != 5.5 {
				t.Fatalf("snapshot lost settings: %+v", restored.Database)
			}
		})
	}
	var defaults DatabaseConfig
	size, free := defaults.WarningThresholds()
	if defaults.RetentionAge() != 0 || defaults.RetentionIncludeFailed || size != 10<<30 || free != 10 {
		t.Fatalf("defaults: %+v, %d, %f", defaults, size, free)
	}
	var zeroSize int64
	var zeroFree float64
	disabled := DatabaseConfig{WarnSizeBytes: &zeroSize, WarnFreePercent: &zeroFree}
	size, free = disabled.WarningThresholds()
	if size != 0 || free != 0 {
		t.Fatal("explicit zero did not disable warnings")
	}
}

func TestRejectInvalidDatabaseMaintenanceConfiguration(t *testing.T) {
	t.Parallel()
	for _, field := range []string{"output_retention: -1h", "warn_size_bytes: -1", "warn_free_percent: -1", "warn_free_percent: 101", "warn_free_percent: .nan", "warn_free_percent: .inf"} {
		t.Run(field, func(t *testing.T) {
			path := writeConfig(t, "database: {"+field+"}\nwatches: [{name: input, path: ./input, pipeline: [{name: step, program: unused}]}]\n")
			_, err := Load(path)
			if err == nil || !strings.Contains(err.Error(), "database.") {
				t.Fatalf("accepted invalid %s: %v", field, err)
			}
		})
	}
}
