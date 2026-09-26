package control

import (
	"context"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"time"

	"github.com/GerhardOfRivia/onderzeeer/internal/queue"
)

// SystemConfig contains the daemon's effective startup settings. It reports
// the token's location, never the token itself.
type SystemConfig struct {
	Version       string `json:"version"`
	LogLevel      string `json:"log_level"`
	WebListen     string `json:"web_listen"`
	WebAddress    string `json:"web_address"`
	WebTokenPath  string `json:"web_token_path"`
	WebPublicRead bool   `json:"web_public_read"`
}

type SystemInfo struct {
	SystemConfig
	PID                  int                `json:"pid"`
	StartedAt            time.Time          `json:"started_at"`
	SocketPath           string             `json:"socket_path"`
	SocketLockPath       string             `json:"socket_lock_path"`
	StateDirectory       string             `json:"state_directory"`
	RegistryPath         string             `json:"registry_path"`
	StateLockPath        string             `json:"state_lock_path"`
	QueueDirectory       string             `json:"queue_directory"`
	ActiveQueues         int                `json:"active_queues"`
	InactiveQueues       int                `json:"inactive_queues"`
	QueueStorage         []QueueStorage     `json:"queue_storage"`
	RegistryStorage      *queue.StorageInfo `json:"registry_storage,omitempty"`
	RegistryStorageError string             `json:"registry_storage_error,omitempty"`
}

type QueueStorage struct {
	ID           string            `json:"id"`
	Name         string            `json:"name"`
	DatabasePath string            `json:"database_path"`
	Storage      queue.StorageInfo `json:"storage"`
	Error        string            `json:"error,omitempty"`
}

// SetSystemConfig records resolved startup settings before Serve is called.
func (server *Server) SetSystemConfig(config SystemConfig) {
	server.mu.Lock()
	defer server.mu.Unlock()
	server.systemConfig = config
}

func (server *Server) handleSystem(output http.ResponseWriter, request *http.Request) {
	server.mu.Lock()
	info := SystemInfo{SystemConfig: server.systemConfig, PID: os.Getpid(), StartedAt: server.startedAt}
	server.mu.Unlock()
	info.SocketPath, _ = filepath.Abs(server.path)
	info.SocketLockPath = info.SocketPath + ".lock"
	server.manager.mu.Lock()
	if registry := server.manager.registry; registry != nil {
		info.StateDirectory = registry.directory
		info.RegistryPath = filepath.Join(registry.directory, "registry.sqlite")
		info.StateLockPath = filepath.Join(registry.directory, "daemon.lock")
		info.QueueDirectory = filepath.Join(registry.directory, "queues")
	}
	for _, runtime := range server.manager.instances {
		info.QueueStorage = append(info.QueueStorage, QueueStorage{ID: runtime.view.ID, Name: runtime.view.Name, DatabasePath: runtime.view.DatabasePath})
		if runtime.view.Active() {
			info.ActiveQueues++
		} else {
			info.InactiveQueues++
		}
	}
	server.manager.mu.Unlock()
	if info.RegistryPath != "" {
		storage, err := queue.FileStorage(info.RegistryPath)
		if err != nil {
			info.RegistryStorageError = err.Error()
		} else {
			info.RegistryStorage = &storage
		}
	}
	sort.Slice(info.QueueStorage, func(i, j int) bool { return info.QueueStorage[i].Name < info.QueueStorage[j].Name })
	for i := range info.QueueStorage {
		item := &info.QueueStorage[i]
		storage, err := queue.FileStorage(item.DatabasePath)
		if err != nil {
			item.Error = err.Error()
		} else {
			storage.SetWarnings(server.manager.databaseConfig(item.ID).WarningThresholds())
			item.Storage = storage
		}
	}
	writeJSON(output, http.StatusOK, info)
}

func (server *Server) handleSystemPurge(output http.ResponseWriter, request *http.Request) {
	result, err := server.manager.PurgeInactive(request.Context())
	if err != nil {
		writeAPIError(output, http.StatusServiceUnavailable, "purge_unavailable", err)
		return
	}
	writeJSON(output, http.StatusOK, result)
}

func (server *Server) handleSystemPurgeSelected(output http.ResponseWriter, request *http.Request) {
	var input purgeRequest
	if err := decodeRequest(output, request, &input); err != nil {
		writeAPIError(output, http.StatusBadRequest, "invalid_request", err)
		return
	}
	result, err := server.manager.PurgeSelected(request.Context(), input.Instances)
	if err != nil {
		writeAPIError(output, http.StatusServiceUnavailable, "purge_unavailable", err)
		return
	}
	writeJSON(output, http.StatusOK, result)
}

type purgeRequest struct {
	Instances []Instance `json:"instances"`
}

// System returns the running daemon's effective settings and storage locations.
func (client *Client) System(ctx context.Context) (SystemInfo, error) {
	var info SystemInfo
	err := client.doJSON(ctx, http.MethodGet, "/v1/system", nil, &info)
	return info, err
}

// PurgeInactive removes inactive daemon-owned queues and their registrations.
func (client *Client) PurgeInactive(ctx context.Context) (PurgeResult, error) {
	var result PurgeResult
	err := client.doJSON(ctx, http.MethodPost, "/v1/system/purge", struct{}{}, &result)
	return result, err
}

// PurgeSelected removes only unchanged inactive instances from the supplied
// preview. Newly inactive queues are never added to this request.
func (client *Client) PurgeSelected(ctx context.Context, instances []Instance) (PurgeResult, error) {
	if instances == nil {
		instances = []Instance{}
	}
	var result PurgeResult
	// A distinct endpoint ensures older daemons cannot silently ignore the
	// selection and fall back to purging every inactive queue.
	err := client.doJSON(ctx, http.MethodPost, "/v1/system/purge-selected", purgeRequest{Instances: instances}, &result)
	return result, err
}
