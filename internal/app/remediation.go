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
	return h.humanHandoffResult(command, stack, code, scope, summary, evidence...)
}

func (h *Handler) humanHandoffResult(command cli.CommandName, stack api.Stack, code string,
	scope api.FindingScope, summary string, inputs ...evidenceInput) (cli.Result, error) {
	env, factory, err := h.envelope(command)
	if err != nil {
		return cli.Result{}, cli.Internal("cannot create human-handoff envelope", err)
	}
	env.Stack = &stack
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
	expected, actual map[string]string) (cli.Result, error) {
	env, factory, err := h.envelope(command)
	if err != nil {
		return cli.Result{}, cli.Internal("cannot create remote-change envelope", err)
	}
	env.Stack = &stack
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

func sessionAction(kind, sessionID, cwd string, preconditions ...string) api.Action {
	return api.Action{
		Kind: kind,
		Argv: []string{
			"mrstack", "restack", "continue", "--session", sessionID,
			"--json", "--no-input", "--yes",
		},
		CWD: cwd, Mutates: true, ConfirmationRequired: true,
		Preconditions: preconditions,
		Requires:      api.ActionRequirements{SessionID: &sessionID, JobIDs: []string{}},
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
		actions = []api.Action{sessionAction("continue_restack", session.SessionID,
			session.Worktree.Path, "session_state_current", "conflicts_resolved_and_staged")}
	case "empty_commit":
		code, kind = "empty_commit", "choose_empty_commit"
		summary = fmt.Sprintf("Restack session %s paused for an explicit empty-commit decision", session.SessionID)
		work = api.RequiredWork{
			Kind: "choose_empty_commit_outcome", Options: []string{"drop_current", "keep_empty"},
		}
		actions = []api.Action{
			sessionAction("continue_drop_current", session.SessionID, session.Worktree.Path,
				"session_state_current", "empty_commit_current"),
			sessionAction("continue_keep_empty", session.SessionID, session.Worktree.Path,
				"session_state_current", "empty_commit_current"),
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
