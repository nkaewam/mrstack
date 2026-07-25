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
)

func (h *Handler) reconcileTrackedCompletion(ctx context.Context, inv cli.Invocation,
	rc repositoryContext, mrs []gitlab.MergeRequest, baseSHA string, persist bool) (cli.Result, bool, error) {
	requestedStack := inv.Selector.StackID
	requestedIID, iidSelector := decimalIID(inv.Selector.Value)
	if requestedStack == "" && !iidSelector {
		return cli.Result{}, false, nil
	}
	j, err := h.openJournal(rc.repo.Dir)
	if err != nil {
		return cli.Result{}, true, cli.Unavailable("journal_unavailable",
			"cannot inspect tracked stack completion", false)
	}
	defer j.Close()
	if requestedStack == "" {
		tracked, trackedErr := j.TrackedStacks(ctx, rc.fetch.Host+"/"+rc.fetch.Project)
		if trackedErr != nil {
			return cli.Result{}, true, cli.Unavailable("journal_unavailable",
				"cannot enumerate tracked stack completion", false)
		}
		for _, candidate := range tracked {
			page, pageErr := j.History(ctx, candidate.StackID, 1, "")
			if pageErr != nil || len(page.Records) == 0 {
				continue
			}
			var historical api.Envelope
			if json.Unmarshal(page.Records[0].Payload, &historical) != nil || historical.Stack == nil {
				continue
			}
			for _, member := range historical.Stack.Members {
				if member.IID == requestedIID {
					if requestedStack != "" && requestedStack != candidate.StackID {
						return cli.Result{}, true, cli.Invalid("invalid_selector",
							"merge request belongs to multiple tracked stacks; use --stack")
					}
					requestedStack = candidate.StackID
				}
			}
		}
		if requestedStack == "" {
			return cli.Result{}, false, nil
		}
	}
	page, err := j.History(ctx, requestedStack, 1, "")
	if errors.Is(err, journal.ErrNotFound) || len(page.Records) == 0 {
		return cli.Result{}, true, cli.Invalid("invalid_selector", "tracked stack history was not found")
	}
	if err != nil {
		return cli.Result{}, true, cli.Unavailable("journal_unavailable",
			"cannot read tracked stack completion history", false)
	}
	if err := historyPageBelongsToProject(page, rc.fetch.Host, rc.fetch.Project); err != nil {
		return cli.Result{}, true, err
	}
	var historical api.Envelope
	if err := json.Unmarshal(page.Records[0].Payload, &historical); err != nil || historical.Stack == nil {
		return cli.Result{}, true, cli.Internal("stored stack observation is invalid", err)
	}
	stackValue := *historical.Stack
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
		if inv.Selector.StackID != "" {
			return cli.Result{}, true, cli.Invalid("invalid_selector",
				"tracked stack still has active or non-merged members; select an active member")
		}
		return cli.Result{}, false, nil
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
		resultValue, resultErr := h.humanHandoffResult(inv.Name, stackValue,
			"ambiguous_completion", api.FindingScope{Kind: "stack"},
			"all tracked members are merged, but integration revision or merge order cannot be proven",
			completionEvidence...)
		return resultValue, true, resultErr
	}
	env, _, err := h.envelope(inv.Name)
	if err != nil {
		return cli.Result{}, true, cli.Internal("cannot create completion envelope", err)
	}
	env.Stack = &stackValue
	disposition := api.DispositionComplete
	env.Disposition = &disposition
	if persist {
		if err := h.persistCheck(ctx, rc, env); err != nil {
			return cli.Result{}, true, cli.Unavailable("journal_unavailable",
				"cannot persist completed stack observation", false)
		}
	}
	out, err := result(env, fmt.Sprintf("Stack %s: complete", requestedStack))
	return out, true, err
}
