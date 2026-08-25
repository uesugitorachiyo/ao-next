package mission

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
)

type QualificationOrchestrationPlan struct {
	Schema                          string                                `json:"schema"`
	Status                          string                                `json:"status"`
	MissionID                       string                                `json:"mission_id"`
	ReleaseTargets                  QualificationReleaseTargets           `json:"release_targets"`
	SourceHeads                     map[string]string                     `json:"source_heads"`
	ProfileDigest                   string                                `json:"profile_digest"`
	AffectedShardReruns             []QualificationAffectedShard          `json:"affected_shard_reruns"`
	FinalFullExactHeadQualification FinalFullExactHeadQualification       `json:"final_full_exact_head_qualification"`
	RestartSafety                   QualificationRestartSafety            `json:"restart_safety"`
	ExpectedMissionOrchestration    QualificationOrchestrationExpectation `json:"expected_mission_orchestration"`
}

type QualificationReleaseTargets struct {
	AO2Version                           string `json:"ao2_version"`
	ControlPlaneVersion                  string `json:"control_plane_version"`
	PublicationAuthorizedElsewhere       bool   `json:"publication_authorized_elsewhere"`
	MissionOrchestrationPublishesRelease bool   `json:"mission_orchestration_publishes_release"`
}

type QualificationAffectedShard struct {
	ShardID                    string `json:"shard_id"`
	Reason                     string `json:"reason"`
	RequiredBeforeFinalFullRun bool   `json:"required_before_final_full_run"`
}

type FinalFullExactHeadQualification struct {
	Required                     bool   `json:"required"`
	Mode                         string `json:"mode"`
	AfterAffectedShards          bool   `json:"after_affected_shards"`
	UsesExactSourceHeads         bool   `json:"uses_exact_source_heads"`
	UsesExactProfileDigest       bool   `json:"uses_exact_profile_digest"`
	ReusesExistingWindowsRequest bool   `json:"reuses_existing_windows_request"`
}

type QualificationRestartSafety struct {
	CheckpointAfterEachShard     bool `json:"checkpoint_after_each_shard"`
	ResumeFromLastCompletedShard bool `json:"resume_from_last_completed_shard"`
	NoRestartFromZero            bool `json:"no_restart_from_zero"`
	ProgressReadbackRequired     bool `json:"progress_readback_required"`
	GlobalDeadlineRequired       bool `json:"global_deadline_required"`
}

type QualificationOrchestrationExpectation struct {
	Schema                 string `json:"schema"`
	Status                 string `json:"status"`
	AffectedShardCount     int    `json:"affected_shard_count"`
	FinalQualificationMode string `json:"final_qualification_mode"`
	SourceHeadCount        int    `json:"source_head_count"`
	ExactHeadRequired      bool   `json:"exact_head_required"`
	RestartFromZeroAllowed bool   `json:"restart_from_zero_allowed"`
	SafeToExecute          bool   `json:"safe_to_execute"`
	ExecutesWork           bool   `json:"executes_work"`
	ApprovesWork           bool   `json:"approves_work"`
	MutatesRepositories    bool   `json:"mutates_repositories"`
	CallsProviders         bool   `json:"calls_providers"`
	ReleasesOrDeploys      bool   `json:"releases_or_deploys"`
	ExactNextAction        string `json:"exact_next_action"`
}

type QualificationOrchestrationReadback struct {
	Schema                       string   `json:"schema"`
	Status                       string   `json:"status"`
	MissionID                    string   `json:"mission_id"`
	AO2Version                   string   `json:"ao2_version"`
	ControlPlaneVersion          string   `json:"control_plane_version"`
	ProfileDigest                string   `json:"profile_digest"`
	AffectedShardCount           int      `json:"affected_shard_count"`
	AffectedShards               []string `json:"affected_shards"`
	FinalQualificationMode       string   `json:"final_qualification_mode"`
	SourceHeadCount              int      `json:"source_head_count"`
	ExactHeadRequired            bool     `json:"exact_head_required"`
	CheckpointAfterEachShard     bool     `json:"checkpoint_after_each_shard"`
	ResumeFromLastCompletedShard bool     `json:"resume_from_last_completed_shard"`
	ProgressReadbackRequired     bool     `json:"progress_readback_required"`
	GlobalDeadlineRequired       bool     `json:"global_deadline_required"`
	RestartFromZeroAllowed       bool     `json:"restart_from_zero_allowed"`
	SafeToExecute                bool     `json:"safe_to_execute"`
	ExecutesWork                 bool     `json:"executes_work"`
	ApprovesWork                 bool     `json:"approves_work"`
	MutatesRepositories          bool     `json:"mutates_repositories"`
	CallsProviders               bool     `json:"calls_providers"`
	ReleasesOrDeploys            bool     `json:"releases_or_deploys"`
	ExactNextAction              string   `json:"exact_next_action"`
}

func BuildQualificationOrchestrationReadback(path string) (QualificationOrchestrationReadback, error) {
	body, err := os.ReadFile(path)
	if err != nil {
		return QualificationOrchestrationReadback{}, err
	}
	var plan QualificationOrchestrationPlan
	if err := json.Unmarshal(body, &plan); err != nil {
		return QualificationOrchestrationReadback{}, err
	}
	if err := validateQualificationOrchestrationPlan(plan); err != nil {
		return QualificationOrchestrationReadback{}, err
	}
	shards := make([]string, 0, len(plan.AffectedShardReruns))
	for _, shard := range plan.AffectedShardReruns {
		shards = append(shards, shard.ShardID)
	}
	expected := plan.ExpectedMissionOrchestration
	return QualificationOrchestrationReadback{
		Schema:                       expected.Schema,
		Status:                       expected.Status,
		MissionID:                    plan.MissionID,
		AO2Version:                   plan.ReleaseTargets.AO2Version,
		ControlPlaneVersion:          plan.ReleaseTargets.ControlPlaneVersion,
		ProfileDigest:                plan.ProfileDigest,
		AffectedShardCount:           len(plan.AffectedShardReruns),
		AffectedShards:               shards,
		FinalQualificationMode:       expected.FinalQualificationMode,
		SourceHeadCount:              len(plan.SourceHeads),
		ExactHeadRequired:            expected.ExactHeadRequired,
		CheckpointAfterEachShard:     plan.RestartSafety.CheckpointAfterEachShard,
		ResumeFromLastCompletedShard: plan.RestartSafety.ResumeFromLastCompletedShard,
		ProgressReadbackRequired:     plan.RestartSafety.ProgressReadbackRequired,
		GlobalDeadlineRequired:       plan.RestartSafety.GlobalDeadlineRequired,
		RestartFromZeroAllowed:       false,
		SafeToExecute:                false,
		ExecutesWork:                 false,
		ApprovesWork:                 false,
		MutatesRepositories:          false,
		CallsProviders:               false,
		ReleasesOrDeploys:            false,
		ExactNextAction:              expected.ExactNextAction,
	}, nil
}

func validateQualificationOrchestrationPlan(plan QualificationOrchestrationPlan) error {
	expected := plan.ExpectedMissionOrchestration
	switch {
	case plan.Schema != "ao.mission.stack-qualification-orchestration.v0.1":
		return fmt.Errorf("qualification orchestration schema must be ao.mission.stack-qualification-orchestration.v0.1")
	case expected.Schema != "ao.mission.qualification-orchestration-readback.v0.1":
		return fmt.Errorf("expected Mission orchestration schema must be ao.mission.qualification-orchestration-readback.v0.1")
	case strings.TrimSpace(plan.MissionID) == "":
		return fmt.Errorf("qualification orchestration requires mission_id")
	case len(plan.SourceHeads) == 0 || len(plan.SourceHeads) != expected.SourceHeadCount:
		return fmt.Errorf("qualification orchestration source head count must match expectation")
	case !strings.HasPrefix(plan.ProfileDigest, "sha256:"):
		return fmt.Errorf("qualification orchestration requires sha256 profile_digest")
	case len(plan.AffectedShardReruns) == 0 || len(plan.AffectedShardReruns) != expected.AffectedShardCount:
		return fmt.Errorf("qualification orchestration affected shard count must match expectation")
	case !plan.FinalFullExactHeadQualification.Required ||
		plan.FinalFullExactHeadQualification.Mode != "full_exact_head" ||
		!plan.FinalFullExactHeadQualification.AfterAffectedShards ||
		!plan.FinalFullExactHeadQualification.UsesExactSourceHeads ||
		!plan.FinalFullExactHeadQualification.UsesExactProfileDigest ||
		!plan.FinalFullExactHeadQualification.ReusesExistingWindowsRequest:
		return fmt.Errorf("qualification orchestration requires final full exact-head qualification after affected shards")
	case expected.FinalQualificationMode != "full_exact_head" || !expected.ExactHeadRequired:
		return fmt.Errorf("expected Mission orchestration must require full exact-head qualification")
	case !plan.RestartSafety.CheckpointAfterEachShard ||
		!plan.RestartSafety.ResumeFromLastCompletedShard ||
		!plan.RestartSafety.NoRestartFromZero ||
		!plan.RestartSafety.ProgressReadbackRequired ||
		!plan.RestartSafety.GlobalDeadlineRequired ||
		expected.RestartFromZeroAllowed:
		return fmt.Errorf("qualification orchestration must be restart-safe and forbid restart from zero")
	case plan.ReleaseTargets.MissionOrchestrationPublishesRelease:
		return fmt.Errorf("Mission qualification orchestration must not publish releases")
	case expected.SafeToExecute ||
		expected.ExecutesWork ||
		expected.ApprovesWork ||
		expected.MutatesRepositories ||
		expected.CallsProviders ||
		expected.ReleasesOrDeploys:
		return fmt.Errorf("Mission qualification orchestration readback must not claim execution, approval, mutation, provider, release, or deploy authority")
	case strings.TrimSpace(expected.ExactNextAction) == "":
		return fmt.Errorf("qualification orchestration requires exact_next_action")
	}
	for _, shard := range plan.AffectedShardReruns {
		if strings.TrimSpace(shard.ShardID) == "" || strings.TrimSpace(shard.Reason) == "" || !shard.RequiredBeforeFinalFullRun {
			return fmt.Errorf("each affected qualification shard requires id, reason, and required_before_final_full_run")
		}
	}
	return nil
}
