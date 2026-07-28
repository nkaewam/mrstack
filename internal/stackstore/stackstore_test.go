package stackstore

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func newStore(t *testing.T) *Store {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "stacks")
	s, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func TestCreatePersistsStackAndRejectsDuplicates(t *testing.T) {
	t.Parallel()
	s := newStore(t)
	got, err := s.Create("web-migration", "gitlab.example", "group/project", "2026-07-28T10:00:00Z")
	if err != nil {
		t.Fatal(err)
	}
	if got.Name != "web-migration" || got.Host != "gitlab.example" || got.Project != "group/project" {
		t.Fatalf("unexpected stack: %+v", got)
	}
	if len(got.MemberIIDs) != 0 || got.CreatedAt == "" || got.UpdatedAt != got.CreatedAt {
		t.Fatalf("unexpected defaults: %+v", got)
	}

	if _, err := s.Create("web-migration", "gitlab.example", "group/project", "now"); !errors.Is(err, ErrAlreadyExists) {
		t.Fatalf("duplicate create must fail with ErrAlreadyExists, got %v", err)
	}

	// File is on disk with restrictive perms.
	info, err := os.Stat(filepath.Join(s.dir, "web-migration.json"))
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("stack file perms = %o, want 0600", info.Mode().Perm())
	}
}

func TestCreateLowercasesAndValidatesName(t *testing.T) {
	t.Parallel()
	s := newStore(t)
	for _, name := range []string{"Web-Migration", "WEB"} {
		if _, err := s.Create(name, "h", "p", "now"); err != nil {
			t.Fatalf("Create(%q) error = %v", name, err)
		}
	}
	// Names collapse to lowercase files; "WEB" and "Web-Migration" coexist.
	if _, err := s.Get("web-migration"); err != nil {
		t.Fatalf("lowercase lookup failed: %v", err)
	}
	bad := []string{"", "Web Migration", "../etc", "a/b", ".hidden", "-leading", "trailing-", "UPPER_OK", "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}
	for _, name := range bad {
		if _, err := s.Create(name, "h", "p", "now"); !errors.Is(err, ErrInvalidName) {
			t.Fatalf("Create(%q) must fail with ErrInvalidName, got %v", name, err)
		}
	}
}

func TestAddMembersDeduplicatesAndPreservesOrder(t *testing.T) {
	t.Parallel()
	s := newStore(t)
	if _, err := s.Create("s", "h", "p", "t0"); err != nil {
		t.Fatal(err)
	}
	got, err := s.AddMembers("s", []int{3061, 3062, 3063}, "t1")
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got.MemberIIDs, []int{3061, 3062, 3063}) {
		t.Fatalf("after add: %v", got.MemberIIDs)
	}
	if got.UpdatedAt != "t1" {
		t.Fatalf("UpdatedAt not bumped: %q", got.UpdatedAt)
	}
	// Adding a mix of existing and new preserves order and dedupes.
	got, err = s.AddMembers("s", []int{3063, 3064}, "t2")
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got.MemberIIDs, []int{3061, 3062, 3063, 3064}) {
		t.Fatalf("after dedup add: %v", got.MemberIIDs)
	}
	if _, err := s.AddMembers("s", []int{0}, "t3"); err == nil {
		// non-positive IID must be rejected
		t.Fatalf("adding IID 0 must error")
	}
}

func TestAddMembersFailsForUnknownStack(t *testing.T) {
	t.Parallel()
	s := newStore(t)
	if _, err := s.AddMembers("missing", []int{1}, "now"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}
}

func TestRemoveMembers(t *testing.T) {
	t.Parallel()
	s := newStore(t)
	if _, err := s.Create("s", "h", "p", "t0"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.AddMembers("s", []int{1, 2, 3}, "t1"); err != nil {
		t.Fatal(err)
	}
	got, err := s.RemoveMembers("s", []int{2, 99}, "t2")
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got.MemberIIDs, []int{1, 3}) {
		t.Fatalf("after remove: %v", got.MemberIIDs)
	}
}

func TestListIsSortedByName(t *testing.T) {
	t.Parallel()
	s := newStore(t)
	for _, name := range []string{"charlie", "alpha", "bravo"} {
		if _, err := s.Create(name, "h", "p", "now"); err != nil {
			t.Fatal(err)
		}
	}
	out, err := s.List()
	if err != nil {
		t.Fatal(err)
	}
	got := []string{out[0].Name, out[1].Name, out[2].Name}
	want := []string{"alpha", "bravo", "charlie"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("List order = %v, want %v", got, want)
	}
}

func TestDeleteRemovesAndReportsMissing(t *testing.T) {
	t.Parallel()
	s := newStore(t)
	if _, err := s.Create("s", "h", "p", "now"); err != nil {
		t.Fatal(err)
	}
	if err := s.Delete("s"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Get("s"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Get after Delete must be ErrNotFound, got %v", err)
	}
	if err := s.Delete("s"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Delete missing must be ErrNotFound, got %v", err)
	}
}

func TestOpenCreatesDir(t *testing.T) {
	t.Parallel()
	dir := filepath.Join(t.TempDir(), "nested", "stacks")
	s, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(dir)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o700 {
		t.Fatalf("dir perms = %o, want 0700", info.Mode().Perm())
	}
	_ = s
}
