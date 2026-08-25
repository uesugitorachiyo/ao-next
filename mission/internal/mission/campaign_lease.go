package mission

import (
	"fmt"
	"strings"
)

const (
	GoalLeaseSchema         = "ao.mission.goal-lease.v0.3"
	ReturnGateSchema        = "ao.mission.return-gate.v0.3"
	defaultMinNodes         = 10
	defaultMinMinutes       = 0
	defaultMaxMinutes       = 180
	defaultReturnOnlyWhen   = "mission_done_or_true_hard_blocker_or_no_ready_work_and_no_exact_next_action"
	defaultCheckpointPolicy = "after_each_node_or_timed_interval"
)

func ensureGoalLease(r *Record, opts ContinueOptions) (GoalLease, error) {
	stamp := now(nil)
	minNodes := opts.MinNodes
	if minNodes <= 0 {
		minNodes = defaultMinNodes
	}
	minMinutes := opts.MinMinutes
	if minMinutes < 0 {
		minMinutes = defaultMinMinutes
	}
	maxMinutes := opts.MaxMinutes
	if maxMinutes <= 0 {
		maxMinutes = defaultMaxMinutes
	}
	maxIterations := opts.MaxIterations
	if maxIterations <= 0 {
		maxIterations = minNodes
	}
	returnOnlyWhen := strings.TrimSpace(opts.ReturnOnlyWhen)
	if returnOnlyWhen == "" {
		returnOnlyWhen = defaultReturnOnlyWhen
	}
	checkpointPolicy := strings.TrimSpace(opts.CheckpointPolicy)
	if checkpointPolicy == "" {
		checkpointPolicy = defaultCheckpointPolicy
	}
	if r.GoalLease == nil {
		r.GoalLease = &GoalLease{
			Schema:           GoalLeaseSchema,
			MinNodes:         minNodes,
			MinMinutes:       minMinutes,
			MaxMinutes:       maxMinutes,
			MaxIterations:    maxIterations,
			ReturnOnlyWhen:   returnOnlyWhen,
			CheckpointPolicy: checkpointPolicy,
			CreatedAtUTC:     stamp,
			UpdatedAtUTC:     stamp,
		}
		return *r.GoalLease, nil
	}
	r.GoalLease.Schema = GoalLeaseSchema
	if r.GoalLease.MinNodes <= 0 {
		r.GoalLease.MinNodes = minNodes
	} else if opts.MinNodes > 0 && opts.MinNodes < r.GoalLease.MinNodes {
		if r.Evidence.AtlasWorkgraph == nil || r.Evidence.AtlasWorkgraph.Total != opts.MinNodes {
			return GoalLease{}, fmt.Errorf("min-nodes reduction must equal imported Atlas workgraph total: requested=%d", opts.MinNodes)
		}
		r.GoalLease.MinNodes = opts.MinNodes
	}
	if opts.MinMinutesSet || opts.MinMinutes > 0 {
		r.GoalLease.MinMinutes = minMinutes
	} else if r.GoalLease.MinMinutes < 0 {
		r.GoalLease.MinMinutes = minMinutes
	}
	if r.GoalLease.MaxMinutes <= 0 {
		r.GoalLease.MaxMinutes = maxMinutes
	}
	if r.GoalLease.MaxIterations <= 0 || opts.MaxIterations > r.GoalLease.MaxIterations {
		r.GoalLease.MaxIterations = maxIterations
	}
	if strings.TrimSpace(r.GoalLease.ReturnOnlyWhen) == "" {
		r.GoalLease.ReturnOnlyWhen = returnOnlyWhen
	}
	if strings.TrimSpace(r.GoalLease.CheckpointPolicy) == "" {
		r.GoalLease.CheckpointPolicy = checkpointPolicy
	}
	r.GoalLease.UpdatedAtUTC = stamp
	return *r.GoalLease, nil
}

func EvaluateReturnGate(r Record) ReturnGate {
	minNodes := defaultMinNodes
	if r.GoalLease != nil && r.GoalLease.MinNodes > 0 {
		minNodes = r.GoalLease.MinNodes
	}
	completedNodes := completedEvidenceNodes(r)
	readyNodes := readyNodesRemaining(r)
	hardBlocker := hardBlockerExists(r)
	gate := ReturnGate{
		Schema:               ReturnGateSchema,
		MissionID:            r.MissionID,
		Status:               "return_allowed",
		FinalResponseAllowed: true,
		Reason:               "mission has no ready work, no unmet lease minimum, and no exact next action",
		CompletedNodes:       completedNodes,
		MinNodes:             minNodes,
		ReadyNodesRemaining:  readyNodes,
		HardBlocker:          hardBlocker,
		ExactNextAction:      r.ExactNextAction,
		Blockers:             append([]string{}, r.Blockers...),
		GeneratedAtUTC:       now(nil),
	}
	switch {
	case hardBlocker:
		gate.Reason = "mission has a terminal hard blocker for operator review"
	case readyNodes > 0:
		gate.Status = "early_return_denied"
		gate.FinalResponseAllowed = false
		gate.Reason = fmt.Sprintf("ready Atlas nodes remain: %d", readyNodes)
	case r.Status == "done":
		gate.Reason = "mission status is done"
	case r.Evidence.AtlasRecommendation != nil && !r.Evidence.AtlasRecommendation.FinalResponseAllowed:
		gate.Status = "early_return_denied"
		gate.FinalResponseAllowed = false
		gate.Reason = fmt.Sprintf("Atlas recommendation readback return gate blocked: %s lease_time_status=%s elapsed_minutes=%d", r.Evidence.AtlasRecommendation.ReturnGateStatus, r.Evidence.AtlasRecommendation.LeaseTimeStatus, r.Evidence.AtlasRecommendation.ElapsedMinutes)
		if strings.TrimSpace(r.Evidence.AtlasRecommendation.ExactNextAction) != "" {
			gate.ExactNextAction = r.Evidence.AtlasRecommendation.ExactNextAction
		}
	case completedNodes < minNodes:
		gate.Status = "early_return_denied"
		gate.FinalResponseAllowed = false
		gate.Reason = fmt.Sprintf("lease minimum unmet: completed_nodes=%d min_nodes=%d", completedNodes, minNodes)
	case strings.TrimSpace(r.ExactNextAction) != "" && r.Status != "done":
		gate.Status = "early_return_denied"
		gate.FinalResponseAllowed = false
		gate.Reason = "exact next action remains"
	}
	if !gate.FinalResponseAllowed && !strings.HasPrefix(gate.ExactNextAction, "continue") {
		gate.ExactNextAction = "continue mission: " + strings.TrimSpace(r.ExactNextAction)
	}
	if strings.TrimSpace(gate.ExactNextAction) == "" {
		gate.ExactNextAction = "read final rollup and preserve denied authority boundaries"
	}
	return gate
}

func completedEvidenceNodes(r Record) int {
	completed := 0
	if r.Evidence.AtlasWorkgraph != nil && r.Evidence.AtlasWorkgraph.Completed > completed {
		completed = r.Evidence.AtlasWorkgraph.Completed
	}
	if r.Evidence.AtlasRecommendation != nil && r.Evidence.AtlasRecommendation.CompletedNodes > completed {
		completed = r.Evidence.AtlasRecommendation.CompletedNodes
	}
	if r.Evidence.FoundryRollup != nil && r.Evidence.FoundryRollup.CompletedNodes > completed {
		completed = r.Evidence.FoundryRollup.CompletedNodes
	}
	return completed
}

func readyNodesRemaining(r Record) int {
	if r.Evidence.AtlasRecommendation != nil {
		return r.Evidence.AtlasRecommendation.ReadyNodes
	}
	if r.Evidence.AtlasWorkgraph == nil {
		return 0
	}
	return r.Evidence.AtlasWorkgraph.Ready
}

func hardBlockerExists(r Record) bool {
	if r.Status == "blocked" || len(r.Blockers) > 0 {
		return true
	}
	if r.Evidence.FoundryRollup == nil {
		return false
	}
	switch normalizeFoundryRollupStatus(r.Evidence.FoundryRollup.Status) {
	case "blocked", "denied":
		return true
	default:
		return false
	}
}
