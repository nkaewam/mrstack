package stack

import (
	"math/rand"
	"slices"
	"testing"
)

const (
	project = "group/project"
	baseSHA = "0000000000000000000000000000000000000001"
)

func openMR(i int, source, target string) MergeRequest {
	return MergeRequest{
		IID: i, ProjectID: project, SourceProjectID: project, TargetProjectID: project,
		SourceBranch: source, TargetBranch: target, SourceSHA: sha(i + 1), TargetSHA: sha(i),
		State: StateOpen, SourceBranchExists: true, TargetBranchExists: true,
	}
}

func sha(n int) string {
	const hex = "0123456789abcdef"
	out := make([]byte, 40)
	for i := range out {
		out[i] = hex[(n+i)%len(hex)]
	}
	return string(out)
}

func chain(n int) []MergeRequest {
	out := make([]MergeRequest, n)
	target := "main"
	for i := range n {
		source := "b" + string(rune('a'+i))
		out[i] = openMR(i+1, source, target)
		target = source
	}
	return out
}

func discover(mrs []MergeRequest, selector Selector) DiscoveryResult {
	return Discover(DiscoveryInput{
		ProjectID: project, DefaultBranch: "main", BaseSHA: baseSHA,
		Mode: ModeLegacy, Selector: selector, MergeRequests: mrs,
	})
}

func discoverExplicit(mrs []MergeRequest) DiscoveryResult {
	return Discover(DiscoveryInput{
		ProjectID: project, DefaultBranch: "main", BaseSHA: baseSHA,
		Mode: ModeLegacy, MergeRequests: mrs, Explicit: true,
	})
}

func TestDiscoverExplicitOrdersAndValidatesChain(t *testing.T) {
	t.Parallel()
	got := discoverExplicit(chain(3))
	if len(got.Stack.Members) != 3 {
		t.Fatalf("expected 3 members, got %d: %+v", len(got.Stack.Members), got.Stack.Members)
	}
	if got.Stack.Members[0].TargetBranch != "main" {
		t.Fatalf("front must target main: %+v", got.Stack.Members[0])
	}
	if len(got.Findings) != 0 {
		t.Fatalf("expected no findings for a clean chain: %+v", got.Findings)
	}
}

func TestDiscoverExplicitRejectsFork(t *testing.T) {
	t.Parallel()
	// Two members target the same source branch -> fork.
	mrs := []MergeRequest{
		openMR(1, "feature/a", "main"),
		openMR(2, "feature/b", "feature/a"),
		openMR(3, "feature/c", "feature/a"),
	}
	got := discoverExplicit(mrs)
	requireFinding(t, got, FindingFork, DispositionInvalid)
}

func TestDiscoverExplicitRejectsBrokenLink(t *testing.T) {
	t.Parallel()
	mrs := []MergeRequest{
		openMR(1, "feature/a", "main"),
		openMR(3, "feature/c", "feature/b"), // feature/b has no member
	}
	got := discoverExplicit(mrs)
	// Two disconnected components produce two fronts, which is reported as a
	// cyclic/non-linear relationship rather than an ambiguous edge.
	if len(got.Findings) == 0 {
		t.Fatal("expected a finding for a broken chain")
	}
	if got.Disposition != DispositionInvalid {
		t.Fatalf("expected invalid disposition, got %q", got.Disposition)
	}
}

func TestDiscoverExplicitNoOpenMembers(t *testing.T) {
	t.Parallel()
	merged := openMR(1, "feature/a", "main")
	merged.State = StateMerged
	got := discoverExplicit([]MergeRequest{merged})
	requireFinding(t, got, FindingNoStackSelected, DispositionInvalid)
}

func requireFinding(t *testing.T, got DiscoveryResult, code FindingCode, disposition Disposition) {
	t.Helper()
	if got.Disposition != disposition {
		t.Fatalf("disposition = %q, want %q; findings=%+v", got.Disposition, disposition, got.Findings)
	}
	for _, f := range got.Findings {
		if f.Code == code {
			return
		}
	}
	t.Fatalf("findings = %+v, want %s", got.Findings, code)
}

func TestDiscoverEverySelectorAndInputOrderProduceSameChain_D01(t *testing.T) {
	want := []int{1, 2, 3}
	selectors := []Selector{
		{Kind: SelectCurrentBranch, Branch: "bc"},
		{Kind: SelectBranch, Branch: "bb"},
		{Kind: SelectMergeRequest, IID: 1},
		{Kind: SelectMergeRequest, IID: 2},
		{Kind: SelectMergeRequest, IID: 3},
	}
	for seed := range 20 {
		mrs := chain(3)
		rand.New(rand.NewSource(int64(seed))).Shuffle(len(mrs), func(i, j int) { mrs[i], mrs[j] = mrs[j], mrs[i] })
		for _, selector := range selectors {
			got := discover(mrs, selector)
			if got.Disposition != DispositionReady {
				t.Fatalf("seed %d selector %+v: %+v", seed, selector, got)
			}
			iids := make([]int, len(got.Stack.Members))
			for i, mr := range got.Stack.Members {
				iids[i] = mr.IID
			}
			if !slices.Equal(iids, want) {
				t.Fatalf("seed %d selector %+v: order %v", seed, selector, iids)
			}
		}
	}
}

func TestDiscoverRejectsMalformedTopology_D03(t *testing.T) {
	tests := []struct {
		name string
		mrs  []MergeRequest
		code FindingCode
	}{
		{"fork", []MergeRequest{openMR(1, "b1", "main"), openMR(2, "b2", "b1"), openMR(3, "b3", "b1")}, FindingFork},
		{"ambiguous predecessor", []MergeRequest{openMR(1, "same", "main"), openMR(2, "same", "main"), openMR(3, "top", "same")}, FindingAmbiguousEdge},
		{"self cycle", []MergeRequest{openMR(1, "b1", "b1")}, FindingCycle},
		{"non default base", []MergeRequest{openMR(1, "b1", "release")}, FindingNonDefaultBase},
		{"too deep", chain(11), FindingMaximumDepthExceeded},
		{"cross project", func() []MergeRequest {
			m := chain(2)
			m[1].SourceProjectID = "other/project"
			return m
		}(), FindingCrossProjectMember},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := discover(tt.mrs, Selector{Kind: SelectMergeRequest, IID: 1})
			requireFinding(t, got, tt.code, DispositionInvalid)
		})
	}
}

func TestDiscoverLegacyMergedPredecessor_D04_D05_D06(t *testing.T) {
	successor := openMR(2, "b2", "b1")
	successor.TargetBranchExists = false
	merged := MergeRequest{
		IID: 1, ProjectID: project, SourceProjectID: project, TargetProjectID: project,
		SourceBranch: "b1", TargetBranch: "main", State: StateMerged,
		IntegrationRevision: sha(9), IntegrationInBase: true, HistoricalTargetSHA: sha(1),
	}
	got := discover([]MergeRequest{successor, merged}, Selector{Kind: SelectMergeRequest, IID: 2})
	if got.Disposition != DispositionReady || len(got.Stack.Members) != 1 ||
		got.Stack.Members[0].TargetBranch != "b1" ||
		got.Stack.Members[0].TargetSHA != merged.HistoricalTargetSHA ||
		got.Stack.MergedPredecessor == nil || got.Stack.MergedPredecessor.IID != 1 {
		t.Fatalf("qualified predecessor result = %+v", got)
	}

	t.Run("zero candidates", func(t *testing.T) {
		got := discover([]MergeRequest{successor}, Selector{Kind: SelectMergeRequest, IID: 2})
		requireFinding(t, got, FindingAmbiguousMergedPredecessor, DispositionHumanRequired)
	})
	t.Run("two candidates", func(t *testing.T) {
		second := merged
		second.IID = 9
		got := discover([]MergeRequest{successor, merged, second}, Selector{Kind: SelectMergeRequest, IID: 2})
		requireFinding(t, got, FindingAmbiguousMergedPredecessor, DispositionHumanRequired)
	})
	t.Run("one of two candidates has complete proof", func(t *testing.T) {
		unqualified := merged
		unqualified.IID = 9
		unqualified.IntegrationInBase = false
		got := discover([]MergeRequest{successor, merged, unqualified}, Selector{Kind: SelectMergeRequest, IID: 2})
		if got.Disposition != DispositionReady || got.Stack.MergedPredecessor == nil || got.Stack.MergedPredecessor.IID != merged.IID {
			t.Fatalf("result = %+v", got)
		}
	})
	t.Run("unique candidate missing exact evidence", func(t *testing.T) {
		merged.HistoricalTargetSHA = ""
		got := discover([]MergeRequest{successor, merged}, Selector{Kind: SelectMergeRequest, IID: 2})
		requireFinding(t, got, FindingMissingActiveBranch, DispositionInvalid)
	})
}

func TestDiscoverLifecycle_D07_D08(t *testing.T) {
	successor := openMR(2, "b2", "b1")
	successor.TargetBranchExists = false
	closed := MergeRequest{
		IID: 1, ProjectID: project, SourceProjectID: project, TargetProjectID: project,
		SourceBranch: "b1", TargetBranch: "main", State: StateClosed,
	}
	requireFinding(t, discover([]MergeRequest{successor, closed}, Selector{Kind: SelectMergeRequest, IID: 2}), FindingClosedMember, DispositionHumanRequired)

	predecessor := openMR(1, "b1", "main")
	mergedSuccessor := MergeRequest{
		IID: 2, ProjectID: project, SourceProjectID: project, TargetProjectID: project,
		SourceBranch: "b2", TargetBranch: "b1", State: StateMerged,
	}
	requireFinding(t, discover([]MergeRequest{predecessor, mergedSuccessor}, Selector{Kind: SelectMergeRequest, IID: 1}), FindingOutOfOrderMerge, DispositionInvalid)
}

func TestDiscoverNativeDoesNotRaceRetarget(t *testing.T) {
	successor := openMR(2, "b2", "b1")
	successor.TargetBranchExists = false
	merged := MergeRequest{
		IID: 1, ProjectID: project, SourceProjectID: project, TargetProjectID: project,
		SourceBranch: "b1", TargetBranch: "main", State: StateMerged,
		IntegrationRevision: sha(9), IntegrationInBase: true, HistoricalTargetSHA: sha(1),
	}
	got := Discover(DiscoveryInput{
		ProjectID: project, DefaultBranch: "main", BaseSHA: baseSHA, Mode: ModeNative,
		Selector: Selector{Kind: SelectMergeRequest, IID: 2}, MergeRequests: []MergeRequest{successor, merged},
	})
	requireFinding(t, got, FindingNativeRetargetPending, DispositionWaiting)
}

func FuzzDiscoverLinearChainOrder(f *testing.F) {
	f.Add(uint8(3), int64(1))
	f.Add(uint8(10), int64(99))
	f.Fuzz(func(t *testing.T, rawDepth uint8, seed int64) {
		depth := int(rawDepth%MaxDepth) + 1
		mrs := chain(depth)
		rand.New(rand.NewSource(seed)).Shuffle(depth, func(i, j int) { mrs[i], mrs[j] = mrs[j], mrs[i] })
		got := discover(mrs, Selector{Kind: SelectMergeRequest, IID: depth})
		if got.Disposition != DispositionReady || len(got.Stack.Members) != depth {
			t.Fatalf("depth %d: %+v", depth, got)
		}
		for i, mr := range got.Stack.Members {
			if mr.IID != i+1 {
				t.Fatalf("index %d has MR %d", i, mr.IID)
			}
		}
	})
}
