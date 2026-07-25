package journal

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

func newTestJournal(t *testing.T) *Journal {
	t.Helper()
	now := time.Date(2026, 7, 25, 12, 0, 0, 0, time.UTC)
	j, err := Open(filepath.Join(t.TempDir(), "journal.db"), func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = j.Close() })
	return j
}

func TestJournalUsesWALAndPersistsObservation(t *testing.T) {
	t.Parallel()
	j := newTestJournal(t)
	wal, err := j.WALMode(context.Background())
	if err != nil || !wal {
		t.Fatalf("WAL mode=%v err=%v", wal, err)
	}
	err = j.RecordObservation(context.Background(), Observation{
		ObservationID: "obs_1", StackID: "stk_1", ProjectKey: "host/team/repo",
		SnapshotID: "snap_1", Disposition: "ready", Payload: json.RawMessage(`{"safe":true}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	var count int
	if err := j.db.QueryRow(`SELECT count(*) FROM observations`).Scan(&count); err != nil || count != 1 {
		t.Fatalf("count=%d err=%v", count, err)
	}
}

func TestRepeatedSnapshotUpdatesOnlyLastSeen(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 7, 25, 12, 0, 0, 0, time.UTC)
	j, err := Open(filepath.Join(t.TempDir(), "journal.db"), func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	defer j.Close()
	ctx := context.Background()
	first := Observation{
		ObservationID: "obs_1", StackID: "stk_1", ProjectKey: "p",
		SnapshotID: "snap_same", Disposition: "ready", Payload: json.RawMessage(`{"original":true}`),
	}
	if err := j.RecordObservation(ctx, first); err != nil {
		t.Fatal(err)
	}
	now = now.Add(5 * time.Minute)
	second := first
	second.ObservationID = "obs_should_not_replace"
	second.Payload = json.RawMessage(`{"replacement":true}`)
	if err := j.RecordObservation(ctx, second); err != nil {
		t.Fatal(err)
	}
	var count int
	var observationID, observedAt, lastSeenAt string
	var payload []byte
	if err := j.db.QueryRowContext(ctx, `SELECT count(*),observation_id,observed_at,last_seen_at,payload
		FROM observations WHERE snapshot_id=?`, first.SnapshotID).
		Scan(&count, &observationID, &observedAt, &lastSeenAt, &payload); err != nil {
		t.Fatal(err)
	}
	if count != 1 || observationID != first.ObservationID ||
		observedAt == lastSeenAt || string(payload) != string(first.Payload) {
		t.Fatalf("count=%d id=%s observed=%s last=%s payload=%s",
			count, observationID, observedAt, lastSeenAt, payload)
	}
}

func TestFindingIntervalsRemainStableThenMintOnRecurrence(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 7, 25, 12, 0, 0, 0, time.UTC)
	j, err := Open(filepath.Join(t.TempDir(), "journal.db"), func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	defer j.Close()
	ctx := context.Background()
	first, err := j.StabilizeFindings(ctx, "stk_1", "host/project", []FindingCandidate{{
		SemanticKey: "pipeline_failed/member/1", ProposedID: "fnd_first",
	}})
	if err != nil {
		t.Fatal(err)
	}
	now = now.Add(5 * time.Minute)
	repeated, err := j.StabilizeFindings(ctx, "stk_1", "host/project", []FindingCandidate{{
		SemanticKey: "pipeline_failed/member/1", ProposedID: "fnd_unused",
	}})
	if err != nil {
		t.Fatal(err)
	}
	if repeated[0].FindingID != first[0].FindingID ||
		repeated[0].FirstSeenAt != first[0].FirstSeenAt ||
		repeated[0].LastSeenAt == first[0].LastSeenAt {
		t.Fatalf("active interval was not preserved: first=%+v repeated=%+v", first, repeated)
	}
	now = now.Add(5 * time.Minute)
	if _, err := j.StabilizeFindings(ctx, "stk_1", "host/project", nil); err != nil {
		t.Fatal(err)
	}
	now = now.Add(5 * time.Minute)
	recurred, err := j.StabilizeFindings(ctx, "stk_1", "host/project", []FindingCandidate{{
		SemanticKey: "pipeline_failed/member/1", ProposedID: "fnd_recurred",
	}})
	if err != nil {
		t.Fatal(err)
	}
	if recurred[0].FindingID == first[0].FindingID ||
		recurred[0].FirstSeenAt == first[0].FirstSeenAt {
		t.Fatalf("recurrence reused resolved identity: first=%+v recurred=%+v", first, recurred)
	}
	var active, resolved int
	if err := j.db.QueryRowContext(ctx, `SELECT
		sum(CASE WHEN resolved_at IS NULL THEN 1 ELSE 0 END),
		sum(CASE WHEN resolved_at IS NOT NULL THEN 1 ELSE 0 END)
		FROM finding_intervals WHERE stack_id='stk_1'`).Scan(&active, &resolved); err != nil {
		t.Fatal(err)
	}
	if active != 1 || resolved != 1 {
		t.Fatalf("active=%d resolved=%d", active, resolved)
	}
}

func TestOnlyOneActiveSessionPerProjectUnderConcurrency(t *testing.T) {
	t.Parallel()
	j := newTestJournal(t)
	const racers = 12
	var wg sync.WaitGroup
	start := make(chan struct{})
	results := make(chan error, racers)
	for i := 0; i < racers; i++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			<-start
			results <- j.BeginSession(context.Background(), Session{
				ID: "session_" + string(rune('a'+n)), ProjectKey: "host/team/repo",
				SnapshotID: "snap", State: "preparing",
				OldRefs: map[string]string{"a": "old"}, NewRefs: map[string]string{"a": "new"},
				Payload: json.RawMessage(`{}`),
			})
		}(i)
	}
	close(start)
	wg.Wait()
	close(results)
	var won, blocked int
	for err := range results {
		switch {
		case err == nil:
			won++
		case errors.Is(err, ErrOperationInProgress):
			blocked++
		default:
			t.Fatalf("unexpected error: %v", err)
		}
	}
	if won != 1 || blocked != racers-1 {
		t.Fatalf("won=%d blocked=%d", won, blocked)
	}
}

func TestTransitionIsDurableValidatedAndOptimistic(t *testing.T) {
	t.Parallel()
	j := newTestJournal(t)
	ctx := context.Background()
	if err := j.BeginSession(ctx, Session{
		ID: "ses_1", ProjectKey: "host/team/repo", SnapshotID: "snap",
		State: "preparing", OldRefs: map[string]string{"a": "old"},
		NewRefs: map[string]string{"a": "new"}, Payload: json.RawMessage(`{"step":0}`),
	}); err != nil {
		t.Fatal(err)
	}
	next, err := j.Transition(ctx, "ses_1", 1, "replaying", json.RawMessage(`{"step":1}`))
	if err != nil || next.Revision != 2 || next.State != "replaying" {
		t.Fatalf("next=%#v err=%v", next, err)
	}
	if _, err := j.Transition(ctx, "ses_1", 1, "aborted", json.RawMessage(`{}`)); !errors.Is(err, ErrConcurrentUpdate) {
		t.Fatalf("expected concurrent update, got %v", err)
	}
	if _, err := j.Transition(ctx, "ses_1", 2, "completed", json.RawMessage(`{}`)); err == nil {
		t.Fatal("invalid state-machine edge succeeded")
	}
}

func TestTerminalSessionReleasesProjectSlot(t *testing.T) {
	t.Parallel()
	j := newTestJournal(t)
	ctx := context.Background()
	first := Session{ID: "one", ProjectKey: "p", SnapshotID: "s1", State: "preparing",
		OldRefs: map[string]string{}, NewRefs: map[string]string{}, Payload: json.RawMessage(`{}`)}
	if err := j.BeginSession(ctx, first); err != nil {
		t.Fatal(err)
	}
	if _, err := j.Transition(ctx, "one", 1, "aborted", json.RawMessage(`{}`)); err != nil {
		t.Fatal(err)
	}
	second := first
	second.ID, second.SnapshotID = "two", "s2"
	if err := j.BeginSession(ctx, second); err != nil {
		t.Fatalf("terminal session retained slot: %v", err)
	}
}

func TestPausedSessionSurvivesCloseAndReopen(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "journal.db")
	now := time.Date(2026, 7, 25, 12, 0, 0, 0, time.UTC)
	j, err := Open(path, func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if err := j.BeginSession(ctx, Session{
		ID: "crash_session", ProjectKey: "host/team/repo", SnapshotID: "snap",
		State: "preparing", OldRefs: map[string]string{"a": "old"},
		NewRefs: map[string]string{"a": "new"}, Payload: json.RawMessage(`{"phase":"before-replay"}`),
	}); err != nil {
		t.Fatal(err)
	}
	replaying, err := j.Transition(ctx, "crash_session", 1, "replaying", json.RawMessage(`{"commit":"abc"}`))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := j.Transition(ctx, "crash_session", replaying.Revision, "rebase_conflict", json.RawMessage(`{"paths":["file"]}`)); err != nil {
		t.Fatal(err)
	}
	if err := j.Close(); err != nil {
		t.Fatal(err)
	}

	reopened, err := Open(path, func() time.Time { return now.Add(time.Minute) })
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	session, err := reopened.ActiveSession(ctx, "host/team/repo")
	if err != nil {
		t.Fatal(err)
	}
	if session.ID != "crash_session" || session.State != "rebase_conflict" || session.Revision != 3 {
		t.Fatalf("recovered session=%#v", session)
	}
	if string(session.Payload) != `{"paths":["file"]}` {
		t.Fatalf("recovered payload=%s", session.Payload)
	}
}

func TestReconcileRefsRequiresCompleteExactMap(t *testing.T) {
	t.Parallel()
	old := map[string]string{"a": "1", "b": "2"}
	newMap := map[string]string{"a": "3", "b": "4"}
	cases := []struct {
		actual map[string]string
		want   RefMapResult
	}{
		{map[string]string{"a": "1", "b": "2"}, RefsAllOld},
		{map[string]string{"a": "3", "b": "4"}, RefsAllNew},
		{map[string]string{"a": "3", "b": "2"}, RefsIndeterminate},
		{map[string]string{"a": "1"}, RefsIndeterminate},
		{map[string]string{"a": "1", "b": "2", "c": "5"}, RefsIndeterminate},
	}
	for _, tc := range cases {
		if got := ReconcileRefs(old, newMap, tc.actual); got != tc.want {
			t.Errorf("actual=%v got=%s want=%s", tc.actual, got, tc.want)
		}
	}
}

func TestPreparePublicationPersistsCompleteMapsAtomically(t *testing.T) {
	t.Parallel()
	j := newTestJournal(t)
	ctx := context.Background()
	old := map[string]string{"a": "old-a", "b": "old-b"}
	if err := j.BeginSession(ctx, Session{
		ID: "publish", ProjectKey: "p", SnapshotID: "s", State: "preparing",
		OldRefs: old, NewRefs: map[string]string{}, Payload: json.RawMessage(`{}`),
	}); err != nil {
		t.Fatal(err)
	}
	replaying, err := j.Transition(ctx, "publish", 1, "replaying", json.RawMessage(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := j.PreparePublication(ctx, "publish", replaying.Revision,
		map[string]string{"a": "new-a"}, json.RawMessage(`{}`)); err == nil {
		t.Fatal("incomplete proposed map accepted")
	}
	prepared, err := j.PreparePublication(ctx, "publish", replaying.Revision,
		map[string]string{"a": "new-a", "b": "new-b"}, json.RawMessage(`{"ready":true}`))
	if err != nil {
		t.Fatal(err)
	}
	if prepared.State != "publication_ready" || prepared.Revision != 3 ||
		prepared.NewRefs["a"] != "new-a" || prepared.NewRefs["b"] != "new-b" {
		t.Fatalf("prepared=%#v", prepared)
	}
}

func TestTraceLikeDataIsNeverPersistedByObservationValidationBoundary(t *testing.T) {
	t.Parallel()
	j := newTestJournal(t)
	// Journal accepts only the caller's sanitized public observation. CI traces
	// have no column or method in this package; this regression check guards the
	// schema against accidentally adding one.
	rows, err := j.db.Query(`PRAGMA table_info(observations)`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	for rows.Next() {
		var cid, notNull, pk int
		var name, typ string
		var defaultValue any
		if err := rows.Scan(&cid, &name, &typ, &notNull, &defaultValue, &pk); err != nil {
			t.Fatal(err)
		}
		if name == "trace" || name == "job_log" {
			t.Fatalf("forbidden persisted trace column %q", name)
		}
	}
}
