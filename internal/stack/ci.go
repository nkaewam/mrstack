package stack

import "sort"

type CIPolicy string

const (
	CIPolicyRequired CIPolicy = "required"
	CIPolicyOptional CIPolicy = "optional"
	CIPolicyUnknown  CIPolicy = "unknown"
)

type PipelineKind string

const (
	PipelineBranch       PipelineKind = "branch"
	PipelineDetachedMR   PipelineKind = "detached_merge_request"
	PipelineMergedResult PipelineKind = "merged_result"
	PipelineUnknown      PipelineKind = "unknown"
)

type Pipeline struct {
	ID               int64
	MRIID            int
	Kind             PipelineKind
	SHA              string
	SourceSHA        string
	AssociatedWithMR bool
	SyntheticParents []string
	Status           string
	WebURL           string
	FailedJobIDs     []int64
}

type CIAssessment struct {
	MRIID      int
	Applicable bool
	PipelineID int64
	Findings   []Finding
}

// AssessCI chooses only an exact-current relevant pipeline. Among retries for
// the same exact revision, the greatest GitLab pipeline ID is current.
func AssessCI(member MergeRequest, policy CIPolicy, pipelines []Pipeline) CIAssessment {
	result := CIAssessment{MRIID: member.IID}
	var exact []Pipeline
	var ambiguous bool
	for _, pipeline := range pipelines {
		if pipeline.MRIID != member.IID || pipeline.SourceSHA != member.SourceSHA {
			continue
		}
		switch pipeline.Kind {
		case PipelineBranch, PipelineDetachedMR:
			exact = append(exact, pipeline)
		case PipelineMergedResult:
			if !pipeline.AssociatedWithMR || !sameTwoParents(pipeline.SyntheticParents, member.SourceSHA, member.TargetSHA) {
				ambiguous = true
				continue
			}
			exact = append(exact, pipeline)
		default:
			ambiguous = true
		}
	}
	if ambiguous {
		result.Findings = []Finding{finding(FindingAmbiguousPipeline, DispositionHumanRequired, "current pipeline association or merged-result parents are ambiguous", member.IID)}
		return result
	}
	if len(exact) == 0 {
		switch policy {
		case CIPolicyRequired:
			result.Findings = []Finding{finding(FindingMissingRequiredPipeline, DispositionWaiting, "no exact-current required pipeline exists", member.IID)}
		case CIPolicyOptional:
			result.Applicable = false
		default:
			result.Findings = []Finding{finding(FindingCIPolicyUnknown, DispositionHumanRequired, "pipeline requiredness cannot be established", member.IID)}
		}
		return result
	}

	sort.Slice(exact, func(i, j int) bool { return exact[i].ID > exact[j].ID })
	pipeline := exact[0]
	result.Applicable = true
	result.PipelineID = pipeline.ID
	switch pipeline.Status {
	case "success", "skipped":
		return result
	case "failed", "canceled":
		jobs := append([]int64(nil), pipeline.FailedJobIDs...)
		sort.Slice(jobs, func(i, j int) bool { return jobs[i] < jobs[j] })
		f := finding(FindingCIFailed, DispositionActionRequired, "exact-current pipeline failed", member.IID)
		f.PipelineID = pipeline.ID
		f.FailedJobs = jobs
		result.Findings = []Finding{f}
	case "created", "pending", "running", "preparing", "waiting_for_resource", "manual", "scheduled":
		f := finding(FindingPipelineRunning, DispositionWaiting, "exact-current pipeline has not reached a terminal result", member.IID)
		f.PipelineID = pipeline.ID
		result.Findings = []Finding{f}
	default:
		f := finding(FindingPipelineStatusUnknown, DispositionHumanRequired, "exact-current pipeline has an unrecognized aggregate status", member.IID)
		f.PipelineID = pipeline.ID
		result.Findings = []Finding{f}
	}
	return result
}

func sameTwoParents(parents []string, source, target string) bool {
	return len(parents) == 2 &&
		((parents[0] == source && parents[1] == target) ||
			(parents[0] == target && parents[1] == source))
}

func AssessStackCI(s Stack, policy CIPolicy, pipelines []Pipeline) []Finding {
	var findings []Finding
	for _, member := range s.Members {
		findings = append(findings, AssessCI(member, policy, pipelines).Findings...)
	}
	return findings
}
