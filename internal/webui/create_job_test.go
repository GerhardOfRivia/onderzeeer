package webui

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCreateJobIsIdempotentAndRejectsUnsafePaths(t *testing.T) {
	t.Parallel()
	manager, known, _, _ := webTestQueue(t, true)
	handler, err := newHandler(manager, testLogger(), testWebToken, "dev")
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	watch := known.Watches[0].Path
	target := filepath.Join(watch, "report.txt")
	if err := os.WriteFile(target, []byte("data"), 0o600); err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(t.TempDir(), "outside.txt")
	if err := os.WriteFile(outside, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(watch, "escape.txt")
	if err := os.Symlink(outside, link); err != nil {
		t.Fatal(err)
	}
	endpoint := "/api/v1/queues/" + queueID(known.Identity) + "/jobs"
	body := `{"path":` + jsonString(target) + `,"external_id":"batch-42","priority":4,"metadata":{"source":"bezorg"}}`
	created := createdBody(t, server, http.MethodPost, endpoint, body)
	var job jobView
	decodeResponse(t, created.Result(), http.StatusCreated, &job)
	if job.ExternalID != "batch-42" || job.Priority != 4 || job.Path != target || string(job.Metadata) == "" || !strings.Contains(string(job.Metadata), "bezorg") {
		t.Fatalf("created job = %+v", job)
	}

	again := createdBody(t, server, http.MethodPost, endpoint, body)
	var conflict jobConflictResponse
	decodeResponse(t, again.Result(), http.StatusConflict, &conflict)
	if conflict.Error.Code != "external_id_conflict" || conflict.Job.ID != job.ID {
		t.Fatalf("conflict = %+v", conflict)
	}

	for _, raw := range []string{
		`{"path":"relative.csv","external_id":"other"}`,
		`{"path":` + jsonString(outside) + `}`,
		`{"path":` + jsonString(link) + `}`,
		`{"path":` + jsonString(filepath.Join(watch, "missing.csv")) + `}`,
		`{"path":` + jsonString(target) + `,"metadata":[]}`,
	} {
		response := createdBody(t, server, http.MethodPost, endpoint, raw)
		if response.Code != http.StatusBadRequest {
			t.Fatalf("body %s status = %d %s", raw, response.Code, response.Body.String())
		}
	}
}

func jsonString(value string) string {
	return `"` + strings.ReplaceAll(value, `\`, `\\`) + `"`
}
