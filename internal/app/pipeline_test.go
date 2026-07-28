package app

import (
	"bytes"
	"encoding/json"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/nkaewam/mrstack/internal/api"
	"github.com/nkaewam/mrstack/internal/cli"
	"github.com/nkaewam/mrstack/internal/gitlab"
	"github.com/nkaewam/mrstack/internal/stack"
)

func TestAggregatePipelineFailureWithoutDirectJobsHasSafePacket(t *testing.T) {
	handler := &Handler{Now: func() time.Time {
		return time.Date(2026, 7, 25, 12, 0, 0, 0, time.UTC)
	}}
	factory, err := handler.factory()
	if err != nil {
		t.Fatal(err)
	}
	finding, err := factory.NewFinding("pipeline_failed", api.DispositionActionRequired,
		api.FindingScope{Kind: "member", MRIID: intPtr(1), Position: intPtr(0), PipelineID: stringPtr("9")},
		"aggregate pipeline failed")
	if err != nil {
		t.Fatal(err)
	}
	kind, pipelineID, sourceSHA, webURL := "branch", "9", strings.Repeat("a", 40), "https://gitlab.example/pipelines/9"
	env := api.Envelope{
		Stack: &api.Stack{
			Remote:   api.Remote{Name: "origin"},
			Selector: api.Selector{Kind: "named_stack", Value: testStackName},
			Members: []api.Member{{
				Position: 0, IID: 1,
				Pipeline: &api.Pipeline{
					Currentness: "exact", Kind: &kind, ID: &pipelineID,
					SourceSHA: &sourceSHA, WebURL: &webURL,
					BlockingStatus: "failed", FailedJobs: []api.FailedJob{},
				},
			}},
		},
		Findings: []api.Finding{finding}, Evidence: []api.Evidence{},
		Remediations: []api.Remediation{},
	}
	if err := handler.attachCheckPackets(&env, factory, "/repo"); err != nil {
		t.Fatalf("aggregate failure became internal: %v", err)
	}
	if len(env.Remediations) != 1 || env.Remediations[0].Kind != "wait_and_recheck" ||
		len(env.Remediations[0].Actions) != 1 ||
		env.Remediations[0].Actions[0].Kind != "recheck" {
		t.Fatalf("unsafe aggregate remediation: %+v", env.Remediations)
	}
}

func intPtr(value int) *int          { return &value }
func stringPtr(value string) *string { return &value }

func TestCheckMergeabilityPrecedesReady(t *testing.T) {
	tests := []struct {
		name, status, disposition, code, remediation string
		conflicts                                    bool
	}{
		{"conflict", "mergeable", "action_required", "merge_conflict", "human_handoff", true},
		{"checking", "checking", "waiting", "mergeability_checking", "wait_and_recheck", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			repo, _, mainOID, sourceOID, _ := createStackRepository(t)
			payload := []map[string]any{{
				"iid": 1, "state": "opened", "source_branch": "feature/one", "target_branch": "main",
				"sha": sourceOID, "web_url": "https://gitlab.example/group/project/-/merge_requests/1",
				"author": map[string]any{"id": 7, "username": "developer"},
				"diff_refs": map[string]any{
					"base_sha": mainOID, "head_sha": sourceOID, "start_sha": mainOID,
				},
				"detailed_merge_status": tt.status, "has_conflicts": tt.conflicts,
			}}
			stateDir, stacksDir := testDirs(t)
			responses := glabProjectResponses(false)
			addPerMREndpoints(responses, payload)
			now := time.Date(2026, 7, 25, 12, 0, 0, 0, time.UTC)
			handler := &Handler{
				Runner:    fakeGlabRunner{responses: responses},
				Dir:       repo,
				StateDir:  stateDir,
				StacksDir: stacksDir,
				Now:       func() time.Time { return now },
			}
			registerNamedStack(t, handler, testStackName, 1)
			result := runMachine(t, handler, "check", testStackName)
			if result.exit != 0 {
				t.Fatalf("exit=%d output=%s", result.exit, result.stdout)
			}
			var envelope struct {
				Disposition string `json:"disposition"`
			}
			if err := json.Unmarshal([]byte(result.stdout), &envelope); err != nil {
				t.Fatal(err)
			}
			if envelope.Disposition != tt.disposition {
				t.Fatalf("disposition=%s want=%s\n%s", envelope.Disposition, tt.disposition, result.stdout)
			}
			assertActionPacket(t, result.stdout, tt.code, tt.remediation)
			if tt.name == "conflict" {
				var first struct {
					Findings []api.Finding `json:"findings"`
				}
				if err := json.Unmarshal([]byte(result.stdout), &first); err != nil {
					t.Fatal(err)
				}
				now = now.Add(5 * time.Minute)
				repeated := runMachine(t, handler, "check", testStackName)
				var second struct {
					Findings []api.Finding `json:"findings"`
				}
				if err := json.Unmarshal([]byte(repeated.stdout), &second); err != nil {
					t.Fatal(err)
				}
				if repeated.exit != 0 || len(first.Findings) != 1 || len(second.Findings) != 1 ||
					first.Findings[0].FindingID != second.Findings[0].FindingID ||
					first.Findings[0].FirstSeenAt != second.Findings[0].FirstSeenAt ||
					first.Findings[0].LastSeenAt == second.Findings[0].LastSeenAt {
					t.Fatalf("active finding interval was not stable:\nfirst=%s\nsecond=%s",
						result.stdout, repeated.stdout)
				}
			}
		})
	}
}

func TestClassifyPipelineKind(t *testing.T) {
	tests := []struct {
		name   string
		source string
		ref    string
		iid    int
		want   stack.PipelineKind
	}{
		{"branch", "push", "feature", 7, stack.PipelineBranch},
		{"detached MR", "merge_request_event", "refs/merge-requests/7/head", 7, stack.PipelineDetachedMR},
		{"merged results", "merge_request_event", "refs/merge-requests/7/merge", 7, stack.PipelineMergedResult},
		{"different MR is ambiguous", "merge_request_event", "refs/merge-requests/8/head", 7, stack.PipelineUnknown},
		{"unrecognized MR ref is ambiguous", "merge_request_event", "feature", 7, stack.PipelineUnknown},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := classifyPipelineKind(gitlab.Pipeline{Source: tt.source, Ref: tt.ref}, tt.iid)
			if got != tt.want {
				t.Fatalf("got %q want %q", got, tt.want)
			}
		})
	}
}

func TestCheckPipelineEvidenceIsSchemaValidForValidAndAmbiguousMergedResults(t *testing.T) {
	tests := []struct {
		name        string
		associated  string
		parents     func(source, target string) []string
		disposition string
	}{
		{
			name: "valid", associated: `[{"iid":1}]`,
			parents:     func(source, target string) []string { return []string{target, source} },
			disposition: "ready",
		},
		{
			name: "association mismatch", associated: `[{"iid":2}]`,
			parents:     func(source, target string) []string { return []string{source, target} },
			disposition: "human_required",
		},
		{
			name: "ambiguous association", associated: `[{"iid":1},{"iid":2}]`,
			parents:     func(source, target string) []string { return []string{source, target} },
			disposition: "human_required",
		},
		{
			name: "parent mismatch", associated: `[{"iid":1}]`,
			parents:     func(source, target string) []string { return []string{source, strings.Repeat("d", 40)} },
			disposition: "human_required",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			repo, _, mainOID, sourceOID, _ := createStackRepository(t)
			syntheticOID := strings.Repeat("c", 40)
			payload := []map[string]any{{
				"iid": 1, "state": "opened", "source_branch": "feature/one", "target_branch": "main",
				"sha": sourceOID, "web_url": "https://gitlab.example/group/project/-/merge_requests/1",
				"author":        map[string]any{"id": 7, "username": "developer"},
				"diff_refs":     map[string]any{"base_sha": mainOID, "head_sha": sourceOID, "start_sha": mainOID},
				"head_pipeline": map[string]any{"id": 9},
			}}
			parentsJSON, err := json.Marshal(tt.parents(sourceOID, mainOID))
			if err != nil {
				t.Fatal(err)
			}
			stateDir, stacksDir := testDirs(t)
			responses := glabProjectResponses(true)
			addPerMREndpoints(responses, payload)
			responses["/projects/42/pipelines/9"] = json.RawMessage(`{
				"id":9,"sha":"` + syntheticOID + `","ref":"refs/merge-requests/1/merge",
				"status":"success","source":"merge_request_event","web_url":"https://gitlab.example/pipelines/9"
			}`)
			responses["/projects/42/pipelines/9/merge_requests"] = json.RawMessage(tt.associated)
			responses["/projects/42/repository/commits/"+syntheticOID] = json.RawMessage(
				`{"id":"` + syntheticOID + `","parent_ids":` + string(parentsJSON) + `}`)
			handler := &Handler{
				Runner:    fakeGlabRunner{responses: responses},
				Dir:       repo,
				StateDir:  stateDir,
				StacksDir: stacksDir,
				Now:       func() time.Time { return time.Date(2026, 7, 25, 12, 0, 0, 0, time.UTC) },
			}
			registerNamedStack(t, handler, testStackName, 1)
			var stdout, stderr bytes.Buffer
			exit := cli.RunWithHandler([]string{
				"--json", "--no-input", "--remote", "origin", "check", testStackName,
			}, &stdout, &stderr, handler)
			if exit != 0 {
				t.Fatalf("exit=%d stdout=%s stderr=%s", exit, stdout.String(), stderr.String())
			}
			var envelope struct {
				Disposition string `json:"disposition"`
				Stack       struct {
					Members []struct {
						Pipeline struct {
							Currentness string  `json:"currentness"`
							Kind        *string `json:"kind"`
						} `json:"pipeline"`
					} `json:"members"`
				} `json:"stack"`
			}
			if err := json.Unmarshal(stdout.Bytes(), &envelope); err != nil {
				t.Fatalf("decode output: %v\n%s", err, stdout.String())
			}
			if envelope.Disposition != tt.disposition {
				t.Fatalf("disposition=%q want %q\n%s", envelope.Disposition, tt.disposition, stdout.String())
			}
			pipeline := envelope.Stack.Members[0].Pipeline
			if tt.disposition == "ready" {
				if pipeline.Currentness != "exact" || pipeline.Kind == nil || *pipeline.Kind != "merged_results" {
					t.Fatalf("valid pipeline not pinned: %+v", pipeline)
				}
			} else if pipeline.Currentness != "ambiguous" || pipeline.Kind != nil {
				t.Fatalf("ambiguous pipeline leaked moving identity: %+v", pipeline)
			}
			if tt.disposition == "human_required" {
				assertActionPacket(t, stdout.String(), "ambiguous_pipeline", "human_handoff")
			}
		})
	}
}

func TestCheckExactSourcePipelineDoesNotRequireMRAssociation(t *testing.T) {
	tests := []struct {
		name        string
		source      string
		refForIID   func(int) string
		wantAPIKind string
	}{
		{
			name: "branch", source: "push",
			refForIID: func(int) string { return "feature/one" }, wantAPIKind: "branch",
		},
		{
			name: "detached merge request", source: "merge_request_event",
			refForIID:   func(iid int) string { return "refs/merge-requests/" + strconv.Itoa(iid) + "/head" },
			wantAPIKind: "detached_mr",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			repo, _, mainOID, sourceOID, _ := createStackRepository(t)
			payload := []map[string]any{{
				"iid": 1, "state": "opened", "source_branch": "feature/one", "target_branch": "main",
				"sha": sourceOID, "web_url": "https://gitlab.example/group/project/-/merge_requests/1",
				"author":        map[string]any{"id": 7, "username": "developer"},
				"diff_refs":     map[string]any{"base_sha": mainOID, "head_sha": sourceOID, "start_sha": mainOID},
				"head_pipeline": map[string]any{"id": 9},
			}}
			var calls [][]string
			stateDir, stacksDir := testDirs(t)
			responses := glabProjectResponses(true)
			addPerMREndpoints(responses, payload)
			responses["/projects/42/pipelines/9"] = json.RawMessage(`{
				"id":9,"sha":"` + sourceOID + `","ref":"` + tt.refForIID(1) + `",
				"status":"success","source":"` + tt.source + `",
				"web_url":"https://gitlab.example/pipelines/9"
			}`)
			handler := &Handler{
				Runner: fakeGlabRunner{
					calls:     &calls,
					responses: responses,
				},
				Dir: repo, StateDir: stateDir, StacksDir: stacksDir,
				Now: func() time.Time { return time.Date(2026, 7, 25, 12, 0, 0, 0, time.UTC) },
			}
			registerNamedStack(t, handler, testStackName, 1)
			var stdout, stderr bytes.Buffer
			exit := cli.RunWithHandler([]string{
				"--json", "--no-input", "--remote", "origin", "check", testStackName,
			}, &stdout, &stderr, handler)
			if exit != 0 {
				t.Fatalf("exit=%d stdout=%s stderr=%s", exit, stdout.String(), stderr.String())
			}
			for _, call := range calls {
				if strings.Contains(strings.Join(call, " "), "/pipelines/9/merge_requests") {
					t.Fatalf("exact-source %s pipeline queried unnecessary MR association: %q", tt.name, call)
				}
			}
			var envelope struct {
				Disposition string `json:"disposition"`
				Stack       struct {
					Members []struct {
						Pipeline struct {
							Currentness string  `json:"currentness"`
							Kind        *string `json:"kind"`
						} `json:"pipeline"`
					} `json:"members"`
				} `json:"stack"`
			}
			if err := json.Unmarshal(stdout.Bytes(), &envelope); err != nil {
				t.Fatalf("decode output: %v\n%s", err, stdout.String())
			}
			pipeline := envelope.Stack.Members[0].Pipeline
			if envelope.Disposition != "ready" || pipeline.Currentness != "exact" ||
				pipeline.Kind == nil || *pipeline.Kind != tt.wantAPIKind {
				t.Fatalf("pipeline was not accepted as exact current: %+v\n%s", envelope, stdout.String())
			}
		})
	}
}

func TestPipelineAssociationRequiresExactlySelectedMR(t *testing.T) {
	if !pipelineAssociatedExactly([]gitlab.PipelineMergeRequest{{IID: 7}}, 7) {
		t.Fatal("one exact association was rejected")
	}
	for name, associated := range map[string][]gitlab.PipelineMergeRequest{
		"missing":    nil,
		"mismatch":   {{IID: 8}},
		"ambiguous":  {{IID: 7}, {IID: 8}},
		"duplicated": {{IID: 7}, {IID: 7}},
	} {
		t.Run(name, func(t *testing.T) {
			if pipelineAssociatedExactly(associated, 7) {
				t.Fatalf("invalid association accepted: %#v", associated)
			}
		})
	}
}
