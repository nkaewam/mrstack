// Package app assembles mrstack's repository, GitLab, stack, journal, and
// restack services behind the argv-only cli.Handler boundary.
package app

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/nkaewam/mrstack/internal/api"
	"github.com/nkaewam/mrstack/internal/cli"
	"github.com/nkaewam/mrstack/internal/gitexec"
	"github.com/nkaewam/mrstack/internal/gitlab"
)

// Handler is safe to reuse for multiple invocations. Runner and Dir are
// exported so tests and embedding callers can provide an argv-recording
// transport without replacing production behavior.
type Handler struct {
	Runner   gitexec.CommandRunner
	Dir      string
	StateDir string
	Stderr   io.Writer
	Now      func() time.Time
	// Failpoint is an optional deterministic crash-boundary hook used by
	// mechanical recovery tests. Production handlers leave it nil.
	Failpoint  func(string) error
	idSequence atomic.Uint64
}

// New returns a production handler. The repository root is resolved lazily so
// help and parse failures never touch Git, glab, or the filesystem.
func New() *Handler {
	return &Handler{Runner: gitexec.ExecRunner{}, Dir: ".", Stderr: os.Stderr, Now: time.Now}
}

func (h *Handler) Dispatch(ctx context.Context, inv cli.Invocation) (cli.Result, error) {
	if inv.Globals.Debug && h.Stderr != nil {
		original := h.Runner
		h.Runner = debugRunner{inner: original, log: h.Stderr}
		defer func() { h.Runner = original }()
	}
	switch inv.Name {
	case cli.CommandDoctor:
		return h.doctor(ctx, inv)
	case cli.CommandCheck:
		return h.check(ctx, inv, true)
	case cli.CommandCILogs:
		return h.ciLogs(ctx, inv)
	case cli.CommandRestackPlan:
		return h.restackPlan(ctx, inv)
	case cli.CommandRestackStart:
		return h.restackStart(ctx, inv)
	case cli.CommandRestackContinue, cli.CommandRestackAbort, cli.CommandRestackRecover:
		return h.restackSession(ctx, inv)
	case cli.CommandHistoryShow:
		return h.historyShow(ctx, inv)
	case cli.CommandHistoryAlias:
		return h.historyAlias(ctx, inv)
	case cli.CommandHistoryPrune:
		return h.historyPrune(ctx, inv)
	case cli.CommandRestackAbandon:
		return h.restackAbandon(ctx, inv)
	default:
		return cli.Result{}, cli.Invalid("unknown_command", "unknown command")
	}
}

type repositoryContext struct {
	repo       *gitexec.Repository
	remoteName string
	selection  string
	fetch      gitexec.RemoteIdentity
	push       gitexec.RemoteIdentity
	client     gitlab.Client
	project    gitlab.Project
}

func (h *Handler) repository(ctx context.Context, remoteFlag string, needProject bool) (repositoryContext, error) {
	runner := h.Runner
	if runner == nil {
		runner = gitexec.ExecRunner{}
	}
	dir := h.Dir
	if dir == "" {
		dir = "."
	}
	repo, err := gitexec.Open(ctx, runner, dir)
	if err != nil {
		if errors.Is(err, gitexec.ErrNotRepository) {
			return repositoryContext{}, cli.Unavailable("not_git_repository",
				"run mrstack from a Git repository", false)
		}
		return repositoryContext{}, cli.Unavailable("git_unavailable", "Git is unavailable", false)
	}
	name, selection := remoteFlag, "explicit"
	if name == "" {
		branch, branchErr := repo.CurrentBranch(ctx)
		if branchErr != nil {
			return repositoryContext{}, cli.Unavailable("git_transport_failed",
				"cannot resolve the current branch for upstream remote selection", false)
		}
		name, err = repo.UpstreamRemote(ctx, branch)
		if err != nil {
			return repositoryContext{}, cli.Unavailable("git_transport_failed",
				"the current branch has no usable upstream remote; pass --remote", false)
		}
		selection = "upstream"
	}
	if name == "" || strings.HasPrefix(name, "-") {
		return repositoryContext{}, cli.Invalid("invalid_arguments", "invalid remote name")
	}
	fetch, err := repo.RemoteIdentity(ctx, name, false)
	if err != nil {
		return repositoryContext{}, cli.Unavailable("git_transport_failed",
			fmt.Sprintf("cannot resolve fetch URL for remote %q", name), false)
	}
	push, err := repo.RemoteIdentity(ctx, name, true)
	if err != nil {
		return repositoryContext{}, cli.Unavailable("git_transport_failed",
			fmt.Sprintf("cannot resolve push URL for remote %q", name), false)
	}
	if fetch != push {
		return repositoryContext{}, cli.Unavailable("prerequisite_unsupported",
			"the selected remote fetch and push URLs identify different GitLab projects", false)
	}
	rc := repositoryContext{
		repo: repo, remoteName: name, selection: selection, fetch: fetch, push: push,
		client: gitlab.Client{Runner: runner, Dir: repo.Dir, Host: fetch.Host},
	}
	if needProject {
		project, projectErr := rc.client.Project(ctx, fetch.Project)
		if projectErr != nil {
			return repositoryContext{}, classifyGlab("resolve GitLab project", projectErr)
		}
		if project.ID.String() == "" || project.PathWithNamespace != fetch.Project ||
			project.DefaultBranch == "" {
			return repositoryContext{}, cli.Unavailable("gitlab_transport_failed",
				"GitLab returned an incomplete or mismatched project", false)
		}
		rc.project = project
	}
	return rc, nil
}

func (h *Handler) factory() (*api.Factory, error) {
	now := h.Now
	if now == nil {
		now = time.Now
	}
	return api.NewFactory(api.ClockFunc(now), api.IDSourceFunc(func(prefix string) (string, error) {
		n := h.idSequence.Add(1)
		return fmt.Sprintf("%s_%d", prefix, n), nil
	}))
}

func (h *Handler) envelope(name cli.CommandName) (api.Envelope, *api.Factory, error) {
	factory, err := h.factory()
	if err != nil {
		return api.Envelope{}, nil, err
	}
	env, err := factory.NewEnvelope(api.CommandName(name))
	return env, factory, err
}

func result(env api.Envelope, human string) (cli.Result, error) {
	// Validate before handing the envelope to cli's encoder. This prevents a
	// successful exit from ever carrying a structurally invalid document.
	if _, err := api.MarshalDocument(env); err != nil {
		return cli.Result{}, cli.Internal("application produced an invalid API envelope", err)
	}
	return cli.Result{Human: human, Machine: env}, nil
}

func classifyGlab(operation string, err error) error {
	var commandErr *gitexec.CommandError
	if errors.As(err, &commandErr) {
		message := strings.ToLower(commandErr.Stderr)
		switch {
		case strings.Contains(message, "401"), strings.Contains(message, "unauthorized"),
			strings.Contains(message, "authentication"):
			return cli.Unavailable("authentication_failed",
				operation+" failed: GitLab authentication failed", false)
		default:
			return cli.Unavailable("gitlab_transport_failed",
				fmt.Sprintf("%s failed: glab exited %d: %s", operation, commandErr.ExitCode, commandErr.Stderr), true)
		}
	}
	return cli.Unavailable("glab_unavailable",
		operation+" failed because glab is unavailable: "+err.Error(), false)
}

func stableID(prefix string, value any) string {
	data, _ := json.Marshal(value)
	sum := sha256.Sum256(data)
	return prefix + "_" + hex.EncodeToString(sum[:12])
}

func fullOID(value string) bool { return gitexec.FullOID(strings.ToLower(value)) }

func (h *Handler) stateRoot(repoDir string) string {
	if h.StateDir != "" {
		return h.StateDir
	}
	return filepath.Join(repoDir, ".git", "mrstack")
}

func writeJSONAtomic(path string, value any) error {
	data, err := json.Marshal(value)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".mrstack-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpName, path)
}

func readJSON(path string, value any) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	if len(data) > 8<<20 {
		return errors.New("state file exceeds size limit")
	}
	return json.Unmarshal(data, value)
}

func decimalIID(value string) (int, bool) {
	value = strings.TrimPrefix(value, "!")
	n, err := strconv.Atoi(value)
	return n, err == nil && n > 0
}

// debugRunner is a CommandRunner wrapper that logs each subprocess argv and
// any captured stderr to the configured sink. It is installed by Dispatch
// only when --debug is set, and is removed before Dispatch returns.
type debugRunner struct {
	inner gitexec.CommandRunner
	log   io.Writer
}

func (r debugRunner) Run(ctx context.Context, dir, name string, args ...string) (gitexec.Result, error) {
	result, err := r.inner.Run(ctx, dir, name, args...)
	fmt.Fprintf(r.log, "mrstack: %s %s\n", name, strings.Join(args, " "))
	if len(result.Stderr) > 0 {
		fmt.Fprintf(r.log, "mrstack: %s stderr: %s\n", name, strings.TrimSpace(string(result.Stderr)))
	}
	if err != nil {
		fmt.Fprintf(r.log, "mrstack: %s error: %v\n", name, err)
	}
	return result, err
}
