package config

import (
	"strings"
	"testing"
)

func TestMarkerTriggerValidation(t *testing.T) {
	t.Parallel()
	base := `
watches:
  - name: incoming
    path: /data/incoming
    pipeline:
      - name: process
        program: /bin/true
`
	if _, err := Decode([]byte(base)); err != nil {
		t.Fatal(err)
	}
	if _, err := Decode([]byte(base + "    trigger: marker\n")); err == nil || !strings.Contains(err.Error(), "marker is required") {
		t.Fatalf("missing marker: %v", err)
	}
	if _, err := Decode([]byte(base + "    marker: '*.json'\n")); err == nil || !strings.Contains(err.Error(), "require trigger") {
		t.Fatalf("marker without trigger: %v", err)
	}
	cfg, err := Decode([]byte(base + `
    trigger: marker
    marker: "*.bezorg-done.json"
    marker_format: bezorg
    marker_verify: sha256
`))
	if err != nil {
		t.Fatal(err)
	}
	watch := cfg.Watches[0]
	if !watch.TriggersOnMarker() || watch.MarkerFormat != MarkerBezorg || watch.MarkerVerify != MarkerVerifySHA256 {
		t.Fatalf("watch = %+v", watch)
	}
	empty, err := Decode([]byte(base + "    trigger: marker\n    marker: _SUCCESS\n"))
	if err != nil {
		t.Fatal(err)
	}
	if empty.Watches[0].MarkerFormat != MarkerEmpty || empty.Watches[0].MarkerVerify != MarkerVerifyExists {
		t.Fatalf("defaults = %+v", empty.Watches[0])
	}
}
