package stack

import (
	"errors"
	"testing"
)

func TestAssessAlignmentAffectedSuffix(t *testing.T) {
	s := Stack{BaseSHA: "base", Members: []MergeRequest{
		{IID: 1, SourceSHA: "one"},
		{IID: 2, SourceSHA: "two"},
		{IID: 3, SourceSHA: "three"},
	}}
	tests := []struct {
		name      string
		staleEdge int
		wantStart int
		aligned   bool
	}{
		{"aligned", -1, -1, true},
		{"base moved replays all", 0, 0, false},
		{"middle predecessor moved", 1, 1, false},
		{"last predecessor moved", 2, 2, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			calls := 0
			got := AssessAlignment(s, func(_, _ string) (bool, error) {
				index := calls
				calls++
				return index != tt.staleEdge, nil
			})
			if got.Aligned != tt.aligned || got.AffectedSuffixStart != tt.wantStart {
				t.Fatalf("got %+v", got)
			}
			wantCalls := len(s.Members)
			if tt.staleEdge >= 0 {
				wantCalls = tt.staleEdge + 1
			}
			if calls != wantCalls {
				t.Fatalf("ancestry calls = %d, want %d", calls, wantCalls)
			}
		})
	}
}

func TestAssessAlignmentFailsClosedOnUnknownAncestry(t *testing.T) {
	s := Stack{BaseSHA: "base", Members: []MergeRequest{{IID: 7, SourceSHA: "tip"}}}
	got := AssessAlignment(s, func(_, _ string) (bool, error) { return false, errors.New("missing object") })
	if got.Aligned || got.AffectedSuffixStart != -1 || len(got.Findings) != 1 ||
		got.Findings[0].Code != FindingAncestryUnknown ||
		got.Findings[0].Disposition != DispositionHumanRequired {
		t.Fatalf("got %+v", got)
	}
}

func FuzzAffectedSuffixIsFirstFalseEdge(f *testing.F) {
	f.Add(uint16(0xffff), uint8(5))
	f.Fuzz(func(t *testing.T, bits uint16, rawDepth uint8) {
		depth := int(rawDepth%MaxDepth) + 1
		s := Stack{BaseSHA: "base", Members: make([]MergeRequest, depth)}
		for i := range depth {
			s.Members[i] = MergeRequest{IID: i + 1, SourceSHA: sha(i + 3)}
		}
		index := 0
		got := AssessAlignment(s, func(_, _ string) (bool, error) {
			ok := bits&(1<<index) != 0
			index++
			return ok, nil
		})
		want := -1
		for i := range depth {
			if bits&(1<<i) == 0 {
				want = i
				break
			}
		}
		if got.AffectedSuffixStart != want || got.Aligned != (want == -1) {
			t.Fatalf("bits=%b depth=%d got=%+v want=%d", bits, depth, got, want)
		}
	})
}
