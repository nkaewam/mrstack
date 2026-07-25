package journal

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"testing"
	"time"
)

func TestHistoryPaginationAliasAndOpaqueCursor(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 7, 25, 10, 0, 0, 0, time.UTC)
	j, err := Open(filepath.Join(t.TempDir(), "journal.db"), func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	defer j.Close()
	ctx := context.Background()
	for i := 1; i <= 5; i++ {
		if err := j.RecordObservation(ctx, Observation{
			ObservationID: "obs_" + string(rune('0'+i)), StackID: "stk_1", ProjectKey: "p",
			SnapshotID: "snap_" + string(rune('0'+i)), Disposition: "ready",
			Payload: json.RawMessage(`{"safe":true}`),
		}); err != nil {
			t.Fatal(err)
		}
		now = now.Add(time.Minute)
	}
	alias := "checkout work"
	if err := j.SetAlias(ctx, "stk_1", &alias); err != nil {
		t.Fatal(err)
	}
	tracked, err := j.TrackedStacks(ctx, "p")
	if err != nil || len(tracked) != 1 || tracked[0].StackID != "stk_1" ||
		tracked[0].Alias == nil || *tracked[0].Alias != alias {
		t.Fatalf("tracked=%#v err=%v", tracked, err)
	}
	if count, err := j.ObservationCount(ctx, "stk_1"); err != nil || count != 5 {
		t.Fatalf("count=%d err=%v", count, err)
	}
	first, err := j.History(ctx, "stk_1", 2, "")
	if err != nil {
		t.Fatal(err)
	}
	if first.Alias == nil || *first.Alias != alias || len(first.Records) != 2 ||
		first.Records[0].SnapshotID != "snap_5" || first.NextCursor == "" {
		t.Fatalf("first page=%#v", first)
	}
	second, err := j.History(ctx, "stk_1", 2, first.NextCursor)
	if err != nil {
		t.Fatal(err)
	}
	if len(second.Records) != 2 || second.Records[0].SnapshotID != "snap_3" ||
		second.NextCursor == first.NextCursor {
		t.Fatalf("second page=%#v", second)
	}
	third, err := j.History(ctx, "stk_1", 2, second.NextCursor)
	if err != nil {
		t.Fatal(err)
	}
	if len(third.Records) != 1 || third.Records[0].SnapshotID != "snap_1" || third.NextCursor != "" {
		t.Fatalf("third page=%#v", third)
	}
	if _, err := j.History(ctx, "stk_1", 2, "not/base64!"); err == nil {
		t.Fatal("invalid cursor accepted")
	}
	if err := j.SetAlias(ctx, "stk_1", nil); err != nil {
		t.Fatal(err)
	}
	cleared, err := j.History(ctx, "stk_1", 1, "")
	if err != nil || cleared.Alias != nil {
		t.Fatalf("alias not cleared: page=%#v err=%v", cleared, err)
	}
}

func TestHistoryInputAndMissingIdentity(t *testing.T) {
	t.Parallel()
	j := newTestJournal(t)
	ctx := context.Background()
	if _, err := j.History(ctx, "", 50, ""); err == nil {
		t.Fatal("empty stack accepted")
	}
	if _, err := j.History(ctx, "missing", 201, ""); err == nil {
		t.Fatal("oversized limit accepted")
	}
	if _, err := j.History(ctx, "missing", 50, ""); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing stack error=%v", err)
	}
	if err := j.SetAlias(ctx, "missing", nil); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing alias error=%v", err)
	}
}

func TestPrunePreservesNewestIdentityAndActiveSession(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC)
	j, err := Open(filepath.Join(t.TempDir(), "journal.db"), func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	defer j.Close()
	ctx := context.Background()
	for _, stackID := range []string{"a", "b"} {
		for i := 0; i < 3; i++ {
			if err := j.RecordObservation(ctx, Observation{
				ObservationID: stackID + "_obs_" + string(rune('0'+i)),
				StackID:       stackID, ProjectKey: "p/" + stackID,
				SnapshotID:  stackID + "_snap_" + string(rune('0'+i)),
				Disposition: "ready", Payload: json.RawMessage(`{}`),
			}); err != nil {
				t.Fatal(err)
			}
			now = now.Add(time.Hour)
		}
	}
	if err := j.BeginSession(ctx, Session{
		ID: "active", ProjectKey: "p/a", SnapshotID: "a_snap_0", State: "preparing",
		OldRefs: map[string]string{"a": "old"}, NewRefs: map[string]string{},
		Payload: json.RawMessage(`{}`),
	}); err != nil {
		t.Fatal(err)
	}
	removed, err := j.PruneObservations(ctx, now.Add(time.Hour), "a")
	if err != nil {
		t.Fatal(err)
	}
	if removed != 2 {
		t.Fatalf("removed=%d want 2", removed)
	}
	pageA, err := j.History(ctx, "a", 50, "")
	if err != nil || len(pageA.Records) != 1 || pageA.Records[0].SnapshotID != "a_snap_2" {
		t.Fatalf("a page=%#v err=%v", pageA, err)
	}
	pageB, err := j.History(ctx, "b", 50, "")
	if err != nil || len(pageB.Records) != 3 {
		t.Fatalf("b page=%#v err=%v", pageB, err)
	}
	if session, err := j.ActiveSession(ctx, "p/a"); err != nil || session.ID != "active" {
		t.Fatalf("active session changed: %#v err=%v", session, err)
	}
}
