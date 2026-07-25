package app

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/nkaewam/mrstack/internal/api"
	"github.com/nkaewam/mrstack/internal/cli"
	"github.com/nkaewam/mrstack/internal/gitexec"
	"github.com/nkaewam/mrstack/internal/gitlab"
	"github.com/nkaewam/mrstack/internal/journal"
	"github.com/nkaewam/mrstack/internal/stack"
)

type capturedSnapshot struct {
	Stack api.Stack `json:"stack"`
}

func (h *Handler) check(ctx context.Context, inv cli.Invocation, persist bool) (cli.Result, error) {
	rc, err := h.repository(ctx, inv.Globals.Remote, true)
	if err != nil {
		return cli.Result{}, err
	}
	version, err := rc.client.Version(ctx)
	if err != nil {
		if inv.Globals.GitLabMode == "auto" {
			return cli.Result{}, cli.Unavailable("server_mode_undetermined",
				"cannot detect GitLab version; pass an explicit --gitlab-mode", false)
		}
		version.Version = ""
	}
	var explicit stack.Mode
	if inv.Globals.GitLabMode != "auto" {
		explicit = stack.Mode(inv.Globals.GitLabMode)
	}
	mode, err := stack.SelectMode(version.Version, explicit)
	if err != nil {
		return cli.Result{}, cli.Invalid("invalid_arguments", err.Error())
	}
	mrs, err := rc.client.MergeRequests(ctx, rc.project.ID.String(), "all")
	if err != nil {
		return cli.Result{}, classifyGlab("list merge requests", err)
	}

	branches := map[string]string{rc.project.DefaultBranch: ""}
	for _, mr := range mrs {
		branches[mr.SourceBranch] = ""
		branches[mr.TargetBranch] = ""
	}
	if err := fetchBranches(ctx, rc, branches); err != nil {
		return cli.Result{}, cli.Unavailable("git_transport_failed",
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
		return cli.Result{}, cli.Unavailable("git_transport_failed",
			"the default branch revision could not be resolved", true)
	}
	if completion, handled, completionErr := h.reconcileTrackedCompletion(
		ctx, inv, rc, mrs, baseSHA, persist); handled {
		return completion, completionErr
	}

	var selector stack.Selector
	var selectorAPI api.Selector
	switch {
	case inv.Selector.Value == "":
		branch, branchErr := rc.repo.CurrentBranch(ctx)
		if branchErr != nil {
			return cli.Result{}, cli.Invalid("invalid_selector",
				"detached HEAD requires an explicit merge request or branch selector")
		}
		selector = stack.Selector{Kind: stack.SelectCurrentBranch, Branch: branch}
		selectorAPI = api.Selector{Kind: "current_branch", Value: branch}
	case func() bool { _, ok := decimalIID(inv.Selector.Value); return ok }():
		iid, _ := decimalIID(inv.Selector.Value)
		selector = stack.Selector{Kind: stack.SelectMergeRequest, IID: iid}
		selectorAPI = api.Selector{Kind: "mr", Value: strconv.Itoa(iid)}
	default:
		selector = stack.Selector{Kind: stack.SelectBranch, Branch: inv.Selector.Value}
		selectorAPI = api.Selector{Kind: "branch", Value: inv.Selector.Value}
	}

	domainMRs := make([]stack.MergeRequest, 0, len(mrs))
	byIID := make(map[int]int, len(mrs))
	for i, mr := range mrs {
		sourceSHA := strings.ToLower(mr.SHA)
		sourceExists := fullOID(sourceSHA) && branches[mr.SourceBranch] == sourceSHA
		targetSHA := branches[mr.TargetBranch]
		state := stack.MRState(mr.State)
		switch state {
		case stack.StateOpen, stack.StateClosed, stack.StateMerged:
		default:
			// Unknown provider lifecycle values cannot be allowed to look open.
			state = stack.StateClosed
		}
		integrationRevision := strings.ToLower(mr.SquashCommitSHA)
		if !fullOID(integrationRevision) {
			integrationRevision = strings.ToLower(mr.MergeCommitSHA)
		}
		integrationInBase := false
		if fullOID(integrationRevision) {
			integrationInBase, _ = rc.repo.IsAncestor(ctx, integrationRevision, baseSHA)
		}
		historicalTarget := strings.ToLower(mr.DiffRefs.HeadSHA)
		if !fullOID(historicalTarget) {
			historicalTarget = sourceSHA
		}
		if !fullOID(historicalTarget) {
			historicalTarget = ""
		}
		sourceProjectID := mr.SourceProjectID.String()
		if sourceProjectID == "" {
			sourceProjectID = rc.project.ID.String()
		}
		targetProjectID := mr.TargetProjectID.String()
		if targetProjectID == "" {
			targetProjectID = rc.project.ID.String()
		}
		domainMRs = append(domainMRs, stack.MergeRequest{
			IID: mr.IID, ProjectID: rc.project.ID.String(),
			SourceProjectID: sourceProjectID, TargetProjectID: targetProjectID,
			SourceBranch: mr.SourceBranch, TargetBranch: mr.TargetBranch,
			SourceSHA: sourceSHA, TargetSHA: targetSHA, State: state,
			SourceBranchExists: sourceExists, TargetBranchExists: fullOID(targetSHA),
			IntegrationRevision: integrationRevision, IntegrationInBase: integrationInBase,
			HistoricalTargetSHA: historicalTarget,
		})
		byIID[mr.IID] = i
	}
	discovered := stack.Discover(stack.DiscoveryInput{
		ProjectID: rc.project.ID.String(), DefaultBranch: rc.project.DefaultBranch,
		BaseSHA: baseSHA, Mode: mode, Selector: selector, MergeRequests: domainMRs,
	})
	allFindings := append([]stack.Finding(nil), discovered.Findings...)
	alignment := stack.AlignmentResult{Aligned: true, AffectedSuffixStart: -1}
	if len(discovered.Stack.Members) > 0 {
		alignment = stack.AssessAlignment(discovered.Stack, func(ancestor, descendant string) (bool, error) {
			return rc.repo.IsAncestor(ctx, ancestor, descendant)
		})
		allFindings = append(allFindings, alignment.Findings...)
		for position, member := range discovered.Stack.Members {
			raw := mrs[byIID[member.IID]]
			switch {
			case raw.HasConflicts:
				allFindings = append(allFindings, stack.Finding{
					Code: stack.FindingMergeConflict, Disposition: stack.DispositionActionRequired,
					Message: "GitLab reports conflicts for the current source and target revisions",
					MRIID:   member.IID, LayerIndex: position,
				})
			case raw.DetailedMergeStatus == "checking" || raw.DetailedMergeStatus == "unchecked":
				allFindings = append(allFindings, stack.Finding{
					Code: stack.FindingMergeabilityChecking, Disposition: stack.DispositionWaiting,
					Message: "GitLab mergeability is still being computed",
					MRIID:   member.IID, LayerIndex: position,
				})
			}
		}
	}

	pipelines := make([]stack.Pipeline, 0, len(discovered.Stack.Members))
	jobsByPipeline := map[string][]api.FailedJob{}
	for _, member := range discovered.Stack.Members {
		raw := mrs[byIID[member.IID]]
		if raw.HeadPipeline == nil {
			continue
		}
		head := raw.HeadPipeline
		pid, parseErr := strconv.ParseInt(head.ID.String(), 10, 64)
		if parseErr != nil {
			continue
		}
		p, pipelineErr := rc.client.Pipeline(ctx, rc.project.ID.String(), head.ID.String())
		if pipelineErr != nil {
			return cli.Result{}, classifyGlab("read pipeline evidence", pipelineErr)
		}
		kind := classifyPipelineKind(p, member.IID)
		domainPipeline := stack.Pipeline{
			ID: pid, MRIID: member.IID, Kind: kind,
			SHA: strings.ToLower(p.SHA), SourceSHA: strings.ToLower(p.SHA),
			Status: p.Status, WebURL: p.WebURL,
		}
		if p.ID.String() != head.ID.String() {
			domainPipeline.Kind = stack.PipelineUnknown
		}
		if kind == stack.PipelineMergedResult {
			associated, associationErr := rc.client.PipelineMergeRequests(
				ctx, rc.project.ID.String(), head.ID.String())
			if associationErr != nil {
				return cli.Result{}, classifyGlab("read pipeline merge request association", associationErr)
			}
			domainPipeline.AssociatedWithMR = pipelineAssociatedExactly(associated, member.IID)
		}
		if kind == stack.PipelineMergedResult {
			// A merged-results pipeline runs against a synthetic commit, not
			// the source branch tip. Its exact source currentness is proven by
			// the immutable two-parent commit evidence below.
			domainPipeline.SourceSHA = member.SourceSHA
			if fullOID(p.SHA) {
				commit, commitErr := rc.client.Commit(ctx, rc.project.ID.String(), p.SHA)
				if commitErr != nil {
					return cli.Result{}, classifyGlab("read merged-results commit evidence", commitErr)
				}
				if strings.EqualFold(commit.ID, p.SHA) {
					for _, parent := range commit.ParentIDs {
						domainPipeline.SyntheticParents = append(
							domainPipeline.SyntheticParents, strings.ToLower(parent))
					}
				}
			}
		}
		if p.Status == "failed" || p.Status == "canceled" || p.Status == "skipped" {
			jobs, jobsErr := rc.client.PipelineJobs(ctx, rc.project.ID.String(), head.ID.String())
			if jobsErr != nil {
				return cli.Result{}, classifyGlab("read failed pipeline jobs", jobsErr)
			}
			for _, job := range jobs {
				if (job.Status == "failed" || job.Status == "canceled") && !job.AllowFail {
					id, idErr := strconv.ParseInt(job.ID.String(), 10, 64)
					if idErr == nil {
						domainPipeline.FailedJobIDs = append(domainPipeline.FailedJobIDs, id)
						jobsByPipeline[head.ID.String()] = append(jobsByPipeline[head.ID.String()], api.FailedJob{
							ID: job.ID.String(), Name: job.Name, Status: job.Status, WebURL: job.WebURL,
						})
					}
				}
			}
		}
		pipelines = append(pipelines, domainPipeline)
	}
	policy := stack.CIPolicyUnknown
	if rc.project.OnlyAllowMergeIfPipeline != nil {
		policy = stack.CIPolicyOptional
		if *rc.project.OnlyAllowMergeIfPipeline {
			policy = stack.CIPolicyRequired
		}
	}
	ciFindings := stack.AssessStackCI(discovered.Stack, policy, pipelines)
	allFindings = append(allFindings, ciFindings...)
	disposition := stack.ResolveDisposition(allFindings, stack.DispositionReady)
	commitCounts := map[int]int{}
	for _, member := range discovered.Stack.Members {
		raw := mrs[byIID[member.IID]]
		if !fullOID(raw.DiffRefs.StartSHA) {
			continue
		}
		countResult, countErr := rc.repo.Git(ctx, "rev-list", "--count",
			strings.ToLower(raw.DiffRefs.StartSHA)+".."+member.SourceSHA)
		if countErr != nil {
			continue
		}
		count, countErr := strconv.Atoi(strings.TrimSpace(string(countResult.Stdout)))
		if countErr == nil && count >= 0 {
			commitCounts[member.IID] = count
		}
	}

	env, factory, err := h.envelope(inv.Name)
	if err != nil {
		return cli.Result{}, cli.Internal("cannot create response envelope", err)
	}
	if len(discovered.Stack.Members) == 0 {
		for _, finding := range allFindings {
			out, createErr := convertFinding(factory, finding)
			if createErr != nil {
				return cli.Result{}, cli.Internal("cannot create finding", createErr)
			}
			env.Findings = append(env.Findings, out)
		}
		env.ApplyFindingDisposition()
		return result(env, fmt.Sprintf("Check: %s\n  %s", disposition, env.Findings[0].Summary))
	}
	apiStack := buildAPIStack(h.now(), rc, selectorAPI, mode, discovered.Stack, alignment,
		mrs, byIID, pipelines, jobsByPipeline, commitCounts, policy)
	apiStack.StackID = stableID("stk", struct {
		Project string
		IIDs    []int
	}{rc.fetch.Host + "/" + rc.fetch.Project, memberIIDs(apiStack.Members)})
	apiStack.SnapshotID = snapshotID(apiStack)
	env.Stack = &apiStack
	for _, finding := range allFindings {
		out, createErr := convertFinding(factory, finding)
		if createErr != nil {
			return cli.Result{}, cli.Internal("cannot create finding", createErr)
		}
		env.Findings = append(env.Findings, out)
	}
	if len(env.Findings) == 0 {
		d := api.Disposition(disposition)
		env.Disposition = &d
	} else {
		env.ApplyFindingDisposition()
	}
	if persist {
		if err := h.stabilizeCheckFindings(ctx, rc, &env); err != nil {
			return cli.Result{}, cli.Unavailable("journal_unavailable",
				"cannot stabilize stack finding identities", false)
		}
	}
	if err := h.attachCheckPackets(&env, factory, rc.repo.Dir); err != nil {
		return cli.Result{}, cli.Internal("cannot create check remediation packets", err)
	}

	human := renderCheck(apiStack, api.Disposition(disposition), env.Findings)
	if persist {
		if err := h.persistCheck(ctx, rc, env); err != nil {
			return cli.Result{}, cli.Unavailable("journal_unavailable",
				"cannot persist the stack observation", false)
		}
	}
	return result(env, human)
}

func (h *Handler) stabilizeCheckFindings(ctx context.Context, rc repositoryContext,
	env *api.Envelope) error {
	if env.Stack == nil {
		return nil
	}
	j, err := h.openJournal(rc.repo.Dir)
	if err != nil {
		return err
	}
	defer j.Close()
	candidates := make([]journal.FindingCandidate, len(env.Findings))
	for index := range env.Findings {
		finding := &env.Findings[index]
		candidates[index] = journal.FindingCandidate{
			SemanticKey: stableID("fkey", struct {
				Code  string
				Scope api.FindingScope
			}{finding.Code, finding.Scope}),
			ProposedID: stableID("fnd", struct {
				StackID string
				Code    string
				Scope   api.FindingScope
				SeenAt  string
			}{env.Stack.StackID, finding.Code, finding.Scope, finding.FirstSeenAt}),
		}
	}
	identities, err := j.StabilizeFindings(ctx, env.Stack.StackID,
		rc.fetch.Host+"/"+rc.fetch.Project, candidates)
	if err != nil {
		return err
	}
	for index, identity := range identities {
		env.Findings[index].FindingID = identity.FindingID
		env.Findings[index].FirstSeenAt = identity.FirstSeenAt
		env.Findings[index].LastSeenAt = identity.LastSeenAt
	}
	return nil
}

func snapshotID(value api.Stack) string {
	value.SnapshotID = ""
	value.ObservedAt = ""
	return stableID("snp", value)
}

func (h *Handler) now() string {
	now := h.Now
	if now == nil {
		now = timeNow
	}
	return now().UTC().Format("2006-01-02T15:04:05.999999999Z")
}

var timeNow = func() time.Time { return time.Now() }

func privateRef(branch string) string {
	return "refs/mrstack/check/" + stableID("ref", branch)
}

func fetchBranches(ctx context.Context, rc repositoryContext, branches map[string]string) error {
	names := make([]string, 0, len(branches))
	for branch := range branches {
		if !gitexec.ValidBranch(branch) {
			continue
		}
		names = append(names, branch)
	}
	sort.Strings(names)
	remoteRefs, err := rc.repo.RemoteRefsAllowMissing(ctx, rc.remoteName, names)
	if err != nil {
		return err
	}
	args := []string{"fetch", "--no-tags", rc.remoteName}
	for _, branch := range names {
		if !fullOID(remoteRefs[branch]) {
			continue
		}
		args = append(args, "+refs/heads/"+branch+":"+privateRef(branch))
	}
	if len(args) == 3 {
		return nil
	}
	_, err = rc.repo.Git(ctx, args...)
	return err
}

func classifyPipelineKind(p gitlab.Pipeline, memberIID int) stack.PipelineKind {
	if p.Source != "merge_request_event" {
		return stack.PipelineBranch
	}
	prefix := "refs/merge-requests/" + strconv.Itoa(memberIID) + "/"
	switch p.Ref {
	case prefix + "head":
		return stack.PipelineDetachedMR
	case prefix + "merge":
		return stack.PipelineMergedResult
	default:
		return stack.PipelineUnknown
	}
}

func pipelineAssociatedExactly(requests []gitlab.PipelineMergeRequest, memberIID int) bool {
	return len(requests) == 1 && requests[0].IID == memberIID
}

func buildAPIStack(observedAt string, rc repositoryContext, selector api.Selector, mode stack.Mode,
	discovered stack.Stack, alignment stack.AlignmentResult, raw []gitlab.MergeRequest,
	byIID map[int]int, pipelines []stack.Pipeline, jobs map[string][]api.FailedJob,
	commitCounts map[int]int, policy stack.CIPolicy) api.Stack {
	out := api.Stack{
		ObservedAt: observedAt, Selector: selector, GitLabMode: string(mode),
		Remote: api.Remote{
			Name: rc.remoteName, Selection: rc.selection,
			Fetch: api.RemoteEndpoint{Host: rc.fetch.Host, Project: rc.fetch.Project},
			Push:  api.RemoteEndpoint{Host: rc.push.Host, Project: rc.push.Project},
		},
		Project: api.Project{
			Host: rc.fetch.Host, ID: rc.project.ID.String(),
			PathWithNamespace: rc.project.PathWithNamespace, WebURL: rc.project.WebURL,
			DefaultBranch: rc.project.DefaultBranch,
		},
		Base:    api.Base{Branch: rc.project.DefaultBranch, SHA: discovered.BaseSHA},
		Members: []api.Member{},
	}
	pipelineByMR := map[int]stack.Pipeline{}
	for _, member := range discovered.Members {
		assessment := stack.AssessCI(member, policy, pipelines)
		if !assessment.Applicable {
			continue
		}
		for _, p := range pipelines {
			if p.ID == assessment.PipelineID {
				pipelineByMR[member.IID] = p
				break
			}
		}
	}
	for position, member := range discovered.Members {
		mr := raw[byIID[member.IID]]
		boundary := member.TargetSHA
		boundarySource := "unavailable"
		if fullOID(mr.DiffRefs.StartSHA) {
			boundary = strings.ToLower(mr.DiffRefs.StartSHA)
			boundarySource = "gitlab_diff_version"
		}
		var boundaryPtr *string
		var countPtr *int
		if count, ok := commitCounts[member.IID]; fullOID(boundary) && ok {
			boundaryPtr = &boundary
			countPtr = &count
		} else {
			boundarySource = "unavailable"
		}
		source, target := member.SourceSHA, member.TargetSHA
		var sourcePtr, targetPtr *string
		if fullOID(source) {
			sourcePtr = &source
		}
		if fullOID(target) {
			targetPtr = &target
		}
		alignmentStatus := "aligned"
		if !alignment.Aligned && (alignment.AffectedSuffixStart < 0 || position >= alignment.AffectedSuffixStart) {
			alignmentStatus = "stale"
		}
		conflict := "none"
		if mr.HasConflicts {
			conflict = "reported"
		} else if mr.DetailedMergeStatus == "checking" || mr.DetailedMergeStatus == "unchecked" {
			conflict = "checking"
		}
		apiMember := api.Member{
			Position: position, IID: member.IID, State: string(member.State), WebURL: mr.WebURL,
			SourceBranch: member.SourceBranch, TargetBranch: member.TargetBranch,
			SourceSHA: sourcePtr, TargetSHA: targetPtr, TargetResolution: "live_branch",
			Author: api.Author{ID: mr.Author.ID.String(), Username: mr.Author.Username},
			Layer: api.Layer{
				BoundarySHA: boundaryPtr, BoundarySource: boundarySource, CommitCount: countPtr,
			},
			Alignment: alignmentStatus, ConflictStatus: conflict,
		}
		if position == 0 && discovered.MergedPredecessor != nil {
			apiMember.TargetResolution = "integrated_predecessor"
		}
		if p, ok := pipelineByMR[member.IID]; ok {
			id, sha, sourceSHA := strconv.FormatInt(p.ID, 10), p.SHA, p.SourceSHA
			if !fullOID(sha) {
				sha = sourceSHA
			}
			kind, webURL, status := string(p.Kind), p.WebURL, p.Status
			if kind == "detached_merge_request" {
				kind = "detached_mr"
			} else if kind == "merged_result" {
				kind = "merged_results"
			}
			if webURL == "" && mr.HeadPipeline != nil {
				webURL = mr.HeadPipeline.WebURL
			}
			applicability := "unknown"
			if policy == stack.CIPolicyRequired {
				applicability = "required"
			}
			failedJobs := jobs[id]
			if failedJobs == nil {
				failedJobs = []api.FailedJob{}
			}
			apiPipeline := &api.Pipeline{
				Applicability: applicability, Currentness: "exact", Kind: &kind,
				ID: &id, SHA: &sha, SourceSHA: &sourceSHA,
				BlockingStatus: normalizePipelineStatus(status),
				WebURL:         &webURL, FailedJobs: failedJobs,
			}
			if p.Kind == stack.PipelineMergedResult {
				targetSHA := member.TargetSHA
				apiPipeline.TargetSHA = &targetSHA
			}
			apiMember.Pipeline = apiPipeline
		} else {
			currentness, applicability := "not_applicable", "not_applicable"
			switch policy {
			case stack.CIPolicyRequired:
				currentness, applicability = "missing", "required"
			case stack.CIPolicyUnknown:
				currentness, applicability = "missing", "unknown"
			}
			for _, p := range pipelines {
				if p.MRIID == member.IID && p.SourceSHA == member.SourceSHA {
					currentness = "ambiguous"
					if policy == stack.CIPolicyOptional {
						applicability = "unknown"
					}
					break
				}
			}
			apiMember.Pipeline = &api.Pipeline{
				Applicability: applicability, Currentness: currentness,
				BlockingStatus: "unknown", FailedJobs: []api.FailedJob{},
			}
		}
		out.Members = append(out.Members, apiMember)
	}
	if !alignment.Aligned && alignment.AffectedSuffixStart >= 0 {
		iids := make([]int, 0, len(out.Members)-alignment.AffectedSuffixStart)
		for _, member := range out.Members[alignment.AffectedSuffixStart:] {
			iids = append(iids, member.IID)
		}
		out.AffectedSuffix = &api.AffectedSuffix{
			FromPosition: alignment.AffectedSuffixStart, MemberIIDs: iids,
		}
	}
	return out
}

func normalizePipelineStatus(status string) string {
	switch status {
	case "created", "pending", "running", "manual", "success", "failed", "canceled", "skipped":
		return status
	case "preparing", "waiting_for_resource", "scheduled":
		return "pending"
	default:
		return "unknown"
	}
}

func convertFinding(factory *api.Factory, in stack.Finding) (api.Finding, error) {
	code := map[stack.FindingCode]string{
		stack.FindingNoStackSelected:            "no_stack_selected",
		stack.FindingAmbiguousSelector:          "ambiguous_relationship",
		stack.FindingFork:                       "non_linear_stack",
		stack.FindingCycle:                      "cyclic_relationship",
		stack.FindingAmbiguousEdge:              "ambiguous_relationship",
		stack.FindingCrossProjectMember:         "cross_project_member",
		stack.FindingNonDefaultBase:             "non_default_base",
		stack.FindingMaximumDepthExceeded:       "stack_too_deep",
		stack.FindingMissingActiveBranch:        "missing_active_branch",
		stack.FindingAmbiguousMergedPredecessor: "ambiguous_merged_predecessor",
		stack.FindingClosedMember:               "closed_member",
		stack.FindingOutOfOrderMerge:            "out_of_order_merge",
		stack.FindingNativeRetargetPending:      "native_retarget_pending",
		stack.FindingUnaligned:                  "restack_required",
		stack.FindingAncestryUnknown:            "ambiguous_layer_boundary",
		stack.FindingCIFailed:                   "pipeline_failed",
		stack.FindingAmbiguousPipeline:          "ambiguous_pipeline",
		stack.FindingMissingRequiredPipeline:    "missing_required_pipeline",
		stack.FindingCIPolicyUnknown:            "ci_policy_unknown",
		stack.FindingPipelineStatusUnknown:      "pipeline_status_unknown",
		stack.FindingPipelineRunning:            "pipeline_running",
		stack.FindingMergeConflict:              "merge_conflict",
		stack.FindingMergeabilityChecking:       "mergeability_checking",
	}[in.Code]
	disposition := api.Disposition(in.Disposition)
	scope := api.FindingScope{Kind: "stack"}
	if in.MRIID > 0 {
		scope.Kind = "member"
		scope.MRIID = &in.MRIID
	}
	if in.LayerIndex >= 0 {
		scope.Position = &in.LayerIndex
	}
	if in.PipelineID > 0 {
		id := strconv.FormatInt(in.PipelineID, 10)
		scope.PipelineID = &id
	}
	return factory.NewFinding(code, disposition, scope, in.Message)
}

func memberIIDs(members []api.Member) []int {
	out := make([]int, len(members))
	for i := range members {
		out[i] = members[i].IID
	}
	return out
}

func renderCheck(s api.Stack, disposition api.Disposition, findings []api.Finding) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Stack %s: %s\n", s.StackID, disposition)
	for _, member := range s.Members {
		fmt.Fprintf(&b, "  %d. !%d %s -> %s [%s]\n", member.Position+1, member.IID,
			member.SourceBranch, member.TargetBranch, member.Alignment)
	}
	for _, finding := range findings {
		fmt.Fprintf(&b, "  %s: %s\n", finding.Code, finding.Summary)
	}
	return strings.TrimSuffix(b.String(), "\n")
}

func (h *Handler) persistCheck(ctx context.Context, rc repositoryContext, env api.Envelope) error {
	root := h.stateRoot(rc.repo.Dir)
	if err := writeJSONAtomic(filepath.Join(root, "snapshots", env.Stack.SnapshotID+".json"),
		capturedSnapshot{Stack: *env.Stack}); err != nil {
		return err
	}
	j, err := journal.Open(filepath.Join(root, "journal.sqlite"), func() time.Time {
		now := h.Now
		if now == nil {
			return time.Now()
		}
		return now()
	})
	if err != nil {
		return err
	}
	defer j.Close()
	payload, err := json.Marshal(env)
	if err != nil {
		return err
	}
	err = j.RecordObservation(ctx, journal.Observation{
		ObservationID: stableID("obs", struct {
			Snapshot string
			Command  string
		}{env.Stack.SnapshotID, env.Command.InvocationID}),
		StackID: env.Stack.StackID, ProjectKey: rc.fetch.Host + "/" + rc.fetch.Project,
		SnapshotID: env.Stack.SnapshotID, Disposition: string(*env.Disposition), Payload: payload,
	})
	if err != nil && strings.Contains(strings.ToLower(err.Error()), "unique") {
		// A content-addressed snapshot can legitimately be observed twice.
		return nil
	}
	return err
}
