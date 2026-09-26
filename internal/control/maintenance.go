package control

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"time"

	"github.com/GerhardOfRivia/onderzeeer/internal/config"
	"github.com/GerhardOfRivia/onderzeeer/internal/queue"
)

func (manager *Manager) databaseConfig(id string) config.DatabaseConfig {
	manager.mu.Lock()
	defer manager.mu.Unlock()
	if runtime := manager.instances[id]; runtime != nil && runtime.config != nil {
		return runtime.config.Database
	}
	return config.DatabaseConfig{}
}

func (server *Server) handleQueueMaintenance(output http.ResponseWriter, request *http.Request) {
	operation := request.PathValue("operation")
	if operation != "prune" && operation != "compact" {
		writeAPIError(output, http.StatusNotFound, "not_found", errors.New("unknown maintenance operation"))
		return
	}
	var options queue.PruneOptions
	if operation == "prune" {
		if err := decodeRequest(output, request, &options); err != nil {
			writeAPIError(output, http.StatusBadRequest, "invalid_request", err)
			return
		}
		if err := options.Validate(time.Now()); err != nil {
			writeAPIError(output, http.StatusBadRequest, "invalid_request", err)
			return
		}
	}
	// Prevent purge/restart races throughout maintenance, including compaction.
	if err := server.manager.acquireStartGate(request.Context()); err != nil {
		writeManagerError(output, err, false)
		return
	}
	defer server.manager.releaseStartGate()
	instance, err := server.manager.Get(request.PathValue("selector"))
	if err != nil {
		writeManagerError(output, err, false)
		return
	}
	if operation == "compact" && instance.Active() {
		writeAPIError(output, http.StatusConflict, "queue_active", errors.New("stop the instance before compacting its database"))
		return
	}
	// Read-only open makes dry-run genuinely read-only and ensures maintenance
	// never recreates a missing database.
	store, err := queue.OpenReadOnlyContext(request.Context(), instance.DatabasePath)
	if err != nil {
		writeAPIError(output, http.StatusServiceUnavailable, "queue_unavailable", err)
		return
	}
	if !options.DryRun || operation == "compact" {
		store.Close()
		store, err = queue.Open(instance.DatabasePath)
		if err != nil {
			writeAPIError(output, http.StatusServiceUnavailable, "queue_unavailable", err)
			return
		}
	}
	defer store.Close()
	if operation == "prune" {
		result, err := store.PruneOutput(request.Context(), options)
		// Include committed progress even when a later batch fails.
		response := pruneResponse{Result: result}
		if err != nil {
			response.Error = err.Error()
		}
		writeJSON(output, http.StatusOK, response)
		return
	}
	if err := store.Compact(request.Context()); err != nil {
		writeAPIError(output, http.StatusServiceUnavailable, "compaction_failed", err)
		return
	}
	writeJSON(output, http.StatusOK, struct{}{})
}

type pruneResponse struct {
	Result queue.PruneResult `json:"result"`
	Error  string            `json:"error,omitempty"`
}

func (reader *QueueReader) Storage(ctx context.Context) (queue.StorageInfo, error) {
	var info queue.StorageInfo
	err := reader.read(ctx, "storage", nil, &info)
	return info, err
}

func (reader *QueueReader) PruneOutput(ctx context.Context, options queue.PruneOptions) (queue.PruneResult, error) {
	var response pruneResponse
	err := reader.client.doJSON(ctx, http.MethodPost, "/v1/queues/"+url.PathEscape(reader.id)+"/prune", options, &response)
	if err == nil && response.Error != "" {
		err = errors.New(response.Error)
	}
	return response.Result, err
}

func (reader *QueueReader) Compact(ctx context.Context) error {
	return reader.client.doJSON(ctx, http.MethodPost, "/v1/queues/"+url.PathEscape(reader.id)+"/compact", struct{}{}, &struct{}{})
}
