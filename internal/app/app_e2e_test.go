package app

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/nkaewam/mrstack/internal/api"
	"github.com/nkaewam/mrstack/internal/cli"
	"github.com/nkaewam/mrstack/internal/gitexec"
	"github.com/nkaewam/mrstack/internal/journal"
	"github.com/santhosh-tekuri/jsonschema/v6"
)

type fakeGlabRunner struct {
	gitexec.ExecRunner
	responses  map[string]json.RawMessage
	calls      *[][]string
	rejectPush bool
	dynamic    func(string, []string) (gitexec.Result, error)
}

func (r fakeGlabRunner) Run(ctx context.Context, dir, command string, args ...string) (gitexec.Result, error) {
	if r.calls != nil {
		call := append([]string{command}, args...)
		*r.calls = append(*r.calls, call)
	}
	if command == "git" && len(args) >= 3 && args[0] == "remote" && args[1] == "get-url" {
		return gitexec.Result{Stdout: []byte("https://gitlab.example/group/project.git\n")}, nil
	}
	if command == "git" && len(args) >= 2 && args[0] == "push" && args[1] == "--atomic" && r.rejectPush {
		return gitexec.Result{Stderr: []byte("atomic push rejected")}, &gitexec.CommandError{
			Name: "git", Args: append([]string(nil), args...), ExitCode: 1, Stderr: "atomic push rejected",
		}
	}
	if command != "glab" {
		return r.ExecRunner.Run(ctx, dir, command, args...)
	}
	var endpoint string
	for _, arg := range args {
		if strings.HasPrefix(arg, "/") {
			endpoint = arg
			break
		}
	}
	body, ok := r.responses[endpoint]
	if r.dynamic != nil {
		if result, err := r.dynamic(endpoint, args); err != nil || result.Stdout != nil {
			return result, err
		}
	}
	if !ok {
		return gitexec.Result{}, fmt.Errorf("fake glab: unexpected endpoint %q in argv %q", endpoint, args)
	}
	return gitexec.Result{Stdout: append([]byte(nil), body...)}, nil
}

func TestCheckMachineOutputFromFakeGlabValidatesAgainstPublishedSchema(t *testing.T) {
	repo, remote, mainOID, firstOID, secondOID := createStackRepository(t)
	mrs, err := json.Marshal([]map[string]any{
		{
			"iid": 1, "state": "opened", "source_branch": "feature/one", "target_branch": "main",
			"sha": firstOID, "web_url": "https://gitlab.example/group/project/-/merge_requests/1",
			"author":                map[string]any{"id": 7, "username": "developer"},
			"diff_refs":             map[string]any{"base_sha": mainOID, "head_sha": firstOID, "start_sha": mainOID},
			"detailed_merge_status": "mergeable",
		},
		{
			"iid": 2, "state": "opened", "source_branch": "feature/two", "target_branch": "feature/one",
			"sha": secondOID, "web_url": "https://gitlab.example/group/project/-/merge_requests/2",
			"author":                map[string]any{"id": 7, "username": "developer"},
			"diff_refs":             map[string]any{"base_sha": firstOID, "head_sha": secondOID, "start_sha": firstOID},
			"detailed_merge_status": "mergeable",
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	runner := fakeGlabRunner{responses: map[string]json.RawMessage{
		"/version": json.RawMessage(`{"version":"18.11.2"}`),
		"/projects/group%2Fproject": json.RawMessage(`{
			"id":42,"path_with_namespace":"group/project",
			"web_url":"https://gitlab.example/group/project","default_branch":"main",
			"only_allow_merge_if_pipeline_succeeds":false
		}`),
		"/projects/42/merge_requests?state=all&scope=all&per_page=100": mrs,
	}}
	handler := &Handler{
		Runner: runner, Dir: repo, StateDir: filepath.Join(t.TempDir(), "state"),
		Now: func() time.Time { return time.Date(2026, 7, 25, 12, 0, 0, 0, time.UTC) },
	}
	var stdout, stderr bytes.Buffer
	exit := cli.RunWithHandler([]string{
		"--json", "--no-input", "--remote", "origin", "check", "feature/two",
	}, &stdout, &stderr, handler)
	if exit != 0 {
		t.Fatalf("exit=%d\nstdout=%s\nstderr=%s\nremote=%s", exit, stdout.String(), stderr.String(), remote)
	}
	if stderr.Len() != 0 {
		t.Fatalf("machine command wrote stderr: %s", stderr.String())
	}
	if bytes.Count(stdout.Bytes(), []byte("\n")) != 1 {
		t.Fatalf("machine command must emit exactly one JSON document: %q", stdout.String())
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
	var envelope struct {
		Disposition string `json:"disposition"`
		Stack       struct {
			Members []struct {
				IID      int `json:"iid"`
				Pipeline struct {
					Applicability string  `json:"applicability"`
					Currentness   string  `json:"currentness"`
					Kind          *string `json:"kind"`
				} `json:"pipeline"`
			} `json:"members"`
		} `json:"stack"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &envelope); err != nil {
		t.Fatal(err)
	}
	if envelope.Disposition != "ready" || len(envelope.Stack.Members) != 2 {
		t.Fatalf("unexpected authoritative result: %+v", envelope)
	}
	for _, member := range envelope.Stack.Members {
		if member.Pipeline.Applicability != "not_applicable" ||
			member.Pipeline.Currentness != "not_applicable" || member.Pipeline.Kind != nil {
			t.Fatalf("MR !%d has cross-field-invalid optional CI result: %+v", member.IID, member.Pipeline)
		}
	}
}

func TestRestackPublishesAffectedSuffixOnceAndEmitsSchemaValidSession(t *testing.T) {
	repo, _, originalMain, firstOID, secondOID := createStackRepository(t)
	runGit(t, repo, "checkout", "main")
	if err := os.WriteFile(filepath.Join(repo, "base-advanced.txt"), []byte("advanced\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runGit(t, repo, "add", "base-advanced.txt")
	runGit(t, repo, "commit", "-m", "advance base")
	advancedMain := runGit(t, repo, "rev-parse", "HEAD")
	runGit(t, repo, "push", "origin", "main")
	runGit(t, repo, "checkout", "feature/two")

	mrs, err := json.Marshal([]map[string]any{
		{
			"iid": 1, "state": "opened", "source_branch": "feature/one", "target_branch": "main",
			"sha": firstOID, "web_url": "https://gitlab.example/group/project/-/merge_requests/1",
			"author":                map[string]any{"id": 7, "username": "developer"},
			"diff_refs":             map[string]any{"base_sha": originalMain, "head_sha": firstOID, "start_sha": originalMain},
			"detailed_merge_status": "mergeable",
		},
		{
			"iid": 2, "state": "opened", "source_branch": "feature/two", "target_branch": "feature/one",
			"sha": secondOID, "web_url": "https://gitlab.example/group/project/-/merge_requests/2",
			"author":                map[string]any{"id": 7, "username": "developer"},
			"diff_refs":             map[string]any{"base_sha": firstOID, "head_sha": secondOID, "start_sha": firstOID},
			"detailed_merge_status": "mergeable",
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	var calls [][]string
	handler := &Handler{
		Runner: fakeGlabRunner{responses: map[string]json.RawMessage{
			"/version": json.RawMessage(`{"version":"18.11.2"}`),
			"/user":    json.RawMessage(`{"id":7,"username":"developer"}`),
			"/projects/group%2Fproject": json.RawMessage(`{
				"id":42,"path_with_namespace":"group/project",
				"web_url":"https://gitlab.example/group/project","default_branch":"main",
				"only_allow_merge_if_pipeline_succeeds":false
			}`),
			"/projects/42/merge_requests?state=all&scope=all&per_page=100": mrs,
		}, calls: &calls},
		Dir: repo, StateDir: filepath.Join(t.TempDir(), "state"),
		Now: func() time.Time { return time.Date(2026, 7, 25, 12, 0, 0, 0, time.UTC) },
	}
	var checked bytes.Buffer
	exit := cli.RunWithHandler([]string{
		"--json", "--no-input", "--remote", "origin", "check", "feature/two",
	}, &checked, &bytes.Buffer{}, handler)
	if exit != 0 {
		t.Fatalf("check exit=%d output=%s", exit, checked.String())
	}
	var checkEnvelope struct {
		Disposition string `json:"disposition"`
		Stack       struct {
			SnapshotID string `json:"snapshot_id"`
		} `json:"stack"`
	}
	if err := json.Unmarshal(checked.Bytes(), &checkEnvelope); err != nil {
		t.Fatal(err)
	}
	if checkEnvelope.Disposition != "action_required" || checkEnvelope.Stack.SnapshotID == "" {
		t.Fatalf("expected stale stack snapshot, got %s", checked.String())
	}

	var stdout, stderr bytes.Buffer
	exit = cli.RunWithHandler([]string{
		"--json", "--no-input", "--yes", "--remote", "origin",
		"restack", "--snapshot", checkEnvelope.Stack.SnapshotID,
	}, &stdout, &stderr, handler)
	if exit != 0 {
		t.Fatalf("restack exit=%d\nstdout=%s\nstderr=%s", exit, stdout.String(), stderr.String())
	}
	schema, err := jsonschema.NewCompiler().Compile(
		filepath.Join(repositoryRoot(t), "docs", "schema", "mrstack-v1.schema.json"))
	if err != nil {
		t.Fatal(err)
	}
	instance, err := jsonschema.UnmarshalJSON(bytes.NewReader(stdout.Bytes()))
	if err != nil {
		t.Fatal(err)
	}
	if err := schema.Validate(instance); err != nil {
		t.Fatalf("schema-invalid restack output:\n%s\n%v", stdout.String(), err)
	}
	var result struct {
		Disposition string `json:"disposition"`
		Session     struct {
			State       string `json:"state"`
			Publication struct {
				State string `json:"state"`
				Refs  []struct {
					Branch         string  `json:"branch"`
					OldSHA         string  `json:"old_sha"`
					NewSHA         *string `json:"new_sha"`
					CurrentSHA     *string `json:"current_sha"`
					Classification string  `json:"classification"`
				} `json:"refs"`
			} `json:"publication"`
		} `json:"session"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if result.Disposition != "action_required" || result.Session.State != "completed" ||
		result.Session.Publication.State != "all_new" || len(result.Session.Publication.Refs) != 2 {
		t.Fatalf("unexpected restack result: %s", stdout.String())
	}
	for _, ref := range result.Session.Publication.Refs {
		if ref.NewSHA == nil || ref.CurrentSHA == nil || *ref.NewSHA != *ref.CurrentSHA ||
			ref.Classification != "new" || ref.OldSHA == *ref.NewSHA {
			t.Fatalf("invalid publication reconciliation: %+v", ref)
		}
		local := runGit(t, repo, "rev-parse", "refs/heads/"+ref.Branch)
		if ref.Branch == "feature/two" && local != ref.OldSHA {
			t.Fatalf("checked-out branch was modified: got %s want %s", local, ref.OldSHA)
		}
		if ref.Branch == "feature/one" && local != *ref.NewSHA {
			t.Fatalf("safe un-checked-out branch was not fast-updated: got %s want %s", local, *ref.NewSHA)
		}
	}
	var atomicPushes int
	for _, call := range calls {
		if len(call) >= 3 && call[0] == "git" && call[1] == "push" {
			atomicPushes++
			if call[2] != "--atomic" {
				t.Fatalf("publication used a non-atomic push: %q", call)
			}
			var leases int
			for _, arg := range call {
				if strings.HasPrefix(arg, "--force-with-lease=refs/heads/") {
					leases++
				}
			}
			if leases != 2 {
				t.Fatalf("atomic push has %d leases, want one per affected ref: %q", leases, call)
			}
		}
	}
	if atomicPushes != 1 {
		t.Fatalf("publication sent %d push commands, want exactly one; calls=%q", atomicPushes, calls)
	}
	if got := runGit(t, repo, "ls-remote", "--heads", "origin", "refs/heads/main"); !strings.HasPrefix(got, advancedMain) {
		t.Fatalf("restack changed base branch: %s", got)
	}
}

func TestRestackConflictContinueRequiresResolvedAndExplicitlyStagedWork(t *testing.T) {
	handler, snapshotID := pausedSessionFixture(t, "conflict")
	start := runMachine(t, handler, "--yes", "restack", "--snapshot", snapshotID)
	session := decodeSession(t, start.stdout)
	if start.exit != 0 || session.State != "rebase_conflict" || session.Worktree == nil {
		t.Fatalf("expected conflict pause: exit=%d output=%s", start.exit, start.stdout)
	}
	assertPublishedSchema(t, start.stdout)

	refused := runMachine(t, handler, "--yes", "restack", "continue", "--session", session.ID)
	if refused.exit != 2 {
		t.Fatalf("unresolved conflict continue exit=%d output=%s stderr=%s",
			refused.exit, refused.stdout, refused.stderr)
	}
	if err := os.WriteFile(filepath.Join(session.Worktree.Path, "shared.txt"),
		[]byte("explicit resolution\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	unstaged := runMachine(t, handler, "--yes", "restack", "continue", "--session", session.ID)
	if unstaged.exit != 2 {
		t.Fatalf("unstaged conflict continue exit=%d output=%s", unstaged.exit, unstaged.stdout)
	}
	runGit(t, session.Worktree.Path, "add", "shared.txt")
	completed := runMachine(t, handler, "--yes", "restack", "continue", "--session", session.ID)
	if completed.exit != 0 || decodeSession(t, completed.stdout).State != "completed" {
		t.Fatalf("staged conflict continuation failed: exit=%d output=%s stderr=%s",
			completed.exit, completed.stdout, completed.stderr)
	}
	assertPublishedSchema(t, completed.stdout)
}

func TestRestackEmptyCommitRequiresExplicitDropOrKeep(t *testing.T) {
	for _, choice := range []string{"--drop-current", "--keep-empty"} {
		t.Run(strings.TrimPrefix(choice, "--"), func(t *testing.T) {
			handler, snapshotID := pausedSessionFixture(t, "empty")
			start := runMachine(t, handler, "--yes", "restack", "--snapshot", snapshotID)
			session := decodeSession(t, start.stdout)
			if start.exit != 0 || session.State != "empty_commit" {
				t.Fatalf("expected empty pause: exit=%d output=%s", start.exit, start.stdout)
			}
			assertPublishedSchema(t, start.stdout)
			refused := runMachine(t, handler, "--yes", "restack", "continue", "--session", session.ID)
			if refused.exit != 2 {
				t.Fatalf("plain empty continuation exit=%d output=%s", refused.exit, refused.stdout)
			}
			completed := runMachine(t, handler, "--yes", "restack", "continue",
				"--session", session.ID, choice)
			if completed.exit != 0 || decodeSession(t, completed.stdout).State != "completed" {
				t.Fatalf("%s continuation failed: exit=%d output=%s stderr=%s",
					choice, completed.exit, completed.stdout, completed.stderr)
			}
			assertPublishedSchema(t, completed.stdout)
		})
	}
}

func TestConcurrentRestackStartReturnsAuthoritativeWaitingSession(t *testing.T) {
	handler, snapshotID := pausedSessionFixture(t, "conflict")
	first := runMachine(t, handler, "--yes", "restack", "--snapshot", snapshotID)
	if first.exit != 0 || decodeSession(t, first.stdout).State != "rebase_conflict" {
		t.Fatalf("first restack did not pause: exit=%d output=%s", first.exit, first.stdout)
	}
	second := runMachine(t, handler, "--yes", "restack", "--snapshot", snapshotID)
	if second.exit != 0 {
		t.Fatalf("competing start must be authoritative: exit=%d output=%s stderr=%s",
			second.exit, second.stdout, second.stderr)
	}
	assertPublishedSchema(t, second.stdout)
	var envelope struct {
		Disposition string `json:"disposition"`
		Findings    []struct {
			Code string `json:"code"`
		} `json:"findings"`
	}
	if err := json.Unmarshal([]byte(second.stdout), &envelope); err != nil {
		t.Fatal(err)
	}
	if envelope.Disposition != "waiting" || len(envelope.Findings) != 1 ||
		envelope.Findings[0].Code != "operation_in_progress" {
		t.Fatalf("unexpected competing-start result: %s", second.stdout)
	}
}

func TestRestackPersistsCursorAcrossRepeatedConflictStops(t *testing.T) {
	handler, snapshotID := pausedSessionFixture(t, "repeat")
	first := runMachine(t, handler, "--yes", "restack", "--snapshot", snapshotID)
	session := decodeSession(t, first.stdout)
	if first.exit != 0 || session.State != "rebase_conflict" || session.Worktree == nil {
		t.Fatalf("first conflict missing: exit=%d output=%s", first.exit, first.stdout)
	}
	if err := os.WriteFile(filepath.Join(session.Worktree.Path, "shared.txt"),
		[]byte("first resolution\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runGit(t, session.Worktree.Path, "add", "shared.txt")
	second := runMachine(t, handler, "--yes", "restack", "continue", "--session", session.ID)
	secondSession := decodeSession(t, second.stdout)
	if second.exit != 0 || secondSession.State != "rebase_conflict" ||
		secondSession.Worktree == nil || secondSession.ID != session.ID {
		t.Fatalf("second conflict was not durably resumed: exit=%d output=%s", second.exit, second.stdout)
	}
	assertPublishedSchema(t, second.stdout)
	if err := os.WriteFile(filepath.Join(secondSession.Worktree.Path, "second.txt"),
		[]byte("second resolution\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runGit(t, secondSession.Worktree.Path, "add", "second.txt")
	completed := runMachine(t, handler, "--yes", "restack", "continue", "--session", session.ID)
	if completed.exit != 0 || decodeSession(t, completed.stdout).State != "completed" {
		t.Fatalf("repeated continuation failed: exit=%d output=%s", completed.exit, completed.stdout)
	}
	assertPublishedSchema(t, completed.stdout)
}

func TestHistoryShowAliasAndPruneThroughMachineInterface(t *testing.T) {
	repo, _, mainOID, firstOID, secondOID := createStackRepository(t)
	mrs, err := json.Marshal([]map[string]any{
		{
			"iid": 1, "state": "opened", "source_branch": "feature/one", "target_branch": "main",
			"sha": firstOID, "web_url": "https://gitlab.example/group/project/-/merge_requests/1",
			"author":                map[string]any{"id": 7, "username": "developer"},
			"diff_refs":             map[string]any{"base_sha": mainOID, "head_sha": firstOID, "start_sha": mainOID},
			"detailed_merge_status": "mergeable",
		},
		{
			"iid": 2, "state": "opened", "source_branch": "feature/two", "target_branch": "feature/one",
			"sha": secondOID, "web_url": "https://gitlab.example/group/project/-/merge_requests/2",
			"author":                map[string]any{"id": 7, "username": "developer"},
			"diff_refs":             map[string]any{"base_sha": firstOID, "head_sha": secondOID, "start_sha": firstOID},
			"detailed_merge_status": "mergeable",
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 7, 25, 12, 0, 0, 0, time.UTC)
	handler := &Handler{
		Runner: fakeGlabRunner{responses: map[string]json.RawMessage{
			"/version": json.RawMessage(`{"version":"18.11.2"}`),
			"/projects/group%2Fproject": json.RawMessage(`{
				"id":42,"path_with_namespace":"group/project",
				"web_url":"https://gitlab.example/group/project","default_branch":"main",
				"only_allow_merge_if_pipeline_succeeds":false
			}`),
			"/projects/42/merge_requests?state=all&scope=all&per_page=100": mrs,
		}},
		Dir: repo, StateDir: filepath.Join(t.TempDir(), "state"),
		Now: func() time.Time { return now },
	}
	first := runMachine(t, handler, "check", "feature/two")
	if first.exit != 0 {
		t.Fatalf("first check failed: %s", first.stdout)
	}
	runGit(t, repo, "checkout", "main")
	if err := os.WriteFile(filepath.Join(repo, "history-base.txt"), []byte("advance\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runGit(t, repo, "add", "history-base.txt")
	runGit(t, repo, "commit", "-m", "history advances base")
	runGit(t, repo, "push", "origin", "main")
	runGit(t, repo, "checkout", "feature/two")
	now = now.Add(time.Hour)
	second := runMachine(t, handler, "check", "feature/two")
	if second.exit != 0 {
		t.Fatalf("second check failed: %s", second.stdout)
	}

	page := runMachine(t, handler, "history", "feature/two", "--limit", "1")
	if page.exit != 0 {
		t.Fatalf("history page failed: %s", page.stdout)
	}
	assertPublishedSchema(t, page.stdout)
	var historyEnvelope struct {
		Data struct {
			History api.HistoryData `json:"history"`
		} `json:"data"`
	}
	if err := json.Unmarshal([]byte(page.stdout), &historyEnvelope); err != nil {
		t.Fatal(err)
	}
	history := historyEnvelope.Data.History
	if len(history.Observations) != 1 || history.NextCursor == nil || history.StackID == "" {
		t.Fatalf("unexpected first history page: %+v", history)
	}
	next := runMachine(t, handler, "history", "--stack", history.StackID,
		"--limit", "1", "--cursor", *history.NextCursor)
	if next.exit != 0 {
		t.Fatalf("history cursor failed: %s", next.stdout)
	}
	assertPublishedSchema(t, next.stdout)
	byMR := runMachine(t, handler, "history", "2")
	if byMR.exit != 0 {
		t.Fatalf("MR selector history failed: %s", byMR.stdout)
	}
	current := runMachine(t, handler, "history")
	if current.exit != 0 {
		t.Fatalf("current branch history failed: %s", current.stdout)
	}

	aliased := runMachine(t, handler, "history", "alias", "--stack", history.StackID, "checkout")
	if aliased.exit != 0 {
		t.Fatalf("history alias failed: %s", aliased.stdout)
	}
	assertPublishedSchema(t, aliased.stdout)
	var aliasEnvelope struct {
		Data struct {
			Alias api.HistoryAliasData `json:"history_alias"`
		} `json:"data"`
	}
	if err := json.Unmarshal([]byte(aliased.stdout), &aliasEnvelope); err != nil {
		t.Fatal(err)
	}
	if aliasEnvelope.Data.Alias.PreviousAlias != nil || aliasEnvelope.Data.Alias.Alias == nil ||
		*aliasEnvelope.Data.Alias.Alias != "checkout" {
		t.Fatalf("unexpected alias result: %+v", aliasEnvelope.Data.Alias)
	}
	cleared := runMachine(t, handler, "history", "alias", "--stack", history.StackID, "--clear")
	if cleared.exit != 0 {
		t.Fatalf("history alias clear failed: %s", cleared.stdout)
	}
	assertPublishedSchema(t, cleared.stdout)
	var clearEnvelope struct {
		Data struct {
			Alias api.HistoryAliasData `json:"history_alias"`
		} `json:"data"`
	}
	if err := json.Unmarshal([]byte(cleared.stdout), &clearEnvelope); err != nil {
		t.Fatal(err)
	}
	if clearEnvelope.Data.Alias.PreviousAlias == nil ||
		*clearEnvelope.Data.Alias.PreviousAlias != "checkout" ||
		clearEnvelope.Data.Alias.Alias != nil {
		t.Fatalf("unexpected cleared alias result: %+v", clearEnvelope.Data.Alias)
	}
	restored := runMachine(t, handler, "history", "alias", "--stack", history.StackID, "checkout")
	if restored.exit != 0 {
		t.Fatalf("history alias restore failed: %s", restored.stdout)
	}

	now = now.Add(time.Hour)
	pruned := runMachine(t, handler, "--yes", "history", "prune", "--stack", history.StackID,
		"--before", now.Format(time.RFC3339))
	if pruned.exit != 0 {
		t.Fatalf("history prune failed: %s", pruned.stdout)
	}
	assertPublishedSchema(t, pruned.stdout)
	var pruneEnvelope struct {
		Data struct {
			Prune api.HistoryPruneData `json:"history_prune"`
		} `json:"data"`
	}
	if err := json.Unmarshal([]byte(pruned.stdout), &pruneEnvelope); err != nil {
		t.Fatal(err)
	}
	if pruneEnvelope.Data.Prune.DeletedObservations != 1 ||
		pruneEnvelope.Data.Prune.PreservedRecords != 1 {
		t.Fatalf("unexpected prune result: %+v", pruneEnvelope.Data.Prune)
	}
	remaining := runMachine(t, handler, "history", "--stack", history.StackID)
	var remainingEnvelope struct {
		Data struct {
			History api.HistoryData `json:"history"`
		} `json:"data"`
	}
	if err := json.Unmarshal([]byte(remaining.stdout), &remainingEnvelope); err != nil {
		t.Fatal(err)
	}
	if remaining.exit != 0 || len(remainingEnvelope.Data.History.Observations) != 1 ||
		remainingEnvelope.Data.History.Alias == nil {
		t.Fatalf("prune did not preserve newest observation and identity: %s", remaining.stdout)
	}
}

func TestLegacySquashAdvancementRetargetFailureIsDurableAndRetryOnly(t *testing.T) {
	root := t.TempDir()
	remote := filepath.Join(root, "remote.git")
	runGit(t, root, "init", "--bare", remote)
	repo := filepath.Join(root, "repo")
	runGit(t, root, "init", "-b", "main", repo)
	runGit(t, repo, "config", "user.name", "Test User")
	runGit(t, repo, "config", "user.email", "test@example.invalid")
	if err := os.WriteFile(filepath.Join(repo, "base.txt"), []byte("base\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runGit(t, repo, "add", "base.txt")
	runGit(t, repo, "commit", "-m", "base")
	baseOID := runGit(t, repo, "rev-parse", "HEAD")
	runGit(t, repo, "checkout", "-b", "feature/merged")
	if err := os.WriteFile(filepath.Join(repo, "merged.txt"), []byte("merged change\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runGit(t, repo, "add", "merged.txt")
	runGit(t, repo, "commit", "-m", "merged layer")
	mergedOID := runGit(t, repo, "rev-parse", "HEAD")
	runGit(t, repo, "checkout", "-b", "feature/successor")
	if err := os.WriteFile(filepath.Join(repo, "successor.txt"), []byte("successor\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runGit(t, repo, "add", "successor.txt")
	runGit(t, repo, "commit", "-m", "successor layer")
	successorOID := runGit(t, repo, "rev-parse", "HEAD")
	runGit(t, repo, "checkout", "main")
	if err := os.WriteFile(filepath.Join(repo, "merged.txt"), []byte("merged change\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runGit(t, repo, "add", "merged.txt")
	runGit(t, repo, "commit", "-m", "squashed merged layer")
	squashOID := runGit(t, repo, "rev-parse", "HEAD")
	runGit(t, repo, "remote", "add", "origin", remote)
	runGit(t, repo, "push", "origin", "main", "feature/successor")
	runGit(t, repo, "checkout", "feature/successor")

	mrs, err := json.Marshal([]map[string]any{
		{
			"iid": 1, "state": "merged", "source_branch": "feature/merged", "target_branch": "main",
			"sha": mergedOID, "web_url": "https://gitlab.example/group/project/-/merge_requests/1",
			"author":            map[string]any{"id": 7, "username": "developer"},
			"diff_refs":         map[string]any{"base_sha": baseOID, "head_sha": mergedOID, "start_sha": baseOID},
			"squash_commit_sha": squashOID, "merged_at": "2026-07-25T10:00:00Z",
		},
		{
			"iid": 2, "state": "opened", "source_branch": "feature/successor", "target_branch": "feature/merged",
			"sha": successorOID, "web_url": "https://gitlab.example/group/project/-/merge_requests/2",
			"author":                map[string]any{"id": 7, "username": "developer"},
			"diff_refs":             map[string]any{"base_sha": mergedOID, "head_sha": successorOID, "start_sha": mergedOID},
			"detailed_merge_status": "mergeable",
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	var calls [][]string
	targetAttempts := 0
	dynamic := func(endpoint string, args []string) (gitexec.Result, error) {
		if endpoint != "/projects/42/merge_requests/2" {
			return gitexec.Result{}, nil
		}
		isPUT := false
		for index, arg := range args {
			isPUT = isPUT || index > 0 && args[index-1] == "--method" && arg == "PUT"
		}
		if isPUT {
			targetAttempts++
			if targetAttempts == 1 {
				return gitexec.Result{Stderr: []byte("temporary target failure")},
					&gitexec.CommandError{Name: "glab", ExitCode: 1, Stderr: "temporary target failure"}
			}
			return gitexec.Result{Stdout: []byte(`{"iid":2}`)}, nil
		}
		current := strings.Fields(runGit(t, repo, "ls-remote", "--heads", "origin",
			"refs/heads/feature/successor"))[0]
		body, marshalErr := json.Marshal(map[string]any{
			"iid": 2, "state": "opened", "source_branch": "feature/successor",
			"target_branch": "feature/merged", "sha": current,
		})
		return gitexec.Result{Stdout: body}, marshalErr
	}
	handler := &Handler{
		Runner: fakeGlabRunner{responses: map[string]json.RawMessage{
			"/version": json.RawMessage(`{"version":"18.11.2"}`),
			"/user":    json.RawMessage(`{"id":7,"username":"developer"}`),
			"/projects/group%2Fproject": json.RawMessage(`{
				"id":42,"path_with_namespace":"group/project",
				"web_url":"https://gitlab.example/group/project","default_branch":"main",
				"only_allow_merge_if_pipeline_succeeds":false
			}`),
			"/projects/42/merge_requests?state=all&scope=all&per_page=100": mrs,
		}, calls: &calls, dynamic: dynamic},
		Dir: repo, StateDir: filepath.Join(t.TempDir(), "state"),
		Now: func() time.Time { return time.Date(2026, 7, 25, 12, 0, 0, 0, time.UTC) },
	}
	check := runMachine(t, handler, "check", "feature/successor")
	if check.exit != 0 {
		t.Fatalf("legacy check failed: %s", check.stdout)
	}
	assertPublishedSchema(t, check.stdout)
	var checkEnvelope struct {
		Stack api.Stack `json:"stack"`
	}
	if err := json.Unmarshal([]byte(check.stdout), &checkEnvelope); err != nil {
		t.Fatal(err)
	}
	if len(checkEnvelope.Stack.Members) != 1 ||
		checkEnvelope.Stack.Members[0].TargetResolution != "integrated_predecessor" {
		t.Fatalf("merged predecessor was not qualified: %s", check.stdout)
	}
	start := runMachine(t, handler, "--yes", "restack", "--snapshot", checkEnvelope.Stack.SnapshotID)
	startSession := decodeSession(t, start.stdout)
	if start.exit != 0 || startSession.State != "retarget_pending" || targetAttempts != 1 {
		t.Fatalf("target failure was not durable: exit=%d attempts=%d output=%s",
			start.exit, targetAttempts, start.stdout)
	}
	assertPublishedSchema(t, start.stdout)
	var startEnvelope struct {
		Session api.Session `json:"session"`
	}
	if err := json.Unmarshal([]byte(start.stdout), &startEnvelope); err != nil {
		t.Fatal(err)
	}
	if startEnvelope.Session.TargetUpdate == nil ||
		startEnvelope.Session.TargetUpdate.FromTarget != "feature/merged" ||
		startEnvelope.Session.TargetUpdate.ToTarget != "main" ||
		startEnvelope.Session.TargetUpdate.Status != "pending" {
		t.Fatalf("legacy target intent was not persisted: %s", start.stdout)
	}
	pushesBefore := countAtomicPushes(calls)
	continued := runMachine(t, handler, "--yes", "restack", "continue", "--session", startSession.ID)
	if continued.exit != 0 || decodeSession(t, continued.stdout).State != "completed" ||
		targetAttempts != 2 {
		t.Fatalf("target retry failed: exit=%d attempts=%d output=%s",
			continued.exit, targetAttempts, continued.stdout)
	}
	assertPublishedSchema(t, continued.stdout)
	if pushesBefore != 1 || countAtomicPushes(calls) != pushesBefore {
		t.Fatalf("retarget retry repeated branch publication: calls=%q", calls)
	}
}

func countAtomicPushes(calls [][]string) int {
	count := 0
	for _, call := range calls {
		if len(call) > 2 && call[0] == "git" && call[1] == "push" && call[2] == "--atomic" {
			count++
		}
	}
	return count
}

func TestHumanAbandonArchivesIndeterminateSessionWithoutRemoteMutation(t *testing.T) {
	repo, _, mainOID, _, _ := createStackRepository(t)
	var calls [][]string
	handler := &Handler{
		Runner: fakeGlabRunner{responses: map[string]json.RawMessage{}, calls: &calls},
		Dir:    repo, StateDir: filepath.Join(t.TempDir(), "state"),
		Now: func() time.Time { return time.Date(2026, 7, 25, 12, 0, 0, 0, time.UTC) },
	}
	worktree := filepath.Join(handler.StateDir, "worktrees", "ses_abandon")
	if err := os.MkdirAll(filepath.Dir(worktree), 0o700); err != nil {
		t.Fatal(err)
	}
	runGit(t, repo, "worktree", "add", "--detach", worktree, mainOID)
	oldOID := strings.Repeat("a", 40)
	newOID := strings.Repeat("b", 40)
	remote := api.Remote{
		Name: "origin", Selection: "explicit",
		Fetch: api.RemoteEndpoint{Host: "gitlab.example", Project: "group/project"},
		Push:  api.RemoteEndpoint{Host: "gitlab.example", Project: "group/project"},
	}
	durable := durableSession{API: api.Session{
		SessionID: "ses_abandon", State: "preparing", SnapshotID: "snp_abandon",
		CreatedAt: handler.now(), UpdatedAt: handler.now(), Remote: remote,
		Worktree:           &api.SessionWorktree{Path: worktree, GitState: "clean"},
		AffectedMemberIIDs: []int{1},
		Publication: api.Publication{State: "not_started", Refs: []api.PublicationRef{{
			Branch: "feature", OldSHA: oldOID, CurrentSHA: &oldOID, Classification: "old",
		}}},
		Resumable: true, Abortable: true,
	}, ProjectID: "42"}
	j, err := handler.openJournal(repo)
	if err != nil {
		t.Fatal(err)
	}
	payload, _ := json.Marshal(durable)
	if err := j.BeginSession(context.Background(), journal.Session{
		ID: "ses_abandon", ProjectKey: "gitlab.example/group/project",
		SnapshotID: "snp_abandon", State: "preparing",
		OldRefs: map[string]string{"feature": oldOID}, NewRefs: map[string]string{},
		Payload: payload,
	}); err != nil {
		t.Fatal(err)
	}
	stored, err := handler.transitionSession(context.Background(), j, "ses_abandon", 1,
		&durable, "replaying")
	if err != nil {
		t.Fatal(err)
	}
	durable.API.Publication = api.Publication{State: "all_old", Refs: []api.PublicationRef{{
		Branch: "feature", OldSHA: oldOID, NewSHA: &newOID,
		CurrentSHA: &oldOID, Classification: "old",
	}}}
	durable.API.State = "publication_ready"
	payload, _ = json.Marshal(durable)
	stored, err = j.PreparePublication(context.Background(), "ses_abandon",
		stored.Revision, map[string]string{"feature": newOID}, payload)
	if err != nil {
		t.Fatal(err)
	}
	stored, err = handler.transitionSession(context.Background(), j, "ses_abandon",
		stored.Revision, &durable, "publication_pending_reconcile")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = handler.transitionSession(context.Background(), j, "ses_abandon",
		stored.Revision, &durable, "indeterminate_publication"); err != nil {
		t.Fatal(err)
	}
	if err := j.Close(); err != nil {
		t.Fatal(err)
	}

	agent := runMachine(t, handler, "restack", "abandon", "--session", "ses_abandon",
		"--accept-current-remote")
	if agent.exit != 2 {
		t.Fatalf("agent abandonment must be parser-invalid: exit=%d output=%s", agent.exit, agent.stdout)
	}
	assertPublishedSchema(t, agent.stdout)
	var stdout, stderr bytes.Buffer
	exit := cli.RunWithHandler([]string{
		"--remote", "origin", "restack", "abandon", "--session", "ses_abandon",
		"--accept-current-remote",
	}, &stdout, &stderr, handler)
	if exit != 0 || stderr.Len() != 0 || !strings.Contains(stdout.String(), "abandoned") {
		t.Fatalf("human abandon failed: exit=%d stdout=%s stderr=%s", exit, stdout.String(), stderr.String())
	}
	for _, call := range calls {
		if call[0] == "glab" || len(call) > 1 && call[0] == "git" && call[1] == "push" {
			t.Fatalf("abandon performed a remote mutation: %q", call)
		}
	}
	if _, err := os.Stat(worktree); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("managed worktree was not removed: %v", err)
	}
	j, err = handler.openJournal(repo)
	if err != nil {
		t.Fatal(err)
	}
	defer j.Close()
	archived, err := j.Session(context.Background(), "ses_abandon")
	if err != nil || archived.State != "abandoned" {
		t.Fatalf("session was not archived: state=%s err=%v", archived.State, err)
	}
}

type machineRun struct {
	exit           int
	stdout, stderr string
}

func runMachine(t *testing.T, handler *Handler, args ...string) machineRun {
	t.Helper()
	full := []string{"--json", "--no-input", "--remote", "origin"}
	full = append(full, args...)
	var stdout, stderr bytes.Buffer
	exit := cli.RunWithHandler(full, &stdout, &stderr, handler)
	return machineRun{exit: exit, stdout: stdout.String(), stderr: stderr.String()}
}

type decodedSession struct {
	ID       string `json:"session_id"`
	State    string `json:"state"`
	Worktree *struct {
		Path string `json:"path"`
	} `json:"worktree"`
}

func decodeSession(t *testing.T, document string) decodedSession {
	t.Helper()
	var envelope struct {
		Session decodedSession `json:"session"`
	}
	if err := json.Unmarshal([]byte(document), &envelope); err != nil {
		t.Fatalf("decode session: %v\n%s", err, document)
	}
	return envelope.Session
}

func assertPublishedSchema(t *testing.T, document string) {
	t.Helper()
	schema, err := jsonschema.NewCompiler().Compile(
		filepath.Join(repositoryRoot(t), "docs", "schema", "mrstack-v1.schema.json"))
	if err != nil {
		t.Fatal(err)
	}
	instance, err := jsonschema.UnmarshalJSON(strings.NewReader(document))
	if err != nil {
		t.Fatal(err)
	}
	if err := schema.Validate(instance); err != nil {
		t.Fatalf("schema-invalid output:\n%s\n%v", document, err)
	}
}

func pausedSessionFixture(t *testing.T, kind string) (*Handler, string) {
	t.Helper()
	root := t.TempDir()
	remote := filepath.Join(root, "remote.git")
	runGit(t, root, "init", "--bare", remote)
	repo := filepath.Join(root, "repo")
	runGit(t, root, "init", "-b", "main", repo)
	runGit(t, repo, "config", "user.name", "Test User")
	runGit(t, repo, "config", "user.email", "test@example.invalid")
	if err := os.WriteFile(filepath.Join(repo, "shared.txt"), []byte("base\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, "second.txt"), []byte("base second\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runGit(t, repo, "add", "shared.txt", "second.txt")
	runGit(t, repo, "commit", "-m", "base")
	baseOID := runGit(t, repo, "rev-parse", "HEAD")
	runGit(t, repo, "checkout", "-b", "feature")
	if kind == "conflict" || kind == "repeat" {
		if err := os.WriteFile(filepath.Join(repo, "shared.txt"), []byte("feature\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		runGit(t, repo, "add", "shared.txt")
	} else {
		if err := os.WriteFile(filepath.Join(repo, "same.txt"), []byte("identical\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		runGit(t, repo, "add", "same.txt")
	}
	runGit(t, repo, "commit", "-m", "feature layer")
	if kind == "repeat" {
		if err := os.WriteFile(filepath.Join(repo, "second.txt"), []byte("feature second\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		runGit(t, repo, "add", "second.txt")
		runGit(t, repo, "commit", "-m", "feature second layer commit")
	}
	featureOID := runGit(t, repo, "rev-parse", "HEAD")
	runGit(t, repo, "checkout", "main")
	if kind == "conflict" || kind == "repeat" {
		if err := os.WriteFile(filepath.Join(repo, "shared.txt"), []byte("main\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		runGit(t, repo, "add", "shared.txt")
		if kind == "repeat" {
			if err := os.WriteFile(filepath.Join(repo, "second.txt"), []byte("main second\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			runGit(t, repo, "add", "second.txt")
		}
	} else {
		if err := os.WriteFile(filepath.Join(repo, "same.txt"), []byte("identical\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		runGit(t, repo, "add", "same.txt")
	}
	runGit(t, repo, "commit", "-m", "advance base")
	runGit(t, repo, "remote", "add", "origin", remote)
	runGit(t, repo, "push", "origin", "main", "feature")
	runGit(t, repo, "checkout", "feature")
	mrs, err := json.Marshal([]map[string]any{{
		"iid": 1, "state": "opened", "source_branch": "feature", "target_branch": "main",
		"sha": featureOID, "web_url": "https://gitlab.example/group/project/-/merge_requests/1",
		"author":                map[string]any{"id": 7, "username": "developer"},
		"diff_refs":             map[string]any{"base_sha": baseOID, "head_sha": featureOID, "start_sha": baseOID},
		"detailed_merge_status": "mergeable",
	}})
	if err != nil {
		t.Fatal(err)
	}
	handler := &Handler{
		Runner: fakeGlabRunner{responses: map[string]json.RawMessage{
			"/version": json.RawMessage(`{"version":"18.11.2"}`),
			"/user":    json.RawMessage(`{"id":7,"username":"developer"}`),
			"/projects/group%2Fproject": json.RawMessage(`{
				"id":42,"path_with_namespace":"group/project",
				"web_url":"https://gitlab.example/group/project","default_branch":"main",
				"only_allow_merge_if_pipeline_succeeds":false
			}`),
			"/projects/42/merge_requests?state=all&scope=all&per_page=100": mrs,
		}},
		Dir: repo, StateDir: filepath.Join(t.TempDir(), "state"),
		Now: func() time.Time { return time.Date(2026, 7, 25, 12, 0, 0, 0, time.UTC) },
	}
	check := runMachine(t, handler, "check", "feature")
	if check.exit != 0 {
		t.Fatalf("fixture check failed: %s", check.stdout)
	}
	var envelope struct {
		Stack struct {
			SnapshotID string `json:"snapshot_id"`
		} `json:"stack"`
	}
	if err := json.Unmarshal([]byte(check.stdout), &envelope); err != nil {
		t.Fatal(err)
	}
	return handler, envelope.Stack.SnapshotID
}

func repositoryRoot(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("cannot locate repository root")
	}
	return filepath.Clean(filepath.Join(filepath.Dir(file), "..", ".."))
}

func createStackRepository(t *testing.T) (repo, remote, mainOID, firstOID, secondOID string) {
	t.Helper()
	root := t.TempDir()
	remote = filepath.Join(root, "remote.git")
	runGit(t, root, "init", "--bare", remote)
	repo = filepath.Join(root, "repo")
	runGit(t, root, "init", "-b", "main", repo)
	runGit(t, repo, "config", "user.name", "Test User")
	runGit(t, repo, "config", "user.email", "test@example.invalid")
	if err := os.WriteFile(filepath.Join(repo, "stack.txt"), []byte("main\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runGit(t, repo, "add", "stack.txt")
	runGit(t, repo, "commit", "-m", "main")
	mainOID = runGit(t, repo, "rev-parse", "HEAD")
	runGit(t, repo, "checkout", "-b", "feature/one")
	if err := os.WriteFile(filepath.Join(repo, "one.txt"), []byte("one\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runGit(t, repo, "add", "one.txt")
	runGit(t, repo, "commit", "-m", "layer one")
	firstOID = runGit(t, repo, "rev-parse", "HEAD")
	runGit(t, repo, "checkout", "-b", "feature/two")
	if err := os.WriteFile(filepath.Join(repo, "two.txt"), []byte("two\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runGit(t, repo, "add", "two.txt")
	runGit(t, repo, "commit", "-m", "layer two")
	secondOID = runGit(t, repo, "rev-parse", "HEAD")
	runGit(t, repo, "remote", "add", "origin", remote)
	runGit(t, repo, "push", "origin", "main", "feature/one", "feature/two")
	return repo, remote, mainOID, firstOID, secondOID
}

func runGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_CONFIG_NOSYSTEM=1")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %q: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}
