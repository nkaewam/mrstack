package app

import (
	"context"
	"errors"
	"fmt"

	"github.com/nkaewam/mrstack/internal/api"
	"github.com/nkaewam/mrstack/internal/cli"
	"github.com/nkaewam/mrstack/internal/gitlab"
	"github.com/nkaewam/mrstack/internal/stack"
	"github.com/nkaewam/mrstack/internal/stackstore"
)

// checkNamedStack runs the check pipeline against a user-curated named stack.
func (h *Handler) checkNamedStack(ctx context.Context, inv cli.Invocation,
	rc repositoryContext, mode stack.Mode) (cli.Result, error) {
	store, err := h.stackStore()
	if err != nil {
		return cli.Result{}, err
	}
	named, err := store.Get(inv.Selector.Value)
	if errors.Is(err, stackstore.ErrNotFound) {
		return cli.Result{}, cli.Invalid("invalid_selector",
			fmt.Sprintf("no stack named %q; create it with `mrstack stack create`", inv.Selector.Value))
	}
	if errors.Is(err, stackstore.ErrInvalidName) {
		return cli.Result{}, cli.Invalid("invalid_arguments",
			fmt.Sprintf("invalid stack name %q", inv.Selector.Value))
	}
	if err != nil {
		return cli.Result{}, cli.Unavailable("stackstore_unavailable",
			"cannot read named stack: "+err.Error(), false)
	}
	if named.Host != rc.fetch.Host || named.Project != rc.fetch.Project {
		return cli.Result{}, cli.Invalid("invalid_selector",
			fmt.Sprintf("stack %q belongs to %s/%s, not this repository (%s/%s)",
				named.Name, named.Host, named.Project, rc.fetch.Host, rc.fetch.Project))
	}
	if len(named.MemberIIDs) == 0 {
		return cli.Result{}, cli.Invalid("invalid_selector",
			fmt.Sprintf("named stack %q has no members", named.Name))
	}

	mrs, byIID, branches, baseSHA, err := h.fetchMemberMRs(ctx, rc, named.MemberIIDs)
	if err != nil {
		return cli.Result{}, err
	}
	domainMRs := make([]stack.MergeRequest, 0, len(mrs))
	for _, mr := range mrs {
		domainMRs = append(domainMRs, toDomainMR(ctx, mr, rc, branches, baseSHA))
	}
	selectorAPI := api.Selector{Kind: "named_stack", Value: named.Name}
	discovered := stack.Discover(stack.DiscoveryInput{
		ProjectID: rc.project.ID.String(), DefaultBranch: rc.project.DefaultBranch,
		BaseSHA: baseSHA, Mode: mode, MergeRequests: domainMRs, Explicit: true,
	})
	res, err := h.assessCheck(ctx, inv, rc, mode, mrs, byIID, selectorAPI, discovered, true)
	if err != nil {
		return res, err
	}
	h.persistViewSnapshot(store, named, rc.project.DefaultBranch, discovered, mrs)
	return res, nil
}

// fetchMemberMRs live-fetches the given IIDs and resolves branch revisions.
func (h *Handler) fetchMemberMRs(ctx context.Context, rc repositoryContext, iids []int) (
	[]gitlab.MergeRequest, map[int]int, map[string]string, string, error) {
	mrs := make([]gitlab.MergeRequest, 0, len(iids))
	byIID := make(map[int]int, len(iids))
	branches := map[string]string{rc.project.DefaultBranch: ""}
	for _, iid := range iids {
		mr, mrErr := rc.client.MergeRequest(ctx, rc.project.ID.String(), iid)
		if mrErr != nil {
			return nil, nil, nil, "", classifyGlab("read merge request", mrErr)
		}
		byIID[mr.IID] = len(mrs)
		mrs = append(mrs, mr)
		branches[mr.SourceBranch] = ""
		branches[mr.TargetBranch] = ""
	}
	if err := fetchBranches(ctx, rc, branches); err != nil {
		return nil, nil, nil, "", cli.Unavailable("git_transport_failed",
			"cannot fetch exact stack branch revisions", true)
	}
	for branch := range branches {
		oid, revErr := rc.repo.RevParse(ctx, privateRef(branch))
		if revErr == nil {
			branches[branch] = oid
		}
	}
	baseSHA := branches[rc.project.DefaultBranch]
	if !fullOID(baseSHA) {
		return nil, nil, nil, "", cli.Unavailable("git_transport_failed",
			"the default branch revision could not be resolved", true)
	}
	return mrs, byIID, branches, baseSHA, nil
}

func (h *Handler) persistViewSnapshot(store *stackstore.Store, named stackstore.Stack,
	defaultBranch string, discovered stack.DiscoveryResult, mrs []gitlab.MergeRequest) {
	byIID := make(map[int]gitlab.MergeRequest, len(mrs))
	for _, mr := range mrs {
		byIID[mr.IID] = mr
	}
	snap := stackstore.ViewSnapshot{
		Name: named.Name, Host: named.Host, Project: named.Project,
		DefaultBranch: defaultBranch, CheckedAt: h.nowStamp(),
	}
	for i, member := range discovered.Stack.Members {
		mr := byIID[member.IID]
		snap.Members = append(snap.Members, stackstore.ViewSnapshotMember{
			Position: i, IID: mr.IID, Title: mr.Title,
			SourceBranch: mr.SourceBranch, TargetBranch: mr.TargetBranch,
			State: mr.State, WebURL: mr.WebURL,
			MergeStatus: mergeStatus(mr), PipelineStatus: pipelineStatus(mr),
		})
	}
	_ = store.SaveViewSnapshot(snap)
}
