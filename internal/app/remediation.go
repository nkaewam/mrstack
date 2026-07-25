package app

import (
	"errors"
	"fmt"
	"regexp"
	"strconv"

	"github.com/nkaewam/mrstack/internal/api"
	"github.com/nkaewam/mrstack/internal/cli"
	"github.com/nkaewam/mrstack/internal/restack"
)

type evidenceInput struct {
	kind   string
	fields map[string]any
}

var preflightIdentity = regexp.MustCompile(`!([1-9][0-9]*)(?: commit ([0-9a-fA-F]{40}|[0-9a-fA-F]{64}))?`)

func (h *Handler) preflightBlockerResult(command cli.CommandName, stack api.Stack,
	err error) (cli.Result, error) {
	code, summary := "", err.Error()
	switch {
	case errors.Is(err, restack.ErrSignedCommit):
		code = "signed_commits"
	case errors.Is(err, restack.ErrMergeCommit):
		code = "merge_commit_in_layer"
	case errors.Is(err, restack.ErrEmptyLayer):
		code = "empty_layer"
	case errors.Is(err, restack.ErrAmbiguousBoundary):
		code = "ambiguous_layer_boundary"
	default:
		return cli.Result{}, cli.Unavailable("git_transport_failed",
			"restack preflight could not inspect layer commits", false)
	}
	scope := api.FindingScope{Kind: "stack"}
	var evidence []evidenceInput
	if match := preflightIdentity.FindStringSubmatch(summary); match != nil {
		iid, _ := strconv.Atoi(match[1])
		scope = api.FindingScope{Kind: "member", MRIID: &iid}
		for _, member := range stack.Members {
			if member.IID != iid {
				continue
			}
			scope.Position = &member.Position
			if match[2] != "" {
				evidence = append(evidence, evidenceInput{"git_commit", map[string]any{
					"member_iid": iid, "commit_sha": match[2],
				}})
			} else if member.SourceSHA != nil && member.Layer.BoundarySHA != nil {
				evidence = append(evidence, evidenceInput{"git_ancestry", map[string]any{
					"member_iid": iid, "source_sha": *member.SourceSHA,
					"expected_ancestor_sha": *member.Layer.BoundarySHA,
				}})
			}
			break
		}
	}
	if code == "signed_commits" {
		return h.signatureLossResult(command, stack, scope, summary, evidence...)
	}
	return h.humanHandoffResult(command, stack, code, scope, summary, evidence...)
}

func (h *Handler) signatureLossResult(command cli.CommandName, stack api.Stack,
	scope api.FindingScope, summary string, inputs ...evidenceInput) (cli.Result, error) {
	env, factory, err := h.envelope(command)
	if err != nil {
		return cli.Result{}, cli.Internal("cannot create signature-loss envelope", err)
	}
	env.Stack = &stack
	if command == cli.CommandRestackPlan {
		env.Data["plan"] = nil
	}
	finding, err := factory.NewFinding(
		"signed_commits", api.DispositionHumanRequired, scope, summary)
	if err != nil {
		return cli.Result{}, cli.Internal("cannot create signature-loss finding", err)
	}
	for _, input := range inputs {
		evidence, evidenceErr := factory.NewEvidence(input.kind, input.fields)
		if evidenceErr != nil {
			return cli.Result{}, cli.Internal("cannot create signature-loss evidence", evidenceErr)
		}
		env.Evidence = append(env.Evidence, evidence)
		finding.EvidenceRefs = append(finding.EvidenceRefs, evidence.EvidenceID)
	}
	work := api.RequiredWork{
		Kind: "obtain_human_decision", ReasonCode: "signed_commits",
	}
	remediation, err := factory.NewRemediation(api.Remediation{
		FindingID: finding.FindingID, Kind: "authorize_signature_loss",
		SnapshotID: &stack.SnapshotID, RequiredWork: &work,
		EvidenceRefs: append([]string(nil), finding.EvidenceRefs...), Actions: []api.Action{},
	})
	if err != nil {
		return cli.Result{}, cli.Internal("cannot create signature-loss remediation", err)
	}
	env.Findings = append(env.Findings, finding)
	env.Remediations = append(env.Remediations, remediation)
	env.ApplyFindingDisposition()
	return result(env, summary)
}

func (h *Handler) humanHandoffResult(command cli.CommandName, stack api.Stack, code string,
	scope api.FindingScope, summary string, inputs ...evidenceInput) (cli.Result, error) {
	env, factory, err := h.envelope(command)
	if err != nil {
		return cli.Result{}, cli.Internal("cannot create human-handoff envelope", err)
	}
	env.Stack = &stack
	if command == cli.CommandRestackPlan {
		env.Data["plan"] = nil
	}
	finding, err := factory.NewFinding(code, api.DispositionHumanRequired, scope, summary)
	if err != nil {
		return cli.Result{}, cli.Internal("cannot create human-handoff finding", err)
	}
	for _, input := range inputs {
		evidence, evidenceErr := factory.NewEvidence(input.kind, input.fields)
		if evidenceErr != nil {
			return cli.Result{}, cli.Internal("cannot create human-handoff evidence", evidenceErr)
		}
		env.Evidence = append(env.Evidence, evidence)
		finding.EvidenceRefs = append(finding.EvidenceRefs, evidence.EvidenceID)
	}
	work := api.RequiredWork{Kind: "obtain_human_decision", ReasonCode: code}
	remediation, err := factory.NewRemediation(api.Remediation{
		FindingID: finding.FindingID, Kind: "human_handoff", RequiredWork: &work,
		EvidenceRefs: append([]string(nil), finding.EvidenceRefs...), Actions: []api.Action{},
	})
	if err != nil {
		return cli.Result{}, cli.Internal("cannot create human-handoff remediation", err)
	}
	env.Findings = append(env.Findings, finding)
	env.Remediations = append(env.Remediations, remediation)
	env.ApplyFindingDisposition()
	return result(env, summary)
}

func (h *Handler) remoteChangedResult(command cli.CommandName, stack api.Stack, cwd string,
	expected, actual map[string]string, session *api.Session) (cli.Result, error) {
	env, factory, err := h.envelope(command)
	if err != nil {
		return cli.Result{}, cli.Internal("cannot create remote-change envelope", err)
	}
	env.Stack = &stack
	env.Session = session
	finding, err := factory.NewFinding("remote_changed", api.DispositionActionRequired,
		api.FindingScope{Kind: "stack"}, "an affected remote ref changed; restack did not start")
	if err != nil {
		return cli.Result{}, cli.Internal("cannot create remote-change finding", err)
	}
	for _, branch := range sortedRefNames(expected) {
		fields := map[string]any{
			"branch": branch, "old_sha": expected[branch], "classification": "unexpected",
		}
		if current := actual[branch]; fullOID(current) {
			fields["current_sha"] = current
		}
		evidence, evidenceErr := factory.NewEvidence("remote_ref", fields)
		if evidenceErr != nil {
			return cli.Result{}, cli.Internal("cannot create remote-ref evidence", evidenceErr)
		}
		env.Evidence = append(env.Evidence, evidence)
		finding.EvidenceRefs = append(finding.EvidenceRefs, evidence.EvidenceID)
	}
	action := api.Action{
		Kind: "recheck", Argv: []string{"mrstack", "check", "--json", "--no-input"},
		CWD: cwd, Preconditions: []string{"repository_context_current"},
		Requires: api.ActionRequirements{JobIDs: []string{}},
	}
	work := api.RequiredWork{Kind: "wait_for_external_state", ReasonCode: "remote_changed"}
	remediation, err := factory.NewRemediation(api.Remediation{
		FindingID: finding.FindingID, Kind: "wait_and_recheck", RequiredWork: &work,
		EvidenceRefs: append([]string(nil), finding.EvidenceRefs...), Actions: []api.Action{action},
	})
	if err != nil {
		return cli.Result{}, cli.Internal("cannot create remote-change remediation", err)
	}
	env.Findings = append(env.Findings, finding)
	env.Remediations = append(env.Remediations, remediation)
	env.ApplyFindingDisposition()
	return result(env, finding.Summary)
}

func stackRefMap(stack api.Stack) map[string]string {
	refs := map[string]string{stack.Base.Branch: stack.Base.SHA}
	for _, member := range stack.Members {
		if member.SourceSHA != nil {
			refs[member.SourceBranch] = *member.SourceSHA
		}
		if member.TargetSHA != nil {
			refs[member.TargetBranch] = *member.TargetSHA
		}
	}
	return refs
}

func recheckAction(remote, cwd string) api.Action {
	return api.Action{
		Kind: "recheck",
		Argv: []string{
			"mrstack", "--json", "--no-input", "--remote", remote, "check",
		},
		CWD: cwd, Preconditions: []string{"repository_context_current"},
		Requires: api.ActionRequirements{JobIDs: []string{}},
	}
}

func (h *Handler) attachCheckPackets(env *api.Envelope, factory *api.Factory, cwd string) error {
	if env.Stack == nil {
		return nil
	}
	for index := range env.Findings {
		finding := &env.Findings[index]
		if finding.Disposition == api.DispositionInvalid {
			continue
		}
		var member *api.Member
		if finding.Scope.Position != nil {
			position := *finding.Scope.Position
			if position >= 0 && position < len(env.Stack.Members) {
				member = &env.Stack.Members[position]
			}
		} else if finding.Scope.MRIID != nil {
			for position := range env.Stack.Members {
				if env.Stack.Members[position].IID == *finding.Scope.MRIID {
					member = &env.Stack.Members[position]
					break
				}
			}
		}
		if member != nil {
			fields := map[string]any{
				"member_iid": member.IID, "state": member.State, "web_url": member.WebURL,
				"source_branch": member.SourceBranch, "target_branch": member.TargetBranch,
			}
			if member.SourceSHA != nil {
				fields["source_sha"] = *member.SourceSHA
			}
			if member.TargetSHA != nil {
				fields["target_sha"] = *member.TargetSHA
			}
			evidence, err := factory.NewEvidence("gitlab_mr", fields)
			if err != nil {
				return err
			}
			env.Evidence = append(env.Evidence, evidence)
			finding.EvidenceRefs = append(finding.EvidenceRefs, evidence.EvidenceID)
			if member.Pipeline != nil && member.Pipeline.Currentness == "exact" &&
				member.Pipeline.ID != nil && member.Pipeline.SourceSHA != nil &&
				member.Pipeline.Kind != nil && member.Pipeline.WebURL != nil {
				pipelineFields := map[string]any{
					"member_iid": member.IID, "pipeline_id": *member.Pipeline.ID,
					"source_sha": *member.Pipeline.SourceSHA, "web_url": *member.Pipeline.WebURL,
					"status": member.Pipeline.BlockingStatus, "kind": *member.Pipeline.Kind,
				}
				if member.Pipeline.TargetSHA != nil {
					pipelineFields["target_sha"] = *member.Pipeline.TargetSHA
				}
				pipelineEvidence, pipelineErr := factory.NewEvidence("pipeline", pipelineFields)
				if pipelineErr != nil {
					return pipelineErr
				}
				env.Evidence = append(env.Evidence, pipelineEvidence)
				finding.EvidenceRefs = append(finding.EvidenceRefs, pipelineEvidence.EvidenceID)
				for _, job := range member.Pipeline.FailedJobs {
					jobEvidence, jobErr := factory.NewEvidence("job", map[string]any{
						"pipeline_id": *member.Pipeline.ID, "job_id": job.ID,
						"web_url": job.WebURL, "name": job.Name, "status": job.Status,
					})
					if jobErr != nil {
						return jobErr
					}
					env.Evidence = append(env.Evidence, jobEvidence)
					finding.EvidenceRefs = append(finding.EvidenceRefs, jobEvidence.EvidenceID)
				}
			}
		}
		remediation := api.Remediation{
			FindingID: finding.FindingID, EvidenceRefs: append([]string(nil), finding.EvidenceRefs...),
			Actions: []api.Action{},
		}
		switch finding.Disposition {
		case api.DispositionHumanRequired:
			remediation.Kind = "human_handoff"
			remediation.RequiredWork = &api.RequiredWork{
				Kind: "obtain_human_decision", ReasonCode: finding.Code,
			}
		case api.DispositionWaiting:
			remediation.Kind = "wait_and_recheck"
			remediation.RequiredWork = &api.RequiredWork{
				Kind: "wait_for_external_state", ReasonCode: finding.Code,
			}
			remediation.Actions = []api.Action{
				recheckAction(env.Stack.Remote.Name, cwd),
			}
		case api.DispositionActionRequired:
			switch finding.Code {
			case "restack_required":
				if member == nil || member.Layer.BoundarySHA == nil {
					return fmt.Errorf("restack finding lacks exact member layer")
				}
				remediation.Kind = "restack"
				remediation.SnapshotID = &env.Stack.SnapshotID
				remediation.Member = &api.RemediationMember{
					IID: member.IID, Position: member.Position,
				}
				remediation.Layer = &api.RemediationLayer{
					MRIID: member.IID, BoundarySHA: member.Layer.BoundarySHA,
				}
				remediation.Actions = []api.Action{{
					Kind: "start_restack",
					Argv: []string{
						"mrstack", "--json", "--no-input", "--yes", "--remote",
						env.Stack.Remote.Name, "restack", "--snapshot", env.Stack.SnapshotID,
					},
					CWD: cwd, Mutates: true, ConfirmationRequired: true,
					Preconditions: []string{"snapshot_current"},
					Requires: api.ActionRequirements{
						SnapshotID: &env.Stack.SnapshotID, JobIDs: []string{},
					},
				}}
			case "pipeline_failed":
				if member == nil || member.Pipeline == nil || member.Pipeline.ID == nil ||
					len(member.Pipeline.FailedJobs) == 0 {
					return fmt.Errorf("pipeline failure lacks pinned pipeline jobs")
				}
				jobIDs := make([]string, 0, len(member.Pipeline.FailedJobs))
				for _, job := range member.Pipeline.FailedJobs {
					jobIDs = append(jobIDs, job.ID)
				}
				remediation.Kind = "inspect_ci_failure"
				remediation.RequiredWork = &api.RequiredWork{
					Kind: "repair_ci_failure", PipelineID: *member.Pipeline.ID, JobIDs: jobIDs,
				}
				remediation.Actions = []api.Action{
					{
						Kind: "fetch_ci_logs",
						Argv: append([]string{
							"mrstack", "--json", "--no-input", "--remote", env.Stack.Remote.Name,
							"ci", "logs", "--pipeline", *member.Pipeline.ID,
						}, jobArgs(jobIDs)...),
						CWD: cwd, Preconditions: []string{"pipeline_and_jobs_pinned"},
						Requires: api.ActionRequirements{
							PipelineID: member.Pipeline.ID, JobIDs: append([]string(nil), jobIDs...),
						},
					},
					recheckAction(env.Stack.Remote.Name, cwd),
				}
			default:
				return fmt.Errorf("unsupported actionable check finding %s", finding.Code)
			}
		}
		out, err := factory.NewRemediation(remediation)
		if err != nil {
			return err
		}
		env.Remediations = append(env.Remediations, out)
	}
	return nil
}

func jobArgs(jobIDs []string) []string {
	args := make([]string, 0, len(jobIDs)*2)
	for _, jobID := range jobIDs {
		args = append(args, "--job", jobID)
	}
	return args
}

func sessionAction(kind string, session api.Session, preconditions ...string) api.Action {
	argv := []string{
		"mrstack", "--json", "--no-input", "--yes", "--remote", session.Remote.Name,
		"restack",
	}
	switch kind {
	case "continue_restack":
		argv = append(argv, "continue", "--session", session.SessionID)
	case "continue_drop_current":
		argv = append(argv, "continue", "--session", session.SessionID, "--drop-current")
	case "continue_keep_empty":
		argv = append(argv, "continue", "--session", session.SessionID, "--keep-empty")
	case "abort_restack":
		argv = append(argv, "abort", "--session", session.SessionID)
	case "recover_restack":
		argv = append(argv, "recover", "--session", session.SessionID)
	default:
		panic("unsupported session action " + kind)
	}
	cwd := "/"
	if session.Worktree != nil {
		cwd = session.Worktree.Path
	}
	mutates, confirmation := true, true
	if kind == "recover_restack" {
		mutates, confirmation = false, false
	}
	return api.Action{
		Kind: kind, Argv: argv,
		CWD: cwd, Mutates: mutates, ConfirmationRequired: confirmation,
		Preconditions: preconditions,
		Requires:      api.ActionRequirements{SessionID: &session.SessionID, JobIDs: []string{}},
	}
}

func (h *Handler) replayStopResult(command cli.CommandName, session api.Session) (cli.Result, error) {
	env, factory, err := h.envelope(command)
	if err != nil {
		return cli.Result{}, cli.Internal("cannot create replay-stop envelope", err)
	}
	env.Session = &session
	if session.Worktree == nil || session.CurrentLayer == nil {
		return cli.Result{}, cli.Internal("replay stop lacks managed worktree or current layer", nil)
	}
	layer := session.CurrentLayer
	evidence, err := factory.NewEvidence("managed_worktree", map[string]any{
		"path": session.Worktree.Path, "git_state": session.Worktree.GitState,
		"commit_sha": layer.OriginalCommitSHA,
	})
	if err != nil {
		return cli.Result{}, cli.Internal("cannot create replay-stop evidence", err)
	}
	scope := api.FindingScope{
		Kind: "layer", MRIID: &layer.MRIID, CommitSHA: &layer.OriginalCommitSHA,
	}
	var code, summary, kind string
	var work api.RequiredWork
	var actions []api.Action
	remediationLayer := &api.RemediationLayer{
		MRIID: layer.MRIID, CommitSHA: &layer.OriginalCommitSHA,
	}
	switch session.State {
	case "rebase_conflict":
		code, kind = "rebase_conflict", "resolve_conflict"
		summary = fmt.Sprintf("Restack session %s paused on conflicts that must be resolved and staged", session.SessionID)
		work = api.RequiredWork{
			Kind: "resolve_and_stage_conflicts", Paths: append([]string(nil), layer.ConflictedPaths...),
			Staging: "caller_explicit",
		}
		actions = []api.Action{
			sessionAction("continue_restack", session,
				"session_state_current", "conflicts_resolved_and_staged"),
			sessionAction("abort_restack", session,
				"session_state_current", "no_remote_publication"),
		}
	case "empty_commit":
		code, kind = "empty_commit", "choose_empty_commit"
		summary = fmt.Sprintf("Restack session %s paused for an explicit empty-commit decision", session.SessionID)
		work = api.RequiredWork{
			Kind: "choose_empty_commit_outcome", Options: []string{"drop_current", "keep_empty"},
		}
		actions = []api.Action{
			sessionAction("continue_drop_current", session,
				"session_state_current", "empty_commit_current"),
			sessionAction("continue_keep_empty", session,
				"session_state_current", "empty_commit_current"),
			sessionAction("abort_restack", session,
				"session_state_current", "no_remote_publication"),
		}
	default:
		return cli.Result{}, cli.Internal("unsupported replay stop state", nil)
	}
	finding, err := factory.NewFinding(code, api.DispositionActionRequired, scope, summary)
	if err != nil {
		return cli.Result{}, cli.Internal("cannot create replay-stop finding", err)
	}
	finding.EvidenceRefs = []string{evidence.EvidenceID}
	remediation, err := factory.NewRemediation(api.Remediation{
		FindingID: finding.FindingID, Kind: kind, SessionID: &session.SessionID,
		Layer: remediationLayer, Worktree: session.Worktree, RequiredWork: &work,
		EvidenceRefs: []string{evidence.EvidenceID}, Actions: actions,
	})
	if err != nil {
		return cli.Result{}, cli.Internal("cannot create replay-stop remediation", err)
	}
	env.Evidence = append(env.Evidence, evidence)
	env.Findings = append(env.Findings, finding)
	env.Remediations = append(env.Remediations, remediation)
	env.ApplyFindingDisposition()
	return result(env, summary)
}
