package mission

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"time"
)

const (
	RecordSchema            = "ao.mission.record.v0.1"
	IntentSchema            = "ao.mission.operator-intent.v0.1"
	ResultSchema            = "ao.mission.operator-result.v0.1"
	RouteSchema             = "ao.mission.route-decision.v0.1"
	SnapshotSchema          = "ao.mission.governance-snapshot.v0.1"
	StepSchema              = "ao.mission.continuation-step.v0.1"
	SchedulerRequestSchema  = "ao.mission.scheduler-request.v0.1"
	SchedulerReadbackSchema = "ao.mission.scheduler-readback.v0.1"
	EventLoopDecisionSchema = "ao.mission.event-loop-decision.v0.1"
	KillSwitchSchema        = "ao.mission.kill-switch.v0.1"
	TelegramCommandSchema   = "ao.mission.telegram-command.v0.1"
	TelegramReadbackSchema  = "ao.mission.telegram-readback.v0.1"
	A2AAgentCardSchema      = "ao.mission.a2a-agent-card.v0.1"
	A2ATaskSchema           = "ao.mission.a2a-task.v0.1"
	ArtifactRefSchema       = "ao.mission.artifact-ref.v0.1"
	ErrorSchema             = "ao.mission.error.v0.1"
	ObjectiveWorkflowSchema = "ao.mission.objective-workflow-contract.v0.1"
)

type ArtifactRef struct {
	Schema     string `json:"schema"`
	Ref        string `json:"ref"`
	ContentRef string `json:"content_ref,omitempty"`
	Digest     string `json:"digest,omitempty"`
	Kind       string `json:"kind,omitempty"`
}

type Record struct {
	Schema                     string                      `json:"schema"`
	MissionID                  string                      `json:"mission_id"`
	CorrelationID              string                      `json:"correlation_id,omitempty"`
	Objective                  string                      `json:"objective"`
	ObjectiveDigest            string                      `json:"objective_digest"`
	ObjectiveRedacted          bool                        `json:"objective_redacted,omitempty"`
	Status                     string                      `json:"status"`
	SourceRecordStatus         string                      `json:"source_record_status,omitempty"`
	TerminalProjectionStatus   string                      `json:"terminal_projection_status,omitempty"`
	TerminalProjectionReadOnly bool                        `json:"terminal_projection_read_only,omitempty"`
	EffectiveOperatorStatus    string                      `json:"effective_operator_status,omitempty"`
	CreatedAtUTC               string                      `json:"created_at_utc"`
	UpdatedAtUTC               string                      `json:"updated_at_utc"`
	CurrentRoute               string                      `json:"current_route"`
	CurrentPhase               string                      `json:"current_phase"`
	Blockers                   []string                    `json:"blockers"`
	ExactNextAction            string                      `json:"exact_next_action"`
	ArtifactRefs               []ArtifactRef               `json:"artifact_refs"`
	Steps                      []ContinuationStep          `json:"steps"`
	RouteHistory               []RouteDecision             `json:"route_history,omitempty"`
	Evidence                   EvidenceSummary             `json:"evidence,omitempty"`
	GoalLease                  *GoalLease                  `json:"goal_lease,omitempty"`
	Checkpoints                []MissionCheckpoint         `json:"checkpoints,omitempty"`
	ReturnGate                 *ReturnGate                 `json:"return_gate,omitempty"`
	Reconciliation             *RouteReconciliation        `json:"route_reconciliation,omitempty"`
	WorkflowContract           *ObjectiveWorkflowContract  `json:"workflow_contract,omitempty"`
	CorrelationChainReferences []CorrelationChainReference `json:"correlation_chain_references,omitempty"`
	CorrelatedImports          []CorrelatedImportBinding   `json:"correlated_imports,omitempty"`
}

func (record *Record) UnmarshalJSON(data []byte) error {
	type alias Record
	var decoded alias
	if err := decodeStrictJSONObject(data, &decoded, "Mission record", map[string]string{
		"schema": "string", "mission_id": "string", "correlation_id": "string",
		"objective": "string", "objective_digest": "string", "objective_redacted": "boolean",
		"status": "string", "source_record_status": "string", "terminal_projection_status": "string",
		"terminal_projection_read_only": "boolean", "effective_operator_status": "string",
		"created_at_utc": "string", "updated_at_utc": "string",
		"current_route": "string", "current_phase": "string", "blockers": "array",
		"exact_next_action": "string", "artifact_refs": "array", "steps": "array",
		"route_history": "array", "evidence": "object", "goal_lease": "object",
		"checkpoints": "array", "return_gate": "object", "route_reconciliation": "object",
		"workflow_contract": "object", "correlation_chain_references": "array",
		"correlated_imports": "array",
	}, []string{
		"schema", "mission_id", "objective_digest", "status", "created_at_utc", "current_route",
	}); err != nil {
		return err
	}
	*record = Record(decoded)
	return nil
}

type ObjectiveWorkflowStage struct {
	Name   string `json:"name"`
	Status string `json:"status"`
	Reason string `json:"reason"`
}

func (stage *ObjectiveWorkflowStage) UnmarshalJSON(data []byte) error {
	type alias ObjectiveWorkflowStage
	var decoded alias
	if err := validateNoDuplicateJSONKeys(data); err != nil {
		return err
	}
	if err := decodeStrictJSONObject(data, &decoded, "objective workflow stage", map[string]string{
		"name": "string", "status": "string", "reason": "string",
	}, []string{"name", "status", "reason"}); err != nil {
		return err
	}
	*stage = ObjectiveWorkflowStage(decoded)
	return nil
}

type ObjectiveWorkflowContract struct {
	Schema              string                   `json:"schema"`
	Status              string                   `json:"status"`
	MissionID           string                   `json:"mission_id"`
	CorrelationID       string                   `json:"correlation_id"`
	ObjectiveDigest     string                   `json:"objective_digest"`
	RoutingClass        string                   `json:"routing_class"`
	AcceptanceStatus    string                   `json:"acceptance_status"`
	InitialRoute        string                   `json:"initial_route"`
	Stages              []ObjectiveWorkflowStage `json:"stages"`
	LifecycleCommands   []string                 `json:"lifecycle_commands"`
	ExactNextAction     string                   `json:"exact_next_action"`
	SafeToExecute       bool                     `json:"safe_to_execute"`
	ExecutesWork        bool                     `json:"executes_work"`
	ApprovesWork        bool                     `json:"approves_work"`
	MutatesRepositories bool                     `json:"mutates_repositories"`
	GeneratedAtUTC      string                   `json:"generated_at_utc"`
}

func (contract *ObjectiveWorkflowContract) UnmarshalJSON(data []byte) error {
	type alias ObjectiveWorkflowContract
	var decoded alias
	if err := validateNoDuplicateJSONKeys(data); err != nil {
		return err
	}
	if err := decodeStrictJSONObject(data, &decoded, "objective workflow contract", map[string]string{
		"schema": "string", "status": "string", "mission_id": "string",
		"correlation_id": "string", "objective_digest": "string",
		"routing_class": "string", "acceptance_status": "string",
		"initial_route": "string", "stages": "array",
		"lifecycle_commands": "array", "exact_next_action": "string",
		"safe_to_execute": "boolean", "executes_work": "boolean",
		"approves_work": "boolean", "mutates_repositories": "boolean",
		"generated_at_utc": "string",
	}, []string{
		"schema", "status", "mission_id", "correlation_id", "objective_digest",
		"routing_class", "acceptance_status", "initial_route", "stages",
		"lifecycle_commands", "exact_next_action", "safe_to_execute",
		"executes_work", "approves_work", "mutates_repositories", "generated_at_utc",
	}); err != nil {
		return err
	}
	*contract = ObjectiveWorkflowContract(decoded)
	return nil
}

type EvidenceSummary struct {
	AtlasWorkgraph          *NodeCounts                        `json:"atlas_workgraph,omitempty"`
	AtlasRecommendation     *AtlasRecommendationReadbackCounts `json:"atlas_recommendation,omitempty"`
	AtlasFinalSynthesis     *AtlasFinalSynthesisReadbackCounts `json:"atlas_final_synthesis,omitempty"`
	FoundryRollup           *FoundryRollupCounts               `json:"foundry_rollup,omitempty"`
	SchedulerReadback       *SchedulerEvidenceCounts           `json:"scheduler_readback,omitempty"`
	SchedulerRecovery       *SchedulerRecoveryCounts           `json:"scheduler_recovery,omitempty"`
	LedgerCompaction        *LedgerCompactionCounts            `json:"ledger_compaction,omitempty"`
	IssueRepairSupervisor   *IssueRepairSupervisorState        `json:"issue_repair_supervisor,omitempty"`
	AONextCandidate         *AONextCandidateProjection         `json:"ao_next_candidate,omitempty"`
	AONextJournalProjection *AONextJournalProjection           `json:"ao_next_journal_projection,omitempty"`
}

type AONextCandidateProjection struct {
	Schema                string `json:"schema"`
	Status                string `json:"status"`
	RunID                 string `json:"run_id"`
	TaskID                string `json:"task_id"`
	SourceDigest          string `json:"source_digest"`
	RecordDigest          string `json:"record_digest"`
	ArtifactDigest        string `json:"artifact_digest"`
	ContentRef            string `json:"content_ref"`
	OriginalRef           string `json:"original_ref"`
	OperatorInterventions int    `json:"operator_interventions"`
	RepairAttempts        int    `json:"repair_attempts"`
	ReadOnly              bool   `json:"read_only"`
	ExecutesWork          bool   `json:"executes_work"`
	ApprovesWork          bool   `json:"approves_work"`
	MutatesRepositories   bool   `json:"mutates_repositories"`
	GeneratedAtUTC        string `json:"generated_at_utc"`
}

type AONextJournalProjection struct {
	Schema              string `json:"schema"`
	RunID               string `json:"run_id"`
	PrefixDigest        string `json:"prefix_digest"`
	ArtifactDigest      string `json:"artifact_digest"`
	Status              string `json:"status"`
	ReadOnly            bool   `json:"read_only"`
	ExecutesWork        bool   `json:"executes_work"`
	ApprovesWork        bool   `json:"approves_work"`
	MutatesRepositories bool   `json:"mutates_repositories"`
}

type NodeCounts struct {
	Total     int `json:"total"`
	Ready     int `json:"ready"`
	Blocked   int `json:"blocked"`
	Completed int `json:"completed"`
	Failed    int `json:"failed"`
}

type FoundryRollupCounts struct {
	Status         string `json:"status"`
	CompletedNodes int    `json:"completed_nodes"`
	TotalNodes     int    `json:"total_nodes"`
}

type AtlasRecommendationReadbackCounts struct {
	Status               string `json:"status"`
	TotalNodes           int    `json:"total_nodes"`
	CompletedNodes       int    `json:"completed_nodes"`
	ReadyNodes           int    `json:"ready_nodes"`
	CheckpointCount      int    `json:"checkpoint_count"`
	ElapsedMinutes       int    `json:"elapsed_minutes"`
	MinMinutesMet        bool   `json:"min_minutes_met"`
	LeaseTimeStatus      string `json:"lease_time_status"`
	ReturnGateStatus     string `json:"return_gate_status"`
	FinalResponseAllowed bool   `json:"final_response_allowed"`
	Blocker              string `json:"blocker,omitempty"`
	RSIRemainsDenied     bool   `json:"rsi_remains_denied,omitempty"`
	ExactNextAction      string `json:"exact_next_action"`
}

type AtlasFinalSynthesisReadbackCounts struct {
	MissionID            string `json:"mission_id,omitempty"`
	CorrelationID        string `json:"correlation_id,omitempty"`
	ContractVersion      string `json:"contract_version"`
	Status               string `json:"status"`
	TotalNodes           int    `json:"total_nodes"`
	CompletedNodes       int    `json:"completed_nodes"`
	ReadyNodes           int    `json:"ready_nodes"`
	BlockedNodes         int    `json:"blocked_nodes"`
	MinimumNodes         int    `json:"minimum_nodes"`
	ReturnGateStatus     string `json:"return_gate_status"`
	FinalResponseAllowed bool   `json:"final_response_allowed"`
	FinalResponseReason  string `json:"final_response_reason"`
	AtlasWorkgraphStatus string `json:"atlas_workgraph_status"`
	FoundryRollup        string `json:"foundry_rollup"`
	PromoterStatus       string `json:"promoter_status"`
	CommandReadback      string `json:"command_readback"`
	EventSearchBound     bool   `json:"event_search_bound"`
	BranchCleanupBound   bool   `json:"branch_cleanup_bound"`
	RSIRemainsDenied     bool   `json:"rsi_remains_denied"`
	ExactNextAction      string `json:"exact_next_action"`
}

type GoalLease struct {
	Schema           string `json:"schema"`
	MinNodes         int    `json:"min_nodes"`
	MinMinutes       int    `json:"min_minutes"`
	MaxMinutes       int    `json:"max_minutes"`
	MaxIterations    int    `json:"max_iterations"`
	ReturnOnlyWhen   string `json:"return_only_when"`
	CheckpointPolicy string `json:"checkpoint_policy"`
	CreatedAtUTC     string `json:"created_at_utc"`
	UpdatedAtUTC     string `json:"updated_at_utc"`
}

type MissionCheckpoint struct {
	Schema          string `json:"schema"`
	MissionID       string `json:"mission_id"`
	CorrelationID   string `json:"correlation_id,omitempty"`
	Sequence        int    `json:"sequence"`
	Iteration       int    `json:"iteration"`
	Route           string `json:"route"`
	Phase           string `json:"phase"`
	Result          string `json:"result"`
	ExactNextAction string `json:"exact_next_action"`
	ResumeCommand   string `json:"resume_command"`
	GeneratedAtUTC  string `json:"generated_at_utc"`
}

type ReturnGate struct {
	Schema               string   `json:"schema"`
	MissionID            string   `json:"mission_id"`
	Status               string   `json:"status"`
	FinalResponseAllowed bool     `json:"final_response_allowed"`
	Reason               string   `json:"reason"`
	CompletedNodes       int      `json:"completed_nodes"`
	MinNodes             int      `json:"min_nodes"`
	ReadyNodesRemaining  int      `json:"ready_nodes_remaining"`
	HardBlocker          bool     `json:"hard_blocker"`
	ExactNextAction      string   `json:"exact_next_action"`
	Blockers             []string `json:"blockers,omitempty"`
	GeneratedAtUTC       string   `json:"generated_at_utc"`
}

type RouteReconciliation struct {
	Schema                string `json:"schema"`
	MissionID             string `json:"mission_id"`
	CorrelationID         string `json:"correlation_id,omitempty"`
	Status                string `json:"status"`
	CurrentRoute          string `json:"current_route"`
	LatestRoute           string `json:"latest_route"`
	FoundryTerminalStatus string `json:"foundry_terminal_status,omitempty"`
	AtlasReadyNodes       int    `json:"atlas_ready_nodes"`
	CommandReadbackBound  bool   `json:"command_readback_bound"`
	PromoterReadbackBound bool   `json:"promoter_readback_bound"`
	ExactNextAction       string `json:"exact_next_action"`
	GeneratedAtUTC        string `json:"generated_at_utc"`
}

type FeatureDepthRecommendation struct {
	ID                  string   `json:"id"`
	Owner               string   `json:"owner"`
	Task                string   `json:"task"`
	Gate                string   `json:"gate"`
	EvidenceRequired    []string `json:"evidence_required"`
	EstimatedMinutes    int      `json:"estimated_minutes"`
	ContinuationCommand string   `json:"continuation_command"`
	ExactNextAction     string   `json:"exact_next_action"`
	StopCondition       string   `json:"stop_condition"`
}

type MissionCheckpointBundle struct {
	Schema              string             `json:"schema"`
	MissionID           string             `json:"mission_id"`
	CorrelationID       string             `json:"correlation_id,omitempty"`
	Status              string             `json:"status"`
	CheckpointCount     int                `json:"checkpoint_count"`
	LatestCheckpoint    *MissionCheckpoint `json:"latest_checkpoint,omitempty"`
	ReturnGate          *ReturnGate        `json:"return_gate,omitempty"`
	ResumePrompt        string             `json:"resume_prompt"`
	SafeToExecute       bool               `json:"safe_to_execute"`
	ExecutesWork        bool               `json:"executes_work"`
	ApprovesWork        bool               `json:"approves_work"`
	MutatesRepositories bool               `json:"mutates_repositories"`
	GeneratedAtUTC      string             `json:"generated_at_utc"`
}

type BetaIncidentStopRuleOptions struct {
	IncidentID     string
	Severity       string
	SentinelStatus string
	PromoterStatus string
}

type BetaIncidentStopRuleReadback struct {
	Schema                  string   `json:"schema"`
	MissionID               string   `json:"mission_id"`
	Status                  string   `json:"status"`
	IncidentID              string   `json:"incident_id"`
	IncidentSeverity        string   `json:"incident_severity"`
	SentinelStatus          string   `json:"sentinel_status"`
	PromoterStatus          string   `json:"promoter_status"`
	StopRuleTriggered       bool     `json:"stop_rule_triggered"`
	StopReasons             []string `json:"stop_reasons"`
	PromoterHoldRequired    bool     `json:"promoter_hold_required"`
	ExactNextAction         string   `json:"exact_next_action"`
	ReadOnly                bool     `json:"read_only"`
	SafeToExecute           bool     `json:"safe_to_execute"`
	ExecutesWork            bool     `json:"executes_work"`
	ApprovesWork            bool     `json:"approves_work"`
	MutatesRepositories     bool     `json:"mutates_repositories"`
	ProviderCallsAllowed    bool     `json:"provider_calls_allowed"`
	CredentialUseAllowed    bool     `json:"credential_use_allowed"`
	ReleaseOrPublishAllowed bool     `json:"release_or_publish_allowed"`
	ClaimsAuthorityAdvance  bool     `json:"claims_authority_advance"`
	RSIRemainsDenied        bool     `json:"rsi_remains_denied"`
	GeneratedAtUTC          string   `json:"generated_at_utc"`
}

type PilotFeedbackCaptureOptions struct {
	PilotID        string
	FeedbackWindow string
}

type PilotFeedbackCapturePacket struct {
	Schema                  string   `json:"schema"`
	MissionID               string   `json:"mission_id"`
	Status                  string   `json:"status"`
	PilotID                 string   `json:"pilot_id"`
	FeedbackWindow          string   `json:"feedback_window"`
	CaptureChannels         []string `json:"capture_channels"`
	Questions               []string `json:"questions"`
	EvidenceRequired        []string `json:"evidence_required"`
	ExactNextAction         string   `json:"exact_next_action"`
	ReadOnly                bool     `json:"read_only"`
	SafeToExecute           bool     `json:"safe_to_execute"`
	ExecutesWork            bool     `json:"executes_work"`
	ApprovesWork            bool     `json:"approves_work"`
	MutatesRepositories     bool     `json:"mutates_repositories"`
	ProviderCallsAllowed    bool     `json:"provider_calls_allowed"`
	CredentialUseAllowed    bool     `json:"credential_use_allowed"`
	ReleaseOrPublishAllowed bool     `json:"release_or_publish_allowed"`
	ClaimsAuthorityAdvance  bool     `json:"claims_authority_advance"`
	RSIRemainsDenied        bool     `json:"rsi_remains_denied"`
	GeneratedAtUTC          string   `json:"generated_at_utc"`
}

type SchedulerEvidenceCounts struct {
	Status          string `json:"status"`
	Scheduler       string `json:"scheduler"`
	EventLoop       bool   `json:"event_loop"`
	FreshnessStatus string `json:"freshness_status"`
	ExecutesWork    bool   `json:"executes_work"`
}

type SchedulerRecoveryCounts struct {
	Status        string `json:"status"`
	RecoveryMode  string `json:"recovery_mode"`
	MissedWakeups int    `json:"missed_wakeups"`
	ExecutesWork  bool   `json:"executes_work"`
}

type RouteDecision struct {
	Schema          string `json:"schema"`
	MissionID       string `json:"mission_id"`
	CorrelationID   string `json:"correlation_id,omitempty"`
	Route           string `json:"route"`
	Reason          string `json:"reason"`
	SafeToRequest   bool   `json:"safe_to_request"`
	SafeToExecute   bool   `json:"safe_to_execute"`
	SafeToPromote   bool   `json:"safe_to_promote"`
	ExactNextAction string `json:"exact_next_action"`
	GeneratedAtUTC  string `json:"generated_at_utc,omitempty"`
}

type ContinuationStep struct {
	Schema          string `json:"schema"`
	MissionID       string `json:"mission_id"`
	CorrelationID   string `json:"correlation_id,omitempty"`
	Iteration       int    `json:"iteration"`
	Route           string `json:"route"`
	Result          string `json:"result"`
	ExactNextAction string `json:"exact_next_action"`
	GeneratedAtUTC  string `json:"generated_at_utc"`
}

type GovernanceSnapshot struct {
	Schema                  string        `json:"schema"`
	MissionID               string        `json:"mission_id"`
	CorrelationID           string        `json:"correlation_id,omitempty"`
	ObjectiveDigest         string        `json:"objective_digest"`
	CurrentOwner            string        `json:"current_owner"`
	CurrentPhase            string        `json:"current_phase"`
	CurrentRoute            string        `json:"current_route"`
	HighestProvenLiveClass  string        `json:"highest_proven_live_class"`
	NextDeniedClass         string        `json:"next_denied_class"`
	SafeToRequest           bool          `json:"safe_to_request"`
	SafeToExecute           bool          `json:"safe_to_execute"`
	SafeToPromote           bool          `json:"safe_to_promote"`
	SchedulesWork           bool          `json:"schedules_work"`
	ExecutesWork            bool          `json:"executes_work"`
	ApprovesWork            bool          `json:"approves_work"`
	MutatesRepositories     bool          `json:"mutates_repositories"`
	ProviderCalls           bool          `json:"provider_calls"`
	ReleaseOrPublish        bool          `json:"release_or_publish"`
	CredentialUse           bool          `json:"credential_use"`
	DirectMainMutation      bool          `json:"direct_main_mutation"`
	ConcurrentMutation      bool          `json:"concurrent_mutation"`
	SentinelStatus          string        `json:"sentinel_status"`
	PromoterStatus          string        `json:"promoter_status"`
	CovenantStatus          string        `json:"covenant_status"`
	RollbackStatus          string        `json:"rollback_status"`
	CIStatus                string        `json:"ci_status"`
	RepoHygieneStatus       string        `json:"repo_hygiene_status"`
	EvidenceFreshnessStatus string        `json:"evidence_freshness_status"`
	KillSwitchStatus        string        `json:"kill_switch_status"`
	Blockers                []string      `json:"blockers"`
	ExactNextAction         string        `json:"exact_next_action"`
	ArtifactRefs            []ArtifactRef `json:"artifact_refs"`
	GeneratedAtUTC          string        `json:"generated_at_utc"`
}

type SchedulerReadback struct {
	Schema         string `json:"schema"`
	MissionID      string `json:"mission_id"`
	Status         string `json:"status"`
	Scheduler      string `json:"scheduler"`
	EventLoop      bool   `json:"event_loop"`
	Reason         string `json:"reason,omitempty"`
	GeneratedAtUTC string `json:"generated_at_utc"`
}

type EventLoopDecision struct {
	Schema              string `json:"schema"`
	MissionID           string `json:"mission_id"`
	CorrelationID       string `json:"correlation_id,omitempty"`
	Iteration           int    `json:"iteration"`
	Status              string `json:"status"`
	Route               string `json:"route"`
	ExactNextAction     string `json:"exact_next_action"`
	ExecutesWork        bool   `json:"executes_work"`
	ApprovesWork        bool   `json:"approves_work"`
	MutatesRepositories bool   `json:"mutates_repositories"`
	GeneratedAtUTC      string `json:"generated_at_utc"`
}

type TelegramCommand struct {
	Schema  string `json:"schema"`
	ChatID  string `json:"chat_id"`
	Command string `json:"command"`
	Role    string `json:"role"`
}
type TelegramReadback struct {
	Schema            string `json:"schema"`
	Status            string `json:"status"`
	Message           string `json:"message"`
	MutationAuthority bool   `json:"mutation_authority"`
}
type TelegramCommandMatrix struct {
	Schema   string                       `json:"schema"`
	Commands []TelegramCommandMatrixEntry `json:"commands"`
}
type TelegramCommandMatrixEntry struct {
	Command        string `json:"command"`
	Role           string `json:"role"`
	ExpectedStatus string `json:"expected_status"`
}

type GatewayReplayResult struct {
	RequestID         string `json:"request_id,omitempty"`
	ResponseID        string `json:"response_id,omitempty"`
	Command           string `json:"command,omitempty"`
	Method            string `json:"method,omitempty"`
	ExpectedStatus    string `json:"expected_status"`
	ActualStatus      string `json:"actual_status"`
	MutationAuthority bool   `json:"mutation_authority"`
}

type GatewayReplayReadback struct {
	Schema            string                `json:"schema"`
	Gateway           string                `json:"gateway"`
	Status            string                `json:"status"`
	Total             int                   `json:"total"`
	IntentRecorded    int                   `json:"intent_recorded"`
	Denied            int                   `json:"denied"`
	Invalid           int                   `json:"invalid"`
	Duplicates        int                   `json:"duplicates,omitempty"`
	Fresh             int                   `json:"fresh,omitempty"`
	Stale             int                   `json:"stale,omitempty"`
	UnknownFreshness  int                   `json:"unknown_freshness,omitempty"`
	FreshnessStatus   string                `json:"freshness_status,omitempty"`
	CorrelationID     string                `json:"correlation_id,omitempty"`
	Results           []GatewayReplayResult `json:"results"`
	MutationAuthority bool                  `json:"mutation_authority"`
	ExecutesWork      bool                  `json:"executes_work"`
	ApprovesWork      bool                  `json:"approves_work"`
	GeneratedAtUTC    string                `json:"generated_at_utc"`
}

type GatewayIntentRecord struct {
	Schema            string `json:"schema"`
	MissionID         string `json:"mission_id"`
	Gateway           string `json:"gateway"`
	Command           string `json:"command,omitempty"`
	Method            string `json:"method,omitempty"`
	Status            string `json:"status"`
	ExpectedStatus    string `json:"expected_status,omitempty"`
	MutationAuthority bool   `json:"mutation_authority"`
	ExecutesWork      bool   `json:"executes_work"`
	ApprovesWork      bool   `json:"approves_work"`
	GeneratedAtUTC    string `json:"generated_at_utc"`
}

type GatewayIntentLedger struct {
	Schema            string                `json:"schema"`
	MissionID         string                `json:"mission_id"`
	Status            string                `json:"status"`
	Total             int                   `json:"total"`
	IntentRecorded    int                   `json:"intent_recorded"`
	Denied            int                   `json:"denied"`
	Invalid           int                   `json:"invalid"`
	Intents           []GatewayIntentRecord `json:"intents"`
	MutationAuthority bool                  `json:"mutation_authority"`
	ExecutesWork      bool                  `json:"executes_work"`
	ApprovesWork      bool                  `json:"approves_work"`
	GeneratedAtUTC    string                `json:"generated_at_utc"`
}

type GatewayReplaySuiteReadback struct {
	Schema            string                    `json:"schema"`
	Status            string                    `json:"status"`
	TelegramReplays   int                       `json:"telegram_replays"`
	A2AReplays        int                       `json:"a2a_replays"`
	Total             int                       `json:"total"`
	IntentRecorded    int                       `json:"intent_recorded"`
	Denied            int                       `json:"denied"`
	Invalid           int                       `json:"invalid"`
	ArtifactReadbacks int                       `json:"artifact_readbacks"`
	CorrelationID     string                    `json:"correlation_id,omitempty"`
	ReplayRefs        []string                  `json:"replay_refs"`
	Replays           []GatewayReplayReadback   `json:"replays"`
	A2ALifecycle      *A2ATaskLifecycleReadback `json:"a2a_lifecycle,omitempty"`
	MutationAuthority bool                      `json:"mutation_authority"`
	ExecutesWork      bool                      `json:"executes_work"`
	ApprovesWork      bool                      `json:"approves_work"`
	GeneratedAtUTC    string                    `json:"generated_at_utc"`
}

type A2ATaskLifecycleReadback struct {
	Schema            string    `json:"schema"`
	Status            string    `json:"status"`
	Total             int       `json:"total"`
	IntentRecorded    int       `json:"intent_recorded"`
	CancelRequested   int       `json:"cancel_requested"`
	Cancelled         int       `json:"cancelled"`
	ResumeRequested   int       `json:"resume_requested"`
	Resumed           int       `json:"resumed"`
	ArtifactReadbacks int       `json:"artifact_readbacks"`
	Tasks             []A2ATask `json:"tasks"`
	MutationAuthority bool      `json:"mutation_authority"`
	ExecutesWork      bool      `json:"executes_work"`
	ApprovesWork      bool      `json:"approves_work"`
	GeneratedAtUTC    string    `json:"generated_at_utc"`
}

type A2ACompatibilityReadback struct {
	Schema            string `json:"schema"`
	Status            string `json:"status"`
	ProtocolVersion   string `json:"protocol_version"`
	AgentCardSkills   int    `json:"agent_card_skills"`
	Methods           int    `json:"methods"`
	HTTPRequests      int    `json:"http_requests"`
	LifecycleTasks    int    `json:"lifecycle_tasks"`
	ArtifactReadbacks int    `json:"artifact_readbacks"`
	MutationAuthority bool   `json:"mutation_authority"`
	ExecutesWork      bool   `json:"executes_work"`
	ApprovesWork      bool   `json:"approves_work"`
	GeneratedAtUTC    string `json:"generated_at_utc"`
}

type A2AStreamingDenialReadback struct {
	Schema             string `json:"schema"`
	Status             string `json:"status"`
	StreamingRequested bool   `json:"streaming_requested"`
	SSERequested       bool   `json:"sse_requested,omitempty"`
	PushRequested      bool   `json:"push_notifications_requested"`
	DeniedCapability   string `json:"denied_capability"`
	MutationAuthority  bool   `json:"mutation_authority"`
	ExecutesWork       bool   `json:"executes_work"`
	ApprovesWork       bool   `json:"approves_work"`
	ExactNextAction    string `json:"exact_next_action"`
	GeneratedAtUTC     string `json:"generated_at_utc"`
}

type A2ACancellationReplayReadback struct {
	Schema            string `json:"schema"`
	Status            string `json:"status"`
	Total             int    `json:"total"`
	CancelRequested   int    `json:"cancel_requested"`
	Cancelled         int    `json:"cancelled"`
	MutationAuthority bool   `json:"mutation_authority"`
	ExecutesWork      bool   `json:"executes_work"`
	ApprovesWork      bool   `json:"approves_work"`
	ExactNextAction   string `json:"exact_next_action"`
	GeneratedAtUTC    string `json:"generated_at_utc"`
}

type A2AAgentCard struct {
	Schema             string          `json:"schema"`
	Name               string          `json:"name"`
	ProtocolVersion    string          `json:"protocol_version"`
	Description        string          `json:"description"`
	Endpoint           string          `json:"endpoint"`
	Methods            []string        `json:"methods"`
	Capabilities       []string        `json:"capabilities"`
	CapabilitiesDetail map[string]bool `json:"capabilities_detail,omitempty"`
	Skills             []A2AAgentSkill `json:"skills,omitempty"`
	MutationAuthority  bool            `json:"mutation_authority"`
}
type A2AAgentSkill struct {
	ID          string   `json:"id"`
	Name        string   `json:"name"`
	Description string   `json:"description"`
	Tags        []string `json:"tags"`
}
type A2ATask struct {
	Schema            string        `json:"schema"`
	TaskID            string        `json:"task_id"`
	Method            string        `json:"method"`
	Status            string        `json:"status"`
	ArtifactRefs      []ArtifactRef `json:"artifact_refs,omitempty"`
	MutationAuthority bool          `json:"mutation_authority"`
}

type A2AJSONRPCResponse struct {
	JSONRPC string  `json:"jsonrpc"`
	ID      any     `json:"id,omitempty"`
	Result  A2ATask `json:"result"`
}

type ArtifactManifest struct {
	Schema         string        `json:"schema"`
	MissionID      string        `json:"mission_id"`
	ArtifactRefs   []ArtifactRef `json:"artifact_refs"`
	ManifestDigest string        `json:"manifest_digest"`
	Signature      string        `json:"signature"`
	SafeToExecute  bool          `json:"safe_to_execute"`
	ExecutesWork   bool          `json:"executes_work"`
	ApprovesWork   bool          `json:"approves_work"`
	GeneratedAtUTC string        `json:"generated_at_utc"`
}

type ArtifactManifestValidation struct {
	Schema         string `json:"schema"`
	Status         string `json:"status"`
	MissionID      string `json:"mission_id"`
	ArtifactCount  int    `json:"artifact_count"`
	ManifestDigest string `json:"manifest_digest"`
	ExecutesWork   bool   `json:"executes_work"`
	ApprovesWork   bool   `json:"approves_work"`
	GeneratedAtUTC string `json:"generated_at_utc"`
}

type SchedulerReplayReadback struct {
	Schema         string `json:"schema"`
	Status         string `json:"status"`
	Total          int    `json:"total"`
	Fresh          int    `json:"fresh"`
	Stale          int    `json:"stale"`
	Unknown        int    `json:"unknown"`
	ExecutesWork   bool   `json:"executes_work"`
	ApprovesWork   bool   `json:"approves_work"`
	EvaluatedAtUTC string `json:"evaluated_at_utc"`
	GeneratedAtUTC string `json:"generated_at_utc"`
}

type SchedulerAlertSummary struct {
	Schema         string   `json:"schema"`
	Status         string   `json:"status"`
	Total          int      `json:"total"`
	Fresh          int      `json:"fresh"`
	Stale          int      `json:"stale"`
	Unknown        int      `json:"unknown"`
	Alerts         []string `json:"alerts"`
	ExecutesWork   bool     `json:"executes_work"`
	ApprovesWork   bool     `json:"approves_work"`
	GeneratedAtUTC string   `json:"generated_at_utc"`
}

type SchedulerRecoveryReadback struct {
	Schema          string `json:"schema"`
	MissionID       string `json:"mission_id"`
	Status          string `json:"status"`
	RecoveryMode    string `json:"recovery_mode"`
	MissedWakeups   int    `json:"missed_wakeups"`
	Fresh           int    `json:"fresh"`
	Stale           int    `json:"stale"`
	Unknown         int    `json:"unknown"`
	ExactNextAction string `json:"exact_next_action"`
	ExecutesWork    bool   `json:"executes_work"`
	ApprovesWork    bool   `json:"approves_work"`
	GeneratedAtUTC  string `json:"generated_at_utc"`
}

type LedgerCompactionOptions struct {
	KeepRouteHistory int
	KeepSteps        int
	DryRun           bool
}

type LedgerCompactionCounts struct {
	RouteHistoryBefore int `json:"route_history_before"`
	RouteHistoryAfter  int `json:"route_history_after"`
	StepsBefore        int `json:"steps_before"`
	StepsAfter         int `json:"steps_after"`
}

type LedgerCompactionReadback struct {
	Schema              string `json:"schema"`
	MissionID           string `json:"mission_id"`
	CorrelationID       string `json:"correlation_id,omitempty"`
	Status              string `json:"status"`
	RouteHistoryBefore  int    `json:"route_history_before"`
	RouteHistoryAfter   int    `json:"route_history_after"`
	StepsBefore         int    `json:"steps_before"`
	StepsAfter          int    `json:"steps_after"`
	ExactNextAction     string `json:"exact_next_action"`
	ExecutesWork        bool   `json:"executes_work"`
	ApprovesWork        bool   `json:"approves_work"`
	MutatesRepositories bool   `json:"mutates_repositories"`
	GeneratedAtUTC      string `json:"generated_at_utc"`
}

type TimelineCompactionReadback struct {
	Schema              string `json:"schema"`
	MissionID           string `json:"mission_id"`
	CorrelationID       string `json:"correlation_id,omitempty"`
	Status              string `json:"status"`
	RouteHistoryBefore  int    `json:"route_history_before"`
	RouteHistoryAfter   int    `json:"route_history_after"`
	StepsBefore         int    `json:"steps_before"`
	StepsAfter          int    `json:"steps_after"`
	TimelineDigest      string `json:"timeline_digest"`
	ExactNextAction     string `json:"exact_next_action"`
	ExecutesWork        bool   `json:"executes_work"`
	ApprovesWork        bool   `json:"approves_work"`
	MutatesRepositories bool   `json:"mutates_repositories"`
	GeneratedAtUTC      string `json:"generated_at_utc"`
}

type CommandStatus struct {
	Schema                     string                             `json:"schema"`
	MissionID                  string                             `json:"mission_id"`
	CorrelationID              string                             `json:"correlation_id,omitempty"`
	Status                     string                             `json:"status"`
	SourceRecordStatus         string                             `json:"source_record_status,omitempty"`
	TerminalProjectionStatus   string                             `json:"terminal_projection_status,omitempty"`
	TerminalProjectionReadOnly bool                               `json:"terminal_projection_read_only,omitempty"`
	EffectiveOperatorStatus    string                             `json:"effective_operator_status,omitempty"`
	CurrentRoute               string                             `json:"current_route"`
	CurrentPhase               string                             `json:"current_phase"`
	ExactNextAction            string                             `json:"exact_next_action"`
	GoalLease                  *GoalLease                         `json:"goal_lease,omitempty"`
	CheckpointCount            int                                `json:"checkpoint_count"`
	CheckpointFreshnessStatus  string                             `json:"checkpoint_freshness_status"`
	ReturnGateStatus           string                             `json:"return_gate_status"`
	ReadOnly                   bool                               `json:"read_only"`
	SafeToExecute              bool                               `json:"safe_to_execute"`
	ExecutesWork               bool                               `json:"executes_work"`
	ApprovesWork               bool                               `json:"approves_work"`
	MutatesRepositories        bool                               `json:"mutates_repositories"`
	AtlasRecommendation        *AtlasRecommendationReadbackCounts `json:"atlas_recommendation,omitempty"`
	AONextCandidate            *AONextCandidateProjection         `json:"ao_next_candidate,omitempty"`
	AONextJournalProjection    *AONextJournalProjection           `json:"ao_next_journal_projection,omitempty"`
	Blockers                   []string                           `json:"blockers"`
	GeneratedAtUTC             string                             `json:"generated_at_utc"`
}

type MissionFinalReconciliationPacket struct {
	Schema                    string `json:"schema"`
	MissionID                 string `json:"mission_id"`
	CorrelationID             string `json:"correlation_id,omitempty"`
	Status                    string `json:"status"`
	ArtifactsAgree            bool   `json:"artifacts_agree"`
	MissionStatus             string `json:"mission_status"`
	AtlasRecommendationStatus string `json:"atlas_recommendation_status,omitempty"`
	FoundryStatus             string `json:"foundry_status,omitempty"`
	CommandStatus             string `json:"command_status"`
	CompletedNodes            int    `json:"completed_nodes"`
	TotalNodes                int    `json:"total_nodes"`
	ReadyNodes                int    `json:"ready_nodes"`
	FinalResponseAllowed      bool   `json:"final_response_allowed"`
	ReturnGateStatus          string `json:"return_gate_status"`
	Blocker                   string `json:"blocker,omitempty"`
	PromotionClaimed          bool   `json:"promotion_claimed"`
	RSIRemainsDenied          bool   `json:"rsi_remains_denied"`
	ClaimsAuthorityAdvance    bool   `json:"claims_authority_advance"`
	SafeToExecute             bool   `json:"safe_to_execute"`
	ExecutesWork              bool   `json:"executes_work"`
	ApprovesWork              bool   `json:"approves_work"`
	MutatesRepositories       bool   `json:"mutates_repositories"`
	CorrelationChainStatus    string `json:"correlation_chain_status,omitempty"`
	CorrelationChainDigest    string `json:"correlation_chain_digest,omitempty"`
	GeneratedAtUTC            string `json:"generated_at_utc"`
}

type AtlasWaveFinalSynthesis struct {
	Schema                                string                       `json:"schema"`
	Mission                               string                       `json:"mission"`
	CorrelationID                         string                       `json:"correlation_id,omitempty"`
	Status                                string                       `json:"status"`
	MissionID                             string                       `json:"mission_id"`
	CompletedNodes                        int                          `json:"completed_nodes"`
	ReadyNodes                            int                          `json:"ready_nodes"`
	BlockedNodes                          int                          `json:"blocked_nodes"`
	MinimumNodes                          int                          `json:"minimum_nodes"`
	TargetMinutes                         int                          `json:"target_minutes"`
	MaxMinutes                            int                          `json:"max_minutes"`
	FinalResponseAllowed                  bool                         `json:"final_response_allowed"`
	AtlasWorkgraphStatus                  string                       `json:"atlas_workgraph_status"`
	FoundryRollup                         string                       `json:"foundry_rollup"`
	PromoterStatus                        string                       `json:"promoter_status"`
	CommandReadback                       string                       `json:"command_readback"`
	EventSearchBound                      bool                         `json:"event_search_bound"`
	BranchCleanupBoundThroughPreviousNode bool                         `json:"branch_cleanup_bound_through_previous_node"`
	MergedPRsFinal                        []int                        `json:"merged_prs_final,omitempty"`
	CurrentNodeBranch                     string                       `json:"current_node_branch"`
	CurrentNodePRPending                  bool                         `json:"current_node_pr_pending"`
	PromotionClaimed                      bool                         `json:"promotion_claimed"`
	ClaimsAuthorityAdvance                bool                         `json:"claims_authority_advance"`
	RSIRemainsDenied                      bool                         `json:"rsi_remains_denied"`
	SafeToExecute                         bool                         `json:"safe_to_execute"`
	ExecutesWork                          bool                         `json:"executes_work"`
	ApprovesWork                          bool                         `json:"approves_work"`
	MutatesRepositories                   bool                         `json:"mutates_repositories"`
	FeatureDepthRecommendations           []FeatureDepthRecommendation `json:"feature_depth_recommendations"`
	ExactNextAction                       string                       `json:"exact_next_action"`
	GeneratedAtUTC                        string                       `json:"generated_at_utc"`
}

type AtlasContinuationPromptPacket struct {
	Schema                      string                       `json:"schema"`
	MissionID                   string                       `json:"mission_id"`
	Status                      string                       `json:"status"`
	CurrentRoute                string                       `json:"current_route"`
	CurrentPhase                string                       `json:"current_phase"`
	EventIndexDigest            string                       `json:"event_index_digest"`
	FinalRollupDigest           string                       `json:"final_rollup_digest"`
	CompletedNodes              int                          `json:"completed_nodes"`
	TotalNodes                  int                          `json:"total_nodes"`
	ReadyNodesRemaining         int                          `json:"ready_nodes_remaining"`
	FinalResponseAllowed        bool                         `json:"final_response_allowed"`
	ReturnGateStatus            string                       `json:"return_gate_status"`
	ExactNextAction             string                       `json:"exact_next_action"`
	Prompt                      string                       `json:"prompt"`
	FeatureDepthRecommendations []FeatureDepthRecommendation `json:"feature_depth_recommendations"`
	SafeToExecute               bool                         `json:"safe_to_execute"`
	ExecutesWork                bool                         `json:"executes_work"`
	ApprovesWork                bool                         `json:"approves_work"`
	MutatesRepositories         bool                         `json:"mutates_repositories"`
	GeneratedAtUTC              string                       `json:"generated_at_utc"`
}

type GovernanceSnapshotDiff struct {
	Schema         string   `json:"schema"`
	Status         string   `json:"status"`
	MissionID      string   `json:"mission_id"`
	ChangedFields  int      `json:"changed_fields"`
	Fields         []string `json:"fields"`
	SafeToExecute  bool     `json:"safe_to_execute"`
	ExecutesWork   bool     `json:"executes_work"`
	ApprovesWork   bool     `json:"approves_work"`
	GeneratedAtUTC string   `json:"generated_at_utc"`
}

type TelegramRoleEntry struct {
	ChatID string `json:"chat_id"`
	Role   string `json:"role"`
}

type TelegramRoleMatrixReadback struct {
	Schema            string              `json:"schema"`
	Status            string              `json:"status"`
	ChatCount         int                 `json:"chat_count"`
	AdminCount        int                 `json:"admin_count"`
	UserCount         int                 `json:"user_count"`
	Roles             []TelegramRoleEntry `json:"roles"`
	MutationAuthority bool                `json:"mutation_authority"`
	ExecutesWork      bool                `json:"executes_work"`
	ApprovesWork      bool                `json:"approves_work"`
	GeneratedAtUTC    string              `json:"generated_at_utc"`
}

type MissionArchive struct {
	Schema                string             `json:"schema"`
	MissionID             string             `json:"mission_id"`
	Record                Record             `json:"record"`
	Snapshot              GovernanceSnapshot `json:"snapshot"`
	FinalRollup           FinalRollup        `json:"final_rollup"`
	ArtifactCount         int                `json:"artifact_count"`
	ArchiveDigest         string             `json:"archive_digest"`
	SourceObjectiveDigest string             `json:"source_objective_digest,omitempty"`
	PublicSafeRedactions  []string           `json:"public_safe_redactions,omitempty"`
	SafeToExecute         bool               `json:"safe_to_execute"`
	ExecutesWork          bool               `json:"executes_work"`
	ApprovesWork          bool               `json:"approves_work"`
	GeneratedAtUTC        string             `json:"generated_at_utc"`
}

func (archive *MissionArchive) UnmarshalJSON(data []byte) error {
	type alias MissionArchive
	var decoded alias
	if err := validateNoDuplicateJSONKeys(data); err != nil {
		return err
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&decoded); err != nil {
		return err
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		if err == nil {
			return fmt.Errorf("mission archive contains trailing JSON")
		}
		return err
	}
	*archive = MissionArchive(decoded)
	return nil
}

type MissionArchiveValidation struct {
	Schema         string `json:"schema"`
	Status         string `json:"status"`
	MissionID      string `json:"mission_id"`
	ArchiveDigest  string `json:"archive_digest"`
	ArtifactCount  int    `json:"artifact_count"`
	SafeToExecute  bool   `json:"safe_to_execute"`
	ExecutesWork   bool   `json:"executes_work"`
	ApprovesWork   bool   `json:"approves_work"`
	GeneratedAtUTC string `json:"generated_at_utc"`
}

type MissionArchiveImportReadback struct {
	Schema         string `json:"schema"`
	Status         string `json:"status"`
	MissionID      string `json:"mission_id"`
	ArchiveDigest  string `json:"archive_digest"`
	SafeToExecute  bool   `json:"safe_to_execute"`
	ExecutesWork   bool   `json:"executes_work"`
	ApprovesWork   bool   `json:"approves_work"`
	GeneratedAtUTC string `json:"generated_at_utc"`
}

type GatewayReadinessRollup struct {
	Schema              string   `json:"schema"`
	MissionID           string   `json:"mission_id,omitempty"`
	Status              string   `json:"status"`
	CorrelationID       string   `json:"correlation_id,omitempty"`
	ReadbackCount       int      `json:"readback_count"`
	BlockedReadbacks    int      `json:"blocked_readbacks"`
	ReadbackRefs        []string `json:"readback_refs"`
	SafeToExecute       bool     `json:"safe_to_execute"`
	ExecutesWork        bool     `json:"executes_work"`
	ApprovesWork        bool     `json:"approves_work"`
	MutatesRepositories bool     `json:"mutates_repositories"`
	ExactNextAction     string   `json:"exact_next_action"`
	GeneratedAtUTC      string   `json:"generated_at_utc"`
}

type MissionEvent struct {
	Schema         string `json:"schema"`
	MissionID      string `json:"mission_id"`
	CorrelationID  string `json:"correlation_id,omitempty"`
	Kind           string `json:"kind"`
	Sequence       int    `json:"sequence"`
	Status         string `json:"status,omitempty"`
	Route          string `json:"route,omitempty"`
	Phase          string `json:"phase,omitempty"`
	ArtifactKind   string `json:"artifact_kind,omitempty"`
	Summary        string `json:"summary"`
	GeneratedAtUTC string `json:"generated_at_utc,omitempty"`
}

type MissionEventIndex struct {
	Schema                 string         `json:"schema"`
	Status                 string         `json:"status"`
	IndexVersion           string         `json:"index_version"`
	IndexDigest            string         `json:"index_digest"`
	SourceDigest           string         `json:"source_digest"`
	Root                   string         `json:"root"`
	MissionCount           int            `json:"mission_count"`
	TotalEvents            int            `json:"total_events"`
	StoreFileReads         int            `json:"store_file_reads,omitempty"`
	EventConstructionCount int            `json:"event_construction_count,omitempty"`
	Events                 []MissionEvent `json:"events"`
	SafeToExecute          bool           `json:"safe_to_execute"`
	ExecutesWork           bool           `json:"executes_work"`
	ApprovesWork           bool           `json:"approves_work"`
	MutatesRepositories    bool           `json:"mutates_repositories"`
	GeneratedAtUTC         string         `json:"generated_at_utc"`
}

type MissionEventSearchReadback struct {
	Schema              string         `json:"schema"`
	Status              string         `json:"status"`
	Query               string         `json:"query,omitempty"`
	MissionID           string         `json:"mission_id,omitempty"`
	Kind                string         `json:"kind,omitempty"`
	TotalMatches        int            `json:"total_matches"`
	Events              []MissionEvent `json:"events"`
	SafeToExecute       bool           `json:"safe_to_execute"`
	ExecutesWork        bool           `json:"executes_work"`
	ApprovesWork        bool           `json:"approves_work"`
	MutatesRepositories bool           `json:"mutates_repositories"`
	GeneratedAtUTC      string         `json:"generated_at_utc"`
}

type MissionTimelineQueryIndex struct {
	Schema              string                `json:"schema"`
	Status              string                `json:"status"`
	IndexVersion        string                `json:"index_version"`
	IndexDigest         string                `json:"index_digest"`
	EventIndexDigest    string                `json:"event_index_digest"`
	MissionCount        int                   `json:"mission_count"`
	EventCount          int                   `json:"event_count"`
	TermCount           int                   `json:"term_count"`
	Terms               []MissionTimelineTerm `json:"terms"`
	SafeToExecute       bool                  `json:"safe_to_execute"`
	ExecutesWork        bool                  `json:"executes_work"`
	ApprovesWork        bool                  `json:"approves_work"`
	MutatesRepositories bool                  `json:"mutates_repositories"`
	GeneratedAtUTC      string                `json:"generated_at_utc"`
}

type MissionTimelineTerm struct {
	Term    string                 `json:"term"`
	Matches []MissionTimelineMatch `json:"matches"`
}

type MissionTimelineMatch struct {
	MissionID string `json:"mission_id"`
	Kind      string `json:"kind"`
	Sequence  int    `json:"sequence"`
}

type MissionRestartRecoveryProof struct {
	Schema                     string `json:"schema"`
	Status                     string `json:"status"`
	MissionID                  string `json:"mission_id"`
	BeforeEventSourceDigest    string `json:"before_event_source_digest"`
	AfterEventSourceDigest     string `json:"after_event_source_digest"`
	BeforeTimelineTermDigest   string `json:"before_timeline_term_digest"`
	AfterTimelineTermDigest    string `json:"after_timeline_term_digest"`
	BeforeEventCount           int    `json:"before_event_count"`
	AfterEventCount            int    `json:"after_event_count"`
	BeforeMissionEventCount    int    `json:"before_mission_event_count"`
	AfterMissionEventCount     int    `json:"after_mission_event_count"`
	BeforeTimelineTermCount    int    `json:"before_timeline_term_count"`
	AfterTimelineTermCount     int    `json:"after_timeline_term_count"`
	BeforeTimelineMatchCount   int    `json:"before_timeline_match_count"`
	AfterTimelineMatchCount    int    `json:"after_timeline_match_count"`
	DuplicateTimelineMatches   int    `json:"duplicate_timeline_matches"`
	SourceDigestStable         bool   `json:"source_digest_stable"`
	EventCountStable           bool   `json:"event_count_stable"`
	TimelineTermsStable        bool   `json:"timeline_terms_stable"`
	TimelineMatchesStable      bool   `json:"timeline_matches_stable"`
	NoDuplicateTimelineMatches bool   `json:"no_duplicate_timeline_matches"`
	RecoveryProven             bool   `json:"recovery_proven"`
	SafeToExecute              bool   `json:"safe_to_execute"`
	ExecutesWork               bool   `json:"executes_work"`
	ApprovesWork               bool   `json:"approves_work"`
	MutatesRepositories        bool   `json:"mutates_repositories"`
	GeneratedAtUTC             string `json:"generated_at_utc"`
}

type MissionCompactionResumePrompt struct {
	Schema               string `json:"schema"`
	Status               string `json:"status"`
	MissionID            string `json:"mission_id"`
	MissionStatus        string `json:"mission_status"`
	CurrentPhase         string `json:"current_phase"`
	CurrentRoute         string `json:"current_route"`
	LatestRoute          string `json:"latest_route"`
	EventIndexDigest     string `json:"event_index_digest"`
	TimelineIndexDigest  string `json:"timeline_index_digest"`
	EventCount           int    `json:"event_count"`
	TimelineTermCount    int    `json:"timeline_term_count"`
	CompletedNodes       int    `json:"completed_nodes"`
	ReadyNodes           int    `json:"ready_nodes"`
	MinNodes             int    `json:"min_nodes"`
	ReturnGateStatus     string `json:"return_gate_status"`
	FinalResponseAllowed bool   `json:"final_response_allowed"`
	ExactNextAction      string `json:"exact_next_action"`
	ResumePrompt         string `json:"resume_prompt"`
	SafeToExecute        bool   `json:"safe_to_execute"`
	ExecutesWork         bool   `json:"executes_work"`
	ApprovesWork         bool   `json:"approves_work"`
	MutatesRepositories  bool   `json:"mutates_repositories"`
	GeneratedAtUTC       string `json:"generated_at_utc"`
}

type MissionDoctorReadback struct {
	Schema                    string              `json:"schema"`
	Status                    string              `json:"status"`
	Root                      string              `json:"root"`
	MissionCount              int                 `json:"mission_count"`
	EventCount                int                 `json:"event_count"`
	StoreListCount            int                 `json:"store_list_count,omitempty"`
	StoreFileReads            int                 `json:"store_file_reads,omitempty"`
	LeaseCount                int                 `json:"lease_count"`
	LeaseHealthStatus         string              `json:"lease_health_status"`
	FreshCheckpoints          int                 `json:"fresh_checkpoints"`
	CheckpointFreshnessStatus string              `json:"checkpoint_freshness_status"`
	EarlyReturnRisks          int                 `json:"early_return_risks"`
	EarlyReturnRiskStatus     string              `json:"early_return_risk_status"`
	StaleRoutes               int                 `json:"stale_routes"`
	StaleRouteDecisionStatus  string              `json:"stale_route_decision_status"`
	RiskMissions              []MissionDoctorRisk `json:"risk_missions"`
	ExactNextAction           string              `json:"exact_next_action"`
	Checks                    []string            `json:"checks"`
	Blockers                  []string            `json:"blockers"`
	SafeToExecute             bool                `json:"safe_to_execute"`
	ExecutesWork              bool                `json:"executes_work"`
	ApprovesWork              bool                `json:"approves_work"`
	MutatesRepositories       bool                `json:"mutates_repositories"`
	GeneratedAtUTC            string              `json:"generated_at_utc"`
}

type MissionDoctorRisk struct {
	MissionID       string `json:"mission_id"`
	Kind            string `json:"kind"`
	Status          string `json:"status"`
	Reason          string `json:"reason"`
	ExactNextAction string `json:"exact_next_action"`
}

type MissionReadinessBundleInput struct {
	Repo string
	Path string
}

type MissionReadinessRepoReadback struct {
	Repo   string `json:"repo"`
	Path   string `json:"path"`
	Status string `json:"status"`
	Score  string `json:"score,omitempty"`
	SHA256 string `json:"sha256"`
}

type MissionReadinessBundleReadback struct {
	Schema              string                         `json:"schema"`
	Status              string                         `json:"status"`
	RepoCount           int                            `json:"repo_count"`
	ReadyRepos          int                            `json:"ready_repos"`
	BlockedRepos        int                            `json:"blocked_repos"`
	Repos               []MissionReadinessRepoReadback `json:"repos"`
	SafeToExecute       bool                           `json:"safe_to_execute"`
	ExecutesWork        bool                           `json:"executes_work"`
	ApprovesWork        bool                           `json:"approves_work"`
	MutatesRepositories bool                           `json:"mutates_repositories"`
	ExactNextAction     string                         `json:"exact_next_action"`
	GeneratedAtUTC      string                         `json:"generated_at_utc"`
}

type GatewayReplayBundleInputs struct {
	TelegramConfigPath  string
	TelegramMatrixPath  string
	TelegramUpdatesPath string
	TelegramWebhookPath string
	A2AHTTPPath         string
	A2ALifecyclePath    string
	SchedulerPath       string
}

type GatewayReplayBundleReadback struct {
	Schema              string   `json:"schema"`
	Status              string   `json:"status"`
	TelegramReadbacks   int      `json:"telegram_readbacks"`
	A2AReadbacks        int      `json:"a2a_readbacks"`
	SchedulerReadbacks  int      `json:"scheduler_readbacks"`
	TotalIntents        int      `json:"total_intents"`
	Denied              int      `json:"denied"`
	Invalid             int      `json:"invalid"`
	ReplayRefs          []string `json:"replay_refs"`
	SafeToExecute       bool     `json:"safe_to_execute"`
	ExecutesWork        bool     `json:"executes_work"`
	ApprovesWork        bool     `json:"approves_work"`
	MutatesRepositories bool     `json:"mutates_repositories"`
	ExactNextAction     string   `json:"exact_next_action"`
	GeneratedAtUTC      string   `json:"generated_at_utc"`
}

type MissionDashboardReadback struct {
	Schema                     string         `json:"schema"`
	Status                     string         `json:"status"`
	MissionID                  string         `json:"mission_id"`
	CorrelationID              string         `json:"correlation_id,omitempty"`
	MissionStatus              string         `json:"mission_status"`
	SourceRecordStatus         string         `json:"source_record_status,omitempty"`
	TerminalProjectionStatus   string         `json:"terminal_projection_status,omitempty"`
	TerminalProjectionReadOnly bool           `json:"terminal_projection_read_only,omitempty"`
	EffectiveOperatorStatus    string         `json:"effective_operator_status,omitempty"`
	CurrentPhase               string         `json:"current_phase"`
	CurrentRoute               string         `json:"current_route"`
	LatestRoute                string         `json:"latest_route"`
	TotalNodes                 int            `json:"total_nodes"`
	CompletedNodes             int            `json:"completed_nodes"`
	ReadyNodes                 int            `json:"ready_nodes"`
	BlockedNodes               int            `json:"blocked_nodes"`
	FailedNodes                int            `json:"failed_nodes"`
	EventCount                 int            `json:"event_count"`
	EventIndexDigest           string         `json:"event_index_digest"`
	Compact                    bool           `json:"compact"`
	RecentEvents               []MissionEvent `json:"recent_events"`
	SafeToExecute              bool           `json:"safe_to_execute"`
	ExecutesWork               bool           `json:"executes_work"`
	ApprovesWork               bool           `json:"approves_work"`
	MutatesRepositories        bool           `json:"mutates_repositories"`
	ExactNextAction            string         `json:"exact_next_action"`
	GeneratedAtUTC             string         `json:"generated_at_utc"`
}

type MissionVerificationBundleOptions struct {
	ReadinessBundlePath     string
	GatewayReplayBundlePath string
}

type MissionVerificationBundleComponent struct {
	Name   string `json:"name"`
	Schema string `json:"schema"`
	Path   string `json:"path,omitempty"`
	Status string `json:"status"`
	SHA256 string `json:"sha256"`
}

type MissionVerificationBundleReadback struct {
	Schema              string                               `json:"schema"`
	Status              string                               `json:"status"`
	MissionID           string                               `json:"mission_id"`
	CorrelationID       string                               `json:"correlation_id,omitempty"`
	ComponentCount      int                                  `json:"component_count"`
	Components          []MissionVerificationBundleComponent `json:"components"`
	BundleDigest        string                               `json:"bundle_digest"`
	SafeToExecute       bool                                 `json:"safe_to_execute"`
	ExecutesWork        bool                                 `json:"executes_work"`
	ApprovesWork        bool                                 `json:"approves_work"`
	MutatesRepositories bool                                 `json:"mutates_repositories"`
	ExactNextAction     string                               `json:"exact_next_action"`
	GeneratedAtUTC      string                               `json:"generated_at_utc"`
}

func now(clock func() time.Time) string {
	if clock == nil {
		clock = time.Now
	}
	return clock().UTC().Format(time.RFC3339)
}
