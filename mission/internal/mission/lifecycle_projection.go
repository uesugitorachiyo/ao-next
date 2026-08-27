package mission

import (
	"encoding/json"
	"fmt"
)

const MissionLifecycleProjectionSchema = "ao.mission.lifecycle-projection.v0.1"

type MissionLifecycleProjection struct {
	Schema               string                  `json:"schema"`
	Status               string                  `json:"status"`
	MissionID            string                  `json:"mission_id"`
	MissionStatus        string                  `json:"mission_status"`
	CurrentPhase         string                  `json:"current_phase"`
	CurrentRoute         string                  `json:"current_route"`
	LatestRoute          string                  `json:"latest_route"`
	Metrics              MissionLifecycleMetrics `json:"metrics"`
	EventCount           int                     `json:"event_count"`
	EventIndexDigest     string                  `json:"event_index_digest"`
	SourceRecordDigest   string                  `json:"source_record_digest"`
	SourceRecordUpdated  string                  `json:"source_record_updated_at"`
	FinalResponseAllowed bool                    `json:"final_response_allowed"`
	ExactNextAction      string                  `json:"exact_next_action"`
	SafeToExecute        bool                    `json:"safe_to_execute"`
	ExecutesWork         bool                    `json:"executes_work"`
	ApprovesWork         bool                    `json:"approves_work"`
	MutatesRepositories  bool                    `json:"mutates_repositories"`
	RSIRemainsDenied     bool                    `json:"rsi_remains_denied"`
	GeneratedAtUTC       string                  `json:"generated_at_utc"`
}

func BuildMissionLifecycleProjection(s Store, missionID string) (MissionLifecycleProjection, error) {
	record, err := s.Load(missionID)
	if err != nil {
		return MissionLifecycleProjection{}, err
	}
	metrics := BuildMissionLifecycleMetrics(record)
	if err := ValidateMissionLifecycleMetrics(metrics); err != nil {
		return MissionLifecycleProjection{}, err
	}
	index, err := BuildMissionEventIndex(s)
	if err != nil {
		return MissionLifecycleProjection{}, err
	}
	recordBody, err := json.Marshal(record)
	if err != nil {
		return MissionLifecycleProjection{}, err
	}
	latestRoute := record.CurrentRoute
	if n := len(record.RouteHistory); n > 0 && record.RouteHistory[n-1].Route != "" {
		latestRoute = record.RouteHistory[n-1].Route
	}
	return MissionLifecycleProjection{
		Schema:               MissionLifecycleProjectionSchema,
		Status:               "ready",
		MissionID:            record.MissionID,
		MissionStatus:        record.Status,
		CurrentPhase:         record.CurrentPhase,
		CurrentRoute:         record.CurrentRoute,
		LatestRoute:          latestRoute,
		Metrics:              metrics,
		EventCount:           countMissionEvents(index, record.MissionID),
		EventIndexDigest:     index.IndexDigest,
		SourceRecordDigest:   digestBytes(recordBody),
		SourceRecordUpdated:  record.UpdatedAtUTC,
		FinalResponseAllowed: metrics.FinalResponseAllowed,
		ExactNextAction:      metrics.ExactNextAction,
		SafeToExecute:        false,
		ExecutesWork:         false,
		ApprovesWork:         false,
		MutatesRepositories:  false,
		RSIRemainsDenied:     true,
		GeneratedAtUTC:       now(s.Clock),
	}, nil
}

func countMissionEvents(index MissionEventIndex, missionID string) int {
	count := 0
	for _, event := range index.Events {
		if event.MissionID == missionID {
			count++
		}
	}
	return count
}

func ValidateMissionLifecycleProjection(projection MissionLifecycleProjection) error {
	if projection.Schema != MissionLifecycleProjectionSchema {
		return fmt.Errorf("mission lifecycle projection schema must be %s", MissionLifecycleProjectionSchema)
	}
	if projection.Status != "ready" || projection.MissionID == "" || projection.MissionStatus == "" {
		return fmt.Errorf("mission lifecycle projection identity is incomplete")
	}
	if err := ValidateMissionLifecycleMetrics(projection.Metrics); err != nil {
		return err
	}
	if projection.Metrics.MissionID != projection.MissionID {
		return fmt.Errorf("projection metrics mission_id must match projection")
	}
	if projection.EventCount < 0 || projection.EventIndexDigest == "" || projection.SourceRecordDigest == "" || projection.SourceRecordUpdated == "" {
		return fmt.Errorf("mission lifecycle projection source binding is incomplete")
	}
	if projection.FinalResponseAllowed != projection.Metrics.FinalResponseAllowed || projection.ExactNextAction != projection.Metrics.ExactNextAction {
		return fmt.Errorf("projection gate fields must match metrics")
	}
	if projection.SafeToExecute || projection.ExecutesWork || projection.ApprovesWork || projection.MutatesRepositories {
		return fmt.Errorf("mission lifecycle projection must not claim execution or approval authority")
	}
	if !projection.RSIRemainsDenied {
		return fmt.Errorf("rsi_remains_denied must be true")
	}
	return nil
}
