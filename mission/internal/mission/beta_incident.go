package mission

import "strings"

func BuildBetaIncidentStopRuleReadback(r Record, opts BetaIncidentStopRuleOptions) BetaIncidentStopRuleReadback {
	severity := normalizeBetaIncidentField(opts.Severity, "unknown")
	sentinelStatus := normalizeBetaIncidentField(opts.SentinelStatus, "unknown")
	promoterStatus := normalizeBetaIncidentField(opts.PromoterStatus, "unknown")

	reasons := []string{}
	if severity == "high" || severity == "critical" {
		reasons = append(reasons, severity+" severity beta incident")
	}
	if sentinelStatus != "passed" && sentinelStatus != "clear" {
		reasons = append(reasons, "Sentinel status is "+sentinelStatus)
	}
	if promoterStatus == "hold" || promoterStatus == "blocked" || promoterStatus == "promotion_requested" {
		reasons = append(reasons, "Promoter hold is active")
	}
	if len(r.Blockers) > 0 {
		reasons = append(reasons, "mission blockers are present")
	}

	status := "clear"
	next := "continue beta pilot only after normal Mission, Sentinel, Promoter, and Command readbacks remain clear"
	if len(reasons) > 0 {
		status = "hold_required"
		next = "hold beta pilot activity, record incident evidence, and require Sentinel plus Promoter clearance before continuation"
	}

	return BetaIncidentStopRuleReadback{
		Schema:                  "ao.mission.beta-incident-stop-rule-readback.v0.1",
		MissionID:               r.MissionID,
		Status:                  status,
		IncidentID:              strings.TrimSpace(opts.IncidentID),
		IncidentSeverity:        severity,
		SentinelStatus:          sentinelStatus,
		PromoterStatus:          promoterStatus,
		StopRuleTriggered:       len(reasons) > 0,
		StopReasons:             reasons,
		PromoterHoldRequired:    len(reasons) > 0,
		ExactNextAction:         next,
		ReadOnly:                true,
		SafeToExecute:           false,
		ExecutesWork:            false,
		ApprovesWork:            false,
		MutatesRepositories:     false,
		ProviderCallsAllowed:    false,
		CredentialUseAllowed:    false,
		ReleaseOrPublishAllowed: false,
		ClaimsAuthorityAdvance:  false,
		RSIRemainsDenied:        true,
		GeneratedAtUTC:          now(nil),
	}
}

func normalizeBetaIncidentField(value, fallback string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	if value == "" {
		return fallback
	}
	return value
}
