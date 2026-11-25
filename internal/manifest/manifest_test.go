package manifest

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/GerhardOfRivia/onderzeeer/internal/config"
)

func TestLoadVerifiesFilesAndRejectsEscapes(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	payload := []byte("hello")
	if err := os.WriteFile(filepath.Join(dir, "a.csv"), payload, 0o600); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(payload)
	digest := hex.EncodeToString(sum[:])
	manifestPath := filepath.Join(dir, "drop.bezorg-done.json")
	body := `{
		"version": 1,
		"job_id": "bezorg-1",
		"external_id": " batch-7 ",
		"remote": "/incoming",
		"completed_at": "2026-11-23T12:00:00Z",
		"files": [{"path": "a.csv", "size": 5, "sha256": "` + digest + `"}]
	}`
	if err := os.WriteFile(manifestPath, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	loaded, err := Load(manifestPath, config.MarkerVerifySHA256)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.ExternalID != "batch-7" || len(loaded.Files) != 1 || loaded.Files[0].Path != filepath.Join(dir, "a.csv") {
		t.Fatalf("manifest = %+v", loaded)
	}
	if !strings.Contains(loaded.FilesJSON(), "a.csv") {
		t.Fatalf("files json = %s", loaded.FilesJSON())
	}

	escaped := strings.Replace(body, `"a.csv"`, `"../a.csv"`, 1)
	if err := os.WriteFile(manifestPath, []byte(escaped), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(manifestPath, config.MarkerVerifyExists); err == nil {
		t.Fatal("escaping path was accepted")
	}
	missing := strings.Replace(body, `"a.csv"`, `"missing.csv"`, 1)
	if err := os.WriteFile(manifestPath, []byte(missing), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(manifestPath, config.MarkerVerifyExists); err == nil {
		t.Fatal("missing file was accepted")
	}
}

func TestLoadSizeMismatch(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "a.csv"), []byte("hello"), 0o600); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "done.json")
	body := `{"version":1,"job_id":"j","external_id":"","remote":"/r","completed_at":"2026-11-23T12:00:00Z","files":[{"path":"a.csv","size":1,"sha256":""}]}`
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(path, config.MarkerVerifyExists); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(path, config.MarkerVerifySize); err == nil {
		t.Fatal("size mismatch was accepted")
	}
}
