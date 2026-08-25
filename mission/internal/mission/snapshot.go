package mission

func Snapshot(r Record) GovernanceSnapshot {
	return GovernanceSnapshot{Schema: SnapshotSchema, MissionID: r.MissionID, CorrelationID: r.CorrelationID, ObjectiveDigest: r.ObjectiveDigest, CurrentOwner: "ao-mission", CurrentPhase: r.CurrentPhase, CurrentRoute: r.CurrentRoute, HighestProvenLiveClass: "public_safe_unrestricted_self_modification_authority_request_dry_run_four_attempts", NextDeniedClass: "unrestricted_self_modification", SafeToRequest: true, SafeToExecute: false, SafeToPromote: false, SchedulesWork: false, ExecutesWork: false, ApprovesWork: false, MutatesRepositories: false, ProviderCalls: false, ReleaseOrPublish: false, CredentialUse: false, DirectMainMutation: false, ConcurrentMutation: false, SentinelStatus: "not_requested", PromoterStatus: "not_requested", CovenantStatus: "not_requested", RollbackStatus: "not_required", CIStatus: "not_started", RepoHygieneStatus: "not_started", EvidenceFreshnessStatus: evidenceFreshnessStatus(r), KillSwitchStatus: killSwitchStatus(r), Blockers: r.Blockers, ExactNextAction: r.ExactNextAction, ArtifactRefs: r.ArtifactRefs, GeneratedAtUTC: now(nil)}
}

func evidenceFreshnessStatus(r Record) string {
	if r.Evidence.SchedulerReadback != nil && r.Evidence.SchedulerReadback.FreshnessStatus != "" {
		return r.Evidence.SchedulerReadback.FreshnessStatus
	}
	return "fresh"
}

func killSwitchStatus(r Record) string {
	if r.Status == "stopped" {
		return "engaged"
	}
	return "clear"
}
