package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/nkaewam/mrstack/internal/api"
	"github.com/nkaewam/mrstack/internal/cli"
	"github.com/nkaewam/mrstack/internal/gitlab"
	"github.com/nkaewam/mrstack/internal/journal"
	"github.com/nkaewam/mrstack/internal/stack"
)

// checkTrackedStack reconciles a journal-tracked stack that has fully merged.
func (h *Handler) checkTrackedStack(ctx context.Context, inv cli.Invocation,
	rc repositoryContext, _ stack.Mode, persist bool) (cli.Result, error) {
	return h.reconcileTrackedCompletion(ctx, inv, rc, persist)
}

func (h *Handler) reconcileTrackedCompletion(ctx context.Context, inv cli.Invocation,
	rc repositoryContext, persist bool) (cli.Result, error) {
	requestedStack := inv.Selector.StackID
	j, err := h.openJournal(rc.repo.Dir)
	if err != nil {
		return cli.Result{}, cli.Unavailable("journal_unavailable",
			"cannot inspect tracked stack completion", false)
	}
	defer j.Close()

	page, err := j.History(ctx, requestedStack, 1, "")
	if errors.Is(err, journal.ErrNotFound) || len(page.Records) == 0 {
		return cli.Result{}, cli.Invalid("invalid_selector", "tracked stack history was not found")
	}
	if err != nil {
		return cli.Result{}, cli.Unavailable("journal_unavailable",
			"cannot read tracked stack completion history", false)
	}
	if err := historyPageBelongsToProject(page, rc.fetch.Host, rc.fetch.Project); err != nil {
		return cli.Result{}, err
	}
	var historical api.Envelope
	if err := json.Unmarshal(page.Records[0].Payload, &historical); err != nil || historical.Stack == nil {
		return cli.Result{}, cli.Internal("stored stack observation is invalid", err)
	}
	stackValue := *historical.Stack
	iids := make([]int, len(stackValue.Members))
	for i, member := range stackValue.Members {
		iids[i] = member.IID
	}
	mrs, _, _, baseSHA, err := h.fetchMemberMRs(ctx, rc, iids)
	if err != nil {
		return cli.Result{}, err
	}
	byIID := make(map[int]gitlab.MergeRequest, len(mrs))
	for _, mr := range mrs {
		byIID[mr.IID] = mr
	}
	allMerged := true
	for _, member := range stackValue.Members {
		if byIID[member.IID].State != "merged" {
			allMerged = false
			break
		}
	}
	if !allMerged {
		return cli.Result{}, cli.Invalid("invalid_selector",
			"tracked stack still has active or non-merged members; check the named stack instead")
	}

	var previous time.Time
	var completionEvidence []evidenceInput
	proven := true
	for _, member := range stackValue.Members {
		mr := byIID[member.IID]
		integration := mr.SquashCommitSHA
		if !fullOID(integration) {
			integration = mr.MergeCommitSHA
		}
		mergedAt, timeErr := time.Parse(time.RFC3339Nano, mr.MergedAt)
		inBase := false
		if fullOID(integration) {
			inBase, _ = rc.repo.IsAncestor(ctx, integration, baseSHA)
		}
		if !fullOID(integration) || timeErr != nil || !inBase ||
			(!previous.IsZero() && !mergedAt.After(previous)) {
			proven = false
		}
		if timeErr == nil {
			previous = mergedAt
		}
		fields := map[string]any{
			"member_iid": member.IID, "state": mr.State, "web_url": mr.WebURL,
			"source_branch": mr.SourceBranch, "target_branch": mr.TargetBranch,
		}
		if fullOID(mr.SHA) {
			fields["source_sha"] = mr.SHA
		}
		completionEvidence = append(completionEvidence, evidenceInput{"gitlab_mr", fields})
	}
	stackValue.Base.SHA = baseSHA
	stackValue.ObservedAt = h.now()
	stackValue.Selector = api.Selector{Kind: "tracked_stack", Value: requestedStack}
	stackValue.AffectedSuffix = nil
	for i := range stackValue.Members {
		stackValue.Members[i].State = "merged"
		stackValue.Members[i].Alignment = "aligned"
	}
	stackValue.SnapshotID = snapshotID(stackValue)
	if !proven {
		return h.humanHandoffResult(inv.Name, stackValue,
			"ambiguous_completion", api.FindingScope{Kind: "stack"},
			"all tracked members are merged, but integration revision or merge order cannot be proven",
			completionEvidence...)
	}
	env, _, err := h.envelope(inv.Name)
	if err != nil {
		return cli.Result{}, cli.Internal("cannot create completion envelope", err)
	}
	env.Stack = &stackValue
	disposition := api.DispositionComplete
	env.Disposition = &disposition
	if persist {
		if err := h.persistCheck(ctx, rc, env); err != nil {
			return cli.Result{}, cli.Unavailable("journal_unavailable",
				"cannot persist completed stack observation", false)
		}
	}
	return result(env, fmt.Sprintf("Stack %s: complete", requestedStack))
}
