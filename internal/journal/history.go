package journal

import (
	"context"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

type ObservationRecord struct {
	ObservationID string
	StackID       string
	SnapshotID    string
	ObservedAt    time.Time
	LastSeenAt    time.Time
	Disposition   string
	Payload       json.RawMessage
}

type HistoryPage struct {
	StackID    string
	Alias      *string
	Records    []ObservationRecord
	NextCursor string
}

type TrackedStack struct {
	StackID    string
	Alias      *string
	ProjectKey string
	LastSeenAt time.Time
}

type historyCursor struct {
	ObservedAt string `json:"observed_at"`
	ID         string `json:"id"`
}

func encodeCursor(record ObservationRecord) string {
	value, _ := json.Marshal(historyCursor{
		ObservedAt: record.ObservedAt.UTC().Format(time.RFC3339Nano),
		ID:         record.ObservationID,
	})
	return base64.RawURLEncoding.EncodeToString(value)
}

func decodeCursor(value string) (historyCursor, error) {
	if value == "" {
		return historyCursor{}, nil
	}
	data, err := base64.RawURLEncoding.DecodeString(value)
	if err != nil || len(data) > 1024 {
		return historyCursor{}, errors.New("invalid history cursor")
	}
	var cursor historyCursor
	if err := json.Unmarshal(data, &cursor); err != nil || cursor.ID == "" {
		return historyCursor{}, errors.New("invalid history cursor")
	}
	if _, err := time.Parse(time.RFC3339Nano, cursor.ObservedAt); err != nil {
		return historyCursor{}, errors.New("invalid history cursor")
	}
	return cursor, nil
}

func (j *Journal) History(ctx context.Context, stackID string, limit int, cursorValue string) (HistoryPage, error) {
	if stackID == "" || limit < 1 || limit > 200 {
		return HistoryPage{}, errors.New("invalid history query")
	}
	cursor, err := decodeCursor(cursorValue)
	if err != nil {
		return HistoryPage{}, err
	}
	page := HistoryPage{StackID: stackID, Records: []ObservationRecord{}}
	if err := j.db.QueryRowContext(ctx, `SELECT alias FROM tracked_stacks WHERE stack_id=?`, stackID).
		Scan(&page.Alias); errors.Is(err, sql.ErrNoRows) {
		return HistoryPage{}, ErrNotFound
	} else if err != nil {
		return HistoryPage{}, err
	}
	query := `SELECT observation_id,stack_id,snapshot_id,observed_at,last_seen_at,disposition,payload
		FROM observations WHERE stack_id=?`
	args := []any{stackID}
	if cursor.ID != "" {
		query += ` AND (observed_at < ? OR (observed_at = ? AND observation_id < ?))`
		args = append(args, cursor.ObservedAt, cursor.ObservedAt, cursor.ID)
	}
	query += ` ORDER BY observed_at DESC, observation_id DESC LIMIT ?`
	args = append(args, limit+1)
	rows, err := j.db.QueryContext(ctx, query, args...)
	if err != nil {
		return HistoryPage{}, err
	}
	defer rows.Close()
	for rows.Next() {
		var record ObservationRecord
		var observedAt, lastSeenAt string
		var payload []byte
		if err := rows.Scan(&record.ObservationID, &record.StackID, &record.SnapshotID,
			&observedAt, &lastSeenAt, &record.Disposition, &payload); err != nil {
			return HistoryPage{}, err
		}
		record.ObservedAt, err = time.Parse(time.RFC3339Nano, observedAt)
		if err != nil {
			return HistoryPage{}, fmt.Errorf("invalid observed timestamp in journal: %w", err)
		}
		record.LastSeenAt, err = time.Parse(time.RFC3339Nano, lastSeenAt)
		if err != nil {
			return HistoryPage{}, fmt.Errorf("invalid last-seen timestamp in journal: %w", err)
		}
		record.Payload = append([]byte(nil), payload...)
		page.Records = append(page.Records, record)
	}
	if err := rows.Err(); err != nil {
		return HistoryPage{}, err
	}
	if len(page.Records) > limit {
		page.NextCursor = encodeCursor(page.Records[limit-1])
		page.Records = page.Records[:limit]
	}
	return page, nil
}

func (j *Journal) SetAlias(ctx context.Context, stackID string, alias *string) error {
	if stackID == "" {
		return errors.New("stack ID is required")
	}
	if alias != nil && *alias == "" {
		return errors.New("alias cannot be empty; clear it explicitly")
	}
	result, err := j.db.ExecContext(ctx, `UPDATE tracked_stacks SET alias=? WHERE stack_id=?`, alias, stackID)
	if err != nil {
		return err
	}
	changed, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if changed == 0 {
		return ErrNotFound
	}
	return nil
}

func (j *Journal) TrackedStacks(ctx context.Context, projectKey string) ([]TrackedStack, error) {
	if projectKey == "" {
		return nil, errors.New("project key is required")
	}
	rows, err := j.db.QueryContext(ctx, `SELECT stack_id,alias,project_key,last_seen_at
		FROM tracked_stacks WHERE project_key=? ORDER BY last_seen_at DESC,stack_id DESC`, projectKey)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []TrackedStack
	for rows.Next() {
		var item TrackedStack
		var lastSeen string
		if err := rows.Scan(&item.StackID, &item.Alias, &item.ProjectKey, &lastSeen); err != nil {
			return nil, err
		}
		item.LastSeenAt, err = time.Parse(time.RFC3339Nano, lastSeen)
		if err != nil {
			return nil, fmt.Errorf("invalid tracked-stack timestamp: %w", err)
		}
		result = append(result, item)
	}
	return result, rows.Err()
}

func (j *Journal) ObservationCount(ctx context.Context, stackID string) (int, error) {
	query := `SELECT count(*) FROM observations`
	args := []any{}
	if stackID != "" {
		query += ` WHERE stack_id=?`
		args = append(args, stackID)
	}
	var count int
	if err := j.db.QueryRowContext(ctx, query, args...).Scan(&count); err != nil {
		return 0, err
	}
	return count, nil
}

// PruneObservations removes old history while retaining every stack identity
// and each stack's newest observation. Sessions are stored separately and are
// never touched.
func (j *Journal) PruneObservations(ctx context.Context, before time.Time, stackID string) (int64, error) {
	if before.IsZero() {
		return 0, errors.New("prune cutoff is required")
	}
	cutoff := before.UTC().Format(time.RFC3339Nano)
	query := `DELETE FROM observations
		WHERE observed_at < ?
		AND observation_id NOT IN (
			SELECT observation_id FROM observations newest
			WHERE newest.stack_id=observations.stack_id
			ORDER BY observed_at DESC, observation_id DESC LIMIT 1
		)`
	args := []any{cutoff}
	if stackID != "" {
		query += ` AND stack_id=?`
		args = append(args, stackID)
	}
	result, err := j.db.ExecContext(ctx, query, args...)
	if err != nil {
		return 0, err
	}
	return result.RowsAffected()
}
