package config

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestResourceValidation(t *testing.T) {
	t.Parallel()
	for _, test := range []struct{ name, declarations, reference, want string }{
		{"omitted", "", "", ""},
		{"empty list", "resources: []", "", ""},
		{"valid", "resources: [gpu]", "resources: gpu", ""},
		{"literal", `resources: ['{{file}}']`, `resources: '{{file}}'`, ""},
		{"case sensitive", "resources: [gpu]", "resources: GPU", `resource "GPU" is not declared`},
		{"undeclared", "", "resources: gpu", `resource "gpu" is not declared`},
		{"unused", "resources: [gpu]", "", `resource "gpu" is declared but unused`},
		{"duplicate", "resources: [gpu, gpu]", "resources: gpu", `resource "gpu" is declared more than once`},
		{"empty declaration", `resources: ['']`, "", `resource "" must be nonempty`},
		{"space declaration", `resources: [' gpu']`, "", `resource " gpu"`},
		{"space reference", "resources: [gpu]", `resources: 'gpu '`, `resource "gpu "`},
		{"empty reference", "", `resources: ''`, `resource "" must be nonempty`},
		{"null reference", "", "resources: null", "single nonempty literal string"},
		{"list reference", "resources: [gpu]", "resources: [gpu]", "single nonempty literal string"},
		{"map reference", "resources: [gpu]", "resources: {name: gpu}", "single nonempty literal string"},
		{"number reference", "", "resources: 12", `resource "12" must be a single`},
		{"bool reference", "", "resources: true", `resource "true" must be a single`},
		{"null list", "resources: null", "", "must be a list"},
		{"scalar list", "resources: gpu", "", "must be a list"},
		{"mapping list", "resources: {gpu: 1}", "", "must be a list"},
		{"number name", "resources: [12]", "", `resource "12" must be a single`},
		{"bool name", "resources: [false]", "", `resource "false" must be a single`},
		{"null name", "resources: [null]", "", "single nonempty literal string"},
		{"nested name", "resources: [[gpu]]", "", "single nonempty literal string"},
		{"quoted number", `resources: ['12']`, `resources: '12'`, ""},
		{"alias", "resources: [&gpu gpu]", "resources: *gpu", ""},
		{"merge null", "", "<<: {resources: null}", "single nonempty literal string"},
	} {
		t.Run(test.name, func(t *testing.T) {
			filename := filepath.Join(t.TempDir(), "config.yaml")
			source := fmt.Sprintf("%s\nwatches:\n  - name: incoming\n    path: .\n    pipeline:\n      - name: test\n        program: unused\n        %s\n", test.declarations, test.reference)
			if err := os.WriteFile(filename, []byte(source), 0600); err != nil {
				t.Fatal(err)
			}
			for _, load := range []func(string) (*Config, error){Load, LoadManaged} {
				_, err := load(filename)
				if test.want == "" {
					if err != nil {
						t.Fatal(err)
					}
					continue
				}
				if err == nil || !strings.Contains(err.Error(), test.want) {
					t.Fatalf("got %v, want %q", err, test.want)
				}
				if test.reference != "" && !strings.Contains(test.name, "declaration") && !strings.Contains(test.name, "duplicate") {
					for _, want := range []string{`watch "incoming"`, `step 1 ("test")`} {
						if !strings.Contains(err.Error(), want) {
							t.Errorf("missing %s in %v", want, err)
						}
					}
				}
			}
		})
	}
}

func TestResourcesInValidationAndSnapshots(t *testing.T) {
	cfg := fingerprintTestConfig(nil, nil)
	baseline, _ := EffectiveFingerprint(cfg)
	cfg.Resources = []string{"gpu", "GPU"}
	cfg.Watches[0].Pipeline[0].Resources = "gpu"
	if err := cfg.Validate(); err == nil || !strings.Contains(err.Error(), `resource "GPU" is declared but unused`) {
		t.Fatalf("Validate = %v", err)
	}
	cfg.Watches[0].Pipeline = append(cfg.Watches[0].Pipeline, CommandConfig{Name: "second", Program: "unused", Executor: ExecutorCommand, Resources: "GPU"})
	if err := cfg.Validate(); err != nil {
		t.Fatal(err)
	}
	data, err := json.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	var restored Config
	if err := json.Unmarshal(data, &restored); err != nil {
		t.Fatal(err)
	}
	if err := restored.Validate(); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(restored.Resources, cfg.Resources) || restored.Watches[0].Pipeline[0].Resources != "gpu" {
		t.Fatalf("lost resources: %+v", restored)
	}
	originalHash, _ := EffectiveFingerprint(cfg)
	restoredHash, _ := EffectiveFingerprint(&restored)
	if originalHash == baseline || originalHash != restoredHash {
		t.Fatal("resource fingerprint not preserved")
	}
	restored.Watches[0].Pipeline[0].Resources = "GPU"
	changed, _ := EffectiveFingerprint(&restored)
	if changed == originalHash {
		t.Fatal("step resource missing from fingerprint")
	}
	restored.Resources = append(restored.Resources, "other")
	declarationHash, _ := EffectiveFingerprint(&restored)
	if changed == declarationHash {
		t.Fatal("declarations missing from fingerprint")
	}
}
