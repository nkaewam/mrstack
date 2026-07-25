package stack

type Disposition string

const (
	DispositionInvalid        Disposition = "invalid"
	DispositionActionRequired Disposition = "action_required"
	DispositionHumanRequired  Disposition = "human_required"
	DispositionWaiting        Disposition = "waiting"
	DispositionReady          Disposition = "ready"
	DispositionComplete       Disposition = "complete"
)

type FindingCode string

const (
	FindingNoStackSelected            FindingCode = "no_stack_selected"
	FindingAmbiguousSelector          FindingCode = "ambiguous_selector"
	FindingFork                       FindingCode = "fork"
	FindingCycle                      FindingCode = "cycle"
	FindingAmbiguousEdge              FindingCode = "ambiguous_edge"
	FindingCrossProjectMember         FindingCode = "cross_project_member"
	FindingNonDefaultBase             FindingCode = "non_default_base"
	FindingMaximumDepthExceeded       FindingCode = "maximum_depth_exceeded"
	FindingMissingActiveBranch        FindingCode = "missing_active_branch"
	FindingAmbiguousMergedPredecessor FindingCode = "ambiguous_merged_predecessor"
	FindingClosedMember               FindingCode = "closed_member"
	FindingOutOfOrderMerge            FindingCode = "out_of_order_merge"
	FindingNativeRetargetPending      FindingCode = "native_retarget_pending"
	FindingUnaligned                  FindingCode = "unaligned"
	FindingAncestryUnknown            FindingCode = "ancestry_unknown"
	FindingCIFailed                   FindingCode = "ci_failed"
	FindingAmbiguousPipeline          FindingCode = "ambiguous_pipeline"
	FindingMissingRequiredPipeline    FindingCode = "missing_required_pipeline"
	FindingCIPolicyUnknown            FindingCode = "ci_policy_unknown"
	FindingPipelineStatusUnknown      FindingCode = "pipeline_status_unknown"
	FindingPipelineRunning            FindingCode = "pipeline_running"
	FindingMergeConflict              FindingCode = "merge_conflict"
	FindingMergeabilityChecking       FindingCode = "mergeability_checking"
	FindingInvalidArguments           FindingCode = "invalid_arguments"
)

type Finding struct {
	Code        FindingCode
	Disposition Disposition
	Message     string
	MRIID       int
	LayerIndex  int
	PipelineID  int64
	FailedJobs  []int64
}

var dispositionRank = map[Disposition]int{
	DispositionInvalid:        6,
	DispositionActionRequired: 5,
	DispositionHumanRequired:  4,
	DispositionWaiting:        3,
	DispositionReady:          2,
	DispositionComplete:       1,
}

// ResolveDisposition applies the documented precedence independently of
// finding order: invalid > action_required > human_required > waiting.
func ResolveDisposition(findings []Finding, terminal Disposition) Disposition {
	best := terminal
	if best == "" {
		best = DispositionReady
	}
	for _, finding := range findings {
		if dispositionRank[finding.Disposition] > dispositionRank[best] {
			best = finding.Disposition
		}
	}
	return best
}

func finding(code FindingCode, disposition Disposition, message string, mr int) Finding {
	return Finding{Code: code, Disposition: disposition, Message: message, MRIID: mr, LayerIndex: -1}
}
