package webui

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestPublicReadPermissions(t *testing.T) {
	t.Parallel()
	manager, known, jobID, commandID := webTestQueue(t, true)
	queuePath := "/api/v1/queues/" + queueID(known.Identity)
	reads := []string{
		"/api/v1/info", "/api/v1/queues", "/api/v1/instances?all=true",
		queuePath + "/jobs?status=SUCCEEDED&limit=1",
		fmt.Sprintf("%s/jobs/%d", queuePath, jobID),
		fmt.Sprintf("%s/commands/%d/output", queuePath, commandID),
	}
	for _, publicRead := range []bool{false, true} {
		t.Run(fmt.Sprintf("public=%t", publicRead), func(t *testing.T) {
			handler, err := newHandlerForListener(manager, testLogger(), testWebToken, "dev", false, publicRead)
			if err != nil {
				t.Fatal(err)
			}
			for _, path := range reads {
				for _, method := range []string{http.MethodGet, http.MethodHead} {
					for _, auth := range []struct {
						name    string
						headers []string
						valid   bool
					}{
						{name: "anonymous"},
						{name: "valid", headers: []string{"Bearer " + testWebToken}, valid: true},
						{name: "wrong", headers: []string{"Bearer wrong"}},
						{name: "empty", headers: []string{""}},
						{name: "empty bearer", headers: []string{"Bearer "}},
						{name: "wrong scheme", headers: []string{"Basic " + testWebToken}},
						{name: "duplicate", headers: []string{"Bearer " + testWebToken, "Bearer wrong"}},
					} {
						t.Run(method+" "+path+" "+auth.name, func(t *testing.T) {
							request := httptest.NewRequest(method, "http://localhost"+path, nil)
							for _, header := range auth.headers {
								request.Header.Add("Authorization", header)
							}
							response := httptest.NewRecorder()
							handler.ServeHTTP(response, request)
							want := http.StatusUnauthorized
							if auth.valid || (publicRead && len(auth.headers) == 0) {
								want = http.StatusOK
							}
							if response.Code != want {
								t.Fatalf("status=%d, want %d: %s", response.Code, want, response.Body.String())
							}
							if response.Header().Get("Cache-Control") != "no-store" {
								t.Error("API response must not be cached")
							}
							if want == http.StatusUnauthorized && response.Header().Get("WWW-Authenticate") == "" {
								t.Error("missing authentication challenge")
							}
							if path == "/api/v1/info" && method == http.MethodGet && want == http.StatusOK {
								var info infoResponse
								decodeResponse(t, response.Result(), http.StatusOK, &info)
								if info.PublicRead != publicRead || info.CanControl != auth.valid || info.Version != "dev" {
									t.Fatalf("incorrect permissions: %+v", info)
								}
							}
						})
					}
				}
			}
			for _, path := range []string{"/api", "/api/v1/future", "/api/v1/queues/" + queueID(known.Identity) + "/future"} {
				request := httptest.NewRequest(http.MethodGet, "http://localhost"+path, nil)
				response := httptest.NewRecorder()
				handler.ServeHTTP(response, request)
				if response.Code != http.StatusUnauthorized {
					t.Errorf("unlisted route %s: status=%d, want 401", path, response.Code)
				}
			}
			for _, method := range []string{http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete, http.MethodOptions} {
				request := httptest.NewRequest(method, "http://localhost/api/v1/queues", nil)
				response := httptest.NewRecorder()
				handler.ServeHTTP(response, request)
				if response.Code != http.StatusUnauthorized {
					t.Errorf("%s on read route: status=%d, want 401", method, response.Code)
				}
			}
			request := httptest.NewRequest(http.MethodGet, "http://evil.example/api/v1/queues", nil)
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, request)
			if response.Code != http.StatusMisdirectedRequest {
				t.Errorf("unrecognized Host: status=%d, want 421", response.Code)
			}
		})
	}
}

func TestPublicReadStillRequiresTokenForControl(t *testing.T) {
	t.Parallel()
	manager, known, _, _ := webTestQueue(t, true)
	handler, err := newHandlerForListener(manager, testLogger(), testWebToken, "dev", false, true)
	if err != nil {
		t.Fatal(err)
	}
	instance := manager.List(false)[0]
	startPath := "/api/v1/queues/" + queueID(known.Identity) + "/start"
	stopPath := "/api/v1/instances/" + instance.ID + "/stop"
	mutate := func(path, token, origin string) *httptest.ResponseRecorder {
		t.Helper()
		request := httptest.NewRequest(http.MethodPost, "http://localhost"+path, strings.NewReader("{}"))
		request.Header.Set("Content-Type", "application/json")
		request.Header.Set("X-onderzeeer-Web", "1")
		request.Header.Set("Origin", origin)
		if token != "" {
			request.Header.Set("Authorization", "Bearer "+token)
		}
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		return response
	}
	for _, token := range []string{"", "incorrect"} {
		if response := mutate(stopPath, token, "http://localhost"); response.Code != http.StatusUnauthorized {
			t.Fatalf("unauthenticated stop: %d %s", response.Code, response.Body.String())
		}
	}
	if response := mutate(stopPath, testWebToken, "http://evil.example"); response.Code != http.StatusBadRequest {
		t.Fatalf("cross-origin stop: %d", response.Code)
	}
	if active := manager.List(false); len(active) != 1 || active[0].ID != instance.ID || active[0].State != instance.State || active[0].DesiredState != instance.DesiredState {
		t.Fatalf("denied stop changed the instance: %+v", active)
	}
	if response := mutate(stopPath, testWebToken, "http://localhost"); response.Code != http.StatusOK {
		t.Fatalf("authorized stop: %d %s", response.Code, response.Body.String())
	}
	for _, token := range []string{"", "incorrect"} {
		if response := mutate(startPath, token, "http://localhost"); response.Code != http.StatusUnauthorized {
			t.Fatalf("unauthenticated start: %d %s", response.Code, response.Body.String())
		}
	}
	if active := manager.List(false); len(active) != 0 {
		t.Fatalf("denied start launched an instance: %+v", active)
	}
	if response := mutate(startPath, testWebToken, "http://localhost"); response.Code != http.StatusCreated {
		t.Fatalf("authorized start: %d %s", response.Code, response.Body.String())
	}
	if active := manager.List(false); len(active) != 1 {
		t.Fatalf("authorized start did not launch an instance: %+v", active)
	}
}

func TestNewAPIEndpointRequiresAuthenticationByDefault(t *testing.T) {
	t.Parallel()
	mux := http.NewServeMux()
	called := false
	mux.HandleFunc("GET /api/v1/future", func(http.ResponseWriter, *http.Request) { called = true })
	handler := requireAPIAuth(testWebToken, mux, map[string]bool{"GET /api/v1/info": true})
	request := httptest.NewRequest(http.MethodGet, "http://localhost/api/v1/future", nil)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusUnauthorized || called {
		t.Fatalf("new endpoint was not protected: status=%d, called=%t", response.Code, called)
	}
}
