package webui

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/GerhardOfRivia/onderzeeer/internal/commandparse"
	"github.com/GerhardOfRivia/onderzeeer/internal/control"
)

func pipelineRequest(t *testing.T, handler http.Handler, route string, body any, token bool, protection bool) *httptest.ResponseRecorder {
	t.Helper()
	encoded, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest("POST", "http://localhost/api/v1/pipelines/"+route, bytes.NewReader(encoded))
	if token {
		request.Header.Set("Authorization", "Bearer "+testWebToken)
	}
	if protection {
		request.Header.Set("X-onderzeeer-Web", "1")
	}
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	return response
}
func pipelineHandler(t *testing.T, public bool) http.Handler {
	t.Helper()
	manager, err := control.NewManager(control.Options{Logger: testLogger()})
	if err != nil {
		t.Fatal(err)
	}
	handler, err := newHandlerForListener(manager, testLogger(), testWebToken, "test", false, public)
	if err != nil {
		t.Fatal(err)
	}
	return handler
}
func TestPipelineValidation(t *testing.T) {
	handler := pipelineHandler(t, true)
	valid := "watches:\n  - name: input\n    path: /does/not/exist\n    pipeline:\n      - name: echo\n        program: /does/not/exist\n        args: ['{{file}}']\n"
	for _, test := range []struct {
		name, yaml, kind string
		valid            bool
	}{
		{"valid", valid, "", true},
		{"syntax", "watches: [", "syntax", false},
		{"semantic", strings.Replace(valid, "        program: /does/not/exist\n", "", 1), "configuration", false},
		{"unknown", valid + "unexpected: true\n", "configuration", false},
		{"empty", "", "configuration", false},
		{"multi", valid + "---\nwatches: []\n", "syntax", false},
	} {
		t.Run(test.name, func(t *testing.T) {
			response := pipelineRequest(t, handler, "validate", map[string]string{"yaml": test.yaml}, true, true)
			if response.Code != 200 {
				t.Fatal(response.Body.String())
			}
			var result pipelineValidation
			if err := json.Unmarshal(response.Body.Bytes(), &result); err != nil {
				t.Fatal(err)
			}
			if result.Valid != test.valid {
				t.Fatalf("%+v", result)
			}
			if !test.valid && (len(result.Diagnostics) != 1 || result.Diagnostics[0].Kind != test.kind) {
				t.Fatalf("%+v", result)
			}
			if test.name == "semantic" && (result.Diagnostics[0].Path != "watches[0].pipeline[0].program" || result.Diagnostics[0].Line < 5) {
				t.Fatalf("missing location: %+v", result)
			}
		})
	}
}
func TestPipelineParseMatchesSharedParser(t *testing.T) {
	handler := pipelineHandler(t, false)
	for _, command := range []string{`echo 'hello world' '{{file}}'`, `docker run --rm -v /data:/input:ro python:3.12 python /app/process.py /input/image.tif`, `podman run --env-file vars alpine echo hi`, `apptainer exec image.sif echo hi`} {
		args, err := commandparse.Tokenize(command)
		if err != nil {
			t.Fatal(err)
		}
		want, warnings, err := commandparse.Parse(args, "test")
		if err != nil {
			t.Fatal(err)
		}
		response := pipelineRequest(t, handler, "parse", map[string]string{"command": command, "name": "test"}, true, true)
		var result struct {
			YAML     string   `json:"yaml"`
			Warnings []string `json:"warnings"`
		}
		if response.Code != 200 {
			t.Fatal(response.Body.String())
		}
		if err := json.Unmarshal(response.Body.Bytes(), &result); err != nil {
			t.Fatal(err)
		}
		if result.YAML != want || len(result.Warnings) != len(warnings) {
			t.Fatalf("parser mismatch: %+v", result)
		}
	}
}
func TestPipelineAuthAndRequestProtection(t *testing.T) {
	for _, public := range []bool{false, true} {
		handler := pipelineHandler(t, public)
		for _, route := range []string{"validate", "parse"} {
			if response := pipelineRequest(t, handler, route, map[string]string{}, false, true); response.Code != 401 {
				t.Fatalf("anonymous %s: %d", route, response.Code)
			}
			if response := pipelineRequest(t, handler, route, map[string]string{}, true, false); response.Code != 400 {
				t.Fatalf("unprotected %s: %d", route, response.Code)
			}
			if response := pipelineRequest(t, handler, route, map[string]string{"unknown": "x"}, true, true); response.Code != 400 {
				t.Fatalf("unknown field %s: %d", route, response.Code)
			}
		}
	}
}
func TestPipelineEndpointsNeverExecute(t *testing.T) {
	handler := pipelineHandler(t, true)
	marker := filepath.Join(t.TempDir(), "executed")
	response := pipelineRequest(t, handler, "parse", map[string]string{"command": "touch '" + marker + "'"}, true, true)
	if response.Code != 200 {
		t.Fatal(response.Body.String())
	}
	document := "watches:\n- name: never\n  path: /missing\n  pipeline:\n  - name: never\n    executor: shell\n    command: touch " + marker + "\n"
	response = pipelineRequest(t, handler, "validate", map[string]string{"yaml": document}, true, true)
	if response.Code != 200 || !strings.Contains(response.Body.String(), `"valid":true`) {
		t.Fatal(response.Body.String())
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatal("endpoint executed a command")
	}
	response = pipelineRequest(t, handler, "parse", map[string]string{"command": "echo x; touch " + marker}, true, true)
	if response.Code != 400 {
		t.Fatal("accepted shell operator")
	}
}

func TestPipelineRequestLimitsAndOrigins(t *testing.T) {
	handler := pipelineHandler(t, true)
	for _, test := range []struct{ name, body, origin, site string }{
		{"oversized", `{"yaml":"` + strings.Repeat("x", maxPipelineBody) + `"}`, "", ""},
		{"trailing", `{"yaml":""} {}`, "", ""},
		{"cross-origin", `{"yaml":""}`, "http://other.example", ""},
		{"cross-site", `{"yaml":""}`, "", "cross-site"},
	} {
		t.Run(test.name, func(t *testing.T) {
			request := httptest.NewRequest("POST", "http://localhost/api/v1/pipelines/validate", strings.NewReader(test.body))
			request.Header.Set("Authorization", "Bearer "+testWebToken)
			request.Header.Set("Content-Type", "application/json")
			request.Header.Set("X-onderzeeer-Web", "1")
			if test.origin != "" {
				request.Header.Set("Origin", test.origin)
			}
			if test.site != "" {
				request.Header.Set("Sec-Fetch-Site", test.site)
			}
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, request)
			if response.Code != 400 {
				t.Fatalf("status %d", response.Code)
			}
		})
	}
}
