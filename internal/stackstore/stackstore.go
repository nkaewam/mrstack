// Package stackstore owns mrstack's user-global registry of named stacks.
//
// A named stack is a user-curated, persisted list of merge request IIDs bound
// to a single GitLab project. Membership is stored; ordering and live status
// are derived at view/check time. Each stack is one JSON file under the
// store directory (typically ~/.mrstack/stacks).
package stackstore

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

var (
	// ErrNotFound is returned when no stack exists with the requested name.
	ErrNotFound = errors.New("stack not found")
	// ErrAlreadyExists is returned when Create would overwrite an existing stack.
	ErrAlreadyExists = errors.New("stack already exists")
	// ErrInvalidName is returned when a name is not a safe stack identifier.
	ErrInvalidName = errors.New("invalid stack name")
)

// namePattern restricts stack names to safe, portable filename characters so
// a name can never escape the store directory or collide with path metadata.
var namePattern = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,62}[a-z0-9]$|^[a-z0-9]$`)

// Stack is the persisted, user-curated shape of a named stack.
type Stack struct {
	Name       string `json:"name"`
	Host       string `json:"host"`    // GitLab host, e.g. gitlab.agodadev.io
	Project    string `json:"project"` // path_with_namespace, e.g. devops/engineering-tools/developer-portal/web
	MemberIIDs []int  `json:"member_iids"`
	CreatedAt  string `json:"created_at"`
	UpdatedAt  string `json:"updated_at"`
}

// Store is a file-per-stack registry rooted at dir. The directory and files
// are created with restrictive permissions because stack membership reveals
// project structure and workflow intent.
type Store struct {
	dir string
}

// Open returns a store rooted at dir, creating the directory if needed.
func Open(dir string) (*Store, error) {
	if dir == "" {
		return nil, errors.New("stackstore: empty directory")
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	return &Store{dir: dir}, nil
}

// ValidateName returns ErrInvalidName unless name is a safe stack identifier.
func ValidateName(name string) error {
	if !namePattern.MatchString(strings.ToLower(name)) {
		return ErrInvalidName
	}
	return nil
}

func (s *Store) path(name string) string {
	return filepath.Join(s.dir, strings.ToLower(name)+".json")
}

// Create persists a new stack bound to host/project. It fails if a stack with
// the same name already exists.
func (s *Store) Create(name, host, project, now string) (Stack, error) {
	if err := ValidateName(name); err != nil {
		return Stack{}, err
	}
	if project == "" || host == "" {
		return Stack{}, errors.New("stackstore: host and project are required")
	}
	lower := strings.ToLower(name)
	if _, err := os.Stat(s.path(lower)); err == nil {
		return Stack{}, ErrAlreadyExists
	}
	stack := Stack{
		Name: lower, Host: host, Project: project,
		MemberIIDs: []int{}, CreatedAt: now, UpdatedAt: now,
	}
	if err := s.write(stack); err != nil {
		return Stack{}, err
	}
	return stack, nil
}

// Get returns the stack with the given name.
func (s *Store) Get(name string) (Stack, error) {
	if err := ValidateName(name); err != nil {
		return Stack{}, err
	}
	data, err := os.ReadFile(s.path(strings.ToLower(name)))
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return Stack{}, ErrNotFound
		}
		return Stack{}, err
	}
	var stack Stack
	if err := json.Unmarshal(data, &stack); err != nil {
		return Stack{}, fmt.Errorf("stackstore: decode %s: %w", name, err)
	}
	return stack, nil
}

// AddMembers appends iids to the named stack, deduplicating against the
// existing membership and preserving existing order.
func (s *Store) AddMembers(name string, iids []int, now string) (Stack, error) {
	stack, err := s.Get(name)
	if err != nil {
		return Stack{}, err
	}
	seen := make(map[int]bool, len(stack.MemberIIDs))
	for _, iid := range stack.MemberIIDs {
		seen[iid] = true
	}
	for _, iid := range iids {
		if iid <= 0 {
			return Stack{}, fmt.Errorf("stackstore: invalid MR IID %d", iid)
		}
		if !seen[iid] {
			seen[iid] = true
			stack.MemberIIDs = append(stack.MemberIIDs, iid)
		}
	}
	stack.UpdatedAt = now
	if err := s.write(stack); err != nil {
		return Stack{}, err
	}
	return stack, nil
}

// RemoveMembers removes iids from the named stack. Missing IIDs are ignored.
func (s *Store) RemoveMembers(name string, iids []int, now string) (Stack, error) {
	stack, err := s.Get(name)
	if err != nil {
		return Stack{}, err
	}
	remove := make(map[int]bool, len(iids))
	for _, iid := range iids {
		remove[iid] = true
	}
	filtered := stack.MemberIIDs[:0]
	for _, iid := range stack.MemberIIDs {
		if !remove[iid] {
			filtered = append(filtered, iid)
		}
	}
	stack.MemberIIDs = filtered
	stack.UpdatedAt = now
	if err := s.write(stack); err != nil {
		return Stack{}, err
	}
	return stack, nil
}

// List returns every stack in the store sorted by name.
func (s *Store) List() ([]Stack, error) {
	entries, err := os.ReadDir(s.dir)
	if err != nil {
		return nil, err
	}
	var out []Stack
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		data, err := os.ReadFile(filepath.Join(s.dir, entry.Name()))
		if err != nil {
			continue
		}
		var stack Stack
		if err := json.Unmarshal(data, &stack); err != nil {
			continue
		}
		out = append(out, stack)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

// Delete removes the named stack. A missing stack is reported as ErrNotFound.
func (s *Store) Delete(name string) error {
	if err := ValidateName(name); err != nil {
		return err
	}
	err := os.Remove(s.path(strings.ToLower(name)))
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return ErrNotFound
		}
		return err
	}
	return nil
}

// write atomically replaces the stack's file via temp+rename with 0o600 perms.
func (s *Store) write(stack Stack) error {
	data, err := json.MarshalIndent(stack, "", "  ")
	if err != nil {
		return err
	}
	target := s.path(stack.Name)
	tmp, err := os.CreateTemp(s.dir, ".mrstack-*")
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
