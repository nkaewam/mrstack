package restack

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/nkaewam/mrstack/internal/gitexec"
)

type ReplayStop string

const (
	StopConflict ReplayStop = "rebase_conflict"
	StopEmpty    ReplayStop = "empty_commit"
)

type ReplayError struct {
	Stop           ReplayStop
	Commit         string
	Layer          int
	CommitIndex    int
	CompletedHeads []string
	OntoOID        string
	Err            error
}

func (e *ReplayError) Error() string {
	return fmt.Sprintf("replay stopped at layer %d commit %s: %s: %v", e.Layer, e.Commit, e.Stop, e.Err)
}
func (e *ReplayError) Unwrap() error { return e.Err }

type Replayer struct {
	Repo *gitexec.Repository
}

// Prepare creates an isolated, detached managed worktree. sessionID is
// validated and used as a directory component only after filepath.Base agrees.
func (r Replayer) Prepare(ctx context.Context, root, sessionID, baseOID string) (string, error) {
	if r.Repo == nil || sessionID == "" || filepath.Base(sessionID) != sessionID ||
		!gitexec.FullOID(baseOID) {
		return "", errors.New("invalid managed worktree request")
	}
	if err := os.MkdirAll(root, 0o700); err != nil {
		return "", err
	}
	path := filepath.Join(root, sessionID)
	if _, err := r.Repo.Git(ctx, "worktree", "add", "--detach", path, baseOID); err != nil {
		return "", err
	}
	return path, nil
}

// Replay attempts every enumerated commit in order. Git's patch-equivalence
// skipping is not used. A conflict or empty result is returned as a typed stop
// while the managed worktree is retained.
func (r Replayer) Replay(ctx context.Context, worktree string, plan Plan) ([]string, error) {
	if r.Repo == nil || worktree == "" {
		return nil, errors.New("invalid replay request")
	}
	return r.replayFrom(ctx, worktree, plan, 0, 0, nil)
}

func (r Replayer) replayFrom(ctx context.Context, worktree string, plan Plan,
	startLayer, startCommit int, completedHeads []string) ([]string, error) {
	newHeads := append([]string(nil), completedHeads...)
	for layerIndex := startLayer; layerIndex < len(plan.Layers); layerIndex++ {
		layer := plan.Layers[layerIndex]
		commitStart := 0
		if layerIndex == startLayer {
			commitStart = startCommit
		}
		for commitIndex := commitStart; commitIndex < len(layer.Commits); commitIndex++ {
			commit := layer.Commits[commitIndex]
			_, err := r.Repo.Runner.Run(ctx, worktree, "git",
				"-c", "commit.gpgSign=false", "cherry-pick", "--no-gpg-sign", commit.OID)
			if err != nil {
				onto, _ := currentHead(ctx, r.Repo.Runner, worktree)
				status, statusErr := r.Repo.Runner.Run(ctx, worktree, "git", "status", "--porcelain=v1")
				if statusErr != nil {
					return nil, err
				}
				stop := StopEmpty
				if len(status.Stdout) > 0 || hasUnmerged(ctx, r.Repo.Runner, worktree) {
					stop = StopConflict
				}
				return nil, &ReplayError{
					Stop: stop, Commit: commit.OID, Layer: layerIndex, CommitIndex: commitIndex,
					CompletedHeads: append([]string(nil), newHeads...), OntoOID: onto, Err: err,
				}
			}
		}
		result, err := r.Repo.Runner.Run(ctx, worktree, "git", "rev-parse", "--verify", "HEAD^{commit}")
		if err != nil {
			return nil, err
		}
		head := strings.TrimSpace(string(result.Stdout))
		if !gitexec.FullOID(head) {
			return nil, errors.New("replay returned invalid head object ID")
		}
		newHeads = append(newHeads, head)
	}
	return newHeads, nil
}

type Resolution string

const (
	ResolutionContinue Resolution = "continue"
	ResolutionDrop     Resolution = "drop"
	ResolutionKeep     Resolution = "keep"
)

// Resume resolves one durable replay stop and then continues the exact
// remaining commit sequence. Conflict continuation requires an unmerged-free,
// explicitly staged index. Empty commits require an explicit drop or keep.
func (r Replayer) Resume(ctx context.Context, worktree string, plan Plan,
	layerIndex, commitIndex int, completedHeads []string, resolution Resolution) ([]string, error) {
	if r.Repo == nil || worktree == "" || layerIndex < 0 || layerIndex >= len(plan.Layers) ||
		commitIndex < 0 || commitIndex >= len(plan.Layers[layerIndex].Commits) ||
		len(completedHeads) != layerIndex {
		return nil, errors.New("invalid replay resume cursor")
	}
	commit := plan.Layers[layerIndex].Commits[commitIndex]
	switch resolution {
	case ResolutionContinue:
		if hasUnmerged(ctx, r.Repo.Runner, worktree) {
			return nil, errors.New("conflicts remain unresolved")
		}
		staged, err := hasStagedChanges(ctx, r.Repo.Runner, worktree)
		if err != nil {
			return nil, err
		}
		if !staged {
			return nil, errors.New("conflict resolution must be explicitly staged")
		}
		if _, err := r.Repo.Runner.Run(ctx, worktree, "git",
			"-c", "commit.gpgSign=false", "cherry-pick", "--continue"); err != nil {
			status, statusErr := r.Repo.Runner.Run(ctx, worktree, "git", "status", "--porcelain=v1")
			if statusErr == nil && len(status.Stdout) == 0 && !hasUnmerged(ctx, r.Repo.Runner, worktree) {
				onto, _ := currentHead(ctx, r.Repo.Runner, worktree)
				return nil, &ReplayError{
					Stop: StopEmpty, Commit: commit.OID, Layer: layerIndex, CommitIndex: commitIndex,
					CompletedHeads: append([]string(nil), completedHeads...), OntoOID: onto, Err: err,
				}
			}
			return nil, err
		}
	case ResolutionDrop:
		if _, err := r.Repo.Runner.Run(ctx, worktree, "git", "cherry-pick", "--skip"); err != nil {
			return nil, err
		}
	case ResolutionKeep:
		// Quit clears CHERRY_PICK_HEAD without resetting already replayed work.
		if _, err := r.Repo.Runner.Run(ctx, worktree, "git", "cherry-pick", "--quit"); err != nil {
			return nil, err
		}
		if _, err := r.Repo.Runner.Run(ctx, worktree, "git",
			"-c", "commit.gpgSign=false", "commit", "--allow-empty", "--no-gpg-sign", "-C", commit.OID); err != nil {
			return nil, err
		}
	default:
		return nil, errors.New("an explicit replay resolution is required")
	}

	// If the resolved commit ended its layer, capture that layer's new head
	// before moving to the next layer.
	nextCommit := commitIndex + 1
	if nextCommit == len(plan.Layers[layerIndex].Commits) {
		head, err := currentHead(ctx, r.Repo.Runner, worktree)
		if err != nil {
			return nil, err
		}
		completedHeads = append(append([]string(nil), completedHeads...), head)
		layerIndex++
		nextCommit = 0
	}
	return r.replayFrom(ctx, worktree, plan, layerIndex, nextCommit, completedHeads)
}

func hasUnmerged(ctx context.Context, runner gitexec.CommandRunner, worktree string) bool {
	result, err := runner.Run(ctx, worktree, "git", "diff", "--name-only", "--diff-filter=U")
	return err == nil && len(strings.TrimSpace(string(result.Stdout))) > 0
}

func hasStagedChanges(ctx context.Context, runner gitexec.CommandRunner, worktree string) (bool, error) {
	_, err := runner.Run(ctx, worktree, "git", "diff", "--cached", "--quiet")
	if err == nil {
		return false, nil
	}
	var commandErr *gitexec.CommandError
	if errors.As(err, &commandErr) && commandErr.ExitCode == 1 {
		return true, nil
	}
	return false, err
}

func currentHead(ctx context.Context, runner gitexec.CommandRunner, worktree string) (string, error) {
	result, err := runner.Run(ctx, worktree, "git", "rev-parse", "--verify", "HEAD^{commit}")
	if err != nil {
		return "", err
	}
	head := strings.TrimSpace(string(result.Stdout))
	if !gitexec.FullOID(head) {
		return "", errors.New("replay returned invalid head object ID")
	}
	return head, nil
}

func (r Replayer) Remove(ctx context.Context, worktree string) error {
	if r.Repo == nil || worktree == "" {
		return errors.New("invalid managed worktree")
	}
	_, err := r.Repo.Git(ctx, "worktree", "remove", worktree)
	return err
}
