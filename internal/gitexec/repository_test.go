package gitexec

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestParseRemoteIdentity(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name, raw string
		want      RemoteIdentity
		ok        bool
	}{
		{"https", "https://user:secret@gitlab.example.com/team/service.git", RemoteIdentity{"gitlab.example.com", "team/service"}, true},
		{"ssh url", "ssh://git@gitlab.example.com/team/service.git", RemoteIdentity{"gitlab.example.com", "team/service"}, true},
		{"scp", "git@gitlab.example.com:team/service.git", RemoteIdentity{"gitlab.example.com", "team/service"}, true},
		{"nested", "git@GITLAB.EXAMPLE.COM:group/sub/service", RemoteIdentity{"gitlab.example.com", "group/sub/service"}, true},
		{"userinfo only", "user@example.com", RemoteIdentity{}, false},
		{"path traversal", "git@example.com:team/../secret.git", RemoteIdentity{}, false},
		{"empty", "", RemoteIdentity{}, false},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, err := ParseRemoteIdentity(tt.raw)
			if (err == nil) != tt.ok || !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("got %#v, %v; want %#v, ok=%v", got, err, tt.want, tt.ok)
			}
		})
	}
}

func FuzzParseRemoteIdentityNeverLeaksCredentials(f *testing.F) {
	f.Add("https://alice:token@gitlab.example.com/team/service.git")
	f.Add("git@gitlab.example.com:team/service.git")
	f.Fuzz(func(t *testing.T, raw string) {
		got, err := ParseRemoteIdentity(raw)
		if err != nil {
			return
		}
		if strings.Contains(got.Host, "@") || strings.Contains(got.Project, "@") ||
			strings.Contains(got.Host, "token") || strings.Contains(got.Project, "token") {
			t.Fatalf("credential-shaped output: %#v", got)
		}
	})
}

func TestValidBranchAndFullOID(t *testing.T) {
	t.Parallel()
	for _, branch := range []string{"feature/a", "feat;touch-pwned", "release-1.2"} {
		if !ValidBranch(branch) {
			t.Errorf("expected valid branch %q", branch)
		}
	}
	for _, branch := range []string{"", "-x", "a..b", "a b", "a~b", "a@{b", "/root", "a/", "a.lock."} {
		if ValidBranch(branch) {
			t.Errorf("expected invalid branch %q", branch)
		}
	}
	if !FullOID(strings.Repeat("a", 40)) || !FullOID(strings.Repeat("9", 64)) ||
		FullOID(strings.Repeat("a", 39)) || FullOID(strings.Repeat("A", 40)) {
		t.Fatal("FullOID validation mismatch")
	}
}

type recordingRunner struct {
	calls [][]string
	err   error
}

func (r *recordingRunner) Run(_ context.Context, _ string, name string, args ...string) (Result, error) {
	r.calls = append(r.calls, append([]string{name}, args...))
	return Result{}, r.err
}

func TestAtomicPushUsesOneAtomicCommandAndEveryLease(t *testing.T) {
	t.Parallel()
	runner := &recordingRunner{}
	repo := Repository{Dir: t.TempDir(), Runner: runner}
	a, b, c := strings.Repeat("a", 40), strings.Repeat("b", 40), strings.Repeat("c", 40)
	err := repo.AtomicPush(context.Background(), "origin", []RefUpdate{
		{Branch: "stack/b", OldOID: b, NewOID: c},
		{Branch: "stack/a", OldOID: a, NewOID: b},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(runner.calls) != 1 {
		t.Fatalf("expected one git call, got %d", len(runner.calls))
	}
	got := runner.calls[0]
	want := []string{"git", "push", "--atomic", "--porcelain",
		"--force-with-lease=refs/heads/stack/a:" + a,
		"--force-with-lease=refs/heads/stack/b:" + b,
		"origin",
		b + ":refs/heads/stack/a",
		c + ":refs/heads/stack/b",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("argv mismatch\n got: %#v\nwant: %#v", got, want)
	}
}

func TestAtomicPushRejectsInvalidInputBeforeGit(t *testing.T) {
	t.Parallel()
	runner := &recordingRunner{}
	repo := Repository{Dir: t.TempDir(), Runner: runner}
	err := repo.AtomicPush(context.Background(), "origin", []RefUpdate{{
		Branch: "-oProxyCommand=bad", OldOID: strings.Repeat("a", 40), NewOID: strings.Repeat("b", 40),
	}})
	if err == nil || len(runner.calls) != 0 {
		t.Fatalf("expected preflight rejection, err=%v calls=%v", err, runner.calls)
	}
}

func TestRealRepositoryAncestryDirtyAndLocalOnlyWork(t *testing.T) {
	t.Parallel()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git unavailable")
	}
	ctx := context.Background()
	root := t.TempDir()
	bare := filepath.Join(root, "remote.git")
	runGit(t, root, "init", "--bare", bare)
	clone := filepath.Join(root, "clone")
	runGit(t, root, "clone", bare, clone)
	runGit(t, clone, "config", "user.email", "test@example.com")
	runGit(t, clone, "config", "user.name", "Test")
	if err := os.WriteFile(filepath.Join(clone, "file"), []byte("one\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runGit(t, clone, "add", "file")
	runGit(t, clone, "commit", "-m", "initial")
	runGit(t, clone, "branch", "-M", "main")
	runGit(t, clone, "push", "-u", "origin", "main")
	repo, err := Open(ctx, ExecRunner{}, clone)
	if err != nil {
		t.Fatal(err)
	}
	initial, err := repo.RevParse(ctx, "HEAD")
	if err != nil {
		t.Fatal(err)
	}
	runGit(t, clone, "checkout", "-b", "feature")
	if err := os.WriteFile(filepath.Join(clone, "file"), []byte("two\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runGit(t, clone, "commit", "-am", "feature")
	feature, _ := repo.RevParse(ctx, "HEAD")
	ancestor, err := repo.IsAncestor(ctx, initial, feature)
	if err != nil || !ancestor {
		t.Fatalf("ancestry: %v %v", ancestor, err)
	}
	work, err := repo.AffectedLocalWork(ctx, "origin", map[string]string{"feature": initial})
	if err != nil {
		t.Fatal(err)
	}
	if len(work) != 1 || work[0].Kind != "local_only_commits" {
		t.Fatalf("expected local-only work, got %#v", work)
	}
	if err := os.WriteFile(filepath.Join(clone, "untracked"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	work, err = repo.AffectedLocalWork(ctx, "origin", map[string]string{"feature": feature})
	if err != nil {
		t.Fatal(err)
	}
	if len(work) != 1 || work[0].Kind != "dirty_worktree" {
		t.Fatalf("expected dirty worktree, got %#v", work)
	}

	runGit(t, clone, "branch", "safe-local", initial)
	runGit(t, clone, "branch", "moved-local", feature)
	results, err := repo.UpdateSafeLocalRefs(ctx, []RefUpdate{
		{Branch: "feature", OldOID: feature, NewOID: initial},
		{Branch: "safe-local", OldOID: initial, NewOID: feature},
		{Branch: "moved-local", OldOID: initial, NewOID: feature},
		{Branch: "absent-local", OldOID: initial, NewOID: feature},
	})
	if err != nil {
		t.Fatal(err)
	}
	states := map[string]string{}
	for _, result := range results {
		states[result.Branch] = result.State
	}
	if states["feature"] != "checked_out" || states["safe-local"] != "updated" ||
		states["moved-local"] != "moved" || states["absent-local"] != "absent" {
		t.Fatalf("local ref states=%v", states)
	}
	if got, _ := repo.RevParse(ctx, "refs/heads/feature"); got != feature {
		t.Fatal("checked-out branch was changed")
	}
	if got, _ := repo.RevParse(ctx, "refs/heads/safe-local"); got != feature {
		t.Fatal("safe local branch was not advanced")
	}
}

func TestRealAtomicPushIsAllOrNothingWithHooksAndLeases(t *testing.T) {
	t.Parallel()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git unavailable")
	}
	ctx := context.Background()
	root := t.TempDir()
	bare := filepath.Join(root, "remote.git")
	runGit(t, root, "init", "--bare", bare)
	clone := filepath.Join(root, "clone")
	runGit(t, root, "clone", bare, clone)
	runGit(t, clone, "config", "user.email", "test@example.com")
	runGit(t, clone, "config", "user.name", "Test")
	if err := os.WriteFile(filepath.Join(clone, "file"), []byte("base\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runGit(t, clone, "add", "file")
	runGit(t, clone, "commit", "-m", "base")
	runGit(t, clone, "branch", "-M", "main")
	runGit(t, clone, "branch", "a")
	runGit(t, clone, "branch", "b")
	runGit(t, clone, "push", "origin", "main", "a", "b")
	repo, err := Open(ctx, ExecRunner{}, clone)
	if err != nil {
		t.Fatal(err)
	}
	oldA := remoteOID(t, bare, "a")
	oldB := remoteOID(t, bare, "b")
	runGit(t, clone, "checkout", "a")
	if err := os.WriteFile(filepath.Join(clone, "a"), []byte("a1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runGit(t, clone, "add", "a")
	runGit(t, clone, "commit", "-m", "a1")
	newA, _ := repo.RevParse(ctx, "HEAD")
	runGit(t, clone, "checkout", "b")
	if err := os.WriteFile(filepath.Join(clone, "b"), []byte("b1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runGit(t, clone, "add", "b")
	runGit(t, clone, "commit", "-m", "b1")
	newB, _ := repo.RevParse(ctx, "HEAD")
	if err := repo.AtomicPush(ctx, "origin", []RefUpdate{
		{Branch: "a", OldOID: oldA, NewOID: newA},
		{Branch: "b", OldOID: oldB, NewOID: newB},
	}); err != nil {
		t.Fatalf("initial atomic push: %v", err)
	}
	if remoteOID(t, bare, "a") != newA || remoteOID(t, bare, "b") != newB {
		t.Fatal("successful atomic push did not publish both refs")
	}
	observed, err := repo.RemoteRefs(ctx, "origin", []string{"b", "a"})
	if err != nil || observed["a"] != newA || observed["b"] != newB {
		t.Fatalf("exact remote read=%v err=%v", observed, err)
	}
	optional, err := repo.RemoteRefsAllowMissing(ctx, "origin", []string{"a", "deleted"})
	if err != nil || optional["a"] != newA {
		t.Fatalf("optional remote read=%v err=%v", optional, err)
	}
	if _, exists := optional["deleted"]; exists {
		t.Fatal("missing optional branch was fabricated")
	}
	if _, err := repo.RemoteRefs(ctx, "origin", []string{"a", "deleted"}); err == nil {
		t.Fatal("strict remote read accepted a missing branch")
	}

	// Prepare a second pair, then reject only b. Atomic transport must leave a
	// unchanged too.
	runGit(t, clone, "checkout", "a")
	if err := os.WriteFile(filepath.Join(clone, "a"), []byte("a2\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runGit(t, clone, "commit", "-am", "a2")
	nextA, _ := repo.RevParse(ctx, "HEAD")
	runGit(t, clone, "checkout", "b")
	if err := os.WriteFile(filepath.Join(clone, "b"), []byte("b2\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runGit(t, clone, "commit", "-am", "b2")
	nextB, _ := repo.RevParse(ctx, "HEAD")
	hook := filepath.Join(bare, "hooks", "pre-receive")
	hookBody := "#!/bin/sh\nwhile read old new ref; do\n  test \"$ref\" = refs/heads/b && exit 1\ndone\nexit 0\n"
	if err := os.WriteFile(hook, []byte(hookBody), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := repo.AtomicPush(ctx, "origin", []RefUpdate{
		{Branch: "a", OldOID: newA, NewOID: nextA},
		{Branch: "b", OldOID: newB, NewOID: nextB},
	}); err == nil {
		t.Fatal("receive-hook rejection unexpectedly succeeded")
	}
	if remoteOID(t, bare, "a") != newA || remoteOID(t, bare, "b") != newB {
		t.Fatal("hook rejection published a partial ref set")
	}

	// Move a behind the client's back and allow receives. Its failed lease must
	// also prevent b from moving.
	if err := os.WriteFile(hook, []byte("#!/bin/sh\nexit 0\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	runGit(t, bare, "update-ref", "refs/heads/a", oldA)
	if err := repo.AtomicPush(ctx, "origin", []RefUpdate{
		{Branch: "a", OldOID: newA, NewOID: nextA},
		{Branch: "b", OldOID: newB, NewOID: nextB},
	}); err == nil {
		t.Fatal("stale lease unexpectedly succeeded")
	}
	if remoteOID(t, bare, "a") != oldA || remoteOID(t, bare, "b") != newB {
		t.Fatal("stale lease published another ref")
	}
}

func remoteOID(t *testing.T, bare, branch string) string {
	t.Helper()
	cmd := exec.Command("git", "--git-dir", bare, "rev-parse", "refs/heads/"+branch)
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("read remote %s: %v", branch, err)
	}
	return strings.TrimSpace(string(out))
}

func runGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}
