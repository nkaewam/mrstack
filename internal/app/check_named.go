package app

import (
	"context"
	"fmt"

	"github.com/nkaewam/mrstack/internal/api"
	"github.com/nkaewam/mrstack/internal/cli"
	"github.com/nkaewam/mrstack/internal/gitlab"
	"github.com/nkaewam/mrstack/internal/stack"
)

// checkNamedStack runs the check pipeline against a user-curated named stack.
// It returns handled=false when the selector does not name a stack bound to the
// current repository, so the caller can fall back to autodiscovery.
func (h *Handler) checkNamedStack(ctx context.Context, inv cli.Invocation,
	rc repositoryContext, mode stack.Mode) (bool, cli.Result, error) {
	store, err := h.stackStore()
	if err != nil {
		return true, cli.Result{}, err
	}
	named, lookupErr := store.Get(inv.Selector.Value)
	if lookupErr != nil {
		// Not a registered named stack (or an invalid name): fall back to the
		// autodiscovery selector interpretation.
		return false, cli.Result{}, nil
	}
	if named.Host != rc.fetch.Host || named.Project != rc.fetch.Project {
		// The name exists but belongs to a different project; do not hijack the
		// selector in this repository.
		return false, cli.Result{}, nil
	}
	if len(named.MemberIIDs) == 0 {
		return true, cli.Result{}, cli.Invalid("invalid_selector",
			fmt.Sprintf("named stack %q has no members", named.Name))
	}

	mrs := make([]gitlab.MergeRequest, 0, len(named.MemberIIDs))
	byIID := make(map[int]int, len(named.MemberIIDs))
	branches := map[string]string{rc.project.DefaultBranch: ""}
	for _, iid := range named.MemberIIDs {
		mr, mrErr := rc.client.MergeRequest(ctx, rc.project.ID.String(), iid)
		if mrErr != nil {
			return true, cli.Result{}, classifyGlab("read merge request", mrErr)
		}
		byIID[mr.IID] = len(mrs)
		mrs = append(mrs, mr)
		branches[mr.SourceBranch] = ""
		branches[mr.TargetBranch] = ""
	}
	if err := fetchBranches(ctx, rc, branches); err != nil {
		return true, cli.Result{}, cli.Unavailable("git_transport_failed",
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
		return true, cli.Result{}, cli.Unavailable("git_transport_failed",
			"the default branch revision could not be resolved", true)
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
	return true, res, err
}
