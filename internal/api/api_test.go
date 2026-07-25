package api

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"math/rand"
	"reflect"
	"strings"
	"testing"
	"time"
)

type sequenceIDs struct{ next int }

func (s *sequenceIDs) NewID(prefix string) (string, error) {
	s.next++
	return prefix + "_fixed_" + string(rune('0'+s.next)), nil
}

func newEnvelope(t *testing.T, name CommandName) Envelope {
	t.Helper()
	factory, err := NewFactory(
		ClockFunc(func() time.Time { return time.Date(2026, 7, 25, 19, 34, 56, 123000000, time.FixedZone("ICT", 7*60*60)) }),
		&sequenceIDs{},
	)
	if err != nil {
		t.Fatal(err)
	}
	envelope, err := factory.NewEnvelope(name)
	if err != nil {
		t.Fatal(err)
	}
	return envelope
}

func TestFactoryCreatesStableCompleteEnvelope(t *testing.T) {
	envelope := newEnvelope(t, CommandCheck)
	if envelope.APIVersion != APIVersion || envelope.Command.InvocationID != "cmd_fixed_1" {
		t.Fatalf("unexpected identity: %#v", envelope)
	}
	if envelope.GeneratedAt != "2026-07-25T12:34:56.123Z" {
		t.Fatalf("timestamp was not normalized to UTC: %q", envelope.GeneratedAt)
	}
	if envelope.Findings == nil || envelope.Evidence == nil || envelope.Remediations == nil || envelope.Data == nil {
		t.Fatal("required collections must be non-nil")
	}

	doc, err := MarshalDocument(envelope)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Count(doc, []byte("\n")) != 1 || doc[len(doc)-1] != '\n' {
		t.Fatalf("document must end in exactly one newline: %q", doc)
	}
	var object map[string]json.RawMessage
	if err := json.Unmarshal(doc, &object); err != nil {
		t.Fatal(err)
	}
	wantKeys := []string{"api_version", "generated_at", "command", "outcome", "disposition", "stack", "findings", "evidence", "remediations", "session", "data", "error"}
	if len(object) != len(wantKeys) {
		t.Fatalf("got keys %v", reflect.ValueOf(object).MapKeys())
	}
	for _, key := range wantKeys {
		if _, ok := object[key]; !ok {
			t.Errorf("missing required top-level key %q", key)
		}
	}
	for _, key := range []string{"disposition", "stack", "session", "error"} {
		if string(object[key]) != "null" {
			t.Errorf("%s = %s, want null", key, object[key])
		}
	}
}

func TestFactoryRejectsBadDependenciesAndIDs(t *testing.T) {
	if _, err := NewFactory(nil, &sequenceIDs{}); err == nil {
		t.Fatal("expected nil clock rejection")
	}
	if _, err := NewFactory(ClockFunc(time.Now), nil); err == nil {
		t.Fatal("expected nil ID source rejection")
	}
	factory, _ := NewFactory(ClockFunc(time.Now), IDSourceFunc(func(string) (string, error) { return "", nil }))
	if _, err := factory.NewEnvelope(CommandCheck); err == nil {
		t.Fatal("expected empty ID rejection")
	}
	factory, _ = NewFactory(ClockFunc(time.Now), IDSourceFunc(func(string) (string, error) { return "", errors.New("boom") }))
	if _, err := factory.NewEnvelope(CommandCheck); !strings.Contains(err.Error(), "boom") {
		t.Fatalf("ID error not preserved: %v", err)
	}
	if _, err := factory.NewEnvelope("future.command"); err == nil {
		t.Fatal("expected unknown producer command rejection")
	}
}

func TestOutcomeConstructors(t *testing.T) {
	tests := []struct {
		name string
		got  Outcome
		want Outcome
	}{
		{"authoritative", Authoritative(), Outcome{Status: StatusSucceeded, Class: ClassAuthoritative, Code: CodeOK, ExitCode: 0}},
		{"internal", Internal(), Outcome{Status: StatusFailed, Class: ClassInternal, Code: CodeInternalInvariantFailed, ExitCode: 4}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if test.got != test.want {
				t.Fatalf("got %#v want %#v", test.got, test.want)
			}
		})
	}
	invalid, err := InvalidInput(CodeInvalidSelector, true)
	if err != nil || invalid.ExitCode != 2 || !invalid.Retryable {
		t.Fatalf("invalid input constructor: %#v, %v", invalid, err)
	}
	unavailable, err := Unavailable(CodeGitLabTransportFailed, true)
	if err != nil || unavailable.ExitCode != 3 || !unavailable.Retryable {
		t.Fatalf("unavailable constructor: %#v, %v", unavailable, err)
	}
	if _, err := InvalidInput(CodeGitUnavailable, false); err == nil {
		t.Fatal("cross-class invalid-input code accepted")
	}
	if _, err := Unavailable(CodeInvalidArguments, false); err == nil {
		t.Fatal("cross-class unavailable code accepted")
	}
}

func TestDispositionPrecedenceAllPermutations(t *testing.T) {
	values := []Disposition{DispositionInvalid, DispositionActionRequired, DispositionHumanRequired, DispositionWaiting, DispositionReady, DispositionComplete}
	for i := 0; i < 100; i++ {
		rand.New(rand.NewSource(int64(i))).Shuffle(len(values), func(a, b int) { values[a], values[b] = values[b], values[a] })
		if got, ok := HighestDisposition(values...); !ok || got != DispositionInvalid {
			t.Fatalf("permutation %v selected %q", values, got)
		}
	}
	if _, ok := HighestDisposition(); ok {
		t.Fatal("empty dispositions must not have a winner")
	}
	envelope := newEnvelope(t, CommandCheck)
	envelope.Findings = []Finding{{Disposition: DispositionWaiting}, {Disposition: DispositionActionRequired}}
	envelope.ApplyFindingDisposition()
	if envelope.Disposition == nil || *envelope.Disposition != DispositionActionRequired || len(envelope.Findings) != 2 {
		t.Fatal("finding precedence changed or dropped findings")
	}
}

func TestFindingAndEvidenceConstructors(t *testing.T) {
	ids := &sequenceIDs{}
	factory, _ := NewFactory(ClockFunc(func() time.Time { return time.Date(2026, 7, 25, 0, 0, 0, 0, time.UTC) }), ids)
	finding, err := factory.NewFinding("pipeline_running", DispositionWaiting, FindingScope{Kind: "pipeline"}, "Pipeline is running.")
	if err != nil {
		t.Fatal(err)
	}
	if finding.FindingID != "fnd_fixed_1" || finding.FirstSeenAt != finding.LastSeenAt {
		t.Fatalf("unstable finding: %#v", finding)
	}
	if _, err := factory.NewFinding("pipeline_running", DispositionInvalid, FindingScope{Kind: "pipeline"}, "bad"); err == nil {
		t.Fatal("mismatched finding disposition accepted")
	}
	fields := map[string]any{"member_iid": 42, "source_sha": strings.Repeat("a", 40), "expected_ancestor_sha": strings.Repeat("b", 40)}
	evidence, err := factory.NewEvidence("git_ancestry", fields)
	if err != nil {
		t.Fatal(err)
	}
	fields["diff"] = "later mutation"
	if _, exists := evidence.Fields["diff"]; exists {
		t.Fatal("evidence constructor did not clone input map")
	}
	doc, err := json.Marshal(evidence)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(doc, []byte(`"evidence_id":"ev_fixed_3"`)) || !bytes.Contains(doc, []byte(`"member_iid":42`)) {
		t.Fatalf("evidence fields not flattened: %s", doc)
	}
}

func TestEvidenceAllowlistAndRedaction(t *testing.T) {
	factory, _ := NewFactory(ClockFunc(time.Now), &sequenceIDs{})
	bad := []map[string]any{
		{"diff": "secret source"},
		{"trace": "raw CI"},
		{"access_token": "x"},
		{"web_url": "https://user:password@gitlab.example.com/p"},
	}
	for _, fields := range bad {
		if _, err := factory.NewEvidence("job", fields); err == nil {
			t.Errorf("accepted unsafe evidence fields %#v", fields)
		}
	}
	if _, err := factory.NewEvidence("future_kind", map[string]any{}); err == nil {
		t.Fatal("unknown evidence kind accepted")
	}
}

func TestStackValidationRejectsSchemaEnumDrift(t *testing.T) {
	t.Parallel()
	oidA, oidB := strings.Repeat("a", 40), strings.Repeat("b", 40)
	env := newEnvelope(t, CommandCheck)
	ready := DispositionReady
	env.Disposition = &ready
	env.Stack = &Stack{
		StackID: "stk_1", SnapshotID: "snp_1", ObservedAt: "2026-07-25T12:00:00Z",
		Selector: Selector{Kind: "mr", Value: "7"}, GitLabMode: "legacy",
		Remote: Remote{
			Name: "origin", Selection: "explicit",
			Fetch: RemoteEndpoint{Host: "gitlab.example.com", Project: "team/repo"},
			Push:  RemoteEndpoint{Host: "gitlab.example.com", Project: "team/repo"},
		},
		Project: Project{
			Host: "gitlab.example.com", ID: "1", PathWithNamespace: "team/repo",
			WebURL: "https://gitlab.example.com/team/repo", DefaultBranch: "main",
		},
		Base: Base{Branch: "main", SHA: oidA},
		Members: []Member{{
			Position: 0, IID: 7, State: "opened",
			WebURL:       "https://gitlab.example.com/team/repo/-/merge_requests/7",
			SourceBranch: "feature", TargetBranch: "main", SourceSHA: &oidB, TargetSHA: &oidA,
			TargetResolution: "live_branch", Author: Author{ID: "2", Username: "alice"},
			Layer:     Layer{BoundarySHA: &oidA, BoundarySource: "gitlab_diff_version"},
			Alignment: "aligned", ConflictStatus: "none",
		}},
	}
	if err := Validate(env); err != nil {
		t.Fatalf("valid stack rejected: %v", err)
	}
	for name, mutate := range map[string]func(*Stack){
		"selector":          func(s *Stack) { s.Selector.Kind = "merge_request" },
		"target resolution": func(s *Stack) { s.Members[0].TargetResolution = "remote_branch" },
		"boundary source":   func(s *Stack) { s.Members[0].Layer.BoundarySource = "captured_target" },
		"alignment":         func(s *Stack) { s.Members[0].Alignment = "unaligned" },
		"conflict":          func(s *Stack) { s.Members[0].ConflictStatus = "conflicts" },
	} {
		name, mutate := name, mutate
		t.Run(name, func(t *testing.T) {
			copyEnv := env
			copyStack := *env.Stack
			copyStack.Members = append([]Member(nil), env.Stack.Members...)
			copyEnv.Stack = &copyStack
			mutate(copyEnv.Stack)
			if err := Validate(copyEnv); err == nil {
				t.Fatal("schema-invalid enum drift accepted")
			}
		})
	}
}

func TestEnvelopeRejectsDanglingAndDuplicateReferences(t *testing.T) {
	t.Parallel()
	env := newEnvelope(t, CommandCheck)
	factory, _ := NewFactory(ClockFunc(time.Now), &sequenceIDs{})
	finding, err := factory.NewFinding("pipeline_running", DispositionWaiting,
		FindingScope{Kind: "pipeline"}, "running")
	if err != nil {
		t.Fatal(err)
	}
	waiting := DispositionWaiting
	env.Disposition = &waiting
	finding.EvidenceRefs = []string{"ev_missing"}
	env.Findings = []Finding{finding}
	if err := Validate(env); err == nil {
		t.Fatal("dangling evidence reference accepted")
	}
	finding.EvidenceRefs = []string{}
	env.Findings = []Finding{finding, finding}
	if err := Validate(env); err == nil {
		t.Fatal("duplicate finding identity accepted")
	}
	env.Findings = []Finding{finding}
	env.Remediations = []Remediation{{
		RemediationID: "rem_1", FindingID: "fnd_missing", Kind: "human_handoff",
		EvidenceRefs: []string{}, Actions: []Action{},
	}}
	if err := Validate(env); err == nil {
		t.Fatal("remediation with missing finding accepted")
	}
}

func TestRemediationSemanticIdentitiesMustAgreeWithEnvelope(t *testing.T) {
	t.Parallel()
	snapshotID, sessionID, planID := "snp_1", "ses_1", "pln_1"
	commit := strings.Repeat("a", 40)
	env := Envelope{
		Stack: &Stack{SnapshotID: snapshotID, Members: []Member{
			{Position: 0, IID: 7, Layer: Layer{BoundarySHA: &commit}},
		}},
		Session: &Session{
			SessionID: sessionID, PlanID: &planID,
			Worktree:     &SessionWorktree{Path: "/managed", GitState: "rebase_conflict"},
			CurrentLayer: &CurrentLayer{MRIID: 7, OriginalCommitSHA: commit},
		},
		Findings: []Finding{{FindingID: "fnd_1"}},
		Evidence: []Evidence{},
		Remediations: []Remediation{{
			RemediationID: "rem_1", FindingID: "fnd_1", Kind: "human_handoff",
			EvidenceRefs: []string{}, Actions: []Action{},
		}},
		Data: map[string]any{},
	}
	if err := validateReferences(env); err != nil {
		t.Fatalf("valid semantic bindings rejected: %v", err)
	}
	tests := map[string]func(*Remediation){
		"snapshot": func(r *Remediation) {
			value := "snp_other"
			r.SnapshotID = &value
		},
		"session": func(r *Remediation) {
			value := "ses_other"
			r.SessionID = &value
		},
		"plan": func(r *Remediation) {
			value := "pln_other"
			r.PlanID = &value
		},
		"member": func(r *Remediation) {
			r.Member = &RemediationMember{IID: 8, Position: 0}
		},
		"layer": func(r *Remediation) {
			other := strings.Repeat("b", 40)
			r.Layer = &RemediationLayer{MRIID: 7, CommitSHA: &other}
		},
		"worktree": func(r *Remediation) {
			r.Worktree = &SessionWorktree{Path: "/other", GitState: "rebase_conflict"}
		},
		"action plan": func(r *Remediation) {
			value := "pln_other"
			r.Actions = []Action{{Requires: ActionRequirements{PlanID: &value}}}
		},
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			copyEnv := env
			copyEnv.Remediations = append([]Remediation(nil), env.Remediations...)
			mutate(&copyEnv.Remediations[0])
			if err := validateReferences(copyEnv); err == nil {
				t.Fatal("mismatched semantic binding accepted")
			}
		})
	}
}

func TestRemediationBuilderValidatesDiscriminatedPackets(t *testing.T) {
	t.Parallel()
	factory, _ := NewFactory(ClockFunc(time.Now), &sequenceIDs{})
	sessionID := "ses_1"
	commit := strings.Repeat("a", 40)
	valid := Remediation{
		FindingID: "fnd_1", Kind: "choose_empty_commit", SessionID: &sessionID,
		Layer: &RemediationLayer{MRIID: 7, CommitSHA: &commit},
		RequiredWork: &RequiredWork{
			Kind: "choose_empty_commit_outcome", Options: []string{"drop_current", "keep_empty"},
		},
		EvidenceRefs: []string{},
		Actions: []Action{
			validSessionAction("continue_drop_current", sessionID, "empty_commit_current"),
			validSessionAction("continue_keep_empty", sessionID, "empty_commit_current"),
		},
	}
	if _, err := factory.NewRemediation(valid); err != nil {
		t.Fatalf("valid remediation rejected: %v", err)
	}
	invalid := valid
	invalid.RequiredWork = &RequiredWork{
		Kind: "choose_empty_commit_outcome", Options: []string{"drop_current", "drop_current"},
	}
	if _, err := factory.NewRemediation(invalid); err == nil {
		t.Fatal("schema-invalid empty-commit choices accepted")
	}
	invalid = valid
	invalid.Actions = invalid.Actions[:1]
	if _, err := factory.NewRemediation(invalid); err == nil {
		t.Fatal("empty-commit packet with one transition accepted")
	}
}

func validSessionAction(kind, sessionID, extraPrecondition string) Action {
	return Action{
		Kind: kind, Argv: []string{"mrstack", "restack", "continue", "--session", sessionID},
		CWD: "/repo", Mutates: true, ConfirmationRequired: true,
		Preconditions: []string{"session_state_current", extraPrecondition},
		Requires:      ActionRequirements{SessionID: &sessionID, JobIDs: []string{}},
	}
}

func TestRedactionGuardCoversNestedAdditiveData(t *testing.T) {
	for _, data := range []map[string]any{
		{"nested": map[string]any{"authorization": "Bearer abc"}},
		{"credential": "abc"},
		{"url": "https://oauth2:abc@gitlab.example.com/team/repo"},
		{"payload": []any{map[string]any{"file_content": "package main"}}},
	} {
		envelope := newEnvelope(t, CommandCheck)
		envelope.Data = data
		if _, err := MarshalDocument(envelope); err == nil {
			t.Errorf("unsafe data was encoded: %#v", data)
		}
	}
}

func TestValidationFailureWritesNothing(t *testing.T) {
	envelope := newEnvelope(t, CommandCheck)
	envelope.APIVersion = "mrstack/v2"
	var dst bytes.Buffer
	dst.WriteString("sentinel")
	if err := WriteDocument(&dst, envelope); err == nil {
		t.Fatal("expected validation error")
	}
	if dst.String() != "sentinel" {
		t.Fatalf("writer touched before validation: %q", dst.String())
	}
	if err := WriteDocument(nil, newEnvelope(t, CommandCheck)); err == nil {
		t.Fatal("nil writer accepted")
	}
}

type failingWriter struct{}

func (failingWriter) Write([]byte) (int, error) { return 0, io.ErrClosedPipe }

func TestWriteErrorIsReturned(t *testing.T) {
	if err := WriteDocument(failingWriter{}, newEnvelope(t, CommandCheck)); !errors.Is(err, io.ErrClosedPipe) {
		t.Fatalf("write error not wrapped: %v", err)
	}
}

func TestOperationalOutcomeInvariants(t *testing.T) {
	envelope := newEnvelope(t, CommandCheck)
	outcome, _ := Unavailable(CodeGitUnavailable, true)
	envelope.Outcome = outcome
	envelope.Error = &Error{Message: "git missing"}
	if err := Validate(envelope); err != nil {
		t.Fatal(err)
	}
	d := DispositionWaiting
	envelope.Disposition = &d
	if err := Validate(envelope); err == nil {
		t.Fatal("failed outcome with disposition accepted")
	}
	envelope.Disposition = nil
	envelope.Error = nil
	if err := Validate(envelope); err == nil {
		t.Fatal("failed outcome without error accepted")
	}
	envelope = newEnvelope(t, CommandCheck)
	envelope.Error = &Error{Message: "not allowed"}
	if err := Validate(envelope); err == nil {
		t.Fatal("authoritative outcome with error accepted")
	}
}

func TestCommandSpecificData(t *testing.T) {
	for command, data := range map[CommandName]map[string]any{
		CommandDoctor:       {"doctor": validDoctorDataFixture()},
		CommandRestackPlan:  {"plan": nil},
		CommandCILogs:       {"log_request": LogRequest{PipelineID: "1", JobIDs: []string{"2"}}, "log_budget": LogBudget{RequestedBytes: 1, EffectiveBytes: 1, HardMaxBytes: 4194304, Allocation: "equal_per_job_tail"}, "logs": []LogEntry{{PipelineID: "1", JobID: "2"}}},
		CommandHistoryShow:  {"history": HistoryData{}},
		CommandHistoryAlias: {"history_alias": HistoryAliasData{}},
		CommandHistoryPrune: {"history_prune": HistoryPruneData{}},
	} {
		envelope := newEnvelope(t, command)
		if err := Validate(envelope); err == nil {
			t.Errorf("%s accepted without command data", command)
		}
		for key, value := range data {
			envelope.Data[key] = value
		}
		if err := Validate(envelope); err != nil {
			t.Errorf("%s rejected with required keys: %v", command, err)
		}
	}
}

func TestDoctorDataValidatesNestedCapabilityContract(t *testing.T) {
	valid := validDoctorDataFixture()
	validate := func(t *testing.T, data DoctorData) error {
		t.Helper()
		envelope := newEnvelope(t, CommandDoctor)
		envelope.Data["doctor"] = data
		return Validate(envelope)
	}
	if err := validate(t, valid); err != nil {
		t.Fatalf("valid doctor data rejected: %v", err)
	}

	tests := map[string]func(*DoctorData){
		"unknown capability": func(data *DoctorData) {
			data.Capabilities[0].Name = "stack_discovery"
		},
		"unknown status": func(data *DoctorData) {
			data.Capabilities[0].Status = "available"
		},
		"empty summary": func(data *DoctorData) {
			data.Capabilities[0].Summary = " "
		},
		"duplicate capability": func(data *DoctorData) {
			data.Capabilities[1].Name = data.Capabilities[0].Name
		},
		"missing capability": func(data *DoctorData) {
			data.Capabilities = data.Capabilities[:len(data.Capabilities)-1]
		},
		"unknown requested mode": func(data *DoctorData) {
			data.RequestedMode = "future"
		},
		"unknown effective mode": func(data *DoctorData) {
			data.EffectiveMode = "future"
		},
		"detected mode disagrees": func(data *DoctorData) {
			native := "native"
			data.DetectedMode = &native
		},
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			data := valid
			data.Capabilities = append([]Capability(nil), valid.Capabilities...)
			mutate(&data)
			if err := validate(t, data); err == nil {
				t.Fatal("schema-invalid nested doctor data was accepted")
			}
		})
	}
}

func stringPointer(value string) *string { return &value }

func validDoctorDataFixture() DoctorData {
	mode := "legacy"
	return DoctorData{
		RequestedMode: "auto",
		DetectedMode:  &mode,
		EffectiveMode: mode,
		ServerVersion: stringPointer("18.11.3-ee"),
		GitVersion:    "git version 2.50.1",
		GlabVersion:   "glab 1.70.0",
		Capabilities: []Capability{
			{Name: "repository_context", Status: "verified", Summary: "Repository context resolved."},
			{Name: "git", Status: "verified", Summary: "Git is available."},
			{Name: "glab", Status: "verified", Summary: "glab is available."},
			{Name: "gitlab_auth", Status: "verified", Summary: "Authentication succeeded."},
			{Name: "server_mode", Status: "verified", Summary: "Server mode was detected."},
			{Name: "atomic_push", Status: "unverified", Summary: "Checked during publication."},
			{Name: "target_update", Status: "unverified", Summary: "Checked during target update."},
			{Name: "sqlite_journal", Status: "verified", Summary: "The journal is available."},
		},
	}
}

func TestCILogIdentityValidation(t *testing.T) {
	envelope := newEnvelope(t, CommandCILogs)
	envelope.Data = map[string]any{
		"log_request": LogRequest{PipelineID: "9001", JobIDs: []string{"2", "3"}},
		"log_budget":  LogBudget{RequestedBytes: 10, EffectiveBytes: 10, HardMaxBytes: 4194304, Allocation: "equal_per_job_tail"},
		"logs": []LogEntry{
			{PipelineID: "9001", JobID: "2"},
			{PipelineID: "9001", JobID: "3"},
		},
	}
	if err := Validate(envelope); err != nil {
		t.Fatal(err)
	}
	envelope.Data["logs"] = []LogEntry{{PipelineID: "9001", JobID: "2"}, {PipelineID: "9001", JobID: "2"}}
	if err := Validate(envelope); err == nil {
		t.Fatal("duplicate log result accepted")
	}
	envelope.Data["logs"] = []LogEntry{{PipelineID: "other", JobID: "2"}, {PipelineID: "9001", JobID: "3"}}
	if err := Validate(envelope); err == nil {
		t.Fatal("mismatched pipeline result accepted")
	}
}

func TestUnknownAndAbandonCannotBeAuthoritative(t *testing.T) {
	for _, command := range []CommandName{CommandUnknown, CommandRestackAbandon} {
		envelope := newEnvelope(t, command)
		if err := Validate(envelope); err == nil {
			t.Errorf("%s was authoritative", command)
		}
		outcome, _ := InvalidInput(CodeInvalidArguments, false)
		envelope.Outcome = outcome
		envelope.Error = &Error{Message: "not invocable"}
		if err := Validate(envelope); err != nil {
			t.Errorf("%s invalid_input rejected: %v", command, err)
		}
	}
}

func TestSessionCrossFieldValidation(t *testing.T) {
	oldSHA := strings.Repeat("a", 40)
	newSHA := strings.Repeat("b", 40)
	current := newSHA
	session := &Session{
		State: "completed", Publication: Publication{State: "all_new", Refs: []PublicationRef{{
			OldSHA: oldSHA, NewSHA: &newSHA, CurrentSHA: &current, Classification: "new",
		}}},
	}
	if err := validateSession(session); err != nil {
		t.Fatal(err)
	}
	current = oldSHA
	if err := validateSession(session); err == nil {
		t.Fatal("all-new SHA mismatch accepted")
	}
	current = newSHA
	session.State = "retarget_pending"
	session.Resumable = true
	session.TargetUpdate = &TargetUpdate{Status: "applied"}
	if err := validateSession(session); err == nil {
		t.Fatal("retarget_pending with applied update accepted")
	}
}

func TestActionIdentityAndPreconditionValidation(t *testing.T) {
	snapshot := "snap_1"
	action := Action{
		Kind: "start_restack", Argv: []string{"mrstack", "restack"}, CWD: "/repo",
		Mutates: true, ConfirmationRequired: true, Preconditions: []string{"snapshot_current"},
		Requires: ActionRequirements{SnapshotID: &snapshot, JobIDs: []string{}},
	}
	if err := validateAction(action); err != nil {
		t.Fatal(err)
	}
	action.Mutates = false
	if err := validateAction(action); err == nil {
		t.Fatal("non-mutating start action accepted")
	}
	action.Mutates = true
	action.Preconditions = append(action.Preconditions, "snapshot_current")
	if err := validateAction(action); err == nil {
		t.Fatal("duplicate precondition accepted")
	}
}

func TestTimestampMustBeUTC(t *testing.T) {
	envelope := newEnvelope(t, CommandCheck)
	for _, value := range []string{"2026-07-25T12:00:00+07:00", "2026-07-25", "garbage"} {
		envelope.GeneratedAt = value
		if err := Validate(envelope); err == nil {
			t.Errorf("accepted non-UTC timestamp %q", value)
		}
	}
}

func TestNonJSONDataFailsBeforeWrite(t *testing.T) {
	envelope := newEnvelope(t, CommandCheck)
	envelope.Data["bad"] = func() {}
	var dst bytes.Buffer
	if err := WriteDocument(&dst, envelope); err == nil {
		t.Fatal("non-JSON data accepted")
	}
	if dst.Len() != 0 {
		t.Fatal("partial JSON written")
	}
	if JSONEncodable(func() {}) {
		t.Fatal("JSONEncodable accepted function")
	}
}
