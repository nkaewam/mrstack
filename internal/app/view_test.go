package app

import (
	"bytes"
	"encoding/json"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/nkaewam/mrstack/internal/api"
	"github.com/nkaewam/mrstack/internal/cli"
	"github.com/nkaewam/mrstack/internal/gitlab"
	"github.com/nkaewam/mrstack/internal/stackstore"
)

// stackMR builds a minimal gitlab.MergeRequest for view tests.
func stackMR(iid int, title, source, target, state, mergeStatus, pipeline string) gitlab.MergeRequest {
	var hp *gitlab.Pipeline
	if pipeline != "" {
		hp = &gitlab.Pipeline{ID: "100", SHA: "deadbeef",
			Ref:    "refs/merge-requests/" + strconv.Itoa(iid) + "/head",
			Status: pipeline, WebURL: "https://gitlab.example/p/100", Source: "merge_request_event"}
	}
	return gitlab.MergeRequest{
		IID: iid, State: state, Title: title, SourceBranch: source, TargetBranch: target,
		SHA:                 "deadbeef",
		WebURL:              "https://gitlab.example/group/project/-/merge_requests/" + strconv.Itoa(iid),
		Author:              gitlab.MRUser{ID: "7", Username: "dev"},
		DetailedMergeStatus: mergeStatus, HeadPipeline: hp,
	}
}

// viewHandler builds a handler whose fakeGlabRunner serves the given glab
// responses, with the named-stack store rooted at a temp directory.
func viewHandler(t *testing.T, repo string, responses map[string]json.RawMessage) (*Handler, string) {
	t.Helper()
	root := t.TempDir()
	h := &Handler{
		Runner: fakeGlabRunner{responses: responses},
		Dir:    repo, StacksDir: filepath.Join(root, "stacks"),
		StateDir: filepath.Join(root, "state"),
		Now:      func() time.Time { return time.Date(2026, 7, 28, 12, 0, 0, 0, time.UTC) },
	}
	return h, filepath.Join(root, "stacks")
}

func runView(t *testing.T, h *Handler, args ...string) (int, []byte, []byte) {
	t.Helper()
	var stdout, stderr bytes.Buffer
	exit := cli.RunWithHandler(args, &stdout, &stderr, h)
	return exit, stdout.Bytes(), stderr.Bytes()
}

func mustMRJSON(t *testing.T, payload map[string]any) json.RawMessage {
	t.Helper()
	body, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	return body
}

func viewEnvelope(t *testing.T, stdout []byte) api.ViewData {
	t.Helper()
	validateStackEnvelope(t, stdout)
	var env struct {
		Data struct {
			View api.ViewData `json:"view"`
		} `json:"data"`
	}
	if err := json.Unmarshal(stdout, &env); err != nil {
		t.Fatal(err)
	}
	return env.Data.View
}

func TestOrderChainWalksFromDefaultBranch(t *testing.T) {
	t.Parallel()
	mr1 := stackMR(1, "feat: one", "feature/one", "main", "opened", "mergeable", "success")
	mr2 := stackMR(2, "feat: two", "feature/two", "feature/one", "opened", "mergeable", "failed")
	ordered, note := orderChain([]gitlab.MergeRequest{mr2, mr1}, "main")
	if note != "" {
		t.Fatalf("unexpected note: %q", note)
	}
	if len(ordered) != 2 || ordered[0].IID != 1 || ordered[1].IID != 2 {
		t.Fatalf("chain not ordered base->tip: %+v", ordered)
	}
}

func TestOrderChainReportsNoAnchor(t *testing.T) {
	t.Parallel()
	mr := stackMR(3, "feat: orphan", "feature/x", "feature/y", "opened", "mergeable", "success")
	ordered, note := orderChain([]gitlab.MergeRequest{mr}, "main")
	if len(ordered) != 1 || ordered[0].IID != 3 {
		t.Fatalf("orphan should still be returned: %+v", ordered)
	}
	if note == "" {
		t.Fatal("expected a note about no anchor")
	}
}

func TestOrderChainReportsBrokenLink(t *testing.T) {
	t.Parallel()
	mr1 := stackMR(1, "feat: one", "feature/one", "main", "opened", "mergeable", "success")
	mr3 := stackMR(3, "feat: three", "feature/three", "feature/two", "opened", "mergeable", "success")
	ordered, note := orderChain([]gitlab.MergeRequest{mr1, mr3}, "main")
	if len(ordered) != 2 {
		t.Fatalf("all members must be returned: %+v", ordered)
	}
	if note == "" {
		t.Fatal("expected a broken-chain note")
	}
}

func TestViewCurrentRepoLiveFetchesStatus(t *testing.T) {
	repo, _, _, _, _ := createStackRepository(t)
	responses := map[string]json.RawMessage{
		"/projects/group%2Fproject": json.RawMessage(`{
			"id":42,"path_with_namespace":"group/project",
			"web_url":"https://gitlab.example/group/project","default_branch":"main",
			"only_allow_merge_if_pipeline_succeeds":false
		}`),
		"/projects/42/merge_requests/1": json.RawMessage(`{
			"iid":1,"state":"opened","title":"feat: one",
			"source_branch":"feature/one","target_branch":"main","sha":"deadbeef",
			"web_url":"https://gitlab.example/group/project/-/merge_requests/1",
			"author":{"id":7,"username":"dev"},
			"detailed_merge_status":"mergeable","has_conflicts":false,
			"head_pipeline":{"id":"100","sha":"deadbeef","ref":"x","status":"success","web_url":"u","source":"merge_request_event"}
		}`),
		"/projects/42/merge_requests/2": json.RawMessage(`{
			"iid":2,"state":"opened","title":"feat: two",
			"source_branch":"feature/two","target_branch":"feature/one","sha":"deadbeef",
			"web_url":"https://gitlab.example/group/project/-/merge_requests/2",
			"author":{"id":7,"username":"dev"},
			"detailed_merge_status":"conflict","has_conflicts":true,
			"head_pipeline":{"id":"101","sha":"deadbeef","ref":"x","status":"failed","web_url":"u","source":"merge_request_event"}
		}`),
	}
	h, _ := viewHandler(t, repo, responses)
	if exit, _, _ := runView(t, h, "--json", "--no-input", "--remote", "origin", "stack", "create", "s"); exit != 0 {
		t.Fatalf("create failed: exit=%d", exit)
	}
	if exit, _, _ := runView(t, h, "--json", "--no-input", "stack", "add", "s", "1", "2"); exit != 0 {
		t.Fatalf("add failed: exit=%d", exit)
	}

	exit, stdout, stderr := runView(t, h, "--json", "--no-input", "--remote", "origin", "view")
	if exit != 0 {
		t.Fatalf("view failed: exit=%d stderr=%s", exit, stderr)
	}
	data := viewEnvelope(t, stdout)
	if !data.Live {
		t.Fatal("current-repo view must be live")
	}
	if len(data.Stacks) != 1 || data.Stacks[0].Name != "s" {
		t.Fatalf("expected one stack 's': %+v", data.Stacks)
	}
	if len(data.Stacks[0].Members) != 2 {
		t.Fatalf("expected 2 members: %+v", data.Stacks[0].Members)
	}
	if data.Stacks[0].Members[0].IID != 1 || data.Stacks[0].Members[1].IID != 2 {
		t.Fatalf("members not ordered base->tip: %+v", data.Stacks[0].Members)
	}
	if data.Stacks[0].Members[1].MergeStatus != "conflict" {
		t.Fatalf("expected conflict on !2: %q", data.Stacks[0].Members[1].MergeStatus)
	}
	if data.Stacks[0].Members[1].PipelineStatus != "failed" {
		t.Fatalf("expected failed pipeline on !2: %q", data.Stacks[0].Members[1].PipelineStatus)
	}
}

func TestViewAllWithoutRefreshUsesCachedSnapshot(t *testing.T) {
	repo, _, mainOID, firstOID, secondOID := createStackRepository(t)
	responses := map[string]json.RawMessage{
		"/version": json.RawMessage(`{"version":"18.11.2"}`),
		"/projects/group%2Fproject": json.RawMessage(`{
			"id":42,"path_with_namespace":"group/project",
			"web_url":"https://gitlab.example/group/project","default_branch":"main",
			"only_allow_merge_if_pipeline_succeeds":false
		}`),
		"/projects/42/merge_requests/1": mustMRJSON(t, map[string]any{
			"iid": 1, "state": "opened", "title": "feat: one",
			"source_branch": "feature/one", "target_branch": "main", "sha": firstOID,
			"web_url": "https://gitlab.example/group/project/-/merge_requests/1",
			"author":  map[string]any{"id": 7, "username": "dev"},
			"diff_refs": map[string]any{
				"base_sha": mainOID, "head_sha": firstOID, "start_sha": mainOID,
			},
			"detailed_merge_status": "mergeable", "has_conflicts": false,
		}),
		"/projects/42/merge_requests/2": mustMRJSON(t, map[string]any{
			"iid": 2, "state": "opened", "title": "feat: two",
			"source_branch": "feature/two", "target_branch": "feature/one", "sha": secondOID,
			"web_url": "https://gitlab.example/group/project/-/merge_requests/2",
			"author":  map[string]any{"id": 7, "username": "dev"},
			"diff_refs": map[string]any{
				"base_sha": firstOID, "head_sha": secondOID, "start_sha": firstOID,
			},
			"detailed_merge_status": "conflict", "has_conflicts": true,
		}),
	}
	h, stacksDir := viewHandler(t, repo, responses)
	if exit, _, _ := runView(t, h, "--json", "--no-input", "--remote", "origin", "stack", "create", "cur"); exit != 0 {
		t.Fatalf("create failed: exit=%d", exit)
	}
	if exit, _, _ := runView(t, h, "--json", "--no-input", "stack", "add", "cur", "1", "2"); exit != 0 {
		t.Fatalf("add failed: exit=%d", exit)
	}
	if exit, stdout, stderr := runView(t, h, "--json", "--no-input", "--remote", "origin", "check", "cur"); exit != 0 {
		t.Fatalf("check failed: exit=%d stdout=%s stderr=%s", exit, stdout, stderr)
	}
	store, err := stackstore.Open(stacksDir)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Create("foreign", "other.example", "other/project", "2026-07-28T12:00:00Z"); err != nil {
		t.Fatal(err)
	}

	exit, stdout, stderr := runView(t, h, "--json", "--no-input", "--remote", "origin", "view", "--all")
	if exit != 0 {
		t.Fatalf("view --all failed: exit=%d stderr=%s", exit, stderr)
	}
	data := viewEnvelope(t, stdout)
	if data.Live {
		t.Fatal("view --all without --refresh must not be live")
	}
	if len(data.Stacks) != 2 {
		t.Fatalf("expected both stacks: %+v", data.Stacks)
	}
	var cur, foreign *api.ViewStack
	for i := range data.Stacks {
		switch data.Stacks[i].Name {
		case "cur":
			cur = &data.Stacks[i]
		case "foreign":
			foreign = &data.Stacks[i]
		}
	}
	if cur == nil || foreign == nil {
		t.Fatalf("expected cur and foreign stacks: %+v", data.Stacks)
	}
	if len(cur.Members) != 2 || cur.Members[0].Title == "" || cur.Members[1].MergeStatus != "conflict" {
		t.Fatalf("cached stack must include status: %+v", cur.Members)
	}
	if cur.Members[1].PipelineStatus != "" && cur.Members[1].PipelineStatus != "none" {
		t.Fatalf("cached pipeline status unexpected: %q", cur.Members[1].PipelineStatus)
	}
	if cur.Note == "" || !strings.Contains(cur.Note, "cached from") {
		t.Fatalf("cached stack must note cache time: %q", cur.Note)
	}
	for _, m := range foreign.Members {
		if m.Title != "" || m.MergeStatus != "" || m.PipelineStatus != "" {
			t.Fatalf("uncached foreign stack must be membership-only: %+v", m)
		}
	}
	if foreign.Note == "" || !strings.Contains(foreign.Note, "no cached status") {
		t.Fatalf("foreign stack must explain missing cache: %q", foreign.Note)
	}
}
