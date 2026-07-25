package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/nkaewam/mrstack/internal/api"
	"github.com/nkaewam/mrstack/internal/cli"
	"github.com/nkaewam/mrstack/internal/gitexec"
	"github.com/nkaewam/mrstack/internal/journal"
	"github.com/nkaewam/mrstack/internal/restack"
)

type durableSession struct {
	API            api.Session  `json:"api"`
	Plan           restack.Plan `json:"plan"`
	ProjectID      string       `json:"project_id"`
	ReplayLayer    int          `json:"replay_layer"`
	ReplayCommit   int          `json:"replay_commit"`
	CompletedHeads []string     `json:"completed_heads"`
}

func (h *Handler) restackPlan(ctx context.Context, inv cli.Invocation) (cli.Result, error) {
	rc, captured, err := h.loadSnapshot(ctx, inv.Globals.Remote, inv.SnapshotID)
	if err != nil {
		return cli.Result{}, err
	}
	current, err := h.refreshSnapshot(ctx, captured.Stack, inv)
	if err != nil {
		return cli.Result{}, err
	}
	if current.SnapshotID != captured.Stack.SnapshotID {
		return cli.Result{}, cli.Invalid("invalid_selector",
			"snapshot is stale; run check and use the new snapshot_id")
	}
	overrides := make(map[int]string, len(inv.Boundaries))
	for _, item := range inv.Boundaries {
		iid, _ := strconv.Atoi(item.MR)
		if _, duplicate := overrides[iid]; duplicate {
			return cli.Result{}, cli.Invalid("invalid_arguments",
				fmt.Sprintf("layer boundary for MR !%d was supplied more than once", iid))
		}
		overrides[iid] = strings.ToLower(item.SHA)
	}
	first := len(current.Members)
	for i, member := range current.Members {
		if _, ok := overrides[member.IID]; ok && i < first {
			first = i
		}
	}
	if first == len(current.Members) {
		return cli.Result{}, cli.Invalid("invalid_selector",
			"no supplied layer boundary belongs to the captured stack")
	}
	inputs, err := layerInputs(current, first, overrides)
	if err != nil {
		return cli.Result{}, cli.Invalid("invalid_selector", err.Error())
	}
	plan, err := (restack.Planner{Repo: rc.repo}).Build(ctx, replayBase(current, first), inputs, inv.AllowSignatureLoss)
	if err != nil {
		return cli.Result{}, restackPreflightError(err)
	}
	apiPlan := api.Plan{
		SnapshotID: current.SnapshotID, State: "ready", CreatedAt: h.now(),
		Remote: current.Remote, Overrides: []api.PlanOverride{}, Layers: []api.PlanLayer{},
	}
	for _, boundary := range inv.Boundaries {
		iid, _ := strconv.Atoi(boundary.MR)
		apiPlan.Overrides = append(apiPlan.Overrides, api.PlanOverride{
			MRIID: iid, BoundarySHA: strings.ToLower(boundary.SHA),
		})
	}
	sort.Slice(apiPlan.Overrides, func(i, j int) bool {
		return apiPlan.Overrides[i].MRIID < apiPlan.Overrides[j].MRIID
	})
	for i, layer := range plan.Layers {
		commits := make([]string, len(layer.Commits))
		for j := range layer.Commits {
			commits[j] = layer.Commits[j].OID
		}
		apiPlan.Layers = append(apiPlan.Layers, api.PlanLayer{
			Position: first + i, MRIID: layer.MRIID, SourceBranch: layer.Branch,
			OldSHA: layer.HeadOID, BoundarySHA: layer.BoundaryOID, Commits: commits,
		})
	}
	apiPlan.PlanID = stableID("pln", apiPlan)
	if err := writeJSONAtomic(filepath.Join(h.stateRoot(rc.repo.Dir), "plans", apiPlan.PlanID+".json"), apiPlan); err != nil {
		return cli.Result{}, cli.Unavailable("journal_unavailable", "cannot persist restack plan", false)
	}
	env, _, err := h.envelope(inv.Name)
	if err != nil {
		return cli.Result{}, cli.Internal("cannot create response envelope", err)
	}
	env.Stack = &current
	env.Data["plan"] = apiPlan
	human := fmt.Sprintf("Restack plan %s is ready for snapshot %s (%d layers)",
		apiPlan.PlanID, apiPlan.SnapshotID, len(apiPlan.Layers))
	return result(env, human)
}

func (h *Handler) restackStart(ctx context.Context, inv cli.Invocation) (cli.Result, error) {
	var snapshotID string
	var savedPlan *api.Plan
	var rc repositoryContext
	if inv.PlanID != "" {
		base, _, err := h.loadRepositoryForState(ctx, inv.Globals.Remote)
		if err != nil {
			return cli.Result{}, err
		}
		rc = base
		var plan api.Plan
		if err := readJSON(filepath.Join(h.stateRoot(rc.repo.Dir), "plans", inv.PlanID+".json"), &plan); err != nil {
			return cli.Result{}, cli.Invalid("invalid_selector", "restack plan was not found")
		}
		if plan.PlanID != inv.PlanID || plan.State != "ready" || len(plan.Layers) == 0 {
			return cli.Result{}, cli.Invalid("invalid_selector", "restack plan is invalid or no longer ready")
		}
		snapshotID, savedPlan = plan.SnapshotID, &plan
	} else {
		snapshotID = inv.SnapshotID
	}
	loadedRC, captured, err := h.loadSnapshot(ctx, inv.Globals.Remote, snapshotID)
	if err != nil {
		return cli.Result{}, err
	}
	rc = loadedRC
	current, err := h.refreshSnapshot(ctx, captured.Stack, inv)
	if err != nil {
		return cli.Result{}, err
	}
	if current.SnapshotID != snapshotID {
		return cli.Result{}, cli.Invalid("invalid_selector",
			"snapshot is stale; restack did not start")
	}
	user, err := rc.client.CurrentUser(ctx)
	if err != nil {
		return cli.Result{}, classifyGlab("resolve current GitLab user", err)
	}
	for _, member := range current.Members {
		if member.Author.ID == user.ID.String() && member.Author.Username == user.Username {
			continue
		}
		env, factory, envelopeErr := h.envelope(inv.Name)
		if envelopeErr != nil {
			return cli.Result{}, cli.Internal("cannot create response envelope", envelopeErr)
		}
		env.Stack = &current
		scope := api.FindingScope{Kind: "member", MRIID: &member.IID, Position: &member.Position}
		finding, findingErr := factory.NewFinding("foreign_authored_member",
			api.DispositionHumanRequired, scope,
			fmt.Sprintf("MR !%d is not authored by the current GitLab user", member.IID))
		if findingErr != nil {
			return cli.Result{}, cli.Internal("cannot create authorship finding", findingErr)
		}
		env.Findings = append(env.Findings, finding)
		env.ApplyFindingDisposition()
		return result(env, finding.Summary)
	}
	first := 0
	if current.AffectedSuffix != nil {
		first = current.AffectedSuffix.FromPosition
	} else if savedPlan == nil {
		return cli.Result{}, cli.Invalid("invalid_selector",
			"the captured stack has no affected suffix to restack")
	}
	overrides := map[int]string{}
	if savedPlan != nil {
		if savedPlan.SnapshotID != snapshotID || savedPlan.Remote != current.Remote {
			return cli.Result{}, cli.Invalid("invalid_selector", "plan and snapshot do not match")
		}
		first = savedPlan.Layers[0].Position
		for _, override := range savedPlan.Overrides {
			overrides[override.MRIID] = override.BoundarySHA
		}
	}
	capturedRefs := map[string]string{}
	for _, member := range current.Members[first:] {
		if member.SourceSHA == nil {
			return cli.Result{}, cli.Invalid("invalid_selector", "affected source revision is unresolved")
		}
		capturedRefs[member.SourceBranch] = *member.SourceSHA
	}
	localWork, err := rc.repo.AffectedLocalWork(ctx, rc.remoteName, capturedRefs)
	if err != nil {
		return cli.Result{}, cli.Unavailable("git_transport_failed",
			"cannot inspect affected local worktrees", false)
	}
	if len(localWork) != 0 {
		return cli.Result{}, cli.Invalid("invalid_selector",
			"local work is present on an affected branch; restack did not start")
	}
	inputs, err := layerInputs(current, first, overrides)
	if err != nil {
		return cli.Result{}, cli.Invalid("invalid_selector", err.Error())
	}
	plan, err := (restack.Planner{Repo: rc.repo}).Build(ctx, replayBase(current, first), inputs, inv.AllowSignatureLoss)
	if err != nil {
		return cli.Result{}, restackPreflightError(err)
	}
	branches := sortedRefNames(capturedRefs)
	actual, err := rc.repo.RemoteRefs(ctx, rc.remoteName, branches)
	if err != nil || journal.ReconcileRefs(capturedRefs, capturedRefs, actual) != journal.RefsAllOld {
		return cli.Result{}, cli.Invalid("invalid_selector",
			"an affected remote ref changed after the snapshot; restack did not start")
	}
	j, err := h.openJournal(rc.repo.Dir)
	if err != nil {
		return cli.Result{}, cli.Unavailable("journal_unavailable", "cannot open restack journal", false)
	}
	defer j.Close()
	sessionID := stableID("ses", struct {
		Snapshot string
		Plan     string
		Time     string
		Sequence uint64
	}{snapshotID, inv.PlanID, h.now(), h.idSequence.Add(1)})
	now := h.now()
	var planID *string
	if inv.PlanID != "" {
		planID = &inv.PlanID
	}
	session := api.Session{
		SessionID: sessionID, State: "preparing", SnapshotID: snapshotID, PlanID: planID,
		CreatedAt: now, UpdatedAt: now, Remote: current.Remote,
		AffectedMemberIIDs:      memberIIDs(current.Members[first:]),
		Publication:             api.Publication{State: "not_started", Refs: publicationRefs(capturedRefs, nil, capturedRefs)},
		SignatureLossAuthorized: inv.AllowSignatureLoss, Resumable: true, Abortable: true,
	}
	durable := durableSession{API: session, Plan: plan, ProjectID: current.Project.ID}
	payload, err := json.Marshal(durable)
	if err != nil {
		return cli.Result{}, cli.Internal("cannot encode restack session", err)
	}
	projectKey := rc.fetch.Host + "/" + rc.fetch.Project
	if err := j.BeginSession(ctx, journal.Session{
		ID: sessionID, ProjectKey: projectKey, SnapshotID: snapshotID, State: "preparing",
		OldRefs: capturedRefs, NewRefs: map[string]string{}, Payload: payload,
	}); err != nil {
		if errors.Is(err, journal.ErrOperationInProgress) {
			return h.operationInProgress(ctx, inv.Name, current, j, projectKey)
		}
		return cli.Result{}, cli.Unavailable("journal_unavailable", "cannot begin restack session", false)
	}
	replayer := restack.Replayer{Repo: rc.repo}
	worktree, err := replayer.Prepare(ctx, filepath.Join(h.stateRoot(rc.repo.Dir), "worktrees"),
		sessionID, plan.BaseOID)
	if err != nil {
		_, _ = h.transitionSession(ctx, j, sessionID, 1, &durable, "invalidated")
		return cli.Result{}, cli.Unavailable("git_transport_failed",
			"cannot create managed restack worktree", false)
	}
	durable.API.Worktree = &api.SessionWorktree{Path: worktree, GitState: "clean"}
	stored, err := h.transitionSession(ctx, j, sessionID, 1, &durable, "replaying")
	if err != nil {
		return cli.Result{}, cli.Unavailable("journal_unavailable", "cannot persist replay state", false)
	}
	newHeads, replayErr := replayer.Replay(ctx, worktree, plan)
	if replayErr != nil {
		var stopped *restack.ReplayError
		if !errors.As(replayErr, &stopped) {
			_, _ = h.transitionSession(ctx, j, sessionID, stored.Revision, &durable, "invalidated")
			return cli.Result{}, cli.Unavailable("git_transport_failed", "managed replay failed", false)
		}
		if _, err := h.persistReplayStop(ctx, rc, j, stored, &durable, stopped); err != nil {
			return cli.Result{}, cli.Unavailable("journal_unavailable", "cannot persist replay stop", false)
		}
		return h.sessionResult(inv.Name, durable.API, api.DispositionHumanRequired,
			fmt.Sprintf("Restack session %s paused in %s", sessionID, stopped.Stop))
	}
	return h.prepareAndPublish(ctx, inv, rc, j, stored, &durable, newHeads)
}

func replayBase(s api.Stack, first int) string {
	if first > 0 && first <= len(s.Members) && s.Members[first-1].SourceSHA != nil {
		return *s.Members[first-1].SourceSHA
	}
	return s.Base.SHA
}

func (h *Handler) openJournal(repoDir string) (*journal.Journal, error) {
	return journal.Open(filepath.Join(h.stateRoot(repoDir), "journal.sqlite"), func() time.Time {
		now := h.Now
		if now == nil {
			return time.Now()
		}
		return now()
	})
}

func (h *Handler) loadRepositoryForState(ctx context.Context, remote string) (repositoryContext, string, error) {
	rc, err := h.repository(ctx, remote, false)
	if err != nil {
		return repositoryContext{}, "", err
	}
	return rc, h.stateRoot(rc.repo.Dir), nil
}

func (h *Handler) loadSnapshot(ctx context.Context, remote, id string) (repositoryContext, capturedSnapshot, error) {
	rc, root, err := h.loadRepositoryForState(ctx, remote)
	if err != nil {
		return repositoryContext{}, capturedSnapshot{}, err
	}
	var captured capturedSnapshot
	if id == "" || readJSON(filepath.Join(root, "snapshots", id+".json"), &captured) != nil ||
		captured.Stack.SnapshotID != id {
		return repositoryContext{}, capturedSnapshot{}, cli.Invalid("invalid_selector", "snapshot was not found")
	}
	if captured.Stack.Remote.Name != rc.remoteName ||
		captured.Stack.Remote.Fetch.Host != rc.fetch.Host ||
		captured.Stack.Remote.Fetch.Project != rc.fetch.Project {
		return repositoryContext{}, capturedSnapshot{}, cli.Invalid("invalid_selector",
			"snapshot belongs to a different mutation remote")
	}
	return rc, captured, nil
}

func (h *Handler) refreshSnapshot(ctx context.Context, captured api.Stack, original cli.Invocation) (api.Stack, error) {
	inv := cli.Invocation{Name: cli.CommandCheck}
	inv.Globals.GitLabMode = captured.GitLabMode
	if captured.Remote.Selection == "explicit" {
		inv.Globals.Remote = captured.Remote.Name
	}
	switch captured.Selector.Kind {
	case "mr":
		inv.Selector.Value = "!" + captured.Selector.Value
	case "branch":
		inv.Selector.Value = captured.Selector.Value
	case "current_branch":
		// Keep selection anchored to the captured branch, not a possibly changed
		// current checkout.
		inv.Selector.Value = captured.Selector.Value
	default:
		return api.Stack{}, cli.Invalid("invalid_selector", "snapshot selector is invalid")
	}
	res, err := h.check(ctx, inv, false)
	if err != nil {
		return api.Stack{}, err
	}
	env, ok := res.Machine.(api.Envelope)
	if !ok || env.Stack == nil {
		return api.Stack{}, cli.Internal("check returned no stack during restack preflight", nil)
	}
	return *env.Stack, nil
}

func layerInputs(s api.Stack, first int, overrides map[int]string) ([]restack.LayerInput, error) {
	if first < 0 || first >= len(s.Members) {
		return nil, errors.New("affected layer range is invalid")
	}
	inputs := make([]restack.LayerInput, 0, len(s.Members)-first)
	for _, member := range s.Members[first:] {
		if member.SourceSHA == nil {
			return nil, fmt.Errorf("MR !%d source revision is unresolved", member.IID)
		}
		boundary := ""
		if member.Layer.BoundarySHA != nil {
			boundary = *member.Layer.BoundarySHA
		}
		if override, ok := overrides[member.IID]; ok {
			boundary = override
		}
		if !fullOID(boundary) {
			return nil, fmt.Errorf("MR !%d has no exact layer boundary", member.IID)
		}
		inputs = append(inputs, restack.LayerInput{
			MRIID: member.IID, Branch: member.SourceBranch,
			BoundaryOID: boundary, HeadOID: *member.SourceSHA,
		})
	}
	return inputs, nil
}

func restackPreflightError(err error) error {
	switch {
	case errors.Is(err, restack.ErrSignedCommit):
		return cli.Invalid("invalid_selector",
			"signed commits require explicit --allow-signature-loss")
	case errors.Is(err, restack.ErrAmbiguousBoundary), errors.Is(err, restack.ErrEmptyLayer),
		errors.Is(err, restack.ErrMergeCommit):
		return cli.Invalid("invalid_selector", err.Error())
	default:
		return cli.Unavailable("git_transport_failed", "restack preflight could not inspect layer commits", false)
	}
}

func (h *Handler) persistReplayStop(ctx context.Context, rc repositoryContext, j *journal.Journal,
	stored journal.Session, durable *durableSession, stopped *restack.ReplayError) (journal.Session, error) {
	if stopped.Layer < 0 || stopped.Layer >= len(durable.Plan.Layers) ||
		stopped.CommitIndex < 0 || stopped.CommitIndex >= len(durable.Plan.Layers[stopped.Layer].Commits) {
		return journal.Session{}, errors.New("invalid replay stop cursor")
	}
	layer := durable.Plan.Layers[stopped.Layer]
	onto := stopped.OntoOID
	if !fullOID(onto) {
		onto = durable.Plan.BaseOID
	}
	if !fullOID(stopped.OntoOID) {
		if head, headErr := rc.repo.Runner.Run(ctx, durable.API.Worktree.Path, "git",
			"rev-parse", "--verify", "HEAD^{commit}"); headErr == nil {
			candidate := strings.TrimSpace(string(head.Stdout))
			if fullOID(candidate) {
				onto = candidate
			}
		}
	}
	conflictedPaths := []string{}
	if paths, pathsErr := rc.repo.Runner.Run(ctx, durable.API.Worktree.Path, "git",
		"diff", "--name-only", "--diff-filter=U"); pathsErr == nil {
		for _, path := range strings.Split(strings.TrimSpace(string(paths.Stdout)), "\n") {
			if path != "" {
				conflictedPaths = append(conflictedPaths, path)
			}
		}
	}
	durable.ReplayLayer = stopped.Layer
	durable.ReplayCommit = stopped.CommitIndex
	durable.CompletedHeads = append([]string(nil), stopped.CompletedHeads...)
	durable.API.CurrentLayer = &api.CurrentLayer{
		MRIID: layer.MRIID, OriginalCommitSHA: stopped.Commit,
		OntoSHA: onto, ConflictedPaths: conflictedPaths,
	}
	durable.API.Worktree.GitState = string(stopped.Stop)
	return h.transitionSession(ctx, j, stored.ID, stored.Revision, durable, string(stopped.Stop))
}

func (h *Handler) prepareAndPublish(ctx context.Context, inv cli.Invocation, rc repositoryContext,
	j *journal.Journal, stored journal.Session, durable *durableSession,
	newHeads []string) (cli.Result, error) {
	oldRefs, newRefs, err := durable.Plan.RefMaps(newHeads)
	if err != nil || journal.ReconcileRefs(stored.OldRefs, stored.OldRefs, oldRefs) != journal.RefsAllOld {
		return cli.Result{}, cli.Internal("replay produced an invalid publication map", err)
	}
	durable.API.CurrentLayer = nil
	durable.CompletedHeads = append([]string(nil), newHeads...)
	durable.API.Worktree.GitState = "clean"
	durable.API.Publication = api.Publication{
		State: "all_old", Refs: publicationRefs(oldRefs, newRefs, oldRefs),
	}
	_, captured, err := h.loadSnapshot(ctx, inv.Globals.Remote, stored.SnapshotID)
	if err != nil {
		return cli.Result{}, err
	}
	if captured.Stack.GitLabMode == "legacy" && len(captured.Stack.Members) > 0 &&
		captured.Stack.Members[0].TargetResolution == "integrated_predecessor" {
		front := captured.Stack.Members[0]
		expectedSource := newRefs[front.SourceBranch]
		if !fullOID(expectedSource) {
			return cli.Result{}, cli.Internal("legacy target update lacks proposed source revision", nil)
		}
		durable.API.TargetUpdate = &api.TargetUpdate{
			MRIID: front.IID, FromTarget: front.TargetBranch,
			ToTarget: captured.Stack.Project.DefaultBranch, ExpectedSourceSHA: expectedSource,
			ExpectedMRState: "opened", Status: "pending",
		}
	}
	durable.API.State = "publication_ready"
	durable.API.UpdatedAt = h.now()
	payload, err := json.Marshal(durable)
	if err != nil {
		return cli.Result{}, cli.Internal("cannot encode publication state", err)
	}
	stored, err = j.PreparePublication(ctx, stored.ID, stored.Revision, newRefs, payload)
	if err != nil {
		return cli.Result{}, cli.Unavailable("journal_unavailable",
			"cannot durably prepare atomic publication", false)
	}
	revalidated, err := h.refreshSnapshot(ctx, captured.Stack, inv)
	if err != nil {
		return cli.Result{}, err
	}
	if revalidated.SnapshotID != stored.SnapshotID {
		_, _ = h.transitionSession(ctx, j, stored.ID, stored.Revision, durable, "invalidated")
		return cli.Result{}, cli.Invalid("invalid_selector",
			"snapshot changed during replay; session was invalidated without publishing refs")
	}
	return h.publishSession(ctx, inv.Name, rc, j, stored, durable)
}

func (h *Handler) operationInProgress(ctx context.Context, command cli.CommandName, stack api.Stack,
	j *journal.Journal, projectKey string) (cli.Result, error) {
	stored, err := j.ActiveSession(ctx, projectKey)
	if err != nil {
		return cli.Result{}, cli.Unavailable("journal_unavailable",
			"another restack session is active but cannot be read", true)
	}
	var durable durableSession
	if err := json.Unmarshal(stored.Payload, &durable); err != nil {
		return cli.Result{}, cli.Internal("active restack session payload is invalid", err)
	}
	env, factory, err := h.envelope(command)
	if err != nil {
		return cli.Result{}, cli.Internal("cannot create response envelope", err)
	}
	env.Stack = &stack
	env.Session = &durable.API
	finding, err := factory.NewFinding("operation_in_progress", api.DispositionWaiting,
		api.FindingScope{Kind: "session"}, "another restack session is active for this project")
	if err != nil {
		return cli.Result{}, cli.Internal("cannot create operation-in-progress finding", err)
	}
	env.Findings = append(env.Findings, finding)
	env.ApplyFindingDisposition()
	return result(env, finding.Summary)
}

func sortedRefNames(refs map[string]string) []string {
	names := make([]string, 0, len(refs))
	for name := range refs {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

func publicationRefs(oldRefs, newRefs, currentRefs map[string]string) []api.PublicationRef {
	refs := make([]api.PublicationRef, 0, len(oldRefs))
	for _, branch := range sortedRefNames(oldRefs) {
		oldOID := oldRefs[branch]
		ref := api.PublicationRef{Branch: branch, OldSHA: oldOID, Classification: "unexpected"}
		if newOID := newRefs[branch]; fullOID(newOID) {
			value := newOID
			ref.NewSHA = &value
		}
		if currentOID := currentRefs[branch]; fullOID(currentOID) {
			value := currentOID
			ref.CurrentSHA = &value
			switch {
			case currentOID == oldOID:
				ref.Classification = "old"
			case ref.NewSHA != nil && currentOID == *ref.NewSHA:
				ref.Classification = "new"
			}
		}
		refs = append(refs, ref)
	}
	return refs
}

func (h *Handler) transitionSession(ctx context.Context, j *journal.Journal, id string,
	revision int64, durable *durableSession, state string) (journal.Session, error) {
	durable.API.State = state
	durable.API.UpdatedAt = h.now()
	switch state {
	case "preparing", "replaying", "rebase_conflict", "empty_commit":
		durable.API.Publication.State = "not_started"
		durable.API.Resumable, durable.API.Abortable = true, true
	case "publication_ready":
		durable.API.Publication.State = "all_old"
		durable.API.Resumable, durable.API.Abortable = true, true
	case "publication_pending_reconcile":
		durable.API.Publication.State = "in_flight_unknown"
		durable.API.Resumable, durable.API.Abortable = true, false
	case "retarget_pending":
		durable.API.Publication.State = "all_new"
		durable.API.Resumable, durable.API.Abortable = true, false
	case "completed":
		durable.API.Publication.State = "all_new"
		durable.API.Resumable, durable.API.Abortable = false, false
	case "indeterminate_publication":
		durable.API.Publication.State = "indeterminate"
		durable.API.Resumable, durable.API.Abortable = true, false
	case "aborted":
		durable.API.Resumable, durable.API.Abortable = false, false
	case "abandoned":
		durable.API.Publication.State = "indeterminate"
		durable.API.Resumable, durable.API.Abortable = false, false
	case "invalidated":
		durable.API.Resumable = false
	}
	payload, err := json.Marshal(durable)
	if err != nil {
		return journal.Session{}, err
	}
	return j.Transition(ctx, id, revision, state, payload)
}

func (h *Handler) restackAbandon(ctx context.Context, inv cli.Invocation) (cli.Result, error) {
	rc, err := h.repository(ctx, inv.Globals.Remote, false)
	if err != nil {
		return cli.Result{}, err
	}
	j, err := h.openJournal(rc.repo.Dir)
	if err != nil {
		return cli.Result{}, cli.Unavailable("journal_unavailable", "cannot open restack journal", false)
	}
	defer j.Close()
	stored, err := j.Session(ctx, inv.SessionID)
	if errors.Is(err, journal.ErrNotFound) {
		return cli.Result{}, cli.Invalid("invalid_selector", "restack session was not found")
	}
	if err != nil {
		return cli.Result{}, cli.Unavailable("journal_unavailable", "cannot read restack session", false)
	}
	if stored.ProjectKey != rc.fetch.Host+"/"+rc.fetch.Project ||
		stored.State != "indeterminate_publication" {
		return cli.Result{}, cli.Invalid("invalid_selector",
			"only an indeterminate session for this project can be abandoned")
	}
	var durable durableSession
	if err := json.Unmarshal(stored.Payload, &durable); err != nil ||
		durable.API.SessionID != stored.ID {
		return cli.Result{}, cli.Internal("restack journal payload is invalid", err)
	}
	if durable.API.Worktree != nil {
		if _, err := rc.repo.Git(ctx, "worktree", "remove", "--force",
			durable.API.Worktree.Path); err != nil {
			return cli.Result{}, cli.Unavailable("git_transport_failed",
				"cannot remove abandoned managed worktree", false)
		}
		durable.API.Worktree = nil
	}
	if _, err := h.transitionSession(ctx, j, stored.ID, stored.Revision,
		&durable, "abandoned"); err != nil {
		return cli.Result{}, cli.Unavailable("journal_unavailable",
			"cannot archive abandoned restack session", false)
	}
	// The public contract deliberately has no authoritative machine form for
	// abandonment. parseAbandon rejects --no-input, so only this human result
	// can acknowledge acceptance of the current remote state.
	return cli.Result{Human: fmt.Sprintf(
		"Restack session %s abandoned; current remote refs were accepted without modification",
		stored.ID)}, nil
}

func (h *Handler) sessionResult(command cli.CommandName, session api.Session,
	disposition api.Disposition, human string) (cli.Result, error) {
	env, _, err := h.envelope(command)
	if err != nil {
		return cli.Result{}, cli.Internal("cannot create response envelope", err)
	}
	env.Session = &session
	env.Disposition = &disposition
	return result(env, human)
}

func (h *Handler) publishSession(ctx context.Context, command cli.CommandName, rc repositoryContext,
	j *journal.Journal, stored journal.Session, durable *durableSession) (cli.Result, error) {
	if stored.State != "publication_ready" {
		return cli.Result{}, cli.Invalid("invalid_selector", "session is not ready for publication")
	}
	// Persist the uncertainty boundary before sending the only remote mutation.
	pending, err := h.transitionSession(ctx, j, stored.ID, stored.Revision, durable,
		"publication_pending_reconcile")
	if err != nil {
		return cli.Result{}, cli.Unavailable("journal_unavailable",
			"cannot persist publication reconciliation state", false)
	}
	updates := make([]gitexec.RefUpdate, 0, len(stored.OldRefs))
	for _, branch := range sortedRefNames(stored.OldRefs) {
		updates = append(updates, gitexec.RefUpdate{
			Branch: branch, OldOID: stored.OldRefs[branch], NewOID: stored.NewRefs[branch],
		})
	}
	pushErr := rc.repo.AtomicPush(ctx, rc.remoteName, updates)
	actual, readErr := rc.repo.RemoteRefs(ctx, rc.remoteName, sortedRefNames(stored.OldRefs))
	if readErr != nil {
		return cli.Result{}, cli.Unavailable("git_transport_failed",
			"atomic publication was sent but remote refs could not be reconciled", true)
	}
	durable.API.Publication.Refs = publicationRefs(stored.OldRefs, stored.NewRefs, actual)
	switch journal.ReconcileRefs(stored.OldRefs, stored.NewRefs, actual) {
	case journal.RefsAllNew:
		if durable.API.TargetUpdate != nil {
			return h.finishTargetUpdate(ctx, command, rc, j, pending, durable)
		}
		return h.completePublishedSession(ctx, command, rc, j, pending, durable)
	case journal.RefsAllOld:
		_, transitionErr := h.transitionSession(ctx, j, stored.ID, pending.Revision,
			durable, "publication_ready")
		if transitionErr != nil {
			return cli.Result{}, cli.Unavailable("journal_unavailable",
				"atomic publication failed and retry state could not be recorded", false)
		}
		if pushErr != nil && strings.Contains(strings.ToLower(pushErr.Error()), "atomic") {
			return cli.Result{}, cli.Unavailable("prerequisite_unsupported",
				"remote rejected atomic publication; no refs changed", false)
		}
		return cli.Result{}, cli.Unavailable("git_transport_failed",
			"atomic publication failed; all refs remain at their captured revisions", true)
	default:
		_, transitionErr := h.transitionSession(ctx, j, stored.ID, pending.Revision,
			durable, "indeterminate_publication")
		if transitionErr != nil {
			return cli.Result{}, cli.Unavailable("journal_unavailable",
				"indeterminate publication could not be recorded", false)
		}
		return h.sessionResult(command, durable.API, api.DispositionHumanRequired,
			fmt.Sprintf("Restack session %s has indeterminate remote publication", stored.ID))
	}
}

func (h *Handler) finishTargetUpdate(ctx context.Context, command cli.CommandName,
	rc repositoryContext, j *journal.Journal, stored journal.Session,
	durable *durableSession) (cli.Result, error) {
	update := durable.API.TargetUpdate
	if update == nil || update.Status != "pending" {
		return cli.Result{}, cli.Internal("legacy retarget state is invalid", nil)
	}
	if stored.State == "publication_pending_reconcile" || stored.State == "indeterminate_publication" {
		update.AttemptCount++
		now := h.now()
		update.LastAttemptAt = &now
		var err error
		stored, err = h.transitionSession(ctx, j, stored.ID, stored.Revision, durable, "retarget_pending")
		if err != nil {
			return cli.Result{}, cli.Unavailable("journal_unavailable",
				"cannot persist pending legacy target update", false)
		}
	}
	mr, err := rc.client.MergeRequest(ctx, durable.ProjectID, update.MRIID)
	if err != nil {
		return h.sessionResult(command, durable.API, api.DispositionActionRequired,
			fmt.Sprintf("Restack session %s published refs; legacy target update awaits retry", stored.ID))
	}
	if mr.State != update.ExpectedMRState ||
		strings.ToLower(mr.SHA) != update.ExpectedSourceSHA ||
		mr.TargetBranch != update.FromTarget {
		return h.sessionResult(command, durable.API, api.DispositionWaiting,
			fmt.Sprintf("Restack session %s is waiting for GitLab to expose the published source revision", stored.ID))
	}
	if err := rc.client.UpdateTarget(ctx, durable.ProjectID, update.MRIID, update.ToTarget); err != nil {
		return h.sessionResult(command, durable.API, api.DispositionActionRequired,
			fmt.Sprintf("Restack session %s published refs; legacy target update failed and is retryable", stored.ID))
	}
	update.Status = "applied"
	return h.completePublishedSession(ctx, command, rc, j, stored, durable)
}

func (h *Handler) completePublishedSession(ctx context.Context, command cli.CommandName,
	rc repositoryContext, j *journal.Journal, stored journal.Session,
	durable *durableSession) (cli.Result, error) {
	if durable.API.Worktree != nil {
		if removeErr := (restack.Replayer{Repo: rc.repo}).Remove(ctx, durable.API.Worktree.Path); removeErr == nil {
			durable.API.Worktree = nil
		}
	}
	updates := make([]gitexec.RefUpdate, 0, len(stored.OldRefs))
	for _, branch := range sortedRefNames(stored.OldRefs) {
		updates = append(updates, gitexec.RefUpdate{
			Branch: branch, OldOID: stored.OldRefs[branch], NewOID: stored.NewRefs[branch],
		})
	}
	localResults, err := rc.repo.UpdateSafeLocalRefs(ctx, updates)
	if err != nil {
		return cli.Result{}, cli.Unavailable("git_transport_failed",
			"remote publication succeeded but local refs could not be inspected", false)
	}
	_, err = h.transitionSession(ctx, j, stored.ID, stored.Revision, durable, "completed")
	if err != nil {
		return cli.Result{}, cli.Unavailable("journal_unavailable",
			"refs published but completion could not be recorded", false)
	}
	var stale []string
	for _, local := range localResults {
		if local.State != "updated" && local.State != "absent" {
			stale = append(stale, local.Branch)
		}
	}
	if len(stale) == 0 {
		return h.sessionResult(command, durable.API, api.DispositionComplete,
			fmt.Sprintf("Restack session %s completed", stored.ID))
	}
	env, factory, envelopeErr := h.envelope(command)
	if envelopeErr != nil {
		return cli.Result{}, cli.Internal("cannot create response envelope", envelopeErr)
	}
	env.Session = &durable.API
	finding, findingErr := factory.NewFinding("local_checkout_stale",
		api.DispositionActionRequired, api.FindingScope{Kind: "repository"},
		"remote publication succeeded; checked-out or locally moved branches remain on old history")
	if findingErr != nil {
		return cli.Result{}, cli.Internal("cannot create local checkout finding", findingErr)
	}
	finding.Details["branches"] = stale
	env.Findings = append(env.Findings, finding)
	env.ApplyFindingDisposition()
	return result(env, finding.Summary)
}

func (h *Handler) restackSession(ctx context.Context, inv cli.Invocation) (cli.Result, error) {
	rc, err := h.repository(ctx, inv.Globals.Remote, false)
	if err != nil {
		return cli.Result{}, err
	}
	j, err := h.openJournal(rc.repo.Dir)
	if err != nil {
		return cli.Result{}, cli.Unavailable("journal_unavailable", "cannot open restack journal", false)
	}
	defer j.Close()
	stored, err := j.Session(ctx, inv.SessionID)
	if errors.Is(err, journal.ErrNotFound) {
		return cli.Result{}, cli.Invalid("invalid_selector", "restack session was not found")
	}
	if err != nil {
		return cli.Result{}, cli.Unavailable("journal_unavailable", "cannot read restack session", false)
	}
	if stored.ProjectKey != rc.fetch.Host+"/"+rc.fetch.Project {
		return cli.Result{}, cli.Invalid("invalid_selector",
			"restack session belongs to a different mutation remote")
	}
	var durable durableSession
	if err := json.Unmarshal(stored.Payload, &durable); err != nil ||
		durable.API.SessionID != stored.ID || durable.API.SnapshotID != stored.SnapshotID {
		return cli.Result{}, cli.Internal("restack journal payload is invalid", err)
	}
	switch inv.Name {
	case cli.CommandRestackAbort:
		return h.abortSession(ctx, rc, j, stored, &durable)
	case cli.CommandRestackRecover:
		return h.recoverSession(ctx, inv.Name, rc, j, stored, &durable)
	case cli.CommandRestackContinue:
		switch stored.State {
		case "rebase_conflict", "empty_commit":
			return h.resumeReplay(ctx, inv, rc, j, stored, &durable)
		case "publication_ready":
			// A retry is safe only while every remote ref is still all-old.
			actual, readErr := rc.repo.RemoteRefs(ctx, rc.remoteName, sortedRefNames(stored.OldRefs))
			if readErr != nil {
				return cli.Result{}, cli.Unavailable("git_transport_failed",
					"cannot revalidate remote refs before publication retry", true)
			}
			if journal.ReconcileRefs(stored.OldRefs, stored.NewRefs, actual) != journal.RefsAllOld {
				return h.recoverSession(ctx, inv.Name, rc, j, stored, &durable)
			}
			_, captured, loadErr := h.loadSnapshot(ctx, inv.Globals.Remote, stored.SnapshotID)
			if loadErr != nil {
				return cli.Result{}, loadErr
			}
			current, refreshErr := h.refreshSnapshot(ctx, captured.Stack, inv)
			if refreshErr != nil {
				return cli.Result{}, refreshErr
			}
			if current.SnapshotID != stored.SnapshotID {
				_, _ = h.transitionSession(ctx, j, stored.ID, stored.Revision, &durable, "invalidated")
				return cli.Result{}, cli.Invalid("invalid_selector",
					"snapshot changed before publication retry; session was invalidated")
			}
			return h.publishSession(ctx, inv.Name, rc, j, stored, &durable)
		case "publication_pending_reconcile", "indeterminate_publication":
			return h.recoverSession(ctx, inv.Name, rc, j, stored, &durable)
		case "retarget_pending":
			return h.finishTargetUpdate(ctx, inv.Name, rc, j, stored, &durable)
		default:
			return cli.Result{}, cli.Invalid("invalid_selector",
				fmt.Sprintf("restack continue is not safe from session state %s", stored.State))
		}
	default:
		return cli.Result{}, cli.Invalid("invalid_selector", "unsupported session command")
	}
}

func (h *Handler) resumeReplay(ctx context.Context, inv cli.Invocation, rc repositoryContext,
	j *journal.Journal, stored journal.Session, durable *durableSession) (cli.Result, error) {
	if durable.API.Worktree == nil || durable.API.CurrentLayer == nil {
		return cli.Result{}, cli.Internal("paused replay session lacks managed worktree state", nil)
	}
	var resolution restack.Resolution
	switch stored.State {
	case "rebase_conflict":
		if inv.DropCurrent || inv.KeepEmpty {
			return cli.Result{}, cli.Invalid("invalid_arguments",
				"conflict continuation does not accept empty-commit choices")
		}
		paths, err := rc.repo.Runner.Run(ctx, durable.API.Worktree.Path, "git",
			"diff", "--name-only", "--diff-filter=U")
		if err != nil {
			return cli.Result{}, cli.Unavailable("git_transport_failed",
				"cannot inspect managed conflict state", false)
		}
		if strings.TrimSpace(string(paths.Stdout)) != "" {
			return cli.Result{}, cli.Invalid("invalid_selector",
				"conflicts remain unresolved in the managed worktree")
		}
		_, err = rc.repo.Runner.Run(ctx, durable.API.Worktree.Path, "git",
			"diff", "--cached", "--quiet")
		var commandErr *gitexec.CommandError
		if err == nil {
			return cli.Result{}, cli.Invalid("invalid_selector",
				"conflict resolution must be explicitly staged before continue")
		}
		if !errors.As(err, &commandErr) || commandErr.ExitCode != 1 {
			return cli.Result{}, cli.Unavailable("git_transport_failed",
				"cannot inspect staged conflict resolution", false)
		}
		resolution = restack.ResolutionContinue
	case "empty_commit":
		if inv.DropCurrent == inv.KeepEmpty {
			return cli.Result{}, cli.Invalid("invalid_arguments",
				"empty commit continuation requires exactly one of --drop-current or --keep-empty")
		}
		if inv.DropCurrent {
			resolution = restack.ResolutionDrop
		} else {
			resolution = restack.ResolutionKeep
		}
	default:
		return cli.Result{}, cli.Invalid("invalid_selector", "session is not paused during replay")
	}
	durable.API.Worktree.GitState = "replaying"
	replaying, err := h.transitionSession(ctx, j, stored.ID, stored.Revision, durable, "replaying")
	if err != nil {
		return cli.Result{}, cli.Unavailable("journal_unavailable", "cannot persist replay continuation", false)
	}
	newHeads, resumeErr := (restack.Replayer{Repo: rc.repo}).Resume(ctx,
		durable.API.Worktree.Path, durable.Plan, durable.ReplayLayer, durable.ReplayCommit,
		durable.CompletedHeads, resolution)
	if resumeErr != nil {
		var stopped *restack.ReplayError
		if errors.As(resumeErr, &stopped) {
			if _, err := h.persistReplayStop(ctx, rc, j, replaying, durable, stopped); err != nil {
				return cli.Result{}, cli.Unavailable("journal_unavailable",
					"cannot persist repeated replay stop", false)
			}
			return h.sessionResult(inv.Name, durable.API, api.DispositionHumanRequired,
				fmt.Sprintf("Restack session %s paused again in %s", stored.ID, stopped.Stop))
		}
		_, _ = h.transitionSession(ctx, j, stored.ID, replaying.Revision, durable, "invalidated")
		return cli.Result{}, cli.Unavailable("git_transport_failed",
			"managed replay could not resume safely", false)
	}
	return h.prepareAndPublish(ctx, inv, rc, j, replaying, durable, newHeads)
}

func (h *Handler) abortSession(ctx context.Context, rc repositoryContext, j *journal.Journal,
	stored journal.Session, durable *durableSession) (cli.Result, error) {
	switch stored.State {
	case "preparing", "replaying", "rebase_conflict", "empty_commit", "publication_ready", "invalidated":
	default:
		return cli.Result{}, cli.Invalid("invalid_selector",
			"session cannot be aborted after remote publication may have occurred")
	}
	if len(stored.OldRefs) > 0 {
		actual, err := rc.repo.RemoteRefs(ctx, rc.remoteName, sortedRefNames(stored.OldRefs))
		if err != nil || journal.ReconcileRefs(stored.OldRefs, stored.OldRefs, actual) != journal.RefsAllOld {
			return cli.Result{}, cli.Invalid("invalid_selector",
				"remote refs no longer match the session's old map; abort refused")
		}
	}
	if durable.API.Worktree != nil {
		if _, err := rc.repo.Git(ctx, "worktree", "remove", "--force", durable.API.Worktree.Path); err != nil {
			return cli.Result{}, cli.Unavailable("git_transport_failed",
				"cannot remove managed restack worktree", false)
		}
		durable.API.Worktree = nil
	}
	if durable.API.Publication.State != "all_old" {
		durable.API.Publication.State = "not_started"
	}
	_, err := h.transitionSession(ctx, j, stored.ID, stored.Revision, durable, "aborted")
	if err != nil {
		return cli.Result{}, cli.Unavailable("journal_unavailable", "cannot record session abort", false)
	}
	return h.sessionResult(cli.CommandRestackAbort, durable.API, api.DispositionComplete,
		fmt.Sprintf("Restack session %s aborted without changing remote refs", stored.ID))
}

func (h *Handler) recoverSession(ctx context.Context, command cli.CommandName, rc repositoryContext,
	j *journal.Journal, stored journal.Session, durable *durableSession) (cli.Result, error) {
	switch stored.State {
	case "publication_pending_reconcile", "indeterminate_publication", "publication_ready":
	default:
		return cli.Result{}, cli.Invalid("invalid_selector",
			"session is not awaiting publication reconciliation")
	}
	actual, err := rc.repo.RemoteRefs(ctx, rc.remoteName, sortedRefNames(stored.OldRefs))
	if err != nil {
		return cli.Result{}, cli.Unavailable("git_transport_failed",
			"cannot read remote refs for recovery", true)
	}
	durable.API.Publication.Refs = publicationRefs(stored.OldRefs, stored.NewRefs, actual)
	classification := journal.ReconcileRefs(stored.OldRefs, stored.NewRefs, actual)
	if stored.State == "publication_ready" && classification == journal.RefsAllOld {
		return h.sessionResult(command, durable.API, api.DispositionActionRequired,
			fmt.Sprintf("Restack session %s is safely retryable or abortable", stored.ID))
	}
	switch classification {
	case journal.RefsAllOld:
		_, err = h.transitionSession(ctx, j, stored.ID, stored.Revision, durable, "publication_ready")
		if err == nil {
			return h.sessionResult(command, durable.API, api.DispositionActionRequired,
				fmt.Sprintf("Restack session %s reconciled all refs as old", stored.ID))
		}
	case journal.RefsAllNew:
		if stored.State == "publication_ready" {
			stored, err = h.transitionSession(ctx, j, stored.ID, stored.Revision, durable,
				"publication_pending_reconcile")
		}
		if err == nil {
			if durable.API.TargetUpdate != nil {
				return h.finishTargetUpdate(ctx, command, rc, j, stored, durable)
			}
			return h.completePublishedSession(ctx, command, rc, j, stored, durable)
		}
	default:
		if stored.State != "indeterminate_publication" {
			_, err = h.transitionSession(ctx, j, stored.ID, stored.Revision, durable,
				"indeterminate_publication")
		}
		if err == nil {
			return h.sessionResult(command, durable.API, api.DispositionHumanRequired,
				fmt.Sprintf("Restack session %s remains indeterminate", stored.ID))
		}
	}
	return cli.Result{}, cli.Unavailable("journal_unavailable",
		"cannot persist recovered session state", false)
}
