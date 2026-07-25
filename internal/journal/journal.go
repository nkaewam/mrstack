// Package journal owns mrstack's local observational and recovery database.
// Domain payloads are stored as versioned JSON, while identities and state
// transitions remain queryable columns with database-enforced invariants.
package journal

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	_ "modernc.org/sqlite"
)

const schemaVersion = 1

var (
	ErrOperationInProgress = errors.New("operation already in progress")
	ErrConcurrentUpdate    = errors.New("session changed concurrently")
	ErrNotFound            = errors.New("journal record not found")
)

type Clock func() time.Time

type Journal struct {
	db    *sql.DB
	clock Clock
}

func Open(path string, clock Clock) (*Journal, error) {
	if clock == nil {
		clock = time.Now
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, err
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, err
	}
	// A process serializes its short write transitions through one connection.
	// WAL still permits readers in other processes while a mutation is paused,
	// and avoids SQLite's per-connection PRAGMA drift.
	db.SetMaxOpenConns(1)
	j := &Journal{db: db, clock: clock}
	if err := j.initialize(context.Background()); err != nil {
		db.Close()
		return nil, err
	}
	return j, nil
}

func (j *Journal) initialize(ctx context.Context) error {
	statements := []string{
		`PRAGMA journal_mode=WAL`,
		`PRAGMA foreign_keys=ON`,
		`PRAGMA busy_timeout=5000`,
		`CREATE TABLE IF NOT EXISTS metadata (
			key TEXT PRIMARY KEY,
			value TEXT NOT NULL
		)`,
		`CREATE TABLE IF NOT EXISTS tracked_stacks (
			stack_id TEXT PRIMARY KEY,
			project_key TEXT NOT NULL,
			alias TEXT,
			created_at TEXT NOT NULL,
			last_seen_at TEXT NOT NULL
		)`,
		`CREATE INDEX IF NOT EXISTS tracked_stacks_project ON tracked_stacks(project_key)`,
		`CREATE TABLE IF NOT EXISTS observations (
			observation_id TEXT PRIMARY KEY,
			stack_id TEXT NOT NULL REFERENCES tracked_stacks(stack_id),
			snapshot_id TEXT NOT NULL UNIQUE,
			observed_at TEXT NOT NULL,
			last_seen_at TEXT NOT NULL,
			disposition TEXT NOT NULL,
			payload BLOB NOT NULL
		)`,
		`CREATE INDEX IF NOT EXISTS observations_stack_time ON observations(stack_id, observed_at DESC)`,
		`CREATE TABLE IF NOT EXISTS sessions (
			session_id TEXT PRIMARY KEY,
			project_key TEXT NOT NULL,
			snapshot_id TEXT NOT NULL,
			state TEXT NOT NULL,
			revision INTEGER NOT NULL DEFAULT 1,
			old_refs BLOB NOT NULL,
			new_refs BLOB NOT NULL,
			payload BLOB NOT NULL,
			created_at TEXT NOT NULL,
			updated_at TEXT NOT NULL
		)`,
		`CREATE UNIQUE INDEX IF NOT EXISTS one_active_session_per_project
		 ON sessions(project_key)
		 WHERE state IN ('preparing','replaying','rebase_conflict','empty_commit',
		 'publication_ready','publication_pending_reconcile','retarget_pending',
		 'indeterminate_publication')`,
	}
	for _, statement := range statements {
		if _, err := j.db.ExecContext(ctx, statement); err != nil {
			return fmt.Errorf("initialize journal: %w", err)
		}
	}
	if err := j.ensureObservationLastSeen(ctx); err != nil {
		return err
	}
	_, err := j.db.ExecContext(ctx,
		`INSERT INTO metadata(key,value) VALUES('schema_version',?)
		 ON CONFLICT(key) DO UPDATE SET value=excluded.value`, schemaVersion)
	return err
}

func (j *Journal) ensureObservationLastSeen(ctx context.Context) error {
	rows, err := j.db.QueryContext(ctx, `PRAGMA table_info(observations)`)
	if err != nil {
		return err
	}
	found := false
	for rows.Next() {
		var cid, notNull, primaryKey int
		var name, typ string
		var defaultValue any
		if err := rows.Scan(&cid, &name, &typ, &notNull, &defaultValue, &primaryKey); err != nil {
			rows.Close()
			return err
		}
		found = found || name == "last_seen_at"
	}
	if err := rows.Close(); err != nil {
		return err
	}
	if found {
		return nil
	}
	if _, err := j.db.ExecContext(ctx, `ALTER TABLE observations ADD COLUMN last_seen_at TEXT`); err != nil {
		return err
	}
	_, err = j.db.ExecContext(ctx, `UPDATE observations SET last_seen_at=observed_at WHERE last_seen_at IS NULL`)
	return err
}

func (j *Journal) Close() error { return j.db.Close() }

func (j *Journal) WALMode(ctx context.Context) (bool, error) {
	var mode string
	if err := j.db.QueryRowContext(ctx, `PRAGMA journal_mode`).Scan(&mode); err != nil {
		return false, err
	}
	return mode == "wal", nil
}

type Observation struct {
	ObservationID string
	StackID       string
	ProjectKey    string
	SnapshotID    string
	Disposition   string
	Payload       json.RawMessage
}

func (j *Journal) RecordObservation(ctx context.Context, observation Observation) error {
	if observation.ObservationID == "" || observation.StackID == "" ||
		observation.ProjectKey == "" || observation.SnapshotID == "" ||
		!json.Valid(observation.Payload) {
		return errors.New("invalid observation")
	}
	now := j.clock().UTC().Format(time.RFC3339Nano)
	tx, err := j.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `INSERT INTO tracked_stacks
		(stack_id, project_key, created_at, last_seen_at) VALUES(?,?,?,?)
		ON CONFLICT(stack_id) DO UPDATE SET last_seen_at=excluded.last_seen_at`,
		observation.StackID, observation.ProjectKey, now, now); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO observations
		(observation_id,stack_id,snapshot_id,observed_at,last_seen_at,disposition,payload)
		VALUES(?,?,?,?,?,?,?)
		ON CONFLICT(snapshot_id) DO UPDATE SET last_seen_at=excluded.last_seen_at`,
		observation.ObservationID, observation.StackID, observation.SnapshotID, now, now,
		observation.Disposition, []byte(observation.Payload)); err != nil {
		return err
	}
	return tx.Commit()
}

type Session struct {
	ID         string
	ProjectKey string
	SnapshotID string
	State      string
	Revision   int64
	OldRefs    map[string]string
	NewRefs    map[string]string
	Payload    json.RawMessage
	CreatedAt  time.Time
	UpdatedAt  time.Time
}

var terminalStates = map[string]bool{
	"completed": true, "aborted": true, "invalidated": true, "abandoned": true,
}

func validState(state string) bool {
	switch state {
	case "preparing", "replaying", "rebase_conflict", "empty_commit",
		"publication_ready", "publication_pending_reconcile", "retarget_pending",
		"indeterminate_publication", "completed", "aborted", "invalidated", "abandoned":
		return true
	default:
		return false
	}
}

func (j *Journal) BeginSession(ctx context.Context, session Session) error {
	if session.ID == "" || session.ProjectKey == "" || session.SnapshotID == "" ||
		session.State != "preparing" || !json.Valid(session.Payload) {
		return errors.New("invalid new session")
	}
	oldRefs, err := json.Marshal(session.OldRefs)
	if err != nil {
		return err
	}
	newRefs, err := json.Marshal(session.NewRefs)
	if err != nil {
		return err
	}
	now := j.clock().UTC().Format(time.RFC3339Nano)
	_, err = j.db.ExecContext(ctx, `INSERT INTO sessions
		(session_id,project_key,snapshot_id,state,old_refs,new_refs,payload,created_at,updated_at)
		VALUES(?,?,?,?,?,?,?,?,?)`, session.ID, session.ProjectKey, session.SnapshotID,
		session.State, oldRefs, newRefs, []byte(session.Payload), now, now)
	if err != nil && isUniqueConstraint(err) {
		return ErrOperationInProgress
	}
	return err
}

func (j *Journal) Session(ctx context.Context, id string) (Session, error) {
	var s Session
	var oldRefs, newRefs, payload []byte
	var created, updated string
	err := j.db.QueryRowContext(ctx, `SELECT session_id,project_key,snapshot_id,state,
		revision,old_refs,new_refs,payload,created_at,updated_at FROM sessions
		WHERE session_id=?`, id).Scan(&s.ID, &s.ProjectKey, &s.SnapshotID, &s.State,
		&s.Revision, &oldRefs, &newRefs, &payload, &created, &updated)
	if errors.Is(err, sql.ErrNoRows) {
		return Session{}, ErrNotFound
	}
	if err != nil {
		return Session{}, err
	}
	if err := json.Unmarshal(oldRefs, &s.OldRefs); err != nil {
		return Session{}, err
	}
	if err := json.Unmarshal(newRefs, &s.NewRefs); err != nil {
		return Session{}, err
	}
	s.Payload = append([]byte(nil), payload...)
	s.CreatedAt, _ = time.Parse(time.RFC3339Nano, created)
	s.UpdatedAt, _ = time.Parse(time.RFC3339Nano, updated)
	return s, nil
}

func (j *Journal) ActiveSession(ctx context.Context, projectKey string) (Session, error) {
	var id string
	err := j.db.QueryRowContext(ctx, `SELECT session_id FROM sessions
		WHERE project_key=? AND state NOT IN ('completed','aborted','invalidated','abandoned')
		ORDER BY created_at LIMIT 1`, projectKey).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return Session{}, ErrNotFound
	}
	if err != nil {
		return Session{}, err
	}
	return j.Session(ctx, id)
}

// Transition performs an optimistic state transition. The caller supplies the
// revision it read; stale writers cannot overwrite a newer durable state.
func (j *Journal) Transition(ctx context.Context, id string, expectedRevision int64, state string, payload json.RawMessage) (Session, error) {
	if !validState(state) || !json.Valid(payload) {
		return Session{}, errors.New("invalid session transition")
	}
	current, err := j.Session(ctx, id)
	if err != nil {
		return Session{}, err
	}
	if current.Revision != expectedRevision {
		return Session{}, ErrConcurrentUpdate
	}
	if !allowedTransition(current.State, state) {
		return Session{}, fmt.Errorf("invalid session transition %s -> %s", current.State, state)
	}
	now := j.clock().UTC().Format(time.RFC3339Nano)
	result, err := j.db.ExecContext(ctx, `UPDATE sessions SET state=?,payload=?,
		revision=revision+1,updated_at=? WHERE session_id=? AND revision=?`,
		state, []byte(payload), now, id, expectedRevision)
	if err != nil {
		return Session{}, err
	}
	n, err := result.RowsAffected()
	if err != nil {
		return Session{}, err
	}
	if n == 0 {
		if _, err := j.Session(ctx, id); errors.Is(err, ErrNotFound) {
			return Session{}, ErrNotFound
		}
		return Session{}, ErrConcurrentUpdate
	}
	return j.Session(ctx, id)
}

// PreparePublication atomically persists the complete proposed ref map before
// making the session publishable. A crash can therefore never leave a
// publication_ready session without both old and new recovery maps.
func (j *Journal) PreparePublication(ctx context.Context, id string, expectedRevision int64, newRefs map[string]string, payload json.RawMessage) (Session, error) {
	if len(newRefs) == 0 || !json.Valid(payload) {
		return Session{}, errors.New("invalid publication map")
	}
	current, err := j.Session(ctx, id)
	if err != nil {
		return Session{}, err
	}
	if current.Revision != expectedRevision {
		return Session{}, ErrConcurrentUpdate
	}
	if current.State != "replaying" {
		return Session{}, fmt.Errorf("publication can only be prepared from replaying, got %s", current.State)
	}
	if len(current.OldRefs) != len(newRefs) {
		return Session{}, errors.New("old and proposed ref maps differ in size")
	}
	for branch, oldOID := range current.OldRefs {
		newOID, ok := newRefs[branch]
		if !ok || oldOID == "" || newOID == "" {
			return Session{}, errors.New("old and proposed ref maps differ in keys or contain empty revisions")
		}
	}
	encoded, err := json.Marshal(newRefs)
	if err != nil {
		return Session{}, err
	}
	now := j.clock().UTC().Format(time.RFC3339Nano)
	result, err := j.db.ExecContext(ctx, `UPDATE sessions SET state='publication_ready',
		new_refs=?,payload=?,revision=revision+1,updated_at=?
		WHERE session_id=? AND revision=? AND state='replaying'`,
		encoded, []byte(payload), now, id, expectedRevision)
	if err != nil {
		return Session{}, err
	}
	changed, err := result.RowsAffected()
	if err != nil {
		return Session{}, err
	}
	if changed == 0 {
		return Session{}, ErrConcurrentUpdate
	}
	return j.Session(ctx, id)
}

func allowedTransition(from, to string) bool {
	allowed := map[string]map[string]bool{
		"preparing":                     {"replaying": true, "invalidated": true, "aborted": true},
		"replaying":                     {"rebase_conflict": true, "empty_commit": true, "publication_ready": true, "invalidated": true, "aborted": true},
		"rebase_conflict":               {"replaying": true, "invalidated": true, "aborted": true},
		"empty_commit":                  {"replaying": true, "invalidated": true, "aborted": true},
		"publication_ready":             {"publication_pending_reconcile": true, "invalidated": true, "aborted": true},
		"publication_pending_reconcile": {"publication_ready": true, "retarget_pending": true, "completed": true, "indeterminate_publication": true},
		"retarget_pending":              {"completed": true, "invalidated": true},
		"indeterminate_publication":     {"publication_ready": true, "retarget_pending": true, "completed": true, "abandoned": true},
		"invalidated":                   {"aborted": true},
	}
	return allowed[from][to]
}

type RefMapResult string

const (
	RefsAllOld        RefMapResult = "all_old"
	RefsAllNew        RefMapResult = "all_new"
	RefsIndeterminate RefMapResult = "indeterminate"
)

func ReconcileRefs(oldRefs, newRefs, actual map[string]string) RefMapResult {
	if sameRefMap(oldRefs, actual) {
		return RefsAllOld
	}
	if sameRefMap(newRefs, actual) {
		return RefsAllNew
	}
	return RefsIndeterminate
}

func sameRefMap(expected, actual map[string]string) bool {
	if len(expected) != len(actual) {
		return false
	}
	for ref, oid := range expected {
		if actual[ref] != oid {
			return false
		}
	}
	return true
}

func isUniqueConstraint(err error) bool {
	return err != nil && (contains(err.Error(), "UNIQUE constraint failed") ||
		contains(err.Error(), "constraint failed"))
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
