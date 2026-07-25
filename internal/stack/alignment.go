package stack

import "fmt"

type IsAncestor func(ancestor, descendant string) (bool, error)

type AlignmentResult struct {
	Aligned             bool
	AffectedSuffixStart int
	Findings            []Finding
}

// AssessAlignment returns the first layer requiring replay. Zero means the
// base-to-front edge is stale; i>0 means member i is the first stale successor.
// A fully aligned stack uses -1.
func AssessAlignment(s Stack, isAncestor IsAncestor) AlignmentResult {
	result := AlignmentResult{Aligned: true, AffectedSuffixStart: -1}
	if len(s.Members) == 0 {
		return result
	}
	edges := make([][2]string, 0, len(s.Members))
	edges = append(edges, [2]string{s.BaseSHA, s.Members[0].SourceSHA})
	for i := 1; i < len(s.Members); i++ {
		edges = append(edges, [2]string{s.Members[i-1].SourceSHA, s.Members[i].SourceSHA})
	}
	for i, edge := range edges {
		ok, err := isAncestor(edge[0], edge[1])
		if err != nil {
			f := finding(FindingAncestryUnknown, DispositionHumanRequired, fmt.Sprintf("cannot establish ancestry: %v", err), s.Members[i].IID)
			f.LayerIndex = i
			result.Findings = append(result.Findings, f)
			result.Aligned = false
			return result
		}
		if !ok {
			f := finding(FindingUnaligned, DispositionActionRequired, "layer is not based on its current predecessor", s.Members[i].IID)
			f.LayerIndex = i
			result.Findings = append(result.Findings, f)
			result.Aligned = false
			result.AffectedSuffixStart = i
			return result
		}
	}
	return result
}
