package app

import (
	"bytes"
	"encoding/json"
	"path/filepath"
	"testing"
	"time"

	"github.com/nkaewam/mrstack/internal/cli"
	"github.com/santhosh-tekuri/jsonschema/v6"
)

func TestCheckNamedStackRunsExplicitPathAndSchemaValidates(t *testing.T) {
	repo, _, mainOID, firstOID, secondOID := createStackRepository(t)
	mr1, _ := json.Marshal(map[string]any{
		"iid": 1, "state": "opened", "title": "feat: one",
		"source_branch": "feature/one", "target_branch": "main", "sha": firstOID,
		"web_url":               "https://gitlab.example/group/project/-/merge_requests/1",
		"author":                map[string]any{"id": 7, "username": "developer"},
		"diff_refs":             map[string]any{"base_sha": mainOID, "head_sha": firstOID, "start_sha": mainOID},
		"detailed_merge_status": "mergeable", "has_conflicts": false,
	})
	mr2, _ := json.Marshal(map[string]any{
		"iid": 2, "state": "opened", "title": "feat: two",
		"source_branch": "feature/two", "target_branch": "feature/one", "sha": secondOID,
		"web_url":               "https://gitlab.example/group/project/-/merge_requests/2",
		"author":                map[string]any{"id": 7, "username": "developer"},
		"diff_refs":             map[string]any{"base_sha": firstOID, "head_sha": secondOID, "start_sha": firstOID},
		"detailed_merge_status": "mergeable", "has_conflicts": false,
	})
	runner := fakeGlabRunner{responses: map[string]json.RawMessage{
		"/version": json.RawMessage(`{"version":"18.11.2"}`),
		"/projects/group%2Fproject": json.RawMessage(`{
			"id":42,"path_with_namespace":"group/project",
			"web_url":"https://gitlab.example/group/project","default_branch":"main",
			"only_allow_merge_if_pipeline_succeeds":false
		}`),
		"/projects/42/merge_requests/1": mr1,
		"/projects/42/merge_requests/2": mr2,
	}}
	stacksDir := t.TempDir()
	handler := &Handler{
		Runner: runner, Dir: repo, StacksDir: stacksDir,
		Now: func() time.Time { return time.Date(2026, 7, 25, 12, 0, 0, 0, time.UTC) },
	}

	// Register a named stack bound to this repo with members !1 and !2.
	var stdout, stderr bytes.Buffer
	exit := cli.RunWithHandler([]string{
		"--json", "--no-input", "--remote", "origin", "stack", "create", "web",
	}, &stdout, &stderr, handler)
	if exit != 0 {
		t.Fatalf("stack create failed: exit=%d stderr=%s", exit, stderr.String())
	}
	exit = cli.RunWithHandler([]string{
		"--json", "--no-input", "stack", "add", "web", "1", "2",
	}, &stdout, &stderr, handler)
	if exit != 0 {
		t.Fatalf("stack add failed: exit=%d stderr=%s", exit, stderr.String())
	}

	// `check web` runs the explicit named-stack path: it must NOT fetch every
	// project MR, only the two named IIDs.
	stdout.Reset()
	stderr.Reset()
	exit = cli.RunWithHandler([]string{
		"--json", "--no-input", "--remote", "origin", "check", "web",
	}, &stdout, &stderr, handler)
	if exit != 0 {
		t.Fatalf("check web failed: exit=%d stdout=%s stderr=%s", exit, stdout.String(), stderr.String())
	}
	if stderr.Len() != 0 {
		t.Fatalf("machine command wrote stderr: %s", stderr.String())
	}

	schemaPath := filepath.Join(repositoryRoot(t), "docs", "schema", "mrstack-v1.schema.json")
	schema, err := jsonschema.NewCompiler().Compile(schemaPath)
	if err != nil {
		t.Fatalf("compile schema: %v", err)
	}
	instance, err := jsonschema.UnmarshalJSON(bytes.NewReader(stdout.Bytes()))
	if err != nil {
		t.Fatalf("decode JSON: %v", err)
	}
	if err := schema.Validate(instance); err != nil {
		t.Fatalf("schema-invalid check output:\n%s\n%v", stdout.String(), err)
	}

	var env struct {
		Disposition string `json:"disposition"`
		Stack       struct {
			Selector struct {
				Kind  string `json:"kind"`
				Value string `json:"value"`
			} `json:"selector"`
			Members []struct {
				IID int `json:"iid"`
			} `json:"members"`
		} `json:"stack"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &env); err != nil {
		t.Fatal(err)
	}
	if env.Stack.Selector.Kind != "named_stack" || env.Stack.Selector.Value != "web" {
		t.Fatalf("selector must be named_stack/web: %+v", env.Stack.Selector)
	}
	if len(env.Stack.Members) != 2 || env.Stack.Members[0].IID != 1 || env.Stack.Members[1].IID != 2 {
		t.Fatalf("members must be [1,2] in order: %+v", env.Stack.Members)
	}
	if env.Disposition != "ready" {
		t.Fatalf("a clean chain must be ready, got %q", env.Disposition)
	}
}

func TestCheckUnknownNamedStackFails(t *testing.T) {
	repo, _, _, _, _ := createStackRepository(t)
	stacksDir := t.TempDir()
	handler := &Handler{
		Runner: fakeGlabRunner{responses: glabProjectResponses(false)},
		Dir:    repo, StacksDir: stacksDir,
		Now: func() time.Time { return time.Date(2026, 7, 25, 12, 0, 0, 0, time.UTC) },
	}
	var stdout, stderr bytes.Buffer
	exit := cli.RunWithHandler([]string{
		"--json", "--no-input", "--remote", "origin", "check", "missing",
	}, &stdout, &stderr, handler)
	if exit != 2 {
		t.Fatalf("check missing failed: exit=%d stderr=%s", exit, stderr.String())
	}
}
