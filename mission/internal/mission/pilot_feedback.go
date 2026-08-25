package mission

import "strings"

func BuildPilotFeedbackCapturePacket(r Record, opts PilotFeedbackCaptureOptions) PilotFeedbackCapturePacket {
	pilotID := strings.TrimSpace(opts.PilotID)
	if pilotID == "" {
		pilotID = "pilot-unassigned"
	}
	feedbackWindow := strings.TrimSpace(opts.FeedbackWindow)
	if feedbackWindow == "" {
		feedbackWindow = "beta-readiness"
	}
	return PilotFeedbackCapturePacket{
		Schema:                  "ao.mission.pilot-feedback-capture-packet.v0.1",
		MissionID:               r.MissionID,
		Status:                  "ready",
		PilotID:                 pilotID,
		FeedbackWindow:          feedbackWindow,
		CaptureChannels:         []string{"operator_note", "issue_link", "readback_attachment"},
		Questions:               defaultPilotFeedbackQuestions(),
		EvidenceRequired:        defaultPilotFeedbackEvidence(),
		ExactNextAction:         "Collect pilot feedback as read-only evidence before any beta execution or live run.",
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

func defaultPilotFeedbackQuestions() []string {
	return []string{
		"What step required manual state repair?",
		"Which readback was unclear or stale?",
		"What rollback or stop-rule evidence was missing?",
	}
}

func defaultPilotFeedbackEvidence() []string {
	return []string{
		"Mission timeline readback",
		"Command compact status readback",
		"Sentinel public-safety result",
		"Promoter no-promotion readback",
	}
}
