//go:build linux || darwin

package queue

import "golang.org/x/sys/unix"

func diskSpace(path string) (*DiskSpace, error) {
	var stat unix.Statfs_t
	if err := unix.Statfs(path, &stat); err != nil {
		return nil, err
	}
	return &DiskSpace{AvailableBytes: uint64(stat.Bavail) * uint64(stat.Bsize), TotalBytes: uint64(stat.Blocks) * uint64(stat.Bsize)}, nil
}
