package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"
)

// Clock and IDSource make timestamps and opaque identifiers deterministic in tests.
type Clock interface {
	Now() time.Time
}

type IDSource interface {
	NewID(prefix string) (string, error)
}

type ClockFunc func() time.Time

func (f ClockFunc) Now() time.Time { return f() }

type IDSourceFunc func(prefix string) (string, error)

func (f IDSourceFunc) NewID(prefix string) (string, error) { return f(prefix) }

type Factory struct {
	clock Clock
	ids   IDSource
}

func NewFactory(clock Clock, ids IDSource) (*Factory, error) {
	if clock == nil || ids == nil {
		return nil, errors.New("api: clock and ID source are required")
	}
	return &Factory{clock: clock, ids: ids}, nil
}

func (f *Factory) NewEnvelope(name CommandName) (Envelope, error) {
	if !validCommand(name) {
		return Envelope{}, fmt.Errorf("api: unknown stable command name %q", name)
	}
	id, err := f.ids.NewID("cmd")
	if err != nil {
		return Envelope{}, fmt.Errorf("api: create invocation ID: %w", err)
	}
	if strings.TrimSpace(id) == "" {
		return Envelope{}, errors.New("api: ID source returned an empty invocation ID")
	}
	return Envelope{
		APIVersion:   APIVersion,
		GeneratedAt:  timestamp(f.clock.Now()),
		Command:      Command{Name: name, InvocationID: id},
		Outcome:      Authoritative(),
		Findings:     []Finding{},
		Evidence:     []Evidence{},
		Remediations: []Remediation{},
		Data:         map[string]any{},
	}, nil
}

func (f *Factory) NewFinding(code string, disposition Disposition, scope FindingScope, summary string) (Finding, error) {
	id, err := f.ids.NewID("fnd")
	if err != nil {
		return Finding{}, fmt.Errorf("api: create finding ID: %w", err)
	}
	now := timestamp(f.clock.Now())
	finding := Finding{
		FindingID: id, Code: code, Disposition: disposition, Scope: scope,
		Summary: summary, Details: map[string]any{}, EvidenceRefs: []string{},
		FirstSeenAt: now, LastSeenAt: now,
	}
	if err := validateFinding(finding); err != nil {
		return Finding{}, err
	}
	return finding, nil
}

func (f *Factory) NewEvidence(kind string, fields map[string]any) (Evidence, error) {
	id, err := f.ids.NewID("ev")
	if err != nil {
		return Evidence{}, fmt.Errorf("api: create evidence ID: %w", err)
	}
	ev := Evidence{EvidenceID: id, Kind: kind, Fields: cloneMap(fields)}
	if err := validateEvidence(ev); err != nil {
		return Evidence{}, err
	}
	return ev, nil
}

func timestamp(t time.Time) string {
	return t.UTC().Format(time.RFC3339Nano)
}

func Authoritative() Outcome {
	return Outcome{Status: StatusSucceeded, Class: ClassAuthoritative, Code: CodeOK, ExitCode: 0}
}

func InvalidInput(code OutcomeCode, retryable bool) (Outcome, error) {
	if code != CodeInvalidArguments && code != CodeInvalidSelector && code != CodeUnknownCommand {
		return Outcome{}, fmt.Errorf("api: %q is not an invalid_input code", code)
	}
	return Outcome{Status: StatusFailed, Class: ClassInvalidInput, Code: code, ExitCode: 2, Retryable: retryable}, nil
}

func Unavailable(code OutcomeCode, retryable bool) (Outcome, error) {
	switch code {
	case CodeNotGitRepository, CodeGitUnavailable, CodeGlabUnavailable,
		CodeAuthenticationFailed, CodeGitLabTransportFailed, CodeGitTransportFailed,
		CodeServerModeUndetermined, CodePrerequisiteUnsupported, CodeJournalUnavailable:
	default:
		return Outcome{}, fmt.Errorf("api: %q is not an unavailable code", code)
	}
	return Outcome{Status: StatusFailed, Class: ClassUnavailable, Code: code, ExitCode: 3, Retryable: retryable}, nil
}

func Internal() Outcome {
	return Outcome{Status: StatusFailed, Class: ClassInternal, Code: CodeInternalInvariantFailed, ExitCode: 4}
}

// HighestDisposition returns the normative precedence winner. The bool is false
// for an empty input.
func HighestDisposition(values ...Disposition) (Disposition, bool) {
	rank := map[Disposition]int{
		DispositionComplete: 1, DispositionReady: 2, DispositionWaiting: 3,
		DispositionHumanRequired: 4, DispositionActionRequired: 5, DispositionInvalid: 6,
	}
	var best Disposition
	for _, value := range values {
		if rank[value] > rank[best] {
			best = value
		}
	}
	return best, best != ""
}

// ApplyFindingDisposition derives and stores the envelope disposition without
// dropping lower-precedence findings.
func (e *Envelope) ApplyFindingDisposition() {
	values := make([]Disposition, 0, len(e.Findings))
	for _, finding := range e.Findings {
		values = append(values, finding.Disposition)
	}
	if best, ok := HighestDisposition(values...); ok {
		e.Disposition = &best
	} else {
		e.Disposition = nil
	}
}

// MarshalDocument validates first and returns exactly one JSON document,
// terminated by one newline.
func MarshalDocument(e Envelope) ([]byte, error) {
	if err := Validate(e); err != nil {
		return nil, err
	}
	doc, err := json.Marshal(e)
	if err != nil {
		return nil, fmt.Errorf("api: marshal response: %w", err)
	}
	return append(doc, '\n'), nil
}

// WriteDocument performs all validation and marshaling before touching w.
func WriteDocument(w io.Writer, e Envelope) error {
	if w == nil {
		return errors.New("api: nil response writer")
	}
	doc, err := MarshalDocument(e)
	if err != nil {
		return err
	}
	if _, err := w.Write(doc); err != nil {
		return fmt.Errorf("api: write response: %w", err)
	}
	return nil
}

func (e Evidence) MarshalJSON() ([]byte, error) {
	if err := validateEvidence(e); err != nil {
		return nil, err
	}
	out := cloneMap(e.Fields)
	out["evidence_id"] = e.EvidenceID
	out["kind"] = e.Kind
	return json.Marshal(out)
}

func cloneMap(in map[string]any) map[string]any {
	out := make(map[string]any, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}
