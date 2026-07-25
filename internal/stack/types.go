// Package stack contains the I/O-free domain rules for discovering and
// assessing a stack of GitLab merge requests.
package stack

import "fmt"

const MaxDepth = 10

type MRState string

const (
	StateOpen   MRState = "opened"
	StateClosed MRState = "closed"
	StateMerged MRState = "merged"
)

// MergeRequest is a normalized GitLab observation. Adapters are responsible
// for resolving the exact revisions and proof flags; this package never guesses
// from branch names or journal history.
type MergeRequest struct {
	IID             int
	ProjectID       string
	SourceProjectID string
	TargetProjectID string
	SourceBranch    string
	TargetBranch    string
	SourceSHA       string
	TargetSHA       string
	State           MRState

	SourceBranchExists bool
	TargetBranchExists bool

	// The following fields qualify a merged predecessor.
	IntegrationRevision string
	IntegrationInBase   bool
	HistoricalTargetSHA string
}

func (m MergeRequest) sameProject(project string) bool {
	return m.ProjectID == project &&
		(m.SourceProjectID == "" || m.SourceProjectID == project) &&
		(m.TargetProjectID == "" || m.TargetProjectID == project)
}

type SelectorKind string

const (
	SelectBranch        SelectorKind = "branch"
	SelectMergeRequest  SelectorKind = "merge_request"
	SelectCurrentBranch SelectorKind = "current_branch"
)

type Selector struct {
	Kind   SelectorKind
	Branch string
	IID    int
}

type Mode string

const (
	ModeLegacy Mode = "legacy"
	ModeNative Mode = "native"
)

type DiscoveryInput struct {
	ProjectID     string
	DefaultBranch string
	BaseSHA       string
	Mode          Mode
	Selector      Selector
	// MergeRequests must contain all open, closed, and merged candidates for
	// every branch edge reachable from the selected MR.
	MergeRequests []MergeRequest
}

type Stack struct {
	ProjectID         string
	DefaultBranch     string
	BaseSHA           string
	Members           []MergeRequest
	MergedPredecessor *MergeRequest
}

func (s Stack) Validate() error {
	if s.ProjectID == "" || s.DefaultBranch == "" || s.BaseSHA == "" {
		return fmt.Errorf("stack identity, default branch, and base revision are required")
	}
	if len(s.Members) == 0 || len(s.Members) > MaxDepth {
		return fmt.Errorf("stack depth must be between 1 and %d", MaxDepth)
	}
	for i, mr := range s.Members {
		if mr.State != StateOpen {
			return fmt.Errorf("member %d is not open", mr.IID)
		}
		if !mr.sameProject(s.ProjectID) {
			return fmt.Errorf("member %d is cross-project", mr.IID)
		}
		if i == 0 {
			advanced := s.MergedPredecessor != nil &&
				mr.TargetBranch == s.MergedPredecessor.SourceBranch
			if mr.TargetBranch != s.DefaultBranch && !advanced {
				return fmt.Errorf("front targets %q, not default branch %q", mr.TargetBranch, s.DefaultBranch)
			}
		} else if mr.TargetBranch != s.Members[i-1].SourceBranch {
			return fmt.Errorf("member %d does not target predecessor", mr.IID)
		}
	}
	return nil
}
