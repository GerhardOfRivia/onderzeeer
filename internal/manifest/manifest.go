// Package manifest reads a bezorg completion manifest and checks the files it names.
package manifest

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/GerhardOfRivia/onderzeeer/internal/config"
)

// File is one completed payload named by a manifest. Path is absolute.
type File struct {
	Path   string `json:"path"`
	Size   int64  `json:"size"`
	SHA256 string `json:"sha256,omitempty"`
}

// Manifest is a bezorg end-of-transmission document.
type Manifest struct {
	Version     int       `json:"version"`
	JobID       string    `json:"job_id"`
	ExternalID  string    `json:"external_id"`
	Remote      string    `json:"remote"`
	CompletedAt time.Time `json:"completed_at"`
	Files       []File    `json:"files"`
}

type document struct {
	Version     int       `json:"version"`
	JobID       string    `json:"job_id"`
	ExternalID  string    `json:"external_id"`
	Remote      string    `json:"remote"`
	CompletedAt string    `json:"completed_at"`
	Files       []rawFile `json:"files"`
}

type rawFile struct {
	Path   string `json:"path"`
	Size   int64  `json:"size"`
	SHA256 string `json:"sha256"`
}

// Load reads a manifest, resolves its files under the manifest directory, and
// applies verify. verify exists checks that each path is a regular file. size
// also compares the listed size. sha256 also compares the listed digest.
func Load(path string, verify config.MarkerVerify) (*Manifest, error) {
	payload, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read manifest: %w", err)
	}
	var raw document
	decoder := json.NewDecoder(strings.NewReader(string(payload)))
	if err := decoder.Decode(&raw); err != nil {
		return nil, fmt.Errorf("decode manifest: %w", err)
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		if err == nil {
			return nil, errors.New("manifest contains more than one JSON value")
		}
		return nil, fmt.Errorf("decode manifest trailer: %w", err)
	}
	if raw.Version != 1 {
		return nil, fmt.Errorf("manifest version %d is not supported", raw.Version)
	}
	if raw.Files == nil {
		return nil, errors.New("manifest files list is required")
	}
	completedAt, err := time.Parse(time.RFC3339, strings.TrimSpace(raw.CompletedAt))
	if err != nil {
		return nil, fmt.Errorf("manifest completed_at must be RFC3339: %w", err)
	}
	externalID := strings.TrimSpace(raw.ExternalID)
	if strings.ContainsRune(externalID, 0) || len(externalID) > 256 {
		return nil, errors.New("manifest external_id must be at most 256 bytes and cannot contain NUL")
	}
	directory := filepath.Dir(path)
	files := make([]File, 0, len(raw.Files))
	seen := make(map[string]struct{}, len(raw.Files))
	for index, item := range raw.Files {
		resolved, err := resolveFile(directory, item.Path)
		if err != nil {
			return nil, fmt.Errorf("manifest files[%d]: %w", index, err)
		}
		if _, exists := seen[resolved]; exists {
			return nil, fmt.Errorf("manifest files[%d]: duplicate path %s", index, item.Path)
		}
		seen[resolved] = struct{}{}
		info, err := os.Lstat(resolved)
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				return nil, fmt.Errorf("manifest files[%d]: %s does not exist", index, item.Path)
			}
			return nil, fmt.Errorf("manifest files[%d]: %w", index, err)
		}
		if !info.Mode().IsRegular() {
			return nil, fmt.Errorf("manifest files[%d]: %s is not a regular file", index, item.Path)
		}
		if item.Size < 0 {
			return nil, fmt.Errorf("manifest files[%d]: size cannot be negative", index)
		}
		digest := strings.ToLower(strings.TrimSpace(item.SHA256))
		if digest != "" && !validSHA256(digest) {
			return nil, fmt.Errorf("manifest files[%d]: sha256 must be 64 hex characters", index)
		}
		switch verify {
		case "", config.MarkerVerifyExists:
		case config.MarkerVerifySize, config.MarkerVerifySHA256:
			if info.Size() != item.Size {
				return nil, fmt.Errorf("manifest files[%d]: size is %d, manifest says %d", index, info.Size(), item.Size)
			}
			if verify == config.MarkerVerifySHA256 {
				if digest == "" {
					return nil, fmt.Errorf("manifest files[%d]: sha256 is required", index)
				}
				sum, err := hashFile(resolved)
				if err != nil {
					return nil, fmt.Errorf("manifest files[%d]: %w", index, err)
				}
				if sum != digest {
					return nil, fmt.Errorf("manifest files[%d]: sha256 does not match", index)
				}
			}
		default:
			return nil, fmt.Errorf("unsupported manifest verification %q", verify)
		}
		files = append(files, File{Path: resolved, Size: info.Size(), SHA256: digest})
	}
	return &Manifest{
		Version: 1, JobID: strings.TrimSpace(raw.JobID), ExternalID: externalID,
		Remote: strings.TrimSpace(raw.Remote), CompletedAt: completedAt.UTC(), Files: files,
	}, nil
}

// FilesJSON returns the verified file list as a JSON array.
func (manifest *Manifest) FilesJSON() string {
	if manifest == nil || manifest.Files == nil {
		return "[]"
	}
	encoded, err := json.Marshal(manifest.Files)
	if err != nil {
		return "[]"
	}
	return string(encoded)
}

func resolveFile(directory, raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" || strings.ContainsRune(raw, 0) || filepath.IsAbs(raw) {
		return "", errors.New("path must be relative to the manifest directory")
	}
	clean := filepath.Clean(raw)
	if clean == "." || clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
		return "", errors.New("path escapes the manifest directory")
	}
	return filepath.Join(directory, clean), nil
}

func validSHA256(value string) bool {
	if len(value) != 64 {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil
}

func hashFile(path string) (string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer file.Close()
	sum := sha256.New()
	if _, err := io.Copy(sum, file); err != nil {
		return "", err
	}
	return hex.EncodeToString(sum.Sum(nil)), nil
}
