package stack

import (
	"fmt"
	"sort"
)

type DiscoveryResult struct {
	Stack       Stack
	Findings    []Finding
	Disposition Disposition
}

// Discover derives membership only from the supplied live GitLab
// relationships. Input ordering and any journal ordering are irrelevant.
func Discover(in DiscoveryInput) DiscoveryResult {
	result := DiscoveryResult{}
	anchor, fs := selectAnchor(in)
	if len(fs) != 0 {
		result.Findings = fs
		result.Disposition = ResolveDisposition(fs, DispositionReady)
		return result
	}
	if !anchor.sameProject(in.ProjectID) {
		f := finding(FindingCrossProjectMember, DispositionInvalid, "selected merge request spans another project", anchor.IID)
		return discoveryFailure(f)
	}

	open := make([]MergeRequest, 0, len(in.MergeRequests))
	for _, mr := range in.MergeRequests {
		if mr.State == StateOpen {
			open = append(open, mr)
		}
	}

	component, cycle := openComponent(anchor, open)
	if cycle {
		return discoveryFailure(finding(FindingCycle, DispositionInvalid, "merge request relationships contain a cycle", anchor.IID))
	}
	for _, mr := range component {
		if !mr.sameProject(in.ProjectID) {
			return discoveryFailure(finding(FindingCrossProjectMember, DispositionInvalid, "stack member spans another project", mr.IID))
		}
	}

	// Detect lifecycle violations before ordering active members.
	for _, evidence := range in.MergeRequests {
		if evidence.State != StateMerged {
			continue
		}
		for _, active := range component {
			if evidence.TargetBranch == active.SourceBranch {
				return discoveryFailure(finding(FindingOutOfOrderMerge, DispositionInvalid, "a successor merged while its predecessor remained open", evidence.IID))
			}
		}
	}

	predecessors := make(map[int][]MergeRequest)
	successors := make(map[int][]MergeRequest)
	for _, member := range component {
		for _, candidate := range component {
			if candidate.IID == member.IID {
				continue
			}
			if member.TargetBranch == candidate.SourceBranch {
				predecessors[member.IID] = append(predecessors[member.IID], candidate)
				successors[candidate.IID] = append(successors[candidate.IID], member)
			}
		}
	}
	for _, member := range component {
		if len(predecessors[member.IID]) > 1 {
			return discoveryFailure(finding(FindingAmbiguousEdge, DispositionInvalid, "multiple predecessors match one target branch", member.IID))
		}
		if len(successors[member.IID]) > 1 {
			return discoveryFailure(finding(FindingFork, DispositionInvalid, "multiple successors target one source branch", member.IID))
		}
	}

	fronts := make([]MergeRequest, 0, 1)
	for _, member := range component {
		if len(predecessors[member.IID]) == 0 {
			fronts = append(fronts, member)
		}
	}
	if len(fronts) != 1 {
		return discoveryFailure(finding(FindingCycle, DispositionInvalid, "active members do not have exactly one front", anchor.IID))
	}
	front := fronts[0]

	var lifecycle []Finding
	var mergedPredecessor *MergeRequest
	if front.TargetBranch != in.DefaultBranch {
		merged := matchingMerged(in.MergeRequests, in.ProjectID, front.TargetBranch)
		qualifiedMerged := make([]MergeRequest, 0, len(merged))
		for _, candidate := range merged {
			if qualifiesMergedPredecessor(candidate) {
				qualifiedMerged = append(qualifiedMerged, candidate)
			}
		}
		closed := matchingClosed(in.MergeRequests, in.ProjectID, front.TargetBranch)
		if front.TargetBranchExists {
			lifecycle = append(lifecycle, finding(FindingNonDefaultBase, DispositionInvalid, "front merge request targets a live non-default branch", front.IID))
		} else if len(closed) != 0 {
			lifecycle = append(lifecycle, finding(FindingClosedMember, DispositionHumanRequired, "a closed predecessor cannot be skipped", closed[0].IID))
		} else if len(qualifiedMerged) > 1 || len(merged) == 0 {
			lifecycle = append(lifecycle, finding(FindingAmbiguousMergedPredecessor, DispositionHumanRequired, "merged predecessor is not unique", front.IID))
		} else if len(qualifiedMerged) == 0 {
			lifecycle = append(lifecycle, finding(FindingMissingActiveBranch, DispositionInvalid, "merged predecessor lacks exact integration and historical target evidence", front.IID))
		} else if in.Mode == ModeNative {
			lifecycle = append(lifecycle, finding(FindingNativeRetargetPending, DispositionWaiting, "GitLab has not retargeted the successor yet", front.IID))
		} else {
			// Preserve the observed target and exact historical target head.
			// The qualified predecessor records why this is logically the front.
			qualified := qualifiedMerged[0]
			front.TargetSHA = qualified.HistoricalTargetSHA
			mergedPredecessor = &qualified
		}
	}

	ordered := make([]MergeRequest, 0, len(component))
	seen := make(map[int]bool, len(component))
	for current := front; ; {
		if seen[current.IID] {
			return discoveryFailure(finding(FindingCycle, DispositionInvalid, "merge request relationships contain a cycle", current.IID))
		}
		seen[current.IID] = true
		ordered = append(ordered, current)
		next := successors[current.IID]
		if len(next) == 0 {
			break
		}
		current = next[0]
	}
	if len(ordered) != len(component) {
		return discoveryFailure(finding(FindingAmbiguousEdge, DispositionInvalid, "selected relationships are not one linear chain", anchor.IID))
	}
	if len(ordered) > MaxDepth {
		return discoveryFailure(finding(FindingMaximumDepthExceeded, DispositionInvalid, fmt.Sprintf("stack exceeds maximum depth %d", MaxDepth), ordered[MaxDepth].IID))
	}

	for i, mr := range ordered {
		if !mr.SourceBranchExists {
			lifecycle = append(lifecycle, finding(FindingMissingActiveBranch, DispositionInvalid, "open member source branch is absent", mr.IID))
		}
		if i > 0 && !mr.TargetBranchExists {
			lifecycle = append(lifecycle, finding(FindingMissingActiveBranch, DispositionInvalid, "open member target branch is absent", mr.IID))
		}
	}
	if front.TargetBranch != in.DefaultBranch && mergedPredecessor == nil && len(lifecycle) == 0 {
		lifecycle = append(lifecycle, finding(FindingNonDefaultBase, DispositionInvalid, "front merge request does not target the default branch", front.IID))
	}

	result.Stack = Stack{
		ProjectID: in.ProjectID, DefaultBranch: in.DefaultBranch, BaseSHA: in.BaseSHA,
		Members: ordered, MergedPredecessor: mergedPredecessor,
	}
	result.Findings = lifecycle
	result.Disposition = ResolveDisposition(lifecycle, DispositionReady)
	return result
}

func selectAnchor(in DiscoveryInput) (MergeRequest, []Finding) {
	var matches []MergeRequest
	for _, mr := range in.MergeRequests {
		if mr.State != StateOpen {
			continue
		}
		switch in.Selector.Kind {
		case SelectMergeRequest:
			if mr.IID == in.Selector.IID {
				matches = append(matches, mr)
			}
		case SelectBranch, SelectCurrentBranch:
			if mr.SourceBranch == in.Selector.Branch {
				matches = append(matches, mr)
			}
		default:
			return MergeRequest{}, []Finding{finding(FindingNoStackSelected, DispositionInvalid, "no valid stack selector was supplied", 0)}
		}
	}
	if len(matches) == 0 {
		return MergeRequest{}, []Finding{finding(FindingNoStackSelected, DispositionInvalid, "selector does not identify an open merge request", 0)}
	}
	if len(matches) > 1 {
		return MergeRequest{}, []Finding{finding(FindingAmbiguousSelector, DispositionInvalid, "selector identifies multiple open merge requests", 0)}
	}
	return matches[0], nil
}

func openComponent(anchor MergeRequest, open []MergeRequest) ([]MergeRequest, bool) {
	byIID := map[int]MergeRequest{anchor.IID: anchor}
	queue := []MergeRequest{anchor}
	for len(queue) > 0 {
		current := queue[0]
		queue = queue[1:]
		for _, candidate := range open {
			linked := current.TargetBranch == candidate.SourceBranch ||
				candidate.TargetBranch == current.SourceBranch
			if !linked {
				continue
			}
			if candidate.IID == current.IID && current.TargetBranch == current.SourceBranch {
				return nil, true
			}
			if _, ok := byIID[candidate.IID]; !ok {
				byIID[candidate.IID] = candidate
				queue = append(queue, candidate)
			}
		}
	}
	out := make([]MergeRequest, 0, len(byIID))
	for _, mr := range byIID {
		out = append(out, mr)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].IID < out[j].IID })
	return out, false
}

func matchingMerged(all []MergeRequest, project, source string) []MergeRequest {
	var out []MergeRequest
	for _, mr := range all {
		if mr.State == StateMerged && mr.SourceBranch == source && mr.sameProject(project) {
			out = append(out, mr)
		}
	}
	return out
}

func matchingClosed(all []MergeRequest, project, source string) []MergeRequest {
	var out []MergeRequest
	for _, mr := range all {
		if mr.State == StateClosed && mr.SourceBranch == source && mr.sameProject(project) {
			out = append(out, mr)
		}
	}
	return out
}

func qualifiesMergedPredecessor(mr MergeRequest) bool {
	return mr.IntegrationRevision != "" && mr.IntegrationInBase && mr.HistoricalTargetSHA != ""
}

func discoveryFailure(f Finding) DiscoveryResult {
	return DiscoveryResult{Findings: []Finding{f}, Disposition: f.Disposition}
}
