package daemon

import (
	"strings"
	"sync"

	"github.com/GerhardOfRivia/onderzeeer/internal/config"
	"github.com/GerhardOfRivia/onderzeeer/internal/watcher"
)

// ExternalIDHook derives the external_id stored for a file discovered by a
// watch. Return an empty string to leave the id unset. Return an error to
// fail that file's enqueue; the watcher reports the error and does not mark
// the file delivered.
//
// Filename and sidecar derivation are left to the hook. The default file-drop
// path stores an empty external_id, which keeps existing queues unchanged.
// A hook can, for example, use the file name or read a sibling sidecar:
//
//	daemon.SetFileExternalID(func(_ config.WatchConfig, file watcher.File) (string, error) {
//	    return strings.TrimSuffix(filepath.Base(file.Path), filepath.Ext(file.Path)), nil
//	})
type ExternalIDHook func(watch config.WatchConfig, file watcher.File) (string, error)

var fileExternalID struct {
	mu   sync.RWMutex
	hook ExternalIDHook
}

// SetFileExternalID installs the process-wide hook consulted for every
// file-drop enqueue. Pass nil to clear it. Programmatic Enqueue callers set
// ExternalID on the job directly and do not use this hook.
//
// The hook is global to the process and applies to later Run, RunTest, and
// RunWithResources calls. It should be fast and free of side effects; it runs
// on the watcher path before the queue insert.
func SetFileExternalID(hook ExternalIDHook) {
	fileExternalID.mu.Lock()
	fileExternalID.hook = hook
	fileExternalID.mu.Unlock()
}

func externalIDFor(watch config.WatchConfig, file watcher.File) (string, error) {
	fileExternalID.mu.RLock()
	hook := fileExternalID.hook
	fileExternalID.mu.RUnlock()
	if hook == nil {
		return "", nil
	}
	id, err := hook(watch, file)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(id), nil
}
