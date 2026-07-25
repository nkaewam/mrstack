package gitexec

import (
	"context"
	"fmt"
	"sort"
	"strings"
)

type Worktree struct {
	Path   string
	Branch string
	Bare   bool
}

func (r *Repository) Worktrees(ctx context.Context) ([]Worktree, error) {
	res, err := r.Git(ctx, "worktree", "list", "--porcelain", "-z")
	if err != nil {
		return nil, err
	}
	records := strings.Split(string(res.Stdout), "\x00")
	var result []Worktree
	var current *Worktree
	for _, record := range records {
		switch {
		case strings.HasPrefix(record, "worktree "):
			if current != nil {
				result = append(result, *current)
			}
			current = &Worktree{Path: strings.TrimPrefix(record, "worktree ")}
		case strings.HasPrefix(record, "branch refs/heads/") && current != nil:
			current.Branch = strings.TrimPrefix(record, "branch refs/heads/")
		case record == "bare" && current != nil:
			current.Bare = true
		}
	}
	if current != nil {
		result = append(result, *current)
	}
	return result, nil
}

func (r *Repository) Dirty(ctx context.Context, path string) (bool, error) {
	res, err := r.Runner.Run(ctx, path, "git", "status", "--porcelain=v1", "-z", "--untracked-files=all")
	if err != nil {
		return false, err
	}
	return len(res.Stdout) > 0, nil
}

type LocalWork struct {
	Branch string
	Path   string
	Kind   string
}

// AffectedLocalWork checks only affected branches. It detects dirty registered
// worktrees and branch commits not represented by the captured remote OID.
func (r *Repository) AffectedLocalWork(ctx context.Context, remote string, captured map[string]string) ([]LocalWork, error) {
	worktrees, err := r.Worktrees(ctx)
	if err != nil {
		return nil, err
	}
	var found []LocalWork
	worktreePath := make(map[string]string, len(worktrees))
	for _, worktree := range worktrees {
		if worktree.Bare || worktree.Branch == "" {
			continue
		}
		worktreePath[worktree.Branch] = worktree.Path
		if _, affected := captured[worktree.Branch]; !affected {
			continue
		}
		dirty, err := r.Dirty(ctx, worktree.Path)
		if err != nil {
			return nil, err
		}
		if dirty {
			found = append(found, LocalWork{Branch: worktree.Branch, Path: worktree.Path, Kind: "dirty_worktree"})
		}
	}

	branches := make([]string, 0, len(captured))
	for branch := range captured {
		branches = append(branches, branch)
	}
	sort.Strings(branches)
	for _, branch := range branches {
		expected := captured[branch]
		local, err := r.RevParse(ctx, "refs/heads/"+branch)
		if err != nil {
			continue
		}
		if local != expected {
			represented, err := r.IsAncestor(ctx, local, expected)
			if err != nil {
				return nil, fmt.Errorf("inspect local branch %s: %w", branch, err)
			}
			if !represented {
				found = append(found, LocalWork{
					Branch: branch, Path: worktreePath[branch], Kind: "local_only_commits",
				})
			}
		}
	}
	return found, nil
}

type LocalRefResult struct {
	Branch string
	State  string
	OldOID string
	NewOID string
}

// UpdateSafeLocalRefs advances only affected local refs that are not checked
// out anywhere and still exactly equal the captured old revision. Checked-out,
// moved, and absent refs are reported and never changed.
func (r *Repository) UpdateSafeLocalRefs(ctx context.Context, updates []RefUpdate) ([]LocalRefResult, error) {
	worktrees, err := r.Worktrees(ctx)
	if err != nil {
		return nil, err
	}
	checkedOut := map[string]bool{}
	for _, worktree := range worktrees {
		if worktree.Branch != "" && !worktree.Bare {
			checkedOut[worktree.Branch] = true
		}
	}
	sorted := append([]RefUpdate(nil), updates...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].Branch < sorted[j].Branch })
	results := make([]LocalRefResult, 0, len(sorted))
	for _, update := range sorted {
		if !ValidBranch(update.Branch) || !FullOID(update.OldOID) || !FullOID(update.NewOID) {
			return nil, fmt.Errorf("invalid local ref update for %q", update.Branch)
		}
		result := LocalRefResult{
			Branch: update.Branch, OldOID: update.OldOID, NewOID: update.NewOID,
		}
		local, revErr := r.RevParse(ctx, "refs/heads/"+update.Branch)
		if revErr != nil {
			result.State = "absent"
			results = append(results, result)
			continue
		}
		if checkedOut[update.Branch] {
			result.State = "checked_out"
			results = append(results, result)
			continue
		}
		if local != update.OldOID {
			result.State = "moved"
			results = append(results, result)
			continue
		}
		ref := "refs/heads/" + update.Branch
		if _, err := r.Git(ctx, "update-ref", ref, update.NewOID, update.OldOID); err != nil {
			return nil, fmt.Errorf("fast-update local branch %s: %w", update.Branch, err)
		}
		result.State = "updated"
		results = append(results, result)
	}
	return results, nil
}
