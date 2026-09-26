//go:build !linux && !darwin

package queue

import "errors"

func diskSpace(string) (*DiskSpace, error) {
	return nil, errors.New("disk space reporting is unavailable on this platform")
}
