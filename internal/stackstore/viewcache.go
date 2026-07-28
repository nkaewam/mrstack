package stackstore

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// ViewSnapshot is the last-known live status of a named stack, written after a
// successful check and read by `view --all` without --refresh.
type ViewSnapshot struct {
	Name          string               `json:"name"`
	Host          string               `json:"host"`
	Project       string               `json:"project"`
	DefaultBranch string               `json:"default_branch,omitempty"`
	Members       []ViewSnapshotMember `json:"members"`
	Note          string               `json:"note,omitempty"`
	CheckedAt     string               `json:"checked_at"`
}

// ViewSnapshotMember mirrors the per-member status fields shown in view output.
type ViewSnapshotMember struct {
	Position       int    `json:"position"`
	IID            int    `json:"iid"`
	Title          string `json:"title,omitempty"`
	SourceBranch   string `json:"source_branch,omitempty"`
	TargetBranch   string `json:"target_branch,omitempty"`
	State          string `json:"state,omitempty"`
	MergeStatus    string `json:"merge_status,omitempty"`
	PipelineStatus string `json:"pipeline_status,omitempty"`
	WebURL         string `json:"web_url,omitempty"`
}

func (s *Store) viewDir() string {
	return filepath.Join(filepath.Dir(s.dir), "view")
}

func (s *Store) viewPath(name string) string {
	return filepath.Join(s.viewDir(), strings.ToLower(name)+".json")
}

// SaveViewSnapshot atomically persists the last check snapshot for a named stack.
func (s *Store) SaveViewSnapshot(snap ViewSnapshot) error {
	if err := ValidateName(snap.Name); err != nil {
		return err
	}
	dir := s.viewDir()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	data, err := json.MarshalIndent(snap, "", "  ")
	if err != nil {
		return err
	}
	target := s.viewPath(snap.Name)
	tmp, err := os.CreateTemp(dir, ".mrstack-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpName, target)
}

// GetViewSnapshot returns the cached view for a named stack.
func (s *Store) GetViewSnapshot(name string) (ViewSnapshot, error) {
	if err := ValidateName(name); err != nil {
		return ViewSnapshot{}, err
	}
	data, err := os.ReadFile(s.viewPath(name))
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return ViewSnapshot{}, ErrNotFound
		}
		return ViewSnapshot{}, err
	}
	var snap ViewSnapshot
	if err := json.Unmarshal(data, &snap); err != nil {
		return ViewSnapshot{}, fmt.Errorf("stackstore: decode view cache %s: %w", name, err)
	}
	return snap, nil
}
