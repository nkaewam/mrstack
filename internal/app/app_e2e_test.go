package app

import (
	"bytes"
	"context"
	"database/sql"
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

type plannerShapeRunner struct {
	fakeGlabRunner
	shape string
}

func (r plannerShapeRunner) Run(
	ctx context.Context, dir, command string, args ...string,
) (gitexec.Result, error) {
	if command == "git" && len(args) >= 3 && args[0] == "log" &&
		args[1] == "--reverse" && strings.Contains(args[2], "%G?") {
		result, err := r.ExecRunner.Run(ctx, dir, command, args...)
		if err != nil {
			return result, err
		}
		switch r.shape {
		case "signed":
			result.Stdout = bytes.ReplaceAll(result.Stdout, []byte("\x00N"), []byte("\x00G"))
		case "merge":
			lines := strings.Split(strings.TrimSpace(string(result.Stdout)), "\n")
			if len(lines) > 0 {
				fields := strings.Split(lines[0], "\x00")
				if len(fields) == 3 {
					fields[1] += " " + strings.Repeat("f", 40)
					lines[0] = strings.Join(fields, "\x00")
					result.Stdout = []byte(strings.Join(lines, "\n") + "\n")
				}
			}
		case "empty":
			result.Stdout = nil
		}
		return result, nil
	}
	return r.fakeGlabRunner.Run(ctx, dir, command, args...)
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
	payload := []map[string]any{
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
	}
	stateDir, stacksDir := testDirs(t)
	responses := glabProjectResponses(false)
	addPerMREndpoints(responses, payload)
	runner := fakeGlabRunner{responses: responses}
	handler := &Handler{
		Runner: runner, Dir: repo, StateDir: stateDir, StacksDir: stacksDir,
		Now: func() time.Time { return time.Date(2026, 7, 25, 12, 0, 0, 0, time.UTC) },
	}
	registerNamedStack(t, handler, testStackName, 1, 2)
	var stdout, stderr bytes.Buffer
	exit := cli.RunWithHandler([]string{
		"--json", "--no-input", "--remote", "origin", "check", testStackName,
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

	var calls [][]string
	stateDir, stacksDir := testDirs(t)
	responses := glabProjectResponses(false)
	responses["/user"] = json.RawMessage(`{"id":7,"username":"developer"}`)
	addPerMREndpoints(responses, stackMRPayload(originalMain, firstOID, secondOID, "opened", "", ""))
	handler := &Handler{
		Runner: fakeGlabRunner{responses: responses, calls: &calls},
		Dir:    repo, StateDir: stateDir, StacksDir: stacksDir,
		Now: func() time.Time { return time.Date(2026, 7, 25, 12, 0, 0, 0, time.UTC) },
	}
	registerNamedStack(t, handler, testStackName, 1, 2)
	var checked bytes.Buffer
	exit := cli.RunWithHandler([]string{
		"--json", "--no-input", "--remote", "origin", "check", testStackName,
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
	assertActionPacket(t, checked.String(), "restack_required", "restack", "start_restack")

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
	assertActionPacket(t, stdout.String(), "local_checkout_stale", "refresh_local_checkout")
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
	assertActionPacket(t, start.stdout, "rebase_conflict", "resolve_conflict",
		"continue_restack")
	var packet api.Envelope
	if err := json.Unmarshal([]byte(start.stdout), &packet); err != nil {
		t.Fatal(err)
	}
	var continueAction *api.Action
	for index := range packet.Remediations[0].Actions {
		if packet.Remediations[0].Actions[index].Kind == "continue_restack" {
			continueAction = &packet.Remediations[0].Actions[index]
		}
	}
	if continueAction == nil || !containsArgPair(continueAction.Argv, "--remote", "origin") {
		t.Fatalf("continue packet lacks explicit session remote: %s", start.stdout)
	}
	detachedHandler := Handler{
		Runner:   handler.Runner,
		Dir:      continueAction.CWD,
		StateDir: handler.StateDir,
		Now:      handler.Now,
	}
	var dispatchedOut, dispatchedErr bytes.Buffer
	dispatchedExit := cli.RunWithHandler(
		continueAction.Argv[1:], &dispatchedOut, &dispatchedErr, &detachedHandler)
	if dispatchedExit != 2 || strings.Contains(dispatchedOut.String(), "detached HEAD") {
		t.Fatalf("packet argv did not dispatch from declared detached CWD: exit=%d out=%s err=%s",
			dispatchedExit, dispatchedOut.String(), dispatchedErr.String())
	}
	assertPublishedSchema(t, dispatchedOut.String())

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

func TestRestackCrashBoundariesRemainRecoverableAndAbortable(t *testing.T) {
	for _, boundary := range []string{
		"after_session_begin", "after_worktree_prepare", "after_replay_checkpoint",
	} {
		t.Run(boundary, func(t *testing.T) {
			handler, snapshotID := pausedSessionFixture(t, "conflict")
			handler.Failpoint = func(name string) error {
				if name == boundary {
					return errors.New("simulated process death")
				}
				return nil
			}
			interrupted := runMachine(t, handler, "--yes", "restack", "--snapshot", snapshotID)
			if interrupted.exit != 3 {
				t.Fatalf("boundary %s did not interrupt after durable checkpoint: %s",
					boundary, interrupted.stdout)
			}
			j, err := handler.openJournal(handler.Dir)
			if err != nil {
				t.Fatal(err)
			}
			stored, err := j.ActiveSession(context.Background(), "gitlab.example/group/project")
			_ = j.Close()
			if err != nil {
				t.Fatalf("durable session missing after %s: %v", boundary, err)
			}
			handler.Failpoint = nil
			recovered := runMachine(t, handler, "restack", "recover", "--session", stored.ID)
			if recovered.exit != 0 {
				t.Fatalf("read-only recovery rejected %s session: %s", stored.State, recovered.stdout)
			}
			continued := runMachine(t, handler, "--yes", "restack", "continue", "--session", stored.ID)
			session := decodeSession(t, continued.stdout)
			if continued.exit != 0 || session.State != "rebase_conflict" || session.Worktree == nil {
				t.Fatalf("boundary %s did not restart exact replay: exit=%d output=%s",
					boundary, continued.exit, continued.stdout)
			}
			aborted := runMachine(t, handler, "--yes", "restack", "abort", "--session", stored.ID)
			if aborted.exit != 0 || decodeSession(t, aborted.stdout).State != "aborted" {
				t.Fatalf("boundary %s did not abort cleanly: %s", boundary, aborted.stdout)
			}
			if _, err := os.Stat(session.Worktree.Path); !os.IsNotExist(err) {
				t.Fatalf("managed worktree survived abort at %s: err=%v", boundary, err)
			}
		})
	}
}

func TestRestackAbortCleansPreWorktreeCrashCheckpoint(t *testing.T) {
	handler, snapshotID := pausedSessionFixture(t, "conflict")
	handler.Failpoint = func(name string) error {
		if name == "after_session_begin" {
			return errors.New("simulated process death")
		}
		return nil
	}
	if interrupted := runMachine(t, handler, "--yes", "restack", "--snapshot", snapshotID); interrupted.exit != 3 {
		t.Fatalf("expected durable preparation interruption: %s", interrupted.stdout)
	}
	j, err := handler.openJournal(handler.Dir)
	if err != nil {
		t.Fatal(err)
	}
	stored, err := j.ActiveSession(context.Background(), "gitlab.example/group/project")
	_ = j.Close()
	if err != nil {
		t.Fatal(err)
	}
	handler.Failpoint = nil
	aborted := runMachine(t, handler, "--yes", "restack", "abort", "--session", stored.ID)
	if aborted.exit != 0 || decodeSession(t, aborted.stdout).State != "aborted" {
		t.Fatalf("pre-worktree checkpoint was not abortable: %s", aborted.stdout)
	}
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
			assertActionPacket(t, start.stdout, "empty_commit", "choose_empty_commit",
				"continue_drop_current", "continue_keep_empty")
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

func TestRestackBlocksUncheckedOutLocalAheadBranchAuthoritatively(t *testing.T) {
	handler, snapshotID := pausedSessionFixture(t, "conflict")
	repo := handler.Dir
	runGit(t, repo, "checkout", "main")
	old := runGit(t, repo, "rev-parse", "refs/heads/feature")
	tree := runGit(t, repo, "rev-parse", "refs/heads/feature^{tree}")
	local := runGit(t, repo, "commit-tree", tree, "-p", old, "-m", "local only")
	runGit(t, repo, "update-ref", "refs/heads/feature", local, old)

	start := runMachine(t, handler, "--yes", "restack", "--snapshot", snapshotID)
	if start.exit != 0 {
		t.Fatalf("local-work blocker must be authoritative: exit=%d output=%s stderr=%s",
			start.exit, start.stdout, start.stderr)
	}
	assertPublishedSchema(t, start.stdout)
	assertActionPacket(t, start.stdout, "local_work_present", "human_handoff")
	var envelope api.Envelope
	if err := json.Unmarshal([]byte(start.stdout), &envelope); err != nil {
		t.Fatal(err)
	}
	if envelope.Disposition == nil || *envelope.Disposition != api.DispositionHumanRequired {
		t.Fatalf("unexpected local-work disposition: %s", start.stdout)
	}
}

func TestRestackDefaultBranchMovementIsAuthoritativeBeforeAndAfterReplay(t *testing.T) {
	for _, timing := range []string{"before_replay", "after_replay"} {
		t.Run(timing, func(t *testing.T) {
			handler, snapshotID, repo, updater, calls := movingBaseRestackFixture(t)
			oldOne := runGit(t, repo, "ls-remote", "origin", "refs/heads/feature/one")
			oldTwo := runGit(t, repo, "ls-remote", "origin", "refs/heads/feature/two")
			advance := func() {
				if err := os.WriteFile(filepath.Join(updater, "moved-"+timing+".txt"),
					[]byte("moved\n"), 0o600); err != nil {
					t.Fatal(err)
				}
				runGit(t, updater, "add", ".")
				runGit(t, updater, "commit", "-m", "move base "+timing)
				runGit(t, updater, "push", "origin", "main")
			}
			if timing == "before_replay" {
				advance()
			} else {
				runner := handler.Runner.(fakeGlabRunner)
				var refreshes int
				runner.dynamic = func(endpoint string, _ []string) (gitexec.Result, error) {
					if strings.HasPrefix(endpoint, "/projects/42/merge_requests/") {
						refreshes++
						if refreshes == 3 {
							advance()
						}
					}
					return gitexec.Result{}, nil
				}
				handler.Runner = runner
			}
			start := runMachine(t, handler, "--yes", "restack", "--snapshot", snapshotID)
			if start.exit != 0 || strings.Contains(strings.ToLower(start.stdout), "prompt") {
				t.Fatalf("remote movement must be authoritative and noninteractive: %+v", start)
			}
			assertPublishedSchema(t, start.stdout)
			assertActionPacket(t, start.stdout, "remote_changed", "wait_and_recheck", "recheck")
			if timing == "after_replay" {
				session := decodeSession(t, start.stdout)
				if session.State != "invalidated" || session.Worktree != nil {
					t.Fatalf("post-replay movement did not cleanly invalidate: %s", start.stdout)
				}
			}
			if got := runGit(t, repo, "ls-remote", "origin", "refs/heads/feature/one"); got != oldOne {
				t.Fatalf("feature/one published during invalidation: %s != %s", got, oldOne)
			}
			if got := runGit(t, repo, "ls-remote", "origin", "refs/heads/feature/two"); got != oldTwo {
				t.Fatalf("feature/two published during invalidation: %s != %s", got, oldTwo)
			}
			for _, call := range *calls {
				if len(call) > 1 && call[0] == "git" && call[1] == "push" {
					t.Fatalf("remote movement sent publication push: %q", call)
				}
			}
		})
	}
}

func TestRestackPreflightShapesAreAuthoritativeAndNeverPublish(t *testing.T) {
	tests := []struct {
		shape, code, remediation string
	}{
		{"signed", "signed_commits", "authorize_signature_loss"},
		{"merge", "merge_commit_in_layer", "human_handoff"},
		{"empty", "empty_layer", "human_handoff"},
	}
	for _, tt := range tests {
		t.Run(tt.shape, func(t *testing.T) {
			handler, snapshotID, repo, _, calls := movingBaseRestackFixture(t)
			handler.Runner = plannerShapeRunner{
				fakeGlabRunner: handler.Runner.(fakeGlabRunner), shape: tt.shape,
			}
			oldRefs := runGit(t, repo, "ls-remote", "--heads", "origin")
			blocked := runMachine(t, handler, "--yes", "restack", "--snapshot", snapshotID)
			if blocked.exit != 0 || strings.Contains(strings.ToLower(blocked.stdout), "prompt") {
				t.Fatalf("%s blocker was not authoritative/noninteractive: %+v", tt.shape, blocked)
			}
			assertPublishedSchema(t, blocked.stdout)
			assertActionPacket(t, blocked.stdout, tt.code, tt.remediation)
			if got := runGit(t, repo, "ls-remote", "--heads", "origin"); got != oldRefs {
				t.Fatalf("%s blocker changed remote refs", tt.shape)
			}
			for _, call := range *calls {
				if len(call) > 1 && call[0] == "git" && call[1] == "push" {
					t.Fatalf("%s blocker attempted publication: %q", tt.shape, call)
				}
			}
			if tt.shape == "signed" {
				allowed := runMachine(t, handler, "--yes", "restack", "--snapshot", snapshotID,
					"--allow-signature-loss")
				if allowed.exit != 0 {
					t.Fatalf("explicit signature-loss authorization failed: %s", allowed.stdout)
				}
				assertPublishedSchema(t, allowed.stdout)
				var envelope api.Envelope
				if err := json.Unmarshal([]byte(allowed.stdout), &envelope); err != nil {
					t.Fatal(err)
				}
				if envelope.Session == nil || !envelope.Session.SignatureLossAuthorized {
					t.Fatalf("signature-loss authorization was not journaled: %s", allowed.stdout)
				}
			}
		})
	}
}

func TestRestackPlanAmbiguousBoundaryIsAuthoritativeAndReadOnly(t *testing.T) {
	handler, snapshotID, repo, _, calls := movingBaseRestackFixture(t)
	unrelated := runGit(t, repo, "rev-parse", "refs/remotes/origin/main")
	oldRefs := runGit(t, repo, "ls-remote", "--heads", "origin")
	planned := runMachine(t, handler, "restack", "plan", "--snapshot", snapshotID,
		"--layer-boundary", "1="+unrelated)
	if planned.exit != 0 || strings.Contains(strings.ToLower(planned.stdout), "prompt") {
		t.Fatalf("ambiguous boundary was not authoritative/noninteractive: %+v", planned)
	}
	assertPublishedSchema(t, planned.stdout)
	assertActionPacket(t, planned.stdout, "ambiguous_layer_boundary", "human_handoff")
	if got := runGit(t, repo, "ls-remote", "--heads", "origin"); got != oldRefs {
		t.Fatal("ambiguous boundary changed remote refs")
	}
	for _, call := range *calls {
		if len(call) > 1 && call[0] == "git" && call[1] == "push" {
			t.Fatalf("ambiguous boundary attempted publication: %q", call)
		}
	}
}

func TestMalformedDurableReplayCursorFailsInternallyWithoutPublishing(t *testing.T) {
	handler, snapshotID := pausedSessionFixture(t, "conflict")
	start := runMachine(t, handler, "--yes", "restack", "--snapshot", snapshotID)
	session := decodeSession(t, start.stdout)
	if start.exit != 0 || session.Worktree == nil {
		t.Fatalf("conflict fixture did not pause: %s", start.stdout)
	}
	if err := os.WriteFile(filepath.Join(session.Worktree.Path, "shared.txt"),
		[]byte("resolved\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runGit(t, session.Worktree.Path, "add", "shared.txt")
	j, err := handler.openJournal(handler.Dir)
	if err != nil {
		t.Fatal(err)
	}
	stored, err := j.Session(context.Background(), session.ID)
	if err != nil {
		t.Fatal(err)
	}
	var durable durableSession
	if err := json.Unmarshal(stored.Payload, &durable); err != nil {
		t.Fatal(err)
	}
	durable.ReplayLayer = len(durable.Plan.Layers) + 10
	payload, _ := json.Marshal(durable)
	j.Close()
	db, err := sql.Open("sqlite", filepath.Join(handler.StateDir, "journal.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE sessions SET payload=? WHERE session_id=?`, payload, session.ID); err != nil {
		t.Fatal(err)
	}
	db.Close()
	oldRefs := runGit(t, handler.Dir, "ls-remote", "--heads", "origin")
	resumed := runMachine(t, handler, "--yes", "restack", "continue", "--session", session.ID)
	if resumed.exit != 4 || strings.Contains(strings.ToLower(resumed.stdout), "prompt") {
		t.Fatalf("malformed cursor did not fail closed as internal: %+v", resumed)
	}
	assertPublishedSchema(t, resumed.stdout)
	if got := runGit(t, handler.Dir, "ls-remote", "--heads", "origin"); got != oldRefs {
		t.Fatal("malformed cursor published remote refs")
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
	assertActionPacket(t, second.stdout, "operation_in_progress", "wait_and_recheck", "recheck")
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
	stateDir, stacksDir := testDirs(t)
	responses := glabProjectResponses(false)
	var payload []map[string]any
	if err := json.Unmarshal(mrs, &payload); err != nil {
		t.Fatal(err)
	}
	addPerMREndpoints(responses, payload)
	handler := &Handler{
		Runner: fakeGlabRunner{responses: responses},
		Dir:    repo, StateDir: stateDir, StacksDir: stacksDir,
		Now: func() time.Time { return now },
	}
	registerNamedStack(t, handler, testStackName, 1, 2)
	first := runMachine(t, handler, "check", testStackName)
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
	second := runMachine(t, handler, "check", testStackName)
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

func TestCheckReconcilesTrackedFullyMergedStackOrFailsClosed(t *testing.T) {
	for _, ambiguous := range []bool{false, true} {
		t.Run(map[bool]string{false: "complete", true: "ambiguous"}[ambiguous], func(t *testing.T) {
			repo, _, mainOID, firstOID, secondOID := createStackRepository(t)
			responses := glabProjectResponses(false)
			addPerMREndpoints(responses, stackMRPayload(mainOID, firstOID, secondOID, "opened", "", ""))
			stateDir, stacksDir := testDirs(t)
			handler := &Handler{
				Runner: fakeGlabRunner{responses: responses}, Dir: repo,
				StateDir: stateDir, StacksDir: stacksDir,
				Now: func() time.Time { return time.Date(2026, 7, 25, 12, 0, 0, 0, time.UTC) },
			}
			registerNamedStack(t, handler, testStackName, 1, 2)
			observed := runMachine(t, handler, "check", testStackName)
			var captured api.Envelope
			if observed.exit != 0 || json.Unmarshal([]byte(observed.stdout), &captured) != nil ||
				captured.Stack == nil {
				t.Fatalf("capture failed: %s", observed.stdout)
			}
			runGit(t, repo, "checkout", "main")
			runGit(t, repo, "merge", "--no-ff", "-m", "merge one", "feature/one")
			firstIntegration := runGit(t, repo, "rev-parse", "HEAD")
			runGit(t, repo, "merge", "--no-ff", "-m", "merge two", "feature/two")
			secondIntegration := runGit(t, repo, "rev-parse", "HEAD")
			runGit(t, repo, "push", "origin", "main")
			if ambiguous {
				secondIntegration = ""
			}
			addPerMREndpoints(responses, stackMRPayload(
				mainOID, firstOID, secondOID, "merged", firstIntegration, secondIntegration))

			reconciled := runMachine(t, handler, "check", "--stack", captured.Stack.StackID)
			if reconciled.exit != 0 {
				t.Fatalf("completion reconciliation must be authoritative: %s", reconciled.stdout)
			}
			assertPublishedSchema(t, reconciled.stdout)
			var envelope api.Envelope
			if err := json.Unmarshal([]byte(reconciled.stdout), &envelope); err != nil {
				t.Fatal(err)
			}
			want := api.DispositionComplete
			if ambiguous {
				want = api.DispositionHumanRequired
				assertActionPacket(t, reconciled.stdout, "ambiguous_completion", "human_handoff")
			}
			if envelope.Disposition == nil || *envelope.Disposition != want {
				t.Fatalf("completion disposition got %v want %s: %s",
					envelope.Disposition, want, reconciled.stdout)
			}
		})
	}
}

func TestTrackedCompletionRejectsSameIIDsFromDifferentCanonicalProject(t *testing.T) {
	repo, _, mainOID, firstOID, secondOID := createStackRepository(t)
	now := time.Date(2026, 7, 25, 12, 0, 0, 0, time.UTC)
	stateDir, stacksDir := testDirs(t)
	responses := glabProjectResponses(false)
	addPerMREndpoints(responses, stackMRPayload(mainOID, firstOID, secondOID, "opened", "", ""))
	handler := &Handler{
		Runner: fakeGlabRunner{responses: responses},
		Dir:    repo, StateDir: stateDir, StacksDir: stacksDir,
		Now: func() time.Time {
			return now
		},
	}
	registerNamedStack(t, handler, testStackName, 1, 2)
	observed := runMachine(t, handler, "check", testStackName)
	var historical api.Envelope
	if observed.exit != 0 || json.Unmarshal([]byte(observed.stdout), &historical) != nil ||
		historical.Stack == nil {
		t.Fatalf("capture failed: %s", observed.stdout)
	}
	stackID := historical.Stack.StackID
	historical.Stack.Project.Host = "other.gitlab.example"
	historical.Stack.Project.PathWithNamespace = "other/project"
	historical.Stack.Project.ID = "99"
	historical.Stack.SnapshotID = "snp_cross_project"
	payload, _ := json.Marshal(historical)
	now = now.Add(time.Hour)
	j, err := handler.openJournal(repo)
	if err != nil {
		t.Fatal(err)
	}
	err = j.RecordObservation(context.Background(), journal.Observation{
		ObservationID: "obs_cross_project", StackID: stackID,
		ProjectKey: "gitlab.example/group/project", SnapshotID: "snp_cross_project",
		Disposition: string(api.DispositionReady), Payload: payload,
	})
	j.Close()
	if err != nil {
		t.Fatal(err)
	}
	reconciled := runMachine(t, handler, "check", "--stack", stackID)
	if reconciled.exit != 2 {
		t.Fatalf("cross-project same-IID history was joined: %s", reconciled.stdout)
	}
	assertPublishedSchema(t, reconciled.stdout)
}

func stackMRPayload(base, first, second, state, firstIntegration, secondIntegration string) []map[string]any {
	mergedAtOne, mergedAtTwo := "", ""
	if state == "merged" {
		mergedAtOne, mergedAtTwo = "2026-07-25T12:10:00Z", "2026-07-25T12:20:00Z"
	}
	return []map[string]any{
		{
			"iid": 1, "state": state, "source_branch": "feature/one", "target_branch": "main",
			"sha": first, "web_url": "https://gitlab.example/group/project/-/merge_requests/1",
			"author":                map[string]any{"id": 7, "username": "developer"},
			"diff_refs":             map[string]any{"base_sha": base, "head_sha": first, "start_sha": base},
			"detailed_merge_status": "mergeable", "merge_commit_sha": firstIntegration,
			"merged_at": mergedAtOne,
		},
		{
			"iid": 2, "state": state, "source_branch": "feature/two", "target_branch": "feature/one",
			"sha": second, "web_url": "https://gitlab.example/group/project/-/merge_requests/2",
			"author":                map[string]any{"id": 7, "username": "developer"},
			"diff_refs":             map[string]any{"base_sha": first, "head_sha": second, "start_sha": first},
			"detailed_merge_status": "mergeable", "merge_commit_sha": secondIntegration,
			"merged_at": mergedAtTwo,
		},
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
	targetApplied := false
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
				// Simulate the provider applying the mutation before the client
				// loses the response/crashes.
				targetApplied = true
				return gitexec.Result{Stderr: []byte("response lost after apply")},
					&gitexec.CommandError{Name: "glab", ExitCode: 1, Stderr: "response lost after apply"}
			}
			return gitexec.Result{Stdout: []byte(`{"iid":2}`)}, nil
		}
		current := strings.Fields(runGit(t, repo, "ls-remote", "--heads", "origin",
			"refs/heads/feature/successor"))[0]
		target := "feature/merged"
		if targetApplied {
			target = "main"
		}
		body, marshalErr := json.Marshal(map[string]any{
			"iid": 2, "state": "opened", "source_branch": "feature/successor",
			"target_branch": target, "sha": current,
			"web_url": "https://gitlab.example/group/project/-/merge_requests/2",
			"author":  map[string]any{"id": 7, "username": "developer"},
			"diff_refs": map[string]any{
				"base_sha": mergedOID, "head_sha": current, "start_sha": mergedOID,
			},
			"detailed_merge_status": "mergeable",
		})
		return gitexec.Result{Stdout: body}, marshalErr
	}
	stateDir, stacksDir := testDirs(t)
	responses := glabProjectResponses(false)
	responses["/user"] = json.RawMessage(`{"id":7,"username":"developer"}`)
	var payload []map[string]any
	if err := json.Unmarshal(mrs, &payload); err != nil {
		t.Fatal(err)
	}
	addPerMREndpoints(responses, payload)
	handler := &Handler{
		Runner: fakeGlabRunner{responses: responses, calls: &calls, dynamic: dynamic},
		Dir:    repo, StateDir: stateDir, StacksDir: stacksDir,
		Now: func() time.Time { return time.Date(2026, 7, 25, 12, 0, 0, 0, time.UTC) },
	}
	registerNamedStack(t, handler, testStackName, 1, 2)
	check := runMachine(t, handler, "check", testStackName)
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
	assertActionPacket(t, start.stdout, "retarget_pending", "retry_retarget", "continue_restack")
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
		targetAttempts != 1 {
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
			Branch: "feature/one", OldSHA: oldOID, CurrentSHA: &oldOID, Classification: "old",
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
		OldRefs: map[string]string{"feature/one": oldOID}, NewRefs: map[string]string{},
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
		Branch: "feature/one", OldSHA: oldOID, NewSHA: &newOID,
		CurrentSHA: &oldOID, Classification: "old",
	}}}
	durable.API.State = "publication_ready"
	payload, _ = json.Marshal(durable)
	stored, err = j.PreparePublication(context.Background(), "ses_abandon",
		stored.Revision, map[string]string{"feature/one": newOID}, payload)
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
	recovered := runMachine(t, handler, "restack", "recover", "--session", "ses_abandon")
	if recovered.exit != 0 {
		t.Fatalf("indeterminate recovery was not authoritative: %s", recovered.stdout)
	}
	assertPublishedSchema(t, recovered.stdout)
	assertActionPacket(t, recovered.stdout, "indeterminate_publication",
		"recover_publication", "recover_restack")

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

func assertActionPacket(t *testing.T, document, findingCode, remediationKind string, actions ...string) {
	t.Helper()
	var envelope api.Envelope
	if err := json.Unmarshal([]byte(document), &envelope); err != nil {
		t.Fatalf("decode action packet: %v", err)
	}
	if len(envelope.Findings) != 1 || envelope.Findings[0].Code != findingCode ||
		len(envelope.Evidence) == 0 || len(envelope.Remediations) != 1 ||
		envelope.Remediations[0].Kind != remediationKind {
		t.Fatalf("missing typed %s/%s packet: %s", findingCode, remediationKind, document)
	}
	got := map[string]bool{}
	for _, action := range envelope.Remediations[0].Actions {
		got[action.Kind] = true
	}
	for _, action := range actions {
		if !got[action] {
			t.Fatalf("typed packet lacks action %q: %s", action, document)
		}
	}
}

func containsArgPair(argv []string, key, value string) bool {
	for index := 0; index+1 < len(argv); index++ {
		if argv[index] == key && argv[index+1] == value {
			return true
		}
	}
	return false
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
	stateDir, stacksDir := testDirs(t)
	responses := glabProjectResponses(false)
	responses["/user"] = json.RawMessage(`{"id":7,"username":"developer"}`)
	var payload []map[string]any
	if err := json.Unmarshal(mrs, &payload); err != nil {
		t.Fatal(err)
	}
	addPerMREndpoints(responses, payload)
	handler := &Handler{
		Runner: fakeGlabRunner{responses: responses},
		Dir:    repo, StateDir: stateDir, StacksDir: stacksDir,
		Now: func() time.Time { return time.Date(2026, 7, 25, 12, 0, 0, 0, time.UTC) },
	}
	registerNamedStack(t, handler, testStackName, 1)
	check := runMachine(t, handler, "check", testStackName)
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

func movingBaseRestackFixture(t *testing.T) (*Handler, string, string, string, *[][]string) {
	t.Helper()
	repo, remote, originalMain, firstOID, secondOID := createStackRepository(t)
	runGit(t, repo, "checkout", "main")
	if err := os.WriteFile(filepath.Join(repo, "base-advanced.txt"), []byte("advanced\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runGit(t, repo, "add", "base-advanced.txt")
	runGit(t, repo, "commit", "-m", "advance base")
	runGit(t, repo, "push", "origin", "main")
	runGit(t, repo, "checkout", "feature/two")
	var calls [][]string
	stateDir, stacksDir := testDirs(t)
	responses := glabProjectResponses(false)
	responses["/user"] = json.RawMessage(`{"id":7,"username":"developer"}`)
	addPerMREndpoints(responses, stackMRPayload(originalMain, firstOID, secondOID, "opened", "", ""))
	handler := &Handler{
		Runner:    fakeGlabRunner{responses: responses, calls: &calls},
		Dir:       repo,
		StateDir:  stateDir,
		StacksDir: stacksDir,
		Now:       func() time.Time { return time.Date(2026, 7, 25, 12, 0, 0, 0, time.UTC) },
	}
	registerNamedStack(t, handler, testStackName, 1, 2)
	check := runMachine(t, handler, "check", testStackName)
	var envelope api.Envelope
	if check.exit != 0 || json.Unmarshal([]byte(check.stdout), &envelope) != nil || envelope.Stack == nil {
		t.Fatalf("moving-base fixture check failed: %s", check.stdout)
	}
	updater := filepath.Join(t.TempDir(), "updater")
	runGit(t, filepath.Dir(updater), "clone", remote, updater)
	runGit(t, updater, "config", "user.name", "Updater")
	runGit(t, updater, "config", "user.email", "updater@example.invalid")
	runGit(t, updater, "checkout", "main")
	return handler, envelope.Stack.SnapshotID, repo, updater, &calls
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
