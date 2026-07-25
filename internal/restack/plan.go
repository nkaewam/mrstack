// Package restack plans exact layer replays and validates every prerequisite
// before a durable session or managed worktree is created.
package restack

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/nkaewam/mrstack/internal/gitexec"
)

var (
	ErrAmbiguousBoundary = errors.New("ambiguous layer boundary")
	ErrMergeCommit       = errors.New("layer contains a merge commit")
	ErrEmptyLayer        = errors.New("layer is empty")
	ErrSignedCommit      = errors.New("layer contains signed commits")
)

type LayerInput struct {
	MRIID       int
	Branch      string
	BoundaryOID string
	HeadOID     string
}

type Commit struct {
	OID     string
	Parents []string
	Signed  bool
}

type Layer struct {
	MRIID       int
	Branch      string
	BoundaryOID string
	HeadOID     string
	Commits     []Commit
}

type Plan struct {
	BaseOID string
	Layers  []Layer
}

type Planner struct {
	Repo *gitexec.Repository
}

func (p Planner) Build(ctx context.Context, baseOID string, inputs []LayerInput, allowSignatureLoss bool) (Plan, error) {
	if p.Repo == nil || !gitexec.FullOID(baseOID) || len(inputs) == 0 {
		return Plan{}, errors.New("invalid restack plan input")
	}
	plan := Plan{BaseOID: baseOID, Layers: make([]Layer, 0, len(inputs))}
	for _, input := range inputs {
		if input.MRIID <= 0 || !gitexec.ValidBranch(input.Branch) ||
			!gitexec.FullOID(input.BoundaryOID) || !gitexec.FullOID(input.HeadOID) {
			return Plan{}, errors.New("invalid layer input")
		}
		ancestor, err := p.Repo.IsAncestor(ctx, input.BoundaryOID, input.HeadOID)
		if err != nil {
			return Plan{}, fmt.Errorf("validate MR !%d boundary: %w", input.MRIID, err)
		}
		if !ancestor {
			return Plan{}, fmt.Errorf("MR !%d: %w", input.MRIID, ErrAmbiguousBoundary)
		}
		commits, err := p.enumerate(ctx, input.BoundaryOID, input.HeadOID)
		if err != nil {
			return Plan{}, fmt.Errorf("MR !%d: %w", input.MRIID, err)
		}
		if len(commits) == 0 {
			return Plan{}, fmt.Errorf("MR !%d: %w", input.MRIID, ErrEmptyLayer)
		}
		for _, commit := range commits {
			if len(commit.Parents) > 1 {
				return Plan{}, fmt.Errorf("MR !%d commit %s: %w", input.MRIID, commit.OID, ErrMergeCommit)
			}
			if commit.Signed && !allowSignatureLoss {
				return Plan{}, fmt.Errorf("MR !%d commit %s: %w", input.MRIID, commit.OID, ErrSignedCommit)
			}
		}
		plan.Layers = append(plan.Layers, Layer{
			MRIID: input.MRIID, Branch: input.Branch, BoundaryOID: input.BoundaryOID,
			HeadOID: input.HeadOID, Commits: commits,
		})
	}
	return plan, nil
}

// enumerate uses full object IDs, parent lists, and the presence of a good
// signature in one parse-stable record per commit. It does not use merge-base
// or patch equivalence.
func (p Planner) enumerate(ctx context.Context, boundary, head string) ([]Commit, error) {
	res, err := p.Repo.Git(ctx, "log", "--reverse", "--format=%H%x00%P%x00%G?", boundary+".."+head)
	if err != nil {
		return nil, err
	}
	lines := strings.Split(strings.TrimSpace(string(res.Stdout)), "\n")
	commits := make([]Commit, 0, len(lines))
	for _, line := range lines {
		if line == "" {
			continue
		}
		fields := strings.Split(line, "\x00")
		if len(fields) != 3 || !gitexec.FullOID(fields[0]) {
			return nil, errors.New("unexpected git rev-list output")
		}
		parents := strings.Fields(fields[1])
		for _, parent := range parents {
			if !gitexec.FullOID(parent) {
				return nil, errors.New("unexpected parent object ID")
			}
		}
		signature := strings.TrimSpace(fields[2])
		signed := signature != "" && signature != "N"
		commits = append(commits, Commit{OID: fields[0], Parents: parents, Signed: signed})
	}
	return commits, nil
}

func (p Plan) RefMaps(newHeads []string) (oldRefs, newRefs map[string]string, err error) {
	if len(newHeads) != len(p.Layers) {
		return nil, nil, errors.New("new head count does not match layers")
	}
	oldRefs, newRefs = map[string]string{}, map[string]string{}
	for i, layer := range p.Layers {
		if !gitexec.FullOID(newHeads[i]) {
			return nil, nil, errors.New("invalid proposed object ID at layer " + strconv.Itoa(i))
		}
		if _, exists := oldRefs[layer.Branch]; exists {
			return nil, nil, errors.New("duplicate affected branch")
		}
		oldRefs[layer.Branch] = layer.HeadOID
		newRefs[layer.Branch] = newHeads[i]
	}
	return oldRefs, newRefs, nil
}
