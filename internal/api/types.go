package api

// APIVersion is the media-level version emitted by this package.
const APIVersion = "mrstack/v1"

type CommandName string

const (
	CommandDoctor          CommandName = "doctor"
	CommandCheck           CommandName = "check"
	CommandRestackStart    CommandName = "restack.start"
	CommandRestackPlan     CommandName = "restack.plan"
	CommandRestackContinue CommandName = "restack.continue"
	CommandRestackAbort    CommandName = "restack.abort"
	CommandRestackRecover  CommandName = "restack.recover"
	CommandRestackAbandon  CommandName = "restack.abandon"
	CommandCILogs          CommandName = "ci.logs"
	CommandHistoryShow     CommandName = "history.show"
	CommandHistoryAlias    CommandName = "history.alias"
	CommandHistoryPrune    CommandName = "history.prune"
	CommandStackCreate     CommandName = "stack.create"
	CommandStackAdd        CommandName = "stack.add"
	CommandStackList       CommandName = "stack.list"
	CommandUnknown         CommandName = "unknown"
)

type OutcomeStatus string
type OutcomeClass string
type OutcomeCode string

const (
	StatusSucceeded OutcomeStatus = "succeeded"
	StatusFailed    OutcomeStatus = "failed"

	ClassAuthoritative OutcomeClass = "authoritative"
	ClassInvalidInput  OutcomeClass = "invalid_input"
	ClassUnavailable   OutcomeClass = "unavailable"
	ClassInternal      OutcomeClass = "internal"

	CodeOK                      OutcomeCode = "ok"
	CodeInvalidArguments        OutcomeCode = "invalid_arguments"
	CodeInvalidSelector         OutcomeCode = "invalid_selector"
	CodeUnknownCommand          OutcomeCode = "unknown_command"
	CodeNotGitRepository        OutcomeCode = "not_git_repository"
	CodeGitUnavailable          OutcomeCode = "git_unavailable"
	CodeGlabUnavailable         OutcomeCode = "glab_unavailable"
	CodeAuthenticationFailed    OutcomeCode = "authentication_failed"
	CodeGitLabTransportFailed   OutcomeCode = "gitlab_transport_failed"
	CodeGitTransportFailed      OutcomeCode = "git_transport_failed"
	CodeServerModeUndetermined  OutcomeCode = "server_mode_undetermined"
	CodePrerequisiteUnsupported OutcomeCode = "prerequisite_unsupported"
	CodeJournalUnavailable      OutcomeCode = "journal_unavailable"
	CodeInternalInvariantFailed OutcomeCode = "internal_invariant_failed"
)

type Disposition string

const (
	DispositionActionRequired Disposition = "action_required"
	DispositionWaiting        Disposition = "waiting"
	DispositionHumanRequired  Disposition = "human_required"
	DispositionReady          Disposition = "ready"
	DispositionComplete       Disposition = "complete"
	DispositionInvalid        Disposition = "invalid"
)

type Envelope struct {
	APIVersion   string         `json:"api_version"`
	GeneratedAt  string         `json:"generated_at"`
	Command      Command        `json:"command"`
	Outcome      Outcome        `json:"outcome"`
	Disposition  *Disposition   `json:"disposition"`
	Stack        *Stack         `json:"stack"`
	Findings     []Finding      `json:"findings"`
	Evidence     []Evidence     `json:"evidence"`
	Remediations []Remediation  `json:"remediations"`
	Session      *Session       `json:"session"`
	Data         map[string]any `json:"data"`
	Error        *Error         `json:"error"`
}

type Command struct {
	Name         CommandName `json:"name"`
	InvocationID string      `json:"invocation_id"`
}

type Outcome struct {
	Status    OutcomeStatus `json:"status"`
	Class     OutcomeClass  `json:"class"`
	Code      OutcomeCode   `json:"code"`
	ExitCode  int           `json:"exit_code"`
	Retryable bool          `json:"retryable"`
}

type Error struct {
	Message      string  `json:"message"`
	Tool         *string `json:"tool,omitempty"`
	ToolExitCode *int    `json:"tool_exit_code,omitempty"`
}

type Selector struct {
	Kind  string `json:"kind"`
	Value string `json:"value"`
}

type RemoteEndpoint struct {
	Host    string `json:"host"`
	Project string `json:"project"`
}

type Remote struct {
	Name      string         `json:"name"`
	Selection string         `json:"selection"`
	Fetch     RemoteEndpoint `json:"fetch"`
	Push      RemoteEndpoint `json:"push"`
}

type Project struct {
	Host              string `json:"host"`
	ID                string `json:"id"`
	PathWithNamespace string `json:"path_with_namespace"`
	WebURL            string `json:"web_url"`
	DefaultBranch     string `json:"default_branch"`
}

type Base struct {
	Branch string `json:"branch"`
	SHA    string `json:"sha"`
}

type Stack struct {
	StackID        string          `json:"stack_id"`
	Alias          *string         `json:"alias"`
	SnapshotID     string          `json:"snapshot_id"`
	ObservedAt     string          `json:"observed_at"`
	Selector       Selector        `json:"selector"`
	GitLabMode     string          `json:"gitlab_mode"`
	Remote         Remote          `json:"remote"`
	Project        Project         `json:"project"`
	Base           Base            `json:"base"`
	Members        []Member        `json:"members"`
	AffectedSuffix *AffectedSuffix `json:"affected_suffix"`
}

type Author struct {
	ID       string `json:"id"`
	Username string `json:"username"`
}

type Layer struct {
	BoundarySHA    *string `json:"boundary_sha"`
	BoundarySource string  `json:"boundary_source"`
	CommitCount    *int    `json:"commit_count"`
}

type FailedJob struct {
	ID     string `json:"id"`
	Name   string `json:"name"`
	Status string `json:"status"`
	WebURL string `json:"web_url"`
}

type Pipeline struct {
	Applicability   string         `json:"applicability"`
	Currentness     string         `json:"currentness"`
	Kind            *string        `json:"kind"`
	ID              *string        `json:"id"`
	SHA             *string        `json:"sha"`
	SourceSHA       *string        `json:"source_sha"`
	TargetSHA       *string        `json:"target_sha"`
	BlockingStatus  string         `json:"blocking_status"`
	WebURL          *string        `json:"web_url"`
	FailedJobs      []FailedJob    `json:"failed_jobs"`
	ProviderDetails map[string]any `json:"provider_details,omitempty"`
}

type Member struct {
	Position         int       `json:"position"`
	IID              int       `json:"iid"`
	State            string    `json:"state"`
	WebURL           string    `json:"web_url"`
	SourceBranch     string    `json:"source_branch"`
	TargetBranch     string    `json:"target_branch"`
	SourceSHA        *string   `json:"source_sha"`
	TargetSHA        *string   `json:"target_sha"`
	TargetResolution string    `json:"target_resolution"`
	Author           Author    `json:"author"`
	Layer            Layer     `json:"layer"`
	Alignment        string    `json:"alignment"`
	ConflictStatus   string    `json:"conflict_status"`
	Pipeline         *Pipeline `json:"pipeline"`
}

type AffectedSuffix struct {
	FromPosition int   `json:"from_position"`
	MemberIIDs   []int `json:"member_iids"`
}

type FindingScope struct {
	Kind       string  `json:"kind"`
	MRIID      *int    `json:"mr_iid"`
	Position   *int    `json:"position"`
	PipelineID *string `json:"pipeline_id"`
	JobID      *string `json:"job_id"`
	CommitSHA  *string `json:"commit_sha"`
}

type Finding struct {
	FindingID    string         `json:"finding_id"`
	Code         string         `json:"code"`
	Disposition  Disposition    `json:"disposition"`
	Scope        FindingScope   `json:"scope"`
	Summary      string         `json:"summary"`
	Details      map[string]any `json:"details"`
	EvidenceRefs []string       `json:"evidence_refs"`
	FirstSeenAt  string         `json:"first_seen_at"`
	LastSeenAt   string         `json:"last_seen_at"`
}

// Evidence contains only allowlisted fields for its kind. Fields are flattened
// into the evidence object by MarshalJSON.
type Evidence struct {
	EvidenceID string         `json:"evidence_id"`
	Kind       string         `json:"kind"`
	Fields     map[string]any `json:"-"`
}

type RemediationMember struct {
	IID      int `json:"iid"`
	Position int `json:"position"`
}

type RemediationLayer struct {
	MRIID       int     `json:"mr_iid"`
	BoundarySHA *string `json:"boundary_sha"`
	CommitSHA   *string `json:"commit_sha"`
}

type SessionWorktree struct {
	Path     string `json:"path"`
	GitState string `json:"git_state"`
}

type RequiredWork struct {
	Kind        string   `json:"kind"`
	Paths       []string `json:"paths,omitempty"`
	Staging     string   `json:"staging,omitempty"`
	PipelineID  string   `json:"pipeline_id,omitempty"`
	JobIDs      []string `json:"job_ids,omitempty"`
	Options     []string `json:"options,omitempty"`
	ReasonCode  string   `json:"reason_code,omitempty"`
	Branch      string   `json:"branch,omitempty"`
	ExpectedSHA string   `json:"expected_sha,omitempty"`
}

type ActionRequirements struct {
	SnapshotID *string  `json:"snapshot_id"`
	SessionID  *string  `json:"session_id"`
	PlanID     *string  `json:"plan_id"`
	PipelineID *string  `json:"pipeline_id"`
	JobIDs     []string `json:"job_ids"`
}

type Action struct {
	Kind                 string             `json:"kind"`
	Argv                 []string           `json:"argv"`
	CWD                  string             `json:"cwd"`
	Mutates              bool               `json:"mutates"`
	ConfirmationRequired bool               `json:"confirmation_required"`
	Preconditions        []string           `json:"preconditions"`
	Requires             ActionRequirements `json:"requires"`
}

type Remediation struct {
	RemediationID string             `json:"remediation_id"`
	FindingID     string             `json:"finding_id"`
	Kind          string             `json:"kind"`
	SnapshotID    *string            `json:"snapshot_id"`
	SessionID     *string            `json:"session_id"`
	PlanID        *string            `json:"plan_id"`
	Member        *RemediationMember `json:"member"`
	Layer         *RemediationLayer  `json:"layer"`
	Worktree      *SessionWorktree   `json:"worktree"`
	RequiredWork  *RequiredWork      `json:"required_work"`
	EvidenceRefs  []string           `json:"evidence_refs"`
	Actions       []Action           `json:"actions"`
}

type CurrentLayer struct {
	MRIID             int      `json:"mr_iid"`
	OriginalCommitSHA string   `json:"original_commit_sha"`
	OntoSHA           string   `json:"onto_sha"`
	ConflictedPaths   []string `json:"conflicted_paths"`
}

type PublicationRef struct {
	Branch         string  `json:"branch"`
	OldSHA         string  `json:"old_sha"`
	NewSHA         *string `json:"new_sha"`
	CurrentSHA     *string `json:"current_sha"`
	Classification string  `json:"classification"`
}

type Publication struct {
	State string           `json:"state"`
	Refs  []PublicationRef `json:"refs"`
}

type TargetUpdate struct {
	MRIID             int     `json:"mr_iid"`
	FromTarget        string  `json:"from_target"`
	ToTarget          string  `json:"to_target"`
	ExpectedSourceSHA string  `json:"expected_source_sha"`
	ExpectedMRState   string  `json:"expected_mr_state"`
	Status            string  `json:"status"`
	AttemptCount      int     `json:"attempt_count"`
	LastAttemptAt     *string `json:"last_attempt_at"`
}

type Session struct {
	SessionID               string           `json:"session_id"`
	State                   string           `json:"state"`
	SnapshotID              string           `json:"snapshot_id"`
	PlanID                  *string          `json:"plan_id"`
	CreatedAt               string           `json:"created_at"`
	UpdatedAt               string           `json:"updated_at"`
	Remote                  Remote           `json:"remote"`
	Worktree                *SessionWorktree `json:"worktree"`
	AffectedMemberIIDs      []int            `json:"affected_member_iids"`
	CurrentLayer            *CurrentLayer    `json:"current_layer"`
	Publication             Publication      `json:"publication"`
	TargetUpdate            *TargetUpdate    `json:"target_update"`
	SignatureLossAuthorized bool             `json:"signature_loss_authorized"`
	Resumable               bool             `json:"resumable"`
	Abortable               bool             `json:"abortable"`
}

type PlanOverride struct {
	MRIID       int    `json:"mr_iid"`
	BoundarySHA string `json:"boundary_sha"`
}

type PlanLayer struct {
	Position     int      `json:"position"`
	MRIID        int      `json:"mr_iid"`
	SourceBranch string   `json:"source_branch"`
	OldSHA       string   `json:"old_sha"`
	BoundarySHA  string   `json:"boundary_sha"`
	Commits      []string `json:"commits"`
}

type Plan struct {
	PlanID     string         `json:"plan_id"`
	SnapshotID string         `json:"snapshot_id"`
	State      string         `json:"state"`
	CreatedAt  string         `json:"created_at"`
	Remote     Remote         `json:"remote"`
	Overrides  []PlanOverride `json:"overrides"`
	Layers     []PlanLayer    `json:"layers"`
}

type Capability struct {
	Name    string `json:"name"`
	Status  string `json:"status"`
	Summary string `json:"summary"`
}

type DoctorData struct {
	RequestedMode string       `json:"requested_mode"`
	DetectedMode  *string      `json:"detected_mode"`
	EffectiveMode string       `json:"effective_mode"`
	ServerVersion *string      `json:"server_version"`
	GitVersion    string       `json:"git_version"`
	GlabVersion   string       `json:"glab_version"`
	Capabilities  []Capability `json:"capabilities"`
}

type HistoryObservation struct {
	ObservationID string      `json:"observation_id"`
	ObservedAt    string      `json:"observed_at"`
	SnapshotID    string      `json:"snapshot_id"`
	Disposition   Disposition `json:"disposition"`
	MemberIIDs    []int       `json:"member_iids"`
	FindingCodes  []string    `json:"finding_codes"`
}

type FindingInterval struct {
	FindingID   string  `json:"finding_id"`
	Code        string  `json:"code"`
	FirstSeenAt string  `json:"first_seen_at"`
	LastSeenAt  string  `json:"last_seen_at"`
	ResolvedAt  *string `json:"resolved_at"`
}

type HistoryData struct {
	StackID          string               `json:"stack_id"`
	Alias            *string              `json:"alias"`
	Observations     []HistoryObservation `json:"observations"`
	FindingIntervals []FindingInterval    `json:"finding_intervals"`
	NextCursor       *string              `json:"next_cursor"`
}

type HistoryAliasData struct {
	StackID       string  `json:"stack_id"`
	PreviousAlias *string `json:"previous_alias"`
	Alias         *string `json:"alias"`
}

type HistoryPruneData struct {
	StackID             *string `json:"stack_id"`
	Before              string  `json:"before"`
	DeletedObservations int     `json:"deleted_observations"`
	DeletedEvidence     int     `json:"deleted_evidence"`
	PreservedRecords    int     `json:"preserved_records"`
}

// StackData is the published shape of a user-curated named stack. It mirrors
// internal/stackstore.Stack so the transport layer never exposes the store
// type directly.
type StackData struct {
	Name       string `json:"name"`
	Host       string `json:"host"`
	Project    string `json:"project"`
	MemberIIDs []int  `json:"member_iids"`
	CreatedAt  string `json:"created_at"`
	UpdatedAt  string `json:"updated_at"`
}

type LogRequest struct {
	PipelineID string   `json:"pipeline_id"`
	JobIDs     []string `json:"job_ids"`
}

type LogBudget struct {
	RequestedBytes int    `json:"requested_bytes"`
	EffectiveBytes int    `json:"effective_bytes"`
	HardMaxBytes   int    `json:"hard_max_bytes"`
	Allocation     string `json:"allocation"`
}

type LogEntry struct {
	PipelineID          string `json:"pipeline_id"`
	JobID               string `json:"job_id"`
	JobName             string `json:"job_name"`
	Status              string `json:"status"`
	Text                string `json:"text"`
	ReturnedBytes       int    `json:"returned_bytes"`
	TotalBytes          *int   `json:"total_bytes"`
	Truncated           bool   `json:"truncated"`
	InvalidUTF8Replaced bool   `json:"invalid_utf8_replaced"`
}
