package queue

import (
	"context"
	"errors"
	"fmt"
	"os"
)

type DiskSpace struct {
	AvailableBytes uint64 `json:"available_bytes"`
	TotalBytes     uint64 `json:"total_bytes"`
}

// StorageInfo reports physical file sizes, reusable SQLite pages, and space
// available to the daemon user on the database's filesystem.
type StorageInfo struct {
	DatabaseBytes int64      `json:"database_bytes"`
	WALBytes      int64      `json:"wal_bytes"`
	ReusableBytes int64      `json:"reusable_bytes"`
	Disk          *DiskSpace `json:"disk,omitempty"`
	DiskError     string     `json:"disk_error,omitempty"`
	Warnings      []string   `json:"warnings,omitempty"`
}

// FileStorage inspects sizes without opening or creating a database.
func FileStorage(path string) (StorageInfo, error) {
	var result StorageInfo
	info, err := os.Stat(path)
	if err != nil {
		return result, err
	}
	if !info.Mode().IsRegular() {
		return result, fmt.Errorf("queue: database %q is not a regular file", path)
	}
	result.DatabaseBytes = info.Size()
	wal, err := os.Stat(path + "-wal")
	if err == nil {
		if !wal.Mode().IsRegular() {
			return result, fmt.Errorf("queue: WAL %q is not a regular file", path+"-wal")
		}
		result.WALBytes = wal.Size()
	} else if !errors.Is(err, os.ErrNotExist) {
		return result, err
	}
	result.Disk, err = diskSpace(path)
	if err != nil {
		result.DiskError = err.Error()
	}
	return result, nil
}

func (s *Store) Storage(ctx context.Context) (StorageInfo, error) {
	var sequence int
	var name, path string
	if err := s.db.QueryRowContext(ctx, "PRAGMA database_list").Scan(&sequence, &name, &path); err != nil {
		return StorageInfo{}, err
	}
	result, err := FileStorage(path)
	if err != nil {
		return result, err
	}
	var pages, size int64
	if err := s.db.QueryRowContext(ctx, "PRAGMA freelist_count").Scan(&pages); err != nil {
		return result, err
	}
	if err := s.db.QueryRowContext(ctx, "PRAGMA page_size").Scan(&size); err != nil {
		return result, err
	}
	result.ReusableBytes = pages * size
	return result, nil
}

func (info *StorageInfo) SetWarnings(sizeBytes int64, minFreePercent float64) {
	info.Warnings = nil
	if sizeBytes > 0 && info.DatabaseBytes+info.WALBytes >= sizeBytes {
		info.Warnings = append(info.Warnings, fmt.Sprintf("database and WAL use %d bytes (warning threshold %d); consider pruning old captured output", info.DatabaseBytes+info.WALBytes, sizeBytes))
	}
	if minFreePercent > 0 && info.Disk != nil && info.Disk.TotalBytes > 0 {
		free := 100 * float64(info.Disk.AvailableBytes) / float64(info.Disk.TotalBytes)
		if free <= minFreePercent {
			info.Warnings = append(info.Warnings, fmt.Sprintf("database filesystem has %.1f%% available (%d bytes; warning threshold %.1f%%)", free, info.Disk.AvailableBytes, minFreePercent))
		}
	}
}
