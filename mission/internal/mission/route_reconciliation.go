package mission

import "strings"

const RouteReconciliationSchema = "ao.mission.route-reconciliation.v0.3"

func BuildRouteReconciliation(r Record) RouteReconciliation {
	latestRoute := r.CurrentRoute
	if n := len(r.RouteHistory); n > 0 && strings.TrimSpace(r.RouteHistory[n-1].Route) != "" {
		latestRoute = r.RouteHistory[n-1].Route
	}
	foundryStatus := ""
	if r.Evidence.FoundryRollup != nil {
		foundryStatus = normalizeFoundryRollupStatus(r.Evidence.FoundryRollup.Status)
	}
	status := "ready"
	next := "route and readback evidence reconciled; continue from latest exact next action"
	if latestRoute != r.CurrentRoute {
		status = "stale_route_detected"
		next = "refresh route decision before final response"
	}
	if r.Status == "done" && r.CurrentRoute != "complete" {
		status = "stale_route_detected"
		next = "reconcile terminal mission route with final rollup"
	}
	commandBound := artifactKindBound(r, "command-readback") || artifactKindBound(r, "command-status") || atlasFinalSynthesisCommandBound(r)
	promoterBound := artifactKindBound(r, "promoter-readback") || artifactKindBound(r, "promoter-verdict") || atlasFinalSynthesisPromoterBound(r)
	return RouteReconciliation{
		Schema:                RouteReconciliationSchema,
		MissionID:             r.MissionID,
		CorrelationID:         r.CorrelationID,
		Status:                status,
		CurrentRoute:          r.CurrentRoute,
		LatestRoute:           latestRoute,
		FoundryTerminalStatus: foundryStatus,
		AtlasReadyNodes:       readyNodesRemaining(r),
		CommandReadbackBound:  commandBound,
		PromoterReadbackBound: promoterBound,
		ExactNextAction:       next,
		GeneratedAtUTC:        now(nil),
	}
}

func atlasFinalSynthesisCommandBound(r Record) bool {
	return r.Evidence.AtlasFinalSynthesis != nil && r.Evidence.AtlasFinalSynthesis.CommandReadback == "ready"
}

func atlasFinalSynthesisPromoterBound(r Record) bool {
	return r.Evidence.AtlasFinalSynthesis != nil && strings.TrimSpace(r.Evidence.AtlasFinalSynthesis.PromoterStatus) != ""
}

func artifactKindBound(r Record, kind string) bool {
	for _, ref := range r.ArtifactRefs {
		if strings.EqualFold(ref.Kind, kind) {
			return true
		}
	}
	return false
}

func normalizeFoundryRollupStatus(status string) string {
	switch strings.ToLower(strings.TrimSpace(status)) {
	case "complete", "completed", "done":
		return "completed"
	case "promote", "promoted", "promotion_ready":
		return "promoted"
	case "deny", "denied":
		return "denied"
	case "block", "blocked":
		return "blocked"
	default:
		return strings.ToLower(strings.TrimSpace(status))
	}
}

func foundryRollupClosesMission(rollup FoundryRollupCounts) bool {
	switch normalizeFoundryRollupStatus(rollup.Status) {
	case "completed", "promoted":
		return rollup.TotalNodes > 0 && rollup.CompletedNodes == rollup.TotalNodes
	default:
		return false
	}
}
