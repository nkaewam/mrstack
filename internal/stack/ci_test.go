package stack

import (
	"slices"
	"testing"
)

func ciMember() MergeRequest {
	return MergeRequest{IID: 4, SourceSHA: "source", TargetSHA: "target"}
}

func TestAssessCI_C01_C09_C12(t *testing.T) {
	tests := []struct {
		name        string
		policy      CIPolicy
		pipelines   []Pipeline
		code        FindingCode
		disposition Disposition
		applicable  bool
	}{
		{"current success", CIPolicyRequired, []Pipeline{{ID: 1, MRIID: 4, Kind: PipelineBranch, SourceSHA: "source", AssociatedWithMR: true, Status: "success"}}, "", "", true},
		{"older green never counts C02", CIPolicyRequired, []Pipeline{{ID: 1, MRIID: 4, Kind: PipelineBranch, SourceSHA: "old", AssociatedWithMR: true, Status: "success"}}, FindingMissingRequiredPipeline, DispositionWaiting, false},
		{"exact branch pipeline needs no MR association", CIPolicyRequired, []Pipeline{{ID: 1, MRIID: 4, Kind: PipelineBranch, SourceSHA: "source", Status: "success"}}, "", "", true},
		{"failed C03", CIPolicyRequired, []Pipeline{{ID: 2, MRIID: 4, Kind: PipelineDetachedMR, SourceSHA: "source", AssociatedWithMR: true, Status: "failed", FailedJobIDs: []int64{9, 3}}}, FindingCIFailed, DispositionActionRequired, true},
		{"skipped is a blocking failure", CIPolicyRequired, []Pipeline{{ID: 12, MRIID: 4, Kind: PipelineBranch, SourceSHA: "source", Status: "skipped"}}, FindingCIFailed, DispositionActionRequired, true},
		{"exact detached pipeline needs no MR association", CIPolicyRequired, []Pipeline{{ID: 2, MRIID: 4, Kind: PipelineDetachedMR, SourceSHA: "source", Status: "success"}}, "", "", true},
		{"unknown pipeline kind C05", CIPolicyRequired, []Pipeline{{ID: 2, MRIID: 4, Kind: PipelineUnknown, SourceSHA: "source", Status: "success"}}, FindingAmbiguousPipeline, DispositionHumanRequired, false},
		{"merged result parents forward C04", CIPolicyRequired, []Pipeline{{ID: 3, MRIID: 4, Kind: PipelineMergedResult, SourceSHA: "source", AssociatedWithMR: true, SyntheticParents: []string{"source", "target"}, Status: "success"}}, "", "", true},
		{"merged result parents reverse C04", CIPolicyRequired, []Pipeline{{ID: 3, MRIID: 4, Kind: PipelineMergedResult, SourceSHA: "source", AssociatedWithMR: true, SyntheticParents: []string{"target", "source"}, Status: "success"}}, "", "", true},
		{"merged association unknown C05", CIPolicyRequired, []Pipeline{{ID: 3, MRIID: 4, Kind: PipelineMergedResult, SourceSHA: "source", SyntheticParents: []string{"target", "source"}, Status: "success"}}, FindingAmbiguousPipeline, DispositionHumanRequired, false},
		{"aggregate manual waits C06", CIPolicyRequired, []Pipeline{{ID: 4, MRIID: 4, Kind: PipelineBranch, SourceSHA: "source", AssociatedWithMR: true, Status: "manual"}}, FindingPipelineRunning, DispositionWaiting, true},
		{"required missing C07", CIPolicyRequired, nil, FindingMissingRequiredPipeline, DispositionWaiting, false},
		{"optional missing C08", CIPolicyOptional, nil, "", "", false},
		{"unknown policy C09", CIPolicyUnknown, nil, FindingCIPolicyUnknown, DispositionHumanRequired, false},
		{"unknown status C12", CIPolicyRequired, []Pipeline{{ID: 5, MRIID: 4, Kind: PipelineBranch, SourceSHA: "source", AssociatedWithMR: true, Status: "new_gitlab_status"}}, FindingPipelineStatusUnknown, DispositionHumanRequired, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := AssessCI(ciMember(), tt.policy, tt.pipelines)
			if got.Applicable != tt.applicable {
				t.Fatalf("applicable=%v, want %v", got.Applicable, tt.applicable)
			}
			if tt.code == "" {
				if len(got.Findings) != 0 {
					t.Fatalf("unexpected findings %+v", got.Findings)
				}
				return
			}
			if len(got.Findings) != 1 || got.Findings[0].Code != tt.code || got.Findings[0].Disposition != tt.disposition {
				t.Fatalf("findings=%+v", got.Findings)
			}
			if tt.code == FindingCIFailed && len(tt.pipelines[0].FailedJobIDs) > 0 &&
				!slices.Equal(got.Findings[0].FailedJobs, []int64{3, 9}) {
				t.Fatalf("failed jobs not pinned deterministically: %v", got.Findings[0].FailedJobs)
			}
		})
	}
}

func TestAssessCIRetryUsesNewestExactPipeline(t *testing.T) {
	old := Pipeline{ID: 10, MRIID: 4, Kind: PipelineBranch, SourceSHA: "source", AssociatedWithMR: true, Status: "success"}
	retry := Pipeline{ID: 11, MRIID: 4, Kind: PipelineBranch, SourceSHA: "source", AssociatedWithMR: true, Status: "failed"}
	got := AssessCI(ciMember(), CIPolicyRequired, []Pipeline{old, retry})
	if got.PipelineID != 11 || len(got.Findings) != 1 || got.Findings[0].Code != FindingCIFailed {
		t.Fatalf("got %+v", got)
	}
}

func FuzzMergedResultParentOrder(f *testing.F) {
	f.Add("source", "target", true)
	f.Fuzz(func(t *testing.T, a, b string, reverse bool) {
		if a == b || a == "" || b == "" {
			t.Skip()
		}
		parents := []string{a, b}
		if reverse {
			parents[0], parents[1] = parents[1], parents[0]
		}
		if !sameTwoParents(parents, a, b) {
			t.Fatalf("parent order should not matter: %q", parents)
		}
		if sameTwoParents(append(parents, "third"), a, b) {
			t.Fatal("three-parent synthetic commit must not count")
		}
	})
}
