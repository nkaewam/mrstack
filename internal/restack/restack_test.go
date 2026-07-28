package restack

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nkaewam/mrstack/internal/gitexec"
)

type fixture struct {
	repo       *gitexec.Repository
	dir        string
	base, head string
}

func gitFixture(t *testing.T) fixture {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git unavailable")
	}
	dir := t.TempDir()
	run(t, dir, "git", "init", "-q")
	run(t, dir, "git", "config", "user.name", "Test User")
	run(t, dir, "git", "config", "user.email", "test@example.com")
	write(t, filepath.Join(dir, "file"), "base\n")
	run(t, dir, "git", "add", "file")
	run(t, dir, "git", "commit", "-q", "-m", "base")
	base := output(t, dir, "git", "rev-parse", "HEAD")
	run(t, dir, "git", "checkout", "-q", "-b", "feature")
	write(t, filepath.Join(dir, "one"), "one\n")
	run(t, dir, "git", "add", "one")
	run(t, dir, "git", "commit", "-q", "-m", "one")
	write(t, filepath.Join(dir, "two"), "two\n")
	run(t, dir, "git", "add", "two")
	run(t, dir, "git", "commit", "-q", "-m", "two")
	head := output(t, dir, "git", "rev-parse", "HEAD")
	repo, err := gitexec.Open(context.Background(), gitexec.ExecRunner{}, dir)
	if err != nil {
		t.Fatal(err)
	}
	return fixture{repo: repo, dir: dir, base: base, head: head}
}

func TestPlannerEnumeratesExactOrderedMultiCommitLayer(t *testing.T) {
	t.Parallel()
	f := gitFixture(t)
	plan, err := (Planner{Repo: f.repo}).Build(context.Background(), f.base, []LayerInput{{
		MRIID: 1, Branch: "feature", BoundaryOID: f.base, HeadOID: f.head,
	}}, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Layers) != 1 || len(plan.Layers[0].Commits) != 2 {
		t.Fatalf("plan=%#v", plan)
	}
	subject0 := output(t, f.dir, "git", "show", "-s", "--format=%s", plan.Layers[0].Commits[0].OID)
	subject1 := output(t, f.dir, "git", "show", "-s", "--format=%s", plan.Layers[0].Commits[1].OID)
	if subject0 != "one" || subject1 != "two" {
		t.Fatalf("commit order %q, %q", subject0, subject1)
	}
}

func TestPlannerRejectsUnknownBoundaryAndEmptyLayer(t *testing.T) {
	t.Parallel()
	f := gitFixture(t)
	_, err := (Planner{Repo: f.repo}).Build(context.Background(), f.base, []LayerInput{{
		MRIID: 1, Branch: "feature", BoundaryOID: f.head, HeadOID: f.base,
	}}, false)
	if !errors.Is(err, ErrAmbiguousBoundary) {
		t.Fatalf("expected boundary error, got %v", err)
	}
	_, err = (Planner{Repo: f.repo}).Build(context.Background(), f.base, []LayerInput{{
		MRIID: 1, Branch: "feature", BoundaryOID: f.head, HeadOID: f.head,
	}}, false)
	if !errors.Is(err, ErrEmptyLayer) {
		t.Fatalf("expected empty error, got %v", err)
	}
}

func TestPlannerRejectsMergeCommit(t *testing.T) {
	t.Parallel()
	f := gitFixture(t)
	run(t, f.dir, "git", "checkout", "-q", "-b", "side", f.base)
	write(t, filepath.Join(f.dir, "side"), "side\n")
	run(t, f.dir, "git", "add", "side")
	run(t, f.dir, "git", "commit", "-q", "-m", "side")
	run(t, f.dir, "git", "checkout", "-q", "feature")
	run(t, f.dir, "git", "merge", "--no-ff", "-m", "merge side", "side")
	mergeHead := output(t, f.dir, "git", "rev-parse", "HEAD")
	_, err := (Planner{Repo: f.repo}).Build(context.Background(), f.base, []LayerInput{{
		MRIID: 1, Branch: "feature", BoundaryOID: f.base, HeadOID: mergeHead,
	}}, false)
	if !errors.Is(err, ErrMergeCommit) {
		t.Fatalf("expected merge error, got %v", err)
	}
}

func TestReplayUsesManagedWorktreeAndPreservesCommitMetadata(t *testing.T) {
	t.Parallel()
	f := gitFixture(t)
	plan, err := (Planner{Repo: f.repo}).Build(context.Background(), f.base, []LayerInput{{
		MRIID: 1, Branch: "feature", BoundaryOID: f.base, HeadOID: f.head,
	}}, false)
	if err != nil {
		t.Fatal(err)
	}
	// Advance a new base on the original worktree, forcing rewritten identities.
	run(t, f.dir, "git", "checkout", "-q", "-b", "new-base", f.base)
	write(t, filepath.Join(f.dir, "base2"), "new base\n")
	run(t, f.dir, "git", "add", "base2")
	run(t, f.dir, "git", "commit", "-q", "-m", "advance base")
	newBase := output(t, f.dir, "git", "rev-parse", "HEAD")
	replayer := Replayer{Repo: f.repo}
	worktree, err := replayer.Prepare(context.Background(), filepath.Join(t.TempDir(), "managed"), "ses_1", newBase)
	if err != nil {
		t.Fatal(err)
	}
	heads, err := replayer.Replay(context.Background(), worktree, plan)
	if err != nil {
		t.Fatal(err)
	}
	if len(heads) != 1 || heads[0] == f.head {
		t.Fatalf("unexpected heads %#v", heads)
	}
	subjects := strings.Split(output(t, worktree, "git", "log", "--format=%s", "--reverse", newBase+"..HEAD"), "\n")
	if len(subjects) != 2 || subjects[0] != "one" || subjects[1] != "two" {
		t.Fatalf("subjects=%q", subjects)
	}
	author := output(t, worktree, "git", "show", "-s", "--format=%an <%ae>", "HEAD")
	if author != "Test User <test@example.com>" {
		t.Fatalf("author changed: %q", author)
	}
	if err := replayer.Remove(context.Background(), worktree); err != nil {
		t.Fatal(err)
	}
}

func TestReplayStopsAndRetainsManagedWorktreeOnConflict(t *testing.T) {
	t.Parallel()
	f := gitFixture(t)
	// Add a feature commit that changes the same line the new base will change.
	run(t, f.dir, "git", "checkout", "-q", "feature")
	write(t, filepath.Join(f.dir, "file"), "feature version\n")
	run(t, f.dir, "git", "commit", "-qam", "feature conflict")
	head := output(t, f.dir, "git", "rev-parse", "HEAD")
	plan, err := (Planner{Repo: f.repo}).Build(context.Background(), f.base, []LayerInput{{
		MRIID: 1, Branch: "feature", BoundaryOID: f.base, HeadOID: head,
	}}, false)
	if err != nil {
		t.Fatal(err)
	}
	run(t, f.dir, "git", "checkout", "-q", "-B", "conflict-base", f.base)
	write(t, filepath.Join(f.dir, "file"), "base version\n")
	run(t, f.dir, "git", "commit", "-qam", "base conflict")
	newBase := output(t, f.dir, "git", "rev-parse", "HEAD")
	replayer := Replayer{Repo: f.repo}
	worktree, err := replayer.Prepare(context.Background(), filepath.Join(t.TempDir(), "managed"), "ses_conflict", newBase)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := replayer.RemoveForce(context.Background(), worktree); err != nil {
			t.Errorf("cleanup managed worktree: %v", err)
		}
	})
	_, err = replayer.Replay(context.Background(), worktree, plan)
	var stop *ReplayError
	if !errors.As(err, &stop) || stop.Stop != StopConflict {
		t.Fatalf("expected conflict stop, got %#v (%v)", stop, err)
	}
	if _, statErr := os.Stat(worktree); statErr != nil {
		t.Fatalf("managed worktree was not retained: %v", statErr)
	}
	unmerged := output(t, worktree, "git", "diff", "--name-only", "--diff-filter=U")
	if unmerged != "file" {
		t.Fatalf("unmerged paths=%q", unmerged)
	}
	write(t, filepath.Join(worktree, "file"), "resolved version\n")
	run(t, worktree, "git", "add", "file")
	heads, err := replayer.Resume(context.Background(), worktree, plan,
		stop.Layer, stop.CommitIndex, stop.CompletedHeads, ResolutionContinue)
	if err != nil {
		t.Fatalf("resume staged conflict: %v", err)
	}
	if len(heads) != 1 || !gitexec.FullOID(heads[0]) {
		t.Fatalf("resumed heads=%v", heads)
	}
	subject := output(t, worktree, "git", "show", "-s", "--format=%s", "HEAD")
	if subject != "feature conflict" {
		t.Fatalf("resumed commit message=%q", subject)
	}
}

func TestReplayClassifiesAlreadyAppliedCommitAsExplicitEmptyStop(t *testing.T) {
	t.Parallel()
	_, plan, replayer, worktree := emptyReplayFixture(t)
	_, err := replayer.Replay(context.Background(), worktree, plan)
	var stop *ReplayError
	if !errors.As(err, &stop) || stop.Stop != StopEmpty {
		t.Fatalf("expected explicit empty stop, got %#v (%v)", stop, err)
	}
	if _, statErr := os.Stat(worktree); statErr != nil {
		t.Fatalf("managed worktree was not retained: %v", statErr)
	}
	heads, err := replayer.Resume(context.Background(), worktree, plan,
		stop.Layer, stop.CommitIndex, stop.CompletedHeads, ResolutionDrop)
	if err != nil || len(heads) != 1 {
		t.Fatalf("drop empty: heads=%v err=%v", heads, err)
	}
	if got := output(t, worktree, "git", "log", "--format=%s", "-1"); got != "base already has same" {
		t.Fatalf("drop unexpectedly created a commit: %q", got)
	}
}

func TestReplayCanExplicitlyKeepEmptyCommitMetadata(t *testing.T) {
	t.Parallel()
	_, plan, replayer, worktree := emptyReplayFixture(t)
	_, err := replayer.Replay(context.Background(), worktree, plan)
	var stop *ReplayError
	if !errors.As(err, &stop) || stop.Stop != StopEmpty {
		t.Fatalf("expected empty stop, got %v", err)
	}
	heads, err := replayer.Resume(context.Background(), worktree, plan,
		stop.Layer, stop.CommitIndex, stop.CompletedHeads, ResolutionKeep)
	if err != nil || len(heads) != 1 {
		t.Fatalf("keep empty: heads=%v err=%v", heads, err)
	}
	if got := output(t, worktree, "git", "show", "-s", "--format=%s", "HEAD"); got != "feature adds same" {
		t.Fatalf("empty commit message changed: %q", got)
	}
	if got := output(t, worktree, "git", "show", "-s", "--format=%an <%ae>", "HEAD"); got != "Test User <test@example.com>" {
		t.Fatalf("empty commit author changed: %q", got)
	}
	if got := output(t, worktree, "git", "diff-tree", "--no-commit-id", "--name-only", "-r", "HEAD"); got != "" {
		t.Fatalf("kept commit is not empty: %q", got)
	}
}

func emptyReplayFixture(t *testing.T) (*gitexec.Repository, Plan, Replayer, string) {
	t.Helper()
	dir := t.TempDir()
	run(t, dir, "git", "init", "-q")
	run(t, dir, "git", "config", "user.name", "Test User")
	run(t, dir, "git", "config", "user.email", "test@example.com")
	write(t, filepath.Join(dir, "base"), "base\n")
	run(t, dir, "git", "add", "base")
	run(t, dir, "git", "commit", "-q", "-m", "base")
	base := output(t, dir, "git", "rev-parse", "HEAD")
	run(t, dir, "git", "checkout", "-q", "-b", "feature")
	write(t, filepath.Join(dir, "same"), "identical\n")
	run(t, dir, "git", "add", "same")
	run(t, dir, "git", "commit", "-q", "-m", "feature adds same")
	head := output(t, dir, "git", "rev-parse", "HEAD")
	run(t, dir, "git", "checkout", "-q", "-b", "new-base", base)
	write(t, filepath.Join(dir, "same"), "identical\n")
	run(t, dir, "git", "add", "same")
	run(t, dir, "git", "commit", "-q", "-m", "base already has same")
	newBase := output(t, dir, "git", "rev-parse", "HEAD")
	repo, err := gitexec.Open(context.Background(), gitexec.ExecRunner{}, dir)
	if err != nil {
		t.Fatal(err)
	}
	plan, err := (Planner{Repo: repo}).Build(context.Background(), base, []LayerInput{{
		MRIID: 1, Branch: "feature", BoundaryOID: base, HeadOID: head,
	}}, false)
	if err != nil {
		t.Fatal(err)
	}
	replayer := Replayer{Repo: repo}
	worktree, err := replayer.Prepare(context.Background(), filepath.Join(t.TempDir(), "managed"), "ses_empty", newBase)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := replayer.RemoveForce(context.Background(), worktree); err != nil {
			t.Errorf("cleanup managed worktree: %v", err)
		}
	})
	return repo, plan, replayer, worktree
}

func TestRefMapsRejectDuplicatesAndAbbreviatedOIDs(t *testing.T) {
	t.Parallel()
	oid := strings.Repeat("a", 40)
	plan := Plan{Layers: []Layer{{Branch: "a", HeadOID: oid}, {Branch: "a", HeadOID: oid}}}
	if _, _, err := plan.RefMaps([]string{oid, oid}); err == nil {
		t.Fatal("duplicate branch accepted")
	}
	plan.Layers = plan.Layers[:1]
	if _, _, err := plan.RefMaps([]string{"abc123"}); err == nil {
		t.Fatal("abbreviated object ID accepted")
	}
}

func run(t *testing.T, dir, name string, args ...string) {
	t.Helper()
	cmd := exec.Command(name, args...)
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("%s %v: %v\n%s", name, args, err, out)
	}
}

func output(t *testing.T, dir, name string, args ...string) string {
	t.Helper()
	cmd := exec.Command(name, args...)
	cmd.Dir = dir
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("%s %v: %v", name, args, err)
	}
	return strings.TrimSpace(string(out))
}

func write(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}
