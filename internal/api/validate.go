package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"reflect"
	"regexp"
	"strings"
	"time"

	"github.com/nkaewam/mrstack/internal/cli"
)

var (
	shaPattern     = regexp.MustCompile(`^(?:[0-9A-Fa-f]{40}|[0-9A-Fa-f]{64})$`)
	decimalPattern = regexp.MustCompile(`^(?:0|[1-9][0-9]*)$`)
)

var evidenceAllowlist = map[string]map[string]bool{
	"gitlab_mr":           keys("member_iid", "source_sha", "target_sha", "web_url", "state", "source_branch", "target_branch"),
	"gitlab_diff_version": keys("member_iid", "source_sha", "target_sha", "web_url", "boundary_sha"),
	"git_ancestry":        keys("member_iid", "source_sha", "expected_ancestor_sha"),
	"git_commit":          keys("member_iid", "commit_sha"),
	"pipeline":            keys("member_iid", "pipeline_id", "source_sha", "target_sha", "web_url", "status", "kind"),
	"job":                 keys("pipeline_id", "job_id", "web_url", "name", "status"),
	"remote_ref":          keys("branch", "old_sha", "new_sha", "current_sha", "classification"),
	"local_worktree":      keys("path", "branch", "git_state", "commit_sha"),
	"managed_worktree":    keys("path", "git_state", "commit_sha"),
}

func keys(values ...string) map[string]bool {
	out := make(map[string]bool, len(values))
	for _, value := range values {
		out[value] = true
	}
	return out
}

// Validate enforces producer invariants that Go's type system cannot express.
func Validate(e Envelope) error {
	if e.APIVersion != APIVersion {
		return fmt.Errorf("api: api_version must be %q", APIVersion)
	}
	if !utcTimestamp(e.GeneratedAt) {
		return errors.New("api: generated_at must be an RFC 3339 UTC timestamp")
	}
	if !validCommand(e.Command.Name) || strings.TrimSpace(e.Command.InvocationID) == "" {
		return errors.New("api: command name and invocation_id are required")
	}
	if err := validateOutcome(e.Outcome); err != nil {
		return err
	}
	if e.Findings == nil || e.Evidence == nil || e.Remediations == nil || e.Data == nil {
		return errors.New("api: findings, evidence, remediations, and data must be non-nil")
	}
	if e.Outcome.Class == ClassAuthoritative {
		if e.Error != nil {
			return errors.New("api: authoritative outcomes cannot include error")
		}
	} else {
		if e.Disposition != nil || e.Error == nil || strings.TrimSpace(e.Error.Message) == "" {
			return errors.New("api: failed outcomes require error and null disposition")
		}
	}
	if e.Command.Name == CommandUnknown || e.Command.Name == CommandRestackAbandon {
		if e.Outcome.Class != ClassInvalidInput {
			return errors.New("api: unknown and restack.abandon must return invalid_input")
		}
	}
	for _, finding := range e.Findings {
		if err := validateFinding(finding); err != nil {
			return err
		}
	}
	if err := validateStack(e.Stack); err != nil {
		return err
	}
	if len(e.Findings) > 0 {
		values := make([]Disposition, 0, len(e.Findings))
		for _, finding := range e.Findings {
			values = append(values, finding.Disposition)
		}
		best, _ := HighestDisposition(values...)
		if e.Disposition == nil || *e.Disposition != best {
			return fmt.Errorf("api: disposition must be %q for the emitted findings", best)
		}
	}
	for _, evidence := range e.Evidence {
		if err := validateEvidence(evidence); err != nil {
			return err
		}
	}
	for _, remediation := range e.Remediations {
		if err := validateRemediation(remediation); err != nil {
			return err
		}
	}
	if err := validateReferences(e); err != nil {
		return err
	}
	if err := validateCommandData(e); err != nil {
		return err
	}
	if err := validateSession(e.Session); err != nil {
		return err
	}
	if err := redactionGuard(e); err != nil {
		return err
	}
	return nil
}

func validateReferences(e Envelope) error {
	findings := make(map[string]Finding, len(e.Findings))
	evidence := make(map[string]bool, len(e.Evidence))
	remediations := make(map[string]bool, len(e.Remediations))
	for _, item := range e.Evidence {
		if evidence[item.EvidenceID] {
			return fmt.Errorf("api: duplicate evidence ID %q", item.EvidenceID)
		}
		evidence[item.EvidenceID] = true
	}
	for _, item := range e.Findings {
		if _, exists := findings[item.FindingID]; exists {
			return fmt.Errorf("api: duplicate finding ID %q", item.FindingID)
		}
		findings[item.FindingID] = item
		for _, ref := range item.EvidenceRefs {
			if !evidence[ref] {
				return fmt.Errorf("api: finding %q references unknown evidence %q", item.FindingID, ref)
			}
		}
		if e.Stack != nil && item.Scope.Position != nil {
			position := *item.Scope.Position
			if position < 0 || position >= len(e.Stack.Members) {
				return fmt.Errorf("api: finding %q has an out-of-range member position", item.FindingID)
			}
			if item.Scope.MRIID != nil && e.Stack.Members[position].IID != *item.Scope.MRIID {
				return fmt.Errorf("api: finding %q member identity disagrees with its position", item.FindingID)
			}
		}
	}
	for _, item := range e.Remediations {
		if remediations[item.RemediationID] {
			return fmt.Errorf("api: duplicate remediation ID %q", item.RemediationID)
		}
		remediations[item.RemediationID] = true
		if _, exists := findings[item.FindingID]; !exists {
			return fmt.Errorf("api: remediation %q references unknown finding %q", item.RemediationID, item.FindingID)
		}
		for _, ref := range item.EvidenceRefs {
			if !evidence[ref] {
				return fmt.Errorf("api: remediation %q references unknown evidence %q", item.RemediationID, ref)
			}
		}
		if item.SnapshotID != nil && (e.Stack == nil || *item.SnapshotID != e.Stack.SnapshotID) {
			return fmt.Errorf("api: remediation %q snapshot binding disagrees with stack", item.RemediationID)
		}
		if item.SessionID != nil && (e.Session == nil || *item.SessionID != e.Session.SessionID) {
			return fmt.Errorf("api: remediation %q session binding disagrees with session", item.RemediationID)
		}
		if item.PlanID != nil && !envelopeHasPlanID(e, *item.PlanID) {
			return fmt.Errorf("api: remediation %q plan binding disagrees with envelope", item.RemediationID)
		}
		if item.Member != nil {
			if e.Stack == nil || item.Member.Position < 0 ||
				item.Member.Position >= len(e.Stack.Members) ||
				e.Stack.Members[item.Member.Position].IID != item.Member.IID {
				return fmt.Errorf("api: remediation %q member binding disagrees with stack", item.RemediationID)
			}
		}
		if item.Layer != nil {
			if !envelopeHasLayer(e, *item.Layer) {
				return fmt.Errorf("api: remediation %q layer binding disagrees with envelope", item.RemediationID)
			}
		}
		if item.Worktree != nil {
			if e.Session == nil || e.Session.Worktree == nil ||
				item.Worktree.Path != e.Session.Worktree.Path ||
				item.Worktree.GitState != e.Session.Worktree.GitState {
				return fmt.Errorf("api: remediation %q worktree binding disagrees with session", item.RemediationID)
			}
		}
		for _, action := range item.Actions {
			inv, err := actionInvocation(action)
			if err != nil {
				return err
			}
			if action.Requires.SnapshotID != nil &&
				(e.Stack == nil || *action.Requires.SnapshotID != e.Stack.SnapshotID) {
				return fmt.Errorf("api: action %q snapshot binding disagrees with stack", action.Kind)
			}
			if action.Requires.SessionID != nil &&
				(e.Session == nil || *action.Requires.SessionID != e.Session.SessionID) {
				return fmt.Errorf("api: action %q session binding disagrees with session", action.Kind)
			}
			if action.Requires.PlanID != nil && !envelopeHasPlanID(e, *action.Requires.PlanID) {
				return fmt.Errorf("api: action %q plan binding disagrees with envelope", action.Kind)
			}
			if e.Stack != nil && inv.Globals.Remote != e.Stack.Remote.Name {
				return fmt.Errorf("api: action %q remote binding disagrees with stack", action.Kind)
			}
			if item.SessionID != nil &&
				(e.Session == nil || inv.Globals.Remote != e.Session.Remote.Name) {
				return fmt.Errorf("api: action %q remote binding disagrees with remediation session", action.Kind)
			}
			if action.Requires.SessionID != nil {
				if e.Session == nil || inv.Globals.Remote != e.Session.Remote.Name {
					return fmt.Errorf("api: action %q remote binding disagrees with session", action.Kind)
				}
				expectedCWD := "/"
				if e.Session.Worktree != nil {
					expectedCWD = e.Session.Worktree.Path
				}
				if action.CWD != expectedCWD {
					return fmt.Errorf("api: action %q cwd binding disagrees with session worktree", action.Kind)
				}
			}
			if action.Requires.PlanID != nil {
				plan, ok := e.Data["plan"].(Plan)
				if ok && inv.Globals.Remote != plan.Remote.Name {
					return fmt.Errorf("api: action %q remote binding disagrees with plan", action.Kind)
				}
			}
		}
	}
	return nil
}

func envelopeHasPlanID(e Envelope, planID string) bool {
	if e.Session != nil && e.Session.PlanID != nil && *e.Session.PlanID == planID {
		return true
	}
	plan, ok := e.Data["plan"].(Plan)
	return ok && plan.PlanID == planID
}

func envelopeHasLayer(e Envelope, layer RemediationLayer) bool {
	if e.Session != nil && e.Session.CurrentLayer != nil {
		current := e.Session.CurrentLayer
		if current.MRIID != layer.MRIID {
			return false
		}
		if layer.CommitSHA != nil && current.OriginalCommitSHA != *layer.CommitSHA {
			return false
		}
		return layer.BoundarySHA == nil
	}
	if e.Stack == nil {
		return false
	}
	for _, member := range e.Stack.Members {
		if member.IID != layer.MRIID {
			continue
		}
		if layer.BoundarySHA != nil &&
			(member.Layer.BoundarySHA == nil || *member.Layer.BoundarySHA != *layer.BoundarySHA) {
			return false
		}
		return layer.CommitSHA == nil
	}
	return false
}

func validateStack(s *Stack) error {
	if s == nil {
		return nil
	}
	if s.StackID == "" || s.SnapshotID == "" || !utcTimestamp(s.ObservedAt) ||
		(s.GitLabMode != "legacy" && s.GitLabMode != "native") ||
		s.Project.ID == "" || !validDecimal(s.Project.ID) ||
		s.Project.DefaultBranch == "" || s.Base.Branch != s.Project.DefaultBranch ||
		!shaPattern.MatchString(s.Base.SHA) || len(s.Members) == 0 || len(s.Members) > 10 {
		return errors.New("api: invalid stack identity, mode, project, base, or depth")
	}
	validSelector := map[string]bool{"current_branch": true, "branch": true, "mr": true, "tracked_stack": true}
	if !validSelector[s.Selector.Kind] || s.Selector.Value == "" ||
		(s.Selector.Kind == "mr" && !validDecimal(s.Selector.Value)) {
		return errors.New("api: invalid stack selector")
	}
	if err := validateRemote(s.Remote); err != nil {
		return err
	}
	for position, member := range s.Members {
		if member.Position != position || member.IID <= 0 || member.WebURL == "" ||
			member.SourceBranch == "" || member.TargetBranch == "" ||
			!oneOf(member.State, "opened", "closed", "merged") ||
			!oneOf(member.TargetResolution, "live_branch", "integrated_predecessor") ||
			!oneOf(member.Alignment, "aligned", "stale", "unknown") ||
			!oneOf(member.ConflictStatus, "none", "reported", "checking", "unknown") ||
			member.Author.ID == "" || !validDecimal(member.Author.ID) || member.Author.Username == "" {
			return fmt.Errorf("api: invalid stack member at position %d", position)
		}
		if member.SourceSHA == nil || member.TargetSHA == nil ||
			!shaPattern.MatchString(*member.SourceSHA) || !shaPattern.MatchString(*member.TargetSHA) {
			return fmt.Errorf("api: active member %d requires full source and target object IDs", member.IID)
		}
		if !oneOf(member.Layer.BoundarySource, "gitlab_diff_version", "journal", "override", "unavailable") {
			return fmt.Errorf("api: invalid boundary source for member %d", member.IID)
		}
		if member.Layer.BoundarySHA != nil && !shaPattern.MatchString(*member.Layer.BoundarySHA) {
			return fmt.Errorf("api: abbreviated boundary object ID for member %d", member.IID)
		}
		if member.Layer.CommitCount != nil && *member.Layer.CommitCount < 0 {
			return fmt.Errorf("api: negative layer commit count for member %d", member.IID)
		}
		if err := validatePipeline(member.Pipeline, member); err != nil {
			return err
		}
	}
	if s.AffectedSuffix != nil {
		from := s.AffectedSuffix.FromPosition
		if from < 0 || from >= len(s.Members) ||
			len(s.AffectedSuffix.MemberIIDs) != len(s.Members)-from {
			return errors.New("api: invalid affected suffix")
		}
		for i, iid := range s.AffectedSuffix.MemberIIDs {
			if iid != s.Members[from+i].IID {
				return errors.New("api: affected suffix identities do not match members")
			}
		}
	}
	return nil
}

func validateRemote(r Remote) error {
	if r.Name == "" || strings.HasPrefix(r.Name, "-") ||
		!oneOf(r.Selection, "upstream", "explicit") ||
		r.Fetch.Host == "" || r.Fetch.Project == "" ||
		r.Push.Host == "" || r.Push.Project == "" {
		return errors.New("api: invalid sanitized remote identity")
	}
	for _, value := range []string{r.Fetch.Host, r.Fetch.Project, r.Push.Host, r.Push.Project} {
		if strings.Contains(value, "://") || strings.Contains(value, "@") ||
			strings.ContainsAny(value, "\x00\n\r") {
			return errors.New("api: remote identity contains credentials or raw URL syntax")
		}
	}
	return nil
}

func validatePipeline(p *Pipeline, member Member) error {
	if p == nil {
		return nil
	}
	if !oneOf(p.Applicability, "required", "not_applicable", "unknown") ||
		!oneOf(p.Currentness, "exact", "missing", "ambiguous", "not_applicable") ||
		!oneOf(p.BlockingStatus, "created", "pending", "running", "manual", "success",
			"failed", "canceled", "skipped", "unknown") || p.FailedJobs == nil {
		return fmt.Errorf("api: invalid pipeline assessment for member %d", member.IID)
	}
	if p.Currentness != "exact" {
		if p.Kind != nil || p.ID != nil || p.SHA != nil || p.SourceSHA != nil ||
			p.TargetSHA != nil || p.WebURL != nil || len(p.FailedJobs) != 0 ||
			p.BlockingStatus != "unknown" {
			return fmt.Errorf("api: non-exact pipeline contains moving identities for member %d", member.IID)
		}
		if p.Currentness == "not_applicable" && p.Applicability != "not_applicable" {
			return fmt.Errorf("api: not-applicable pipeline mismatch for member %d", member.IID)
		}
		return nil
	}
	if p.Kind == nil || !oneOf(*p.Kind, "branch", "detached_mr", "merged_results") ||
		p.ID == nil || !validDecimal(*p.ID) || p.SHA == nil || !shaPattern.MatchString(*p.SHA) ||
		p.SourceSHA == nil || member.SourceSHA == nil || *p.SourceSHA != *member.SourceSHA ||
		p.WebURL == nil || *p.WebURL == "" {
		return fmt.Errorf("api: exact pipeline lacks pinned identities for member %d", member.IID)
	}
	if *p.Kind == "merged_results" {
		if p.TargetSHA == nil || member.TargetSHA == nil || *p.TargetSHA != *member.TargetSHA {
			return fmt.Errorf("api: merged-results pipeline target mismatch for member %d", member.IID)
		}
	} else if p.TargetSHA != nil {
		return fmt.Errorf("api: branch pipeline unexpectedly contains target SHA for member %d", member.IID)
	}
	for _, job := range p.FailedJobs {
		if !validDecimal(job.ID) || job.Name == "" || job.Status == "" || job.WebURL == "" {
			return fmt.Errorf("api: invalid failed job for member %d", member.IID)
		}
	}
	return nil
}

func oneOf(value string, choices ...string) bool {
	for _, choice := range choices {
		if value == choice {
			return true
		}
	}
	return false
}

func validateRemediation(r Remediation) error {
	if r.RemediationID == "" || r.FindingID == "" || r.EvidenceRefs == nil || r.Actions == nil {
		return errors.New("api: remediation requires IDs and non-nil evidence_refs/actions")
	}
	if r.RequiredWork != nil {
		if err := validateRequiredWork(*r.RequiredWork); err != nil {
			return err
		}
	}
	for _, action := range r.Actions {
		if err := validateAction(action); err != nil {
			return err
		}
	}
	actionKinds := make(map[string]int, len(r.Actions))
	for _, action := range r.Actions {
		actionKinds[action.Kind]++
	}
	actionsOnly := func(kinds ...string) bool {
		allowed := make(map[string]bool, len(kinds))
		for _, kind := range kinds {
			allowed[kind] = true
		}
		for kind, count := range actionKinds {
			if !allowed[kind] || count != 1 {
				return false
			}
		}
		return true
	}
	hasAction := func(kind string) bool { return actionKinds[kind] == 1 }
	switch r.Kind {
	case "restack":
		if r.SnapshotID == nil || r.Layer == nil || r.Layer.BoundarySHA == nil ||
			r.Member == nil || len(r.Actions) != 1 ||
			!actionsOnly("start_restack", "start_planned_restack") {
			return errors.New("api: restack remediation requires snapshot, exact layer, and action")
		}
		if r.PlanID == nil && !hasAction("start_restack") ||
			r.PlanID != nil && !hasAction("start_planned_restack") {
			return errors.New("api: restack remediation action disagrees with plan binding")
		}
	case "resolve_conflict":
		if r.SessionID == nil || r.Worktree == nil || r.RequiredWork == nil ||
			r.RequiredWork.Kind != "resolve_and_stage_conflicts" ||
			!actionsOnly("continue_restack", "abort_restack") || !hasAction("continue_restack") {
			return errors.New("api: resolve_conflict remediation lacks bound conflict work")
		}
	case "choose_empty_commit":
		if r.SessionID == nil || r.Layer == nil || r.Layer.CommitSHA == nil ||
			r.Worktree == nil ||
			r.RequiredWork == nil || r.RequiredWork.Kind != "choose_empty_commit_outcome" ||
			!actionsOnly("continue_drop_current", "continue_keep_empty", "abort_restack") ||
			!hasAction("continue_drop_current") || !hasAction("continue_keep_empty") {
			return errors.New("api: choose_empty_commit remediation lacks bound commit choices")
		}
	case "authorize_signature_loss":
		if r.SnapshotID == nil || len(r.Actions) != 0 {
			return errors.New("api: signature-loss remediation requires snapshot and no executable action")
		}
	case "inspect_ci_failure":
		if r.RequiredWork == nil || r.RequiredWork.Kind != "repair_ci_failure" ||
			!actionsOnly("fetch_ci_logs", "recheck") || !hasAction("fetch_ci_logs") {
			return errors.New("api: CI remediation lacks pinned repair work")
		}
		for _, action := range r.Actions {
			if action.Kind == "fetch_ci_logs" &&
				(*action.Requires.PipelineID != r.RequiredWork.PipelineID ||
					!equalStrings(action.Requires.JobIDs, r.RequiredWork.JobIDs)) {
				return errors.New("api: CI remediation action disagrees with required work")
			}
		}
	case "recover_publication":
		if r.SessionID == nil || len(r.Actions) == 0 ||
			!actionsOnly("recover_restack", "continue_restack", "abort_restack") ||
			!hasAction("recover_restack") && !hasAction("continue_restack") {
			return errors.New("api: recover_publication remediation requires a recovery action")
		}
	case "retry_retarget":
		if r.SessionID == nil || len(r.Actions) != 1 ||
			!actionsOnly("continue_restack") || !hasAction("continue_restack") {
			return errors.New("api: retry_retarget remediation requires a continue action")
		}
	case "wait_and_recheck":
		if len(r.Actions) != 1 || !actionsOnly("recheck") || !hasAction("recheck") {
			return errors.New("api: wait remediation requires recheck action")
		}
	case "refresh_local_checkout":
		if r.RequiredWork == nil || r.RequiredWork.Kind != "refresh_local_checkout" ||
			len(r.Actions) != 0 {
			return errors.New("api: local-checkout remediation lacks refresh work")
		}
	case "human_handoff":
		if len(r.Actions) != 0 {
			return errors.New("api: human handoff cannot contain executable actions")
		}
	default:
		return fmt.Errorf("api: unknown remediation kind %q", r.Kind)
	}
	for _, action := range r.Actions {
		if r.SnapshotID != nil && action.Requires.SnapshotID != nil &&
			*action.Requires.SnapshotID != *r.SnapshotID {
			return fmt.Errorf("api: remediation/action snapshot bindings disagree")
		}
		if r.SessionID != nil && action.Requires.SessionID != nil &&
			*action.Requires.SessionID != *r.SessionID {
			return fmt.Errorf("api: remediation/action session bindings disagree")
		}
		if r.PlanID != nil && action.Requires.PlanID != nil &&
			*action.Requires.PlanID != *r.PlanID {
			return fmt.Errorf("api: remediation/action plan bindings disagree")
		}
		if r.Worktree != nil && action.Requires.SessionID != nil && action.CWD != r.Worktree.Path {
			return fmt.Errorf("api: remediation/action worktree bindings disagree")
		}
	}
	return nil
}

func validateRequiredWork(work RequiredWork) error {
	nonempty := func(values []string) bool {
		if len(values) == 0 {
			return false
		}
		for _, value := range values {
			if strings.TrimSpace(value) == "" {
				return false
			}
		}
		return true
	}
	switch work.Kind {
	case "resolve_and_stage_conflicts":
		if !nonempty(work.Paths) || work.Staging != "caller_explicit" {
			return errors.New("api: conflict work requires paths and caller_explicit staging")
		}
	case "repair_ci_failure":
		if !validDecimal(work.PipelineID) || !nonempty(work.JobIDs) {
			return errors.New("api: CI repair work requires decimal pipeline and job IDs")
		}
		for _, id := range work.JobIDs {
			if !validDecimal(id) {
				return errors.New("api: CI repair work contains a non-decimal job ID")
			}
		}
	case "choose_empty_commit_outcome":
		if len(work.Options) != 2 ||
			!((work.Options[0] == "drop_current" && work.Options[1] == "keep_empty") ||
				(work.Options[1] == "drop_current" && work.Options[0] == "keep_empty")) {
			return errors.New("api: empty-commit work requires the two stable outcomes")
		}
	case "obtain_human_decision", "wait_for_external_state":
		if strings.TrimSpace(work.ReasonCode) == "" {
			return fmt.Errorf("api: %s work requires reason_code", work.Kind)
		}
	case "refresh_local_checkout":
		if strings.TrimSpace(work.Branch) == "" || !shaPattern.MatchString(work.ExpectedSHA) {
			return errors.New("api: checkout refresh work requires branch and full expected SHA")
		}
	default:
		return fmt.Errorf("api: unknown required_work kind %q", work.Kind)
	}
	return nil
}

func validateAction(a Action) error {
	if len(a.Argv) == 0 || a.Argv[0] == "" || !strings.HasPrefix(a.CWD, "/") || a.Preconditions == nil || a.Requires.JobIDs == nil {
		return fmt.Errorf("api: action %q requires argv, absolute cwd, preconditions, and job_ids", a.Kind)
	}
	for name, value := range map[string]*string{
		"snapshot_id": a.Requires.SnapshotID,
		"session_id":  a.Requires.SessionID,
		"plan_id":     a.Requires.PlanID,
		"pipeline_id": a.Requires.PipelineID,
	} {
		if value != nil && strings.TrimSpace(*value) == "" {
			return fmt.Errorf("api: action %q has empty required %s", a.Kind, name)
		}
	}
	seenJobIDs := map[string]bool{}
	for _, jobID := range a.Requires.JobIDs {
		if !validDecimal(jobID) || seenJobIDs[jobID] {
			return fmt.Errorf("api: action %q has invalid or duplicate required job ID", a.Kind)
		}
		seenJobIDs[jobID] = true
	}
	if a.Requires.PipelineID != nil && !validDecimal(*a.Requires.PipelineID) {
		return fmt.Errorf("api: action %q has invalid required pipeline ID", a.Kind)
	}
	contains := func(value string) bool {
		for _, item := range a.Preconditions {
			if item == value {
				return true
			}
		}
		return false
	}
	only := func(snapshot, session, plan, pipeline bool, jobs bool) bool {
		return (a.Requires.SnapshotID != nil) == snapshot &&
			(a.Requires.SessionID != nil) == session &&
			(a.Requires.PlanID != nil) == plan &&
			(a.Requires.PipelineID != nil) == pipeline &&
			(len(a.Requires.JobIDs) > 0) == jobs
	}
	switch a.Kind {
	case "start_restack":
		if !a.Mutates || !a.ConfirmationRequired || !contains("snapshot_current") || !only(true, false, false, false, false) {
			return errors.New("api: invalid start_restack action binding")
		}
	case "start_planned_restack":
		if !a.Mutates || !a.ConfirmationRequired || !contains("snapshot_current") || !contains("plan_current") || !only(true, false, true, false, false) {
			return errors.New("api: invalid start_planned_restack action binding")
		}
	case "continue_restack":
		if !a.Mutates || !a.ConfirmationRequired || !contains("session_state_current") || !only(false, true, false, false, false) {
			return errors.New("api: invalid continue_restack action binding")
		}
	case "continue_drop_current", "continue_keep_empty":
		if !a.Mutates || !a.ConfirmationRequired || !contains("session_state_current") || !contains("empty_commit_current") || !only(false, true, false, false, false) {
			return fmt.Errorf("api: invalid %s action binding", a.Kind)
		}
	case "abort_restack":
		if !a.Mutates || !a.ConfirmationRequired || !contains("session_state_current") || !contains("no_remote_publication") || !only(false, true, false, false, false) {
			return errors.New("api: invalid abort_restack action binding")
		}
	case "recover_restack":
		if a.Mutates || a.ConfirmationRequired || !contains("session_state_current") || !only(false, true, false, false, false) {
			return errors.New("api: invalid recover_restack action binding")
		}
	case "fetch_ci_logs":
		if a.Mutates || a.ConfirmationRequired || !contains("pipeline_and_jobs_pinned") || !only(false, false, false, true, true) || len(a.Requires.JobIDs) > 20 {
			return errors.New("api: invalid fetch_ci_logs action binding")
		}
	case "recheck":
		if a.Mutates || a.ConfirmationRequired || !contains("repository_context_current") || !only(false, false, false, false, false) {
			return errors.New("api: invalid recheck action binding")
		}
	default:
		return fmt.Errorf("api: unknown action kind %q", a.Kind)
	}
	seen := map[string]bool{}
	knownPrecondition := map[string]bool{
		"snapshot_current": true, "plan_current": true, "session_state_current": true,
		"conflicts_resolved_and_staged": true, "remote_all_old": true,
		"empty_commit_current": true, "no_remote_publication": true,
		"pipeline_and_jobs_pinned": true, "repository_context_current": true,
	}
	for _, precondition := range a.Preconditions {
		if !knownPrecondition[precondition] || seen[precondition] {
			return fmt.Errorf("api: unknown or duplicate action precondition %q", precondition)
		}
		seen[precondition] = true
	}
	_, err := actionInvocation(a)
	return err
}

func actionInvocation(a Action) (cli.Invocation, error) {
	var zero cli.Invocation
	if len(a.Argv) == 0 || a.Argv[0] != "mrstack" {
		return zero, fmt.Errorf("api: action %q executable must be mrstack", a.Kind)
	}
	for _, arg := range a.Argv[1:] {
		if arg == "--gitlab-mode" || strings.HasPrefix(arg, "--gitlab-mode=") {
			return zero, fmt.Errorf("api: action %q cannot override gitlab mode", a.Kind)
		}
		if arg == "--max-bytes" || strings.HasPrefix(arg, "--max-bytes=") {
			return zero, fmt.Errorf("api: action %q cannot override the CI log budget", a.Kind)
		}
	}
	inv, err := cli.Parse(a.Argv[1:])
	if err != nil {
		return zero, fmt.Errorf("api: action %q has invalid argv: %w", a.Kind, err)
	}
	if !inv.Machine() || strings.TrimSpace(inv.Globals.Remote) == "" {
		return zero, fmt.Errorf("api: action %q requires machine mode and a bound remote", a.Kind)
	}
	if inv.Globals.Yes != a.Mutates {
		return zero, fmt.Errorf("api: action %q confirmation argv disagrees with mutation", a.Kind)
	}
	equalJobs := func() bool { return equalStrings(inv.JobIDs, a.Requires.JobIDs) }
	switch a.Kind {
	case "start_restack":
		if inv.Name != cli.CommandRestackStart || inv.SnapshotID != deref(a.Requires.SnapshotID) ||
			inv.PlanID != "" || inv.Selector.Value != "" || inv.AllowSignatureLoss {
			return zero, errors.New("api: start_restack argv disagrees with required snapshot")
		}
	case "start_planned_restack":
		if inv.Name != cli.CommandRestackStart || inv.PlanID != deref(a.Requires.PlanID) ||
			inv.SnapshotID != "" || inv.Selector.Value != "" || inv.AllowSignatureLoss {
			return zero, errors.New("api: start_planned_restack argv disagrees with required plan")
		}
	case "continue_restack", "continue_drop_current", "continue_keep_empty":
		wantDrop := a.Kind == "continue_drop_current"
		wantKeep := a.Kind == "continue_keep_empty"
		if inv.Name != cli.CommandRestackContinue || inv.SessionID != deref(a.Requires.SessionID) ||
			inv.DropCurrent != wantDrop || inv.KeepEmpty != wantKeep {
			return zero, fmt.Errorf("api: %s argv disagrees with required session transition", a.Kind)
		}
	case "abort_restack":
		if inv.Name != cli.CommandRestackAbort || inv.SessionID != deref(a.Requires.SessionID) {
			return zero, errors.New("api: abort_restack argv disagrees with required session")
		}
	case "recover_restack":
		if inv.Name != cli.CommandRestackRecover || inv.SessionID != deref(a.Requires.SessionID) {
			return zero, errors.New("api: recover_restack argv disagrees with required session")
		}
	case "fetch_ci_logs":
		if inv.Name != cli.CommandCILogs || inv.PipelineID != deref(a.Requires.PipelineID) || !equalJobs() {
			return zero, errors.New("api: fetch_ci_logs argv disagrees with required pipeline/jobs")
		}
	case "recheck":
		if inv.Name != cli.CommandCheck || inv.Selector.Value != "" || inv.Selector.StackID != "" {
			return zero, errors.New("api: recheck argv must perform an unselected repository check")
		}
	default:
		return zero, fmt.Errorf("api: unknown action kind %q", a.Kind)
	}
	return inv, nil
}

func deref(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}

func equalStrings(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}

func validateOutcome(o Outcome) error {
	switch o.Class {
	case ClassAuthoritative:
		if o != Authoritative() {
			return errors.New("api: authoritative outcome must be succeeded/ok/exit 0/non-retryable")
		}
	case ClassInvalidInput:
		if o.Status != StatusFailed || o.ExitCode != 2 {
			return errors.New("api: invalid_input must be failed with exit 2")
		}
		if _, err := InvalidInput(o.Code, o.Retryable); err != nil {
			return err
		}
	case ClassUnavailable:
		if o.Status != StatusFailed || o.ExitCode != 3 {
			return errors.New("api: unavailable must be failed with exit 3")
		}
		if _, err := Unavailable(o.Code, o.Retryable); err != nil {
			return err
		}
	case ClassInternal:
		if o != Internal() {
			return errors.New("api: internal outcome must be failed/internal_invariant_failed/exit 4/non-retryable")
		}
	default:
		return fmt.Errorf("api: unknown outcome class %q", o.Class)
	}
	return nil
}

func validateFinding(f Finding) error {
	if f.FindingID == "" || f.Summary == "" || !utcTimestamp(f.FirstSeenAt) || !utcTimestamp(f.LastSeenAt) {
		return errors.New("api: finding requires ID, summary, and UTC timestamps")
	}
	if f.Details == nil || f.EvidenceRefs == nil {
		return errors.New("api: finding details and evidence_refs must be non-nil")
	}
	first, _ := time.Parse(time.RFC3339Nano, f.FirstSeenAt)
	last, _ := time.Parse(time.RFC3339Nano, f.LastSeenAt)
	if last.Before(first) {
		return errors.New("api: finding last_seen_at cannot precede first_seen_at")
	}
	want, ok := findingDisposition[f.Code]
	if !ok {
		return fmt.Errorf("api: unknown finding code %q", f.Code)
	}
	if want != f.Disposition {
		return fmt.Errorf("api: finding %q requires disposition %q", f.Code, want)
	}
	if !validScope(f.Scope.Kind) {
		return fmt.Errorf("api: unknown finding scope %q", f.Scope.Kind)
	}
	return nil
}

func validateEvidence(e Evidence) error {
	if strings.TrimSpace(e.EvidenceID) == "" {
		return errors.New("api: evidence_id is required")
	}
	allowed, ok := evidenceAllowlist[e.Kind]
	if !ok {
		return fmt.Errorf("api: unknown evidence kind %q", e.Kind)
	}
	for key := range e.Fields {
		if !allowed[key] {
			return fmt.Errorf("api: evidence kind %q does not allow field %q", e.Kind, key)
		}
	}
	return redactionGuard(e.Fields)
}

func validateCommandData(e Envelope) error {
	if e.Outcome.Class != ClassAuthoritative {
		return nil
	}
	typeOK := func(key string, exemplar any) error {
		got, ok := e.Data[key]
		if !ok {
			return fmt.Errorf("api: authoritative %s requires data.%s", e.Command.Name, key)
		}
		if reflect.TypeOf(got) != reflect.TypeOf(exemplar) {
			return fmt.Errorf("api: data.%s has type %T, want %T", key, got, exemplar)
		}
		return nil
	}
	switch e.Command.Name {
	case CommandDoctor:
		if err := typeOK("doctor", DoctorData{}); err != nil {
			return err
		}
		return validateDoctorData(e.Data["doctor"].(DoctorData))
	case CommandRestackPlan:
		value, ok := e.Data["plan"]
		if !ok {
			return errors.New("api: authoritative restack.plan requires data.plan")
		}
		if value != nil && reflect.TypeOf(value) != reflect.TypeOf(Plan{}) {
			return fmt.Errorf("api: data.plan has type %T, want api.Plan or nil", value)
		}
	case CommandHistoryShow:
		return typeOK("history", HistoryData{})
	case CommandHistoryAlias:
		return typeOK("history_alias", HistoryAliasData{})
	case CommandHistoryPrune:
		return typeOK("history_prune", HistoryPruneData{})
	case CommandCILogs:
		if err := typeOK("log_request", LogRequest{}); err != nil {
			return err
		}
		if err := typeOK("log_budget", LogBudget{}); err != nil {
			return err
		}
		if err := typeOK("logs", []LogEntry{}); err != nil {
			return err
		}
		request := e.Data["log_request"].(LogRequest)
		budget := e.Data["log_budget"].(LogBudget)
		logs := e.Data["logs"].([]LogEntry)
		if !validDecimal(request.PipelineID) || len(request.JobIDs) < 1 || len(request.JobIDs) > 20 || len(logs) != len(request.JobIDs) {
			return errors.New("api: invalid log request or log result cardinality")
		}
		if budget.RequestedBytes < 1 || budget.RequestedBytes > 4194304 ||
			budget.EffectiveBytes < 1 || budget.EffectiveBytes > 4194304 ||
			budget.HardMaxBytes != 4194304 || budget.Allocation != "equal_per_job_tail" {
			return errors.New("api: invalid CI log budget")
		}
		requested := map[string]bool{}
		for _, id := range request.JobIDs {
			if !validDecimal(id) || requested[id] {
				return errors.New("api: log job IDs must be unique decimal strings")
			}
			requested[id] = true
		}
		seen := map[string]bool{}
		for _, log := range logs {
			if log.PipelineID != request.PipelineID || !requested[log.JobID] || seen[log.JobID] {
				return errors.New("api: log results must match requested pipeline and job IDs exactly once")
			}
			seen[log.JobID] = true
		}
	}
	return nil
}

func validateDoctorData(data DoctorData) error {
	if !oneOf(data.RequestedMode, "auto", "legacy", "native") ||
		!oneOf(data.EffectiveMode, "legacy", "native") ||
		strings.TrimSpace(data.GitVersion) == "" ||
		strings.TrimSpace(data.GlabVersion) == "" {
		return errors.New("api: invalid doctor mode or tool version")
	}
	if data.DetectedMode == nil {
		if data.RequestedMode == "auto" || data.ServerVersion != nil ||
			data.EffectiveMode != data.RequestedMode {
			return errors.New("api: undetected doctor mode requires a matching explicit override and null server version")
		}
	} else {
		if !oneOf(*data.DetectedMode, "legacy", "native") ||
			*data.DetectedMode != data.EffectiveMode ||
			data.ServerVersion == nil || strings.TrimSpace(*data.ServerVersion) == "" ||
			(data.RequestedMode != "auto" && data.RequestedMode != *data.DetectedMode) {
			return errors.New("api: detected doctor mode and server version are inconsistent")
		}
	}

	required := map[string]bool{
		"repository_context": false,
		"git":                false,
		"glab":               false,
		"gitlab_auth":        false,
		"server_mode":        false,
		"atomic_push":        false,
		"target_update":      false,
		"sqlite_journal":     false,
	}
	if len(data.Capabilities) != len(required) {
		return errors.New("api: doctor requires exactly one result for every capability")
	}
	for _, capability := range data.Capabilities {
		seen, known := required[capability.Name]
		if !known || seen {
			return fmt.Errorf("api: unknown or duplicate doctor capability %q", capability.Name)
		}
		if !oneOf(capability.Status, "verified", "unverified", "unsupported") ||
			strings.TrimSpace(capability.Summary) == "" {
			return fmt.Errorf("api: invalid doctor capability %q", capability.Name)
		}
		required[capability.Name] = true
	}
	return nil
}

func validateSession(s *Session) error {
	if s == nil {
		return nil
	}
	wantPublication := map[string]string{
		"preparing": "not_started", "replaying": "not_started",
		"rebase_conflict": "not_started", "empty_commit": "not_started",
		"publication_ready": "all_old", "publication_pending_reconcile": "in_flight_unknown",
		"retarget_pending": "all_new", "completed": "all_new",
		"indeterminate_publication": "indeterminate", "abandoned": "indeterminate",
	}
	if want, ok := wantPublication[s.State]; ok && s.Publication.State != want {
		return fmt.Errorf("api: session %s requires publication state %s", s.State, want)
	}
	switch s.State {
	case "preparing", "replaying", "rebase_conflict", "empty_commit", "publication_ready":
		if !s.Resumable || !s.Abortable {
			return fmt.Errorf("api: session %s must be resumable and abortable", s.State)
		}
	case "publication_pending_reconcile", "retarget_pending", "indeterminate_publication":
		if !s.Resumable || s.Abortable {
			return fmt.Errorf("api: session %s must be resumable and not abortable", s.State)
		}
	case "completed", "aborted", "abandoned":
		if s.Resumable || s.Abortable {
			return fmt.Errorf("api: session %s cannot be resumable or abortable", s.State)
		}
	case "invalidated":
		if s.Resumable {
			return errors.New("api: invalidated session cannot be resumable")
		}
	default:
		return fmt.Errorf("api: unknown session state %q", s.State)
	}
	for _, ref := range s.Publication.Refs {
		if !shaPattern.MatchString(ref.OldSHA) {
			return errors.New("api: publication old_sha must be a full object ID")
		}
		if ref.Classification == "old" && ref.CurrentSHA != nil && *ref.CurrentSHA != ref.OldSHA {
			return errors.New("api: old publication ref current_sha must equal old_sha")
		}
		if ref.Classification == "new" && (ref.NewSHA == nil || ref.CurrentSHA == nil || *ref.NewSHA != *ref.CurrentSHA) {
			return errors.New("api: new publication ref current_sha must equal new_sha")
		}
	}
	if s.State == "retarget_pending" && (s.TargetUpdate == nil || s.TargetUpdate.Status != "pending") {
		return errors.New("api: retarget_pending requires a pending target_update")
	}
	return nil
}

func redactionGuard(value any) error {
	return walkValue(reflect.ValueOf(value), "")
}

func walkValue(v reflect.Value, path string) error {
	if !v.IsValid() {
		return nil
	}
	if v.Kind() == reflect.Interface || v.Kind() == reflect.Pointer {
		if v.IsNil() {
			return nil
		}
		return walkValue(v.Elem(), path)
	}
	switch v.Kind() {
	case reflect.Struct:
		t := v.Type()
		for i := 0; i < v.NumField(); i++ {
			tag := strings.Split(t.Field(i).Tag.Get("json"), ",")[0]
			if tag == "-" {
				continue
			}
			if tag == "" {
				tag = t.Field(i).Name
			}
			if err := rejectSensitiveKey(tag, path); err != nil {
				return err
			}
			if err := walkValue(v.Field(i), joinPath(path, tag)); err != nil {
				return err
			}
		}
	case reflect.Map:
		iter := v.MapRange()
		for iter.Next() {
			key := fmt.Sprint(iter.Key().Interface())
			if err := rejectSensitiveKey(key, path); err != nil {
				return err
			}
			if err := walkValue(iter.Value(), joinPath(path, key)); err != nil {
				return err
			}
		}
	case reflect.Slice, reflect.Array:
		for i := 0; i < v.Len(); i++ {
			if err := walkValue(v.Index(i), path); err != nil {
				return err
			}
		}
	case reflect.String:
		if err := rejectCredentialURL(v.String(), path); err != nil {
			return err
		}
	}
	return nil
}

func rejectSensitiveKey(key, path string) error {
	normalized := strings.ToLower(strings.ReplaceAll(strings.ReplaceAll(key, "-", "_"), " ", "_"))
	for _, token := range []string{"password", "passwd", "secret", "token", "authorization", "credential", "private_key", "raw_url", "diff", "patch", "trace", "source_text", "file_content"} {
		if normalized == token || strings.HasSuffix(normalized, "_"+token) {
			return fmt.Errorf("api: sensitive field %q rejected at %s", key, path)
		}
	}
	return nil
}

func rejectCredentialURL(value, path string) error {
	if !strings.Contains(value, "://") {
		return nil
	}
	u, err := url.Parse(value)
	if err != nil {
		return fmt.Errorf("api: malformed URL at %s", path)
	}
	if u.User != nil {
		return fmt.Errorf("api: credential-bearing URL rejected at %s", path)
	}
	return nil
}

func joinPath(path, key string) string {
	if path == "" {
		return key
	}
	return path + "." + key
}

func utcTimestamp(value string) bool {
	t, err := time.Parse(time.RFC3339Nano, value)
	return err == nil && strings.HasSuffix(value, "Z") && t.Location() == time.UTC
}

func validCommand(name CommandName) bool {
	switch name {
	case CommandDoctor, CommandCheck, CommandRestackStart, CommandRestackPlan,
		CommandRestackContinue, CommandRestackAbort, CommandRestackRecover,
		CommandRestackAbandon, CommandCILogs, CommandHistoryShow,
		CommandHistoryAlias, CommandHistoryPrune, CommandUnknown:
		return true
	default:
		return false
	}
}

func validScope(kind string) bool {
	switch kind {
	case "project", "repository", "stack", "member", "layer", "pipeline", "job", "session":
		return true
	default:
		return false
	}
}

var findingDisposition = func() map[string]Disposition {
	out := map[string]Disposition{}
	add := func(d Disposition, codes ...string) {
		for _, code := range codes {
			out[code] = d
		}
	}
	add(DispositionInvalid, "no_stack_selected", "ambiguous_relationship", "cyclic_relationship", "non_linear_stack", "cross_project_member", "non_default_base", "stack_too_deep", "missing_active_branch", "out_of_order_merge", "ambiguous_remote")
	add(DispositionActionRequired, "restack_required", "merge_conflict", "rebase_conflict", "pipeline_failed", "remote_changed", "empty_commit", "retarget_pending", "local_checkout_stale")
	add(DispositionHumanRequired, "ambiguous_merged_predecessor", "closed_member", "ambiguous_layer_boundary", "conflicting_layer_boundary", "missing_layer_objects", "merge_commit_in_layer", "empty_layer", "signed_commits", "local_work_present", "foreign_authored_member", "ambiguous_pipeline", "pipeline_status_unknown", "ci_policy_unknown", "blocking_manual_job", "indeterminate_publication", "ambiguous_completion")
	add(DispositionWaiting, "pipeline_running", "missing_required_pipeline", "mergeability_checking", "operation_in_progress", "native_retarget_pending", "remote_visibility_pending")
	return out
}()

// Compile-time check that arbitrary data remains JSON encodable is deferred to
// MarshalDocument; this helper is used by tests and callers that need a probe.
func JSONEncodable(value any) bool {
	_, err := json.Marshal(value)
	return err == nil
}

func validDecimal(value string) bool { return decimalPattern.MatchString(value) }
