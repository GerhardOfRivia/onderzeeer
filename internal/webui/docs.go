package webui

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"io/fs"
	"net/http"
	"strings"
)

//go:embed openapi.json
var openAPISpec []byte

// routes is shared by registration and the documentation coverage test.
func (api *apiServer) routes() map[string]http.HandlerFunc {
	return map[string]http.HandlerFunc{
		"GET /api/v1/info":                                         api.handleInfo,
		"GET /api/v1/queues":                                       api.handleQueues,
		"GET /api/v1/queues/{queueID}/jobs":                        api.handleJobs,
		"GET /api/v1/queues/{queueID}/jobs/{jobID}":                api.handleJob,
		"GET /api/v1/queues/{queueID}/commands/{commandID}/output": api.handleCommandOutput,
		"GET /api/v1/instances":                                    api.handleInstances,
		"POST /api/v1/queues/{queueID}/start":                      api.handleStart,
		"POST /api/v1/instances/{instanceID}/stop":                 api.handleStop,
	}
}

func registerDocs(mux *http.ServeMux, assets fs.FS, version string, publicReads map[string]bool) error {
	document, err := openAPIDocument(version, publicReads)
	if err != nil {
		return err
	}
	mux.HandleFunc("GET /openapi.json", func(output http.ResponseWriter, request *http.Request) {
		output.Header().Set("Content-Type", "application/json; charset=utf-8")
		output.Header().Set("Cache-Control", "no-store")
		if request.Method != http.MethodHead {
			_, _ = output.Write(document)
		}
	})
	mux.HandleFunc("GET /docs", func(output http.ResponseWriter, request *http.Request) {
		http.Redirect(output, request, "/docs/", http.StatusPermanentRedirect)
	})
	files := http.FileServer(http.FS(assets))
	mux.HandleFunc("GET /docs/", func(output http.ResponseWriter, request *http.Request) {
		output.Header().Set("Cache-Control", "no-cache")
		files.ServeHTTP(output, request)
	})
	return nil
}

// Render once per listener so the advertised authentication matches the exact
// read routes allowed by requireAPIAuth. The embedded document defaults to the
// private API; both forms are valid OpenAPI documents.
func openAPIDocument(version string, publicReads map[string]bool) ([]byte, error) {
	var document map[string]any
	if err := json.Unmarshal(openAPISpec, &document); err != nil {
		return nil, fmt.Errorf("webui: decode OpenAPI document: %w", err)
	}
	info, infoOK := document["info"].(map[string]any)
	paths, pathsOK := document["paths"].(map[string]any)
	if !infoOK || !pathsOK {
		return nil, fmt.Errorf("webui: OpenAPI document is missing info or paths")
	}
	info["version"] = version
	for pattern, public := range publicReads {
		if !public {
			continue
		}
		method, path, _ := strings.Cut(pattern, " ")
		pathItem, _ := paths[path].(map[string]any)
		operation, ok := pathItem[strings.ToLower(method)].(map[string]any)
		if !ok {
			return nil, fmt.Errorf("webui: OpenAPI document is missing operation %q", pattern)
		}
		operation["security"] = []any{map[string]any{}, map[string]any{"bearerAuth": []string{}}}
	}
	return json.MarshalIndent(document, "", "  ")
}
