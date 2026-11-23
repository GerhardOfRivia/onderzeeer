package webui

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/GerhardOfRivia/onderzeeer/internal/config"
	"github.com/GerhardOfRivia/onderzeeer/internal/queue"
)

const maxJobBody = 16 << 10

type createJobRequest struct {
	Path       string          `json:"path"`
	Watch      string          `json:"watch"`
	ExternalID string          `json:"external_id"`
	Priority   *int            `json:"priority"`
	Metadata   json.RawMessage `json:"metadata"`
}

type jobConflictResponse struct {
	Error apiError `json:"error"`
	Job   jobView  `json:"job"`
}

func (api *apiServer) handleCreateJob(output http.ResponseWriter, request *http.Request) {
	var body createJobRequest
	if err := decodeMutation(output, request, &body, maxJobBody); err != nil {
		writeAPIError(output, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	known, ok := api.resolveQueue(request.PathValue("queueID"))
	if !ok {
		writeAPIError(output, http.StatusNotFound, "queue_not_found", "Queue not found")
		return
	}
	cfg, err := config.Load(known.ConfigPath)
	if err != nil {
		writeAPIError(output, http.StatusConflict, "queue_unavailable", err.Error())
		return
	}
	path, watch, err := resolveSubmittedPath(cfg, strings.TrimSpace(body.Watch), body.Path)
	if err != nil {
		writeAPIError(output, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	priority := 0
	if body.Priority != nil {
		priority = *body.Priority
	}
	metadata := ""
	if len(body.Metadata) > 0 && string(body.Metadata) != "null" {
		metadata = string(body.Metadata)
	}
	fingerprint, err := newAPIFingerprint()
	if err != nil {
		writeAPIError(output, http.StatusServiceUnavailable, "internal_error", err.Error())
		return
	}
	store, err := openKnownQueueWritable(request.Context(), known)
	if err != nil {
		writeQueueUnavailable(output, err)
		return
	}
	defer store.Close()
	job, created, err := store.Enqueue(request.Context(), queue.EnqueueParams{
		WatchName: watch, Path: path, Fingerprint: fingerprint, MaxRetries: cfg.Queue.MaxRetries,
		ExternalID: body.ExternalID, Priority: priority, Metadata: metadata,
	})
	if errors.Is(err, queue.ErrExternalIDInUse) {
		writeJSON(output, http.StatusConflict, jobConflictResponse{
			Error: apiError{Code: "external_id_conflict", Message: "A job with this external_id already exists"},
			Job:   toJobView(job, false),
		})
		return
	}
	if err != nil {
		message := err.Error()
		if strings.HasPrefix(message, "queue: external id") || strings.HasPrefix(message, "queue: priority") || strings.HasPrefix(message, "queue: metadata") || strings.HasPrefix(message, "queue: file path") || strings.HasPrefix(message, "queue: watch") {
			writeAPIError(output, http.StatusBadRequest, "invalid_request", message)
			return
		}
		writeAPIError(output, http.StatusServiceUnavailable, "queue_unavailable", message)
		return
	}
	if !created {
		writeJSON(output, http.StatusConflict, jobConflictResponse{
			Error: apiError{Code: "job_exists", Message: "A job for this path and fingerprint already exists"},
			Job:   toJobView(job, false),
		})
		return
	}
	writeJSON(output, http.StatusCreated, toJobView(job, false))
}

func resolveSubmittedPath(cfg *config.Config, watchName, rawPath string) (string, string, error) {
	if strings.TrimSpace(rawPath) == "" || strings.ContainsRune(rawPath, 0) || !filepath.IsAbs(rawPath) {
		return "", "", errors.New("path must be an absolute file path")
	}
	cleaned := filepath.Clean(rawPath)
	resolved, err := filepath.EvalSymlinks(cleaned)
	if err != nil {
		return "", "", errors.New("path must be an existing file inside a watch directory")
	}
	info, err := os.Stat(resolved)
	if err != nil || !info.Mode().IsRegular() {
		return "", "", errors.New("path must be a regular file inside a watch directory")
	}
	matches := make([]config.WatchConfig, 0, 1)
	for _, watch := range cfg.Watches {
		root := watch.Path
		if resolvedRoot, rootErr := filepath.EvalSymlinks(watch.Path); rootErr == nil {
			root = resolvedRoot
		}
		if pathWithin(root, resolved) {
			matches = append(matches, watch)
		}
	}
	if watchName != "" {
		for _, watch := range cfg.Watches {
			if watch.Name != watchName {
				continue
			}
			root := watch.Path
			if resolvedRoot, rootErr := filepath.EvalSymlinks(watch.Path); rootErr == nil {
				root = resolvedRoot
			}
			if pathWithin(root, resolved) {
				return resolved, watch.Name, nil
			}
			return "", "", fmt.Errorf("path is not inside watch %q", watchName)
		}
		return "", "", fmt.Errorf("unknown watch %q", watchName)
	}
	switch len(matches) {
	case 1:
		return resolved, matches[0].Name, nil
	case 0:
		return "", "", errors.New("path is not inside a configured watch directory")
	default:
		return "", "", errors.New("path matches more than one watch; set watch")
	}
}

func pathWithin(root, candidate string) bool {
	relative, err := filepath.Rel(root, candidate)
	if err != nil {
		return false
	}
	return relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator))
}

func newAPIFingerprint() (string, error) {
	var buf [16]byte
	if _, err := rand.Read(buf[:]); err != nil {
		return "", fmt.Errorf("create job fingerprint: %w", err)
	}
	return "onderzeeer:api:" + hex.EncodeToString(buf[:]), nil
}
