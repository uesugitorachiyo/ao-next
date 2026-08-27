package mission

import (
	"fmt"
	"path/filepath"
	"strings"
)

func BuildFinalReconciliationPacket(r Record) MissionFinalReconciliationPacket {
	packet := buildFinalReconciliationPacket(r)
	applyPersistedCorrelationGate(&packet, r)
	return packet
}

func BuildFinalReconciliationPacketWithCorrelationChain(r Record, chainPath string) (MissionFinalReconciliationPacket, error) {
	packet := buildFinalReconciliationPacket(r)
	if err := validateRecordCorrelationState(r); err != nil {
		return packet, err
	}
	chain, validation, err := loadValidatedCorrelationChainForRecord(chainPath, r)
	if err != nil {
		return packet, err
	}
	if len(r.CorrelatedImports) == 0 {
		packet.CorrelationChainStatus = "ready"
		packet.CorrelationChainDigest = validation.ChainDigest
		return packet, nil
	}
	absoluteChainPath, err := filepath.Abs(chainPath)
	if err != nil {
		return packet, err
	}
	canonicalChainPath, err := filepath.EvalSymlinks(absoluteChainPath)
	if err != nil {
		return packet, err
	}
	callerReference := correlationChainReference(
		chain,
		validation.ChainDigest,
		filepath.Dir(canonicalChainPath),
	)
	if !correlationReferenceComplete(callerReference, r.CorrelatedImports) {
		denyFinalForCorrelation(&packet, fmt.Sprintf(
			"complete correlation chain missing for %d correlation-bound imported artifacts",
			len(r.CorrelatedImports),
		))
		return packet, nil
	}
	references := make(map[string]CorrelationChainReference, len(r.CorrelationChainReferences))
	for _, reference := range r.CorrelationChainReferences {
		references[reference.ReferenceDigest] = reference
	}
	for _, binding := range r.CorrelatedImports {
		reference, present := references[binding.ReferenceDigest]
		if !present {
			denyFinalForCorrelation(&packet, fmt.Sprintf(
				"correlation-bound import %q references a missing chain",
				binding.Role,
			))
			return packet, nil
		}
		if err := validateCorrelationReferenceCurrentArtifactsWithChain(
			reference,
			[]CorrelatedImportBinding{binding},
			&chain,
			filepath.Dir(canonicalChainPath),
		); err != nil {
			denyFinalForCorrelation(&packet, err.Error())
			return packet, nil
		}
	}
	if err := validateCallerCorrelationChainIdentity(
		callerReference,
		references,
		r.CorrelatedImports,
	); err != nil {
		denyFinalForCorrelation(&packet, err.Error())
		return packet, nil
	}
	packet.CorrelationChainStatus = "ready"
	packet.CorrelationChainDigest = validation.ChainDigest
	return packet, nil
}

func validateCallerCorrelationChainIdentity(
	caller CorrelationChainReference,
	references map[string]CorrelationChainReference,
	imports []CorrelatedImportBinding,
) error {
	for index := 1; index < len(caller.Entries); index++ {
		if caller.Entries[index-1].Role >= caller.Entries[index].Role {
			return fmt.Errorf("caller correlation chain identity is not in canonical role order")
		}
	}
	chainDigests := make(map[string]struct{}, len(references))
	for _, binding := range imports {
		reference, present := references[binding.ReferenceDigest]
		if !present {
			return fmt.Errorf("correlation-bound import %q references a missing chain identity", binding.Role)
		}
		chainDigests[reference.ChainDigest] = struct{}{}
	}
	if len(chainDigests) == 1 {
		for digest := range chainDigests {
			if caller.ChainDigest != digest {
				return fmt.Errorf("caller correlation chain identity %s does not match persisted chain %s", caller.ChainDigest, digest)
			}
		}
		return nil
	}
	if len(caller.Entries) != len(imports) {
		return fmt.Errorf("caller correlation consolidation identity must contain exactly %d imported roles", len(imports))
	}
	for _, binding := range imports {
		persistedReference := references[binding.ReferenceDigest]
		persistedEntry, persisted := correlationReferenceEntry(persistedReference, binding.Role)
		callerEntry, supplied := correlationReferenceEntry(caller, binding.Role)
		if !persisted || !supplied ||
			!correlationReferenceEntrySemanticsEqual(persistedEntry, callerEntry) {
			return fmt.Errorf("caller correlation consolidation identity does not match persisted role %q", binding.Role)
		}
	}
	return nil
}

func correlationReferenceEntrySemanticsEqual(
	left, right CorrelationChainReferenceEntry,
) bool {
	return left.Role == right.Role &&
		left.Digest == right.Digest &&
		left.Producer == right.Producer &&
		left.BindingMode == right.BindingMode &&
		left.NativeIdentifier == right.NativeIdentifier &&
		left.NativeField == right.NativeField &&
		left.ParentRole == right.ParentRole &&
		left.ParentDigest == right.ParentDigest &&
		left.ParentDigestField == right.ParentDigestField &&
		left.SafeToExecute == right.SafeToExecute &&
		left.ExecutesWork == right.ExecutesWork &&
		left.ApprovesWork == right.ApprovesWork &&
		left.MutatesRepositories == right.MutatesRepositories &&
		left.WidensPolicy == right.WidensPolicy &&
		left.PublishesArtifacts == right.PublishesArtifacts
}

func buildFinalReconciliationPacket(r Record) MissionFinalReconciliationPacket {
	command := BuildCommandStatus(r)
	gate := EvaluateReturnGate(r)
	packet := MissionFinalReconciliationPacket{
		Schema:                 "ao.mission.final-reconciliation-packet.v0.1",
		MissionID:              r.MissionID,
		CorrelationID:          r.CorrelationID,
		Status:                 "blocked",
		MissionStatus:          r.Status,
		CommandStatus:          command.Status,
		CompletedNodes:         gate.CompletedNodes,
		ReadyNodes:             gate.ReadyNodesRemaining,
		FinalResponseAllowed:   gate.FinalResponseAllowed,
		ReturnGateStatus:       gate.Status,
		PromotionClaimed:       false,
		RSIRemainsDenied:       true,
		ClaimsAuthorityAdvance: false,
		SafeToExecute:          false,
		ExecutesWork:           false,
		ApprovesWork:           false,
		MutatesRepositories:    false,
		GeneratedAtUTC:         now(nil),
	}
	if r.Evidence.AtlasRecommendation != nil {
		packet.AtlasRecommendationStatus = r.Evidence.AtlasRecommendation.Status
		packet.CompletedNodes = r.Evidence.AtlasRecommendation.CompletedNodes
		packet.TotalNodes = r.Evidence.AtlasRecommendation.TotalNodes
		packet.ReadyNodes = r.Evidence.AtlasRecommendation.ReadyNodes
		packet.ReturnGateStatus = r.Evidence.AtlasRecommendation.ReturnGateStatus
	}
	if r.Evidence.FoundryRollup != nil {
		packet.FoundryStatus = r.Evidence.FoundryRollup.Status
		if packet.TotalNodes == 0 {
			packet.TotalNodes = r.Evidence.FoundryRollup.TotalNodes
		}
	}
	packet.Blocker = finalReconciliationBlocker(packet, r)
	packet.ArtifactsAgree = packet.Blocker == ""
	if packet.ArtifactsAgree {
		packet.Status = "ready"
	}
	return packet
}

func applyPersistedCorrelationGate(packet *MissionFinalReconciliationPacket, record Record) {
	if len(record.CorrelatedImports) == 0 {
		return
	}
	if err := validateRecordCorrelationState(record); err != nil {
		denyFinalForCorrelation(packet, "invalid Mission correlation state: "+err.Error())
		return
	}
	var validationErr error
	for _, reference := range record.CorrelationChainReferences {
		if !correlationReferenceComplete(reference, record.CorrelatedImports) {
			continue
		}
		if err := validateCorrelationReferenceCurrentArtifacts(reference, record.CorrelatedImports); err != nil {
			validationErr = err
			continue
		}
		packet.CorrelationChainStatus = "ready"
		packet.CorrelationChainDigest = reference.ChainDigest
		return
	}
	if validationErr != nil {
		denyFinalForCorrelation(packet, validationErr.Error())
		return
	}
	denyFinalForCorrelation(packet, fmt.Sprintf(
		"complete correlation chain missing for %d correlation-bound imported artifacts",
		len(record.CorrelatedImports),
	))
}

func denyFinalForCorrelation(packet *MissionFinalReconciliationPacket, blocker string) {
	packet.Status = "blocked"
	packet.ArtifactsAgree = false
	packet.FinalResponseAllowed = false
	packet.CorrelationChainStatus = "blocked"
	if strings.TrimSpace(packet.Blocker) == "" {
		packet.Blocker = blocker
		return
	}
	packet.Blocker += "; correlation chain: " + blocker
}

func finalReconciliationBlocker(packet MissionFinalReconciliationPacket, r Record) string {
	if r.Status != "done" || packet.CommandStatus != r.Status || !packet.FinalResponseAllowed {
		return fmt.Sprintf("Mission status=%s Command status=%s final_response_allowed=%t", r.Status, packet.CommandStatus, packet.FinalResponseAllowed)
	}
	if r.Evidence.AtlasRecommendation == nil {
		return "Atlas recommendation evidence missing"
	}
	if r.Evidence.AtlasRecommendation.Status != "completed" ||
		r.Evidence.AtlasRecommendation.TotalNodes == 0 ||
		r.Evidence.AtlasRecommendation.CompletedNodes != r.Evidence.AtlasRecommendation.TotalNodes ||
		r.Evidence.AtlasRecommendation.ReadyNodes != 0 {
		return fmt.Sprintf("Atlas recommendation incomplete status=%s completed_nodes=%d total_nodes=%d ready_nodes=%d", r.Evidence.AtlasRecommendation.Status, r.Evidence.AtlasRecommendation.CompletedNodes, r.Evidence.AtlasRecommendation.TotalNodes, r.Evidence.AtlasRecommendation.ReadyNodes)
	}
	if r.Evidence.FoundryRollup != nil &&
		(r.Evidence.FoundryRollup.CompletedNodes != r.Evidence.AtlasRecommendation.CompletedNodes ||
			r.Evidence.FoundryRollup.TotalNodes != r.Evidence.AtlasRecommendation.TotalNodes) {
		return fmt.Sprintf("Foundry completed_nodes=%d total_nodes=%d disagrees with Atlas completed_nodes=%d total_nodes=%d", r.Evidence.FoundryRollup.CompletedNodes, r.Evidence.FoundryRollup.TotalNodes, r.Evidence.AtlasRecommendation.CompletedNodes, r.Evidence.AtlasRecommendation.TotalNodes)
	}
	if packet.PromotionClaimed || !packet.RSIRemainsDenied || packet.ClaimsAuthorityAdvance {
		return fmt.Sprintf("promotion boundary mismatch promotion_claimed=%t rsi_remains_denied=%t claims_authority_advance=%t", packet.PromotionClaimed, packet.RSIRemainsDenied, packet.ClaimsAuthorityAdvance)
	}
	return ""
}
