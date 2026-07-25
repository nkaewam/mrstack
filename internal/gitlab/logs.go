package gitlab

import (
	"bytes"
	"errors"
	"unicode/utf8"
)

const (
	DefaultLogBudget = 512 * 1024
	MaxLogBudget     = 4 * 1024 * 1024
	MaxLogJobs       = 20
)

type BoundedLog struct {
	JobID               string
	Text                string
	SourceBytes         int
	ReturnedBytes       int
	Truncated           bool
	InvalidUTF8Replaced bool
}

// BoundLogs divides one source-byte budget deterministically in request order.
// Remainder bytes are allocated to the earliest jobs and the newest tail of
// every trace is retained.
func BoundLogs(jobIDs []string, traces [][]byte, budget int) ([]BoundedLog, error) {
	if len(jobIDs) == 0 || len(jobIDs) != len(traces) || len(jobIDs) > MaxLogJobs {
		return nil, errors.New("one to twenty matching job IDs and traces are required")
	}
	if budget <= 0 {
		budget = DefaultLogBudget
	}
	if budget > MaxLogBudget {
		return nil, errors.New("log budget exceeds hard maximum")
	}
	base, remainder := budget/len(traces), budget%len(traces)
	result := make([]BoundedLog, len(traces))
	for i, trace := range traces {
		allocation := base
		if i < remainder {
			allocation++
		}
		sourceBytes := len(trace)
		truncated := sourceBytes > allocation
		if truncated {
			trace = trace[sourceBytes-allocation:]
		}
		valid := utf8.Valid(trace)
		text := string(bytes.ToValidUTF8(trace, []byte("\uFFFD")))
		result[i] = BoundedLog{
			JobID: jobIDs[i], Text: text, SourceBytes: sourceBytes,
			ReturnedBytes: len(trace), Truncated: truncated, InvalidUTF8Replaced: !valid,
		}
	}
	return result, nil
}
