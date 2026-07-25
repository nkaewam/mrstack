package app

import (
	"bytes"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/nkaewam/mrstack/internal/cli"
	"github.com/nkaewam/mrstack/internal/gitlab"
	"github.com/nkaewam/mrstack/internal/stack"
)

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
			mrs, err := json.Marshal([]map[string]any{{
				"iid": 1, "state": "opened", "source_branch": "feature/one", "target_branch": "main",
				"sha": sourceOID, "web_url": "https://gitlab.example/group/project/-/merge_requests/1",
				"author":        map[string]any{"id": 7, "username": "developer"},
				"diff_refs":     map[string]any{"base_sha": mainOID, "head_sha": sourceOID, "start_sha": mainOID},
				"head_pipeline": map[string]any{"id": 9},
			}})
			if err != nil {
				t.Fatal(err)
			}
			parentsJSON, err := json.Marshal(tt.parents(sourceOID, mainOID))
			if err != nil {
				t.Fatal(err)
			}
			handler := &Handler{
				Runner: fakeGlabRunner{responses: map[string]json.RawMessage{
					"/version": json.RawMessage(`{"version":"18.11.2"}`),
					"/projects/group%2Fproject": json.RawMessage(`{
						"id":42,"path_with_namespace":"group/project",
						"web_url":"https://gitlab.example/group/project","default_branch":"main",
						"only_allow_merge_if_pipeline_succeeds":true
					}`),
					"/projects/42/merge_requests?state=all&scope=all&per_page=100": mrs,
					"/projects/42/pipelines/9": json.RawMessage(`{
						"id":9,"sha":"` + syntheticOID + `","ref":"refs/merge-requests/1/merge",
						"status":"success","source":"merge_request_event","web_url":"https://gitlab.example/pipelines/9"
					}`),
					"/projects/42/pipelines/9/merge_requests": json.RawMessage(tt.associated),
					"/projects/42/repository/commits/" + syntheticOID: json.RawMessage(
						`{"id":"` + syntheticOID + `","parent_ids":` + string(parentsJSON) + `}`),
				}},
				Dir: repo, StateDir: filepath.Join(t.TempDir(), "state"),
				Now: func() time.Time { return time.Date(2026, 7, 25, 12, 0, 0, 0, time.UTC) },
			}
			var stdout, stderr bytes.Buffer
			exit := cli.RunWithHandler([]string{
				"--json", "--no-input", "--remote", "origin", "check", "feature/one",
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
