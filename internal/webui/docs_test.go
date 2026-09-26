package webui

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"

	"github.com/GerhardOfRivia/onderzeeer/internal/control"
	"github.com/santhosh-tekuri/jsonschema/v6"
)

type documentedResponse struct {
	Ref     string `json:"$ref"`
	Content map[string]struct {
		Example json.RawMessage `json:"example"`
		Schema  struct {
			Ref string `json:"$ref"`
		} `json:"schema"`
	} `json:"content"`
}

type documentedOperation struct {
	OperationID string                        `json:"operationId"`
	Security    []map[string][]string         `json:"security"`
	Responses   map[string]documentedResponse `json:"responses"`
}

type documentedAPI struct {
	OpenAPI string `json:"openapi"`
	Info    struct {
		Version string `json:"version"`
	} `json:"info"`
	Security   []map[string][]string                     `json:"security"`
	Paths      map[string]map[string]documentedOperation `json:"paths"`
	Components struct {
		Responses map[string]documentedResponse `json:"responses"`
		Schemas   map[string]struct {
			Example json.RawMessage `json:"example"`
		} `json:"schemas"`
	} `json:"components"`
}

func TestDocsRoutesAndAssets(t *testing.T) {
	t.Parallel()
	manager, err := control.NewManager(control.Options{Logger: testLogger()})
	if err != nil {
		t.Fatal(err)
	}
	handler, err := newHandler(manager, testLogger(), testWebToken, "docs-test")
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		method, target, contentType string
		status                      int
	}{
		{http.MethodGet, "http://localhost/docs", "text/html", http.StatusPermanentRedirect},
		{http.MethodGet, "http://localhost/docs/", "text/html", http.StatusOK},
		{http.MethodHead, "http://localhost/docs/", "text/html", http.StatusOK},
		{http.MethodGet, "http://localhost/openapi.json", "application/json", http.StatusOK},
		{http.MethodHead, "http://localhost/openapi.json", "application/json", http.StatusOK},
		{http.MethodGet, "http://localhost/docs/missing.js", "text/plain", http.StatusNotFound},
		{http.MethodPost, "http://localhost/docs/", "text/plain", http.StatusMethodNotAllowed},
		{http.MethodPost, "http://localhost/openapi.json", "text/plain", http.StatusMethodNotAllowed},
		{http.MethodGet, "http://untrusted.example/docs/", "text/plain", http.StatusMisdirectedRequest},
		{http.MethodGet, "http://untrusted.example/openapi.json", "text/plain", http.StatusMisdirectedRequest},
	} {
		t.Run(test.method+" "+test.target, func(t *testing.T) {
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, httptest.NewRequest(test.method, test.target, nil))
			if response.Code != test.status || !strings.HasPrefix(response.Header().Get("Content-Type"), test.contentType) {
				t.Fatalf("response: %d %v %s", response.Code, response.Header(), response.Body.String())
			}
			if test.method == http.MethodHead && response.Body.Len() != 0 {
				t.Error("HEAD returned a body")
			}
			if response.Code == http.StatusPermanentRedirect && response.Header().Get("Location") != "/docs/" {
				t.Error("missing canonical docs redirect")
			}
			if response.Code == http.StatusOK && response.Header().Get("Content-Security-Policy") == "" {
				t.Error("docs lost the security headers")
			}
			if bytes.Contains(response.Body.Bytes(), []byte(testWebToken)) {
				t.Error("docs disclosed the token")
			}
		})
	}
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "http://localhost/docs/", nil))
	if !strings.Contains(response.Body.String(), `id="swagger-ui"`) || !strings.Contains(response.Body.String(), `href="/openapi.json"`) {
		t.Fatal("docs returned the dashboard instead of the reference")
	}
	assets := regexp.MustCompile(`(?:src|href)="(/assets/[^"?]+)"`).FindAllStringSubmatch(response.Body.String(), -1)
	if len(assets) < 2 {
		t.Fatal("docs must reference bundled scripts and styles")
	}
	for _, asset := range assets {
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "http://localhost"+asset[1], nil))
		if response.Code != http.StatusOK || strings.HasPrefix(response.Header().Get("Content-Type"), "text/html") {
			t.Errorf("missing embedded docs asset %s", asset[1])
		}
	}
}

func TestOpenAPIRoutesAndSecurityMatchListener(t *testing.T) {
	t.Parallel()
	manager, err := control.NewManager(control.Options{Logger: testLogger()})
	if err != nil {
		t.Fatal(err)
	}
	routes := (&apiServer{}).routes()
	for _, publicRead := range []bool{true, false} {
		t.Run(fmt.Sprintf("public=%t", publicRead), func(t *testing.T) {
			handler, err := newHandlerForListener(manager, testLogger(), testWebToken, "v1.2.3-docs", false, publicRead)
			if err != nil {
				t.Fatal(err)
			}
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "http://localhost/openapi.json", nil))
			var document documentedAPI
			decodeResponse(t, response.Result(), http.StatusOK, &document)
			if document.OpenAPI != "3.1.0" || document.Info.Version != "v1.2.3-docs" || response.Header().Get("Cache-Control") != "no-store" {
				t.Fatal("incorrect OpenAPI version, build version, or caching")
			}
			seen := make(map[string]bool)
			ids := make(map[string]bool)
			for path, methods := range document.Paths {
				for method, operation := range methods {
					pattern := strings.ToUpper(method) + " " + path
					if _, exists := routes[pattern]; !exists {
						t.Errorf("documented route is not registered: %s", pattern)
					}
					seen[pattern] = true
					if operation.OperationID == "" || ids[operation.OperationID] {
						t.Errorf("missing or duplicate operation ID: %s", pattern)
					}
					ids[operation.OperationID] = true
					security := operation.Security
					if security == nil {
						security = document.Security
					}
					anonymous, bearer := len(security) == 0, false
					for _, requirement := range security {
						anonymous = anonymous || len(requirement) == 0
						_, hasBearer := requirement["bearerAuth"]
						bearer = bearer || hasBearer
					}
					if !bearer || anonymous != (publicRead && method == "get") {
						t.Errorf("incorrect security for %s: %+v", pattern, security)
					}
				}
			}
			for pattern := range routes {
				if !seen[pattern] {
					t.Errorf("registered route lacks documentation: %s", pattern)
				}
			}
		})
	}
}

const contractURL = "https://onderzeeer.invalid/openapi.json"

func contractCompiler(t *testing.T) (*documentedAPI, *jsonschema.Compiler) {
	t.Helper()
	var document documentedAPI
	if err := json.Unmarshal(openAPISpec, &document); err != nil {
		t.Fatal(err)
	}
	resource, err := jsonschema.UnmarshalJSON(bytes.NewReader(openAPISpec))
	if err != nil {
		t.Fatal(err)
	}
	compiler := jsonschema.NewCompiler()
	compiler.DefaultDraft(jsonschema.Draft2020)
	compiler.AssertFormat()
	if err := compiler.AddResource(contractURL, resource); err != nil {
		t.Fatal(err)
	}
	return &document, compiler
}

func TestOpenAPISchemasAndExamples(t *testing.T) {
	t.Parallel()
	document, compiler := contractCompiler(t)
	validateExample := func(t *testing.T, schemaRef string, example json.RawMessage) {
		t.Helper()
		schema, err := compiler.Compile(contractURL + schemaRef)
		if err != nil {
			t.Fatal(err)
		}
		if len(example) == 0 {
			return
		}
		value, err := jsonschema.UnmarshalJSON(bytes.NewReader(example))
		if err != nil {
			t.Fatal(err)
		}
		if err := schema.Validate(value); err != nil {
			t.Fatalf("invalid example: %v", err)
		}
	}
	for name, definition := range document.Components.Schemas {
		t.Run(name, func(t *testing.T) {
			validateExample(t, "#/components/schemas/"+name, definition.Example)
		})
	}
	for path, methods := range document.Paths {
		for method, operation := range methods {
			for status, response := range operation.Responses {
				t.Run(method+" "+path+" "+status, func(t *testing.T) {
					if response.Ref != "" {
						response = document.Components.Responses[strings.TrimPrefix(response.Ref, "#/components/responses/")]
					}
					content := response.Content["application/json"]
					if content.Schema.Ref == "" {
						t.Fatal("missing response schema")
					}
					validateExample(t, content.Schema.Ref, content.Example)
				})
			}
		}
	}
}

func TestAPIResponsesMatchOpenAPI(t *testing.T) {
	t.Parallel()
	for _, publicRead := range []bool{false, true} {
		t.Run(fmt.Sprintf("public=%t", publicRead), func(t *testing.T) {
			manager, known, jobID, commandID := webTestQueue(t, true)
			handler, err := newHandlerForListener(manager, testLogger(), testWebToken, "dev", false, publicRead)
			if err != nil {
				t.Fatal(err)
			}
			document, compiler := contractCompiler(t)
			queuePath := "/api/v1/queues/" + queueID(known.Identity)
			instancePath := "/api/v1/instances/" + manager.List(false)[0].ID + "/stop"
			readToken := testWebToken
			if publicRead {
				readToken = ""
			}
			check := func(method, path, pattern, token string, status int, mutationHeader bool) {
				t.Helper()
				request := httptest.NewRequest(method, "http://localhost"+path, strings.NewReader("{}"))
				if token != "" {
					request.Header.Set("Authorization", "Bearer "+token)
				}
				if method == http.MethodPost {
					request.Header.Set("Content-Type", "application/json")
					request.Header.Set("Origin", "http://localhost")
					if mutationHeader {
						request.Header.Set("X-onderzeeer-Web", "1")
					}
				}
				response := httptest.NewRecorder()
				handler.ServeHTTP(response, request)
				if response.Code != status {
					t.Fatalf("%s %s: %d, want %d: %s", method, path, response.Code, status, response.Body.String())
				}
				contract := document.Paths[pattern][strings.ToLower(method)].Responses[fmt.Sprint(status)]
				if contract.Ref != "" {
					contract = document.Components.Responses[strings.TrimPrefix(contract.Ref, "#/components/responses/")]
				}
				schemaRef := contract.Content["application/json"].Schema.Ref
				if schemaRef == "" {
					t.Fatalf("missing response schema for %s %s: %d", method, pattern, status)
				}
				schema, err := compiler.Compile(contractURL + schemaRef)
				if err != nil {
					t.Fatal(err)
				}
				body, err := jsonschema.UnmarshalJSON(response.Body)
				if err != nil {
					t.Fatal(err)
				}
				if err := schema.Validate(body); err != nil {
					t.Fatalf("%s %s: %v", method, path, err)
				}
			}
			check("GET", "/api/v1/info", "/api/v1/info", readToken, 200, false)
			check("GET", "/api/v1/info", "/api/v1/info", "invalid", 401, false)
			check("GET", "/api/v1/queues", "/api/v1/queues", readToken, 200, false)
			check("GET", "/api/v1/instances?all=false", "/api/v1/instances", readToken, 200, false)
			check("GET", "/api/v1/instances?all=invalid", "/api/v1/instances", readToken, 400, false)
			check("GET", queuePath+"/jobs?limit=1", "/api/v1/queues/{queueID}/jobs", readToken, 200, false)
			check("GET", queuePath+"/jobs?status=queued", "/api/v1/queues/{queueID}/jobs", readToken, 200, false)
			check("GET", queuePath+"/jobs?limit=201", "/api/v1/queues/{queueID}/jobs", readToken, 400, false)
			check("GET", fmt.Sprintf("%s/jobs/%d", queuePath, jobID), "/api/v1/queues/{queueID}/jobs/{jobID}", readToken, 200, false)
			check("GET", queuePath+"/jobs/99999", "/api/v1/queues/{queueID}/jobs/{jobID}", readToken, 404, false)
			check("GET", fmt.Sprintf("%s/commands/%d/output", queuePath, commandID), "/api/v1/queues/{queueID}/commands/{commandID}/output", readToken, 200, false)
			check("POST", instancePath, "/api/v1/instances/{instanceID}/stop", "", 401, true)
			check("POST", instancePath, "/api/v1/instances/{instanceID}/stop", testWebToken, 400, false)
			check("POST", instancePath, "/api/v1/instances/{instanceID}/stop", testWebToken, 200, true)
			check("GET", "/api/v1/instances", "/api/v1/instances", readToken, 200, false)
			check("GET", "/api/v1/queues", "/api/v1/queues", readToken, 200, false)
			check("POST", queuePath+"/start", "/api/v1/queues/{queueID}/start", "", 401, true)
			check("POST", queuePath+"/start", "/api/v1/queues/{queueID}/start", testWebToken, 201, true)
			check("POST", queuePath+"/start", "/api/v1/queues/{queueID}/start", testWebToken, 409, true)
		})
	}
}
