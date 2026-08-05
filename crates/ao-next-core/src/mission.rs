use std::collections::BTreeMap;

use serde::{Deserialize, Serialize};
use thiserror::Error;

use crate::contracts::{Digest, RunState, TerminalReadback};
use crate::strict_json::{StrictJsonError, canonical_digest};

/// The exact relationship between an AO Next readback and AO Mission's current
/// canonical terminal-index consumer.
#[derive(Clone, Debug, Deserialize, Eq, PartialEq, Serialize)]
#[serde(rename_all = "snake_case")]
pub enum CompatibilityStatus {
    BoundedIncompatibility,
}

/// A read-only compatibility assessment. It is evidence, never authority.
#[derive(Clone, Debug, Deserialize, Eq, PartialEq, Serialize)]
#[serde(deny_unknown_fields)]
pub struct CompatibilityReport {
    pub schema_version: String,
    pub current_consumer: String,
    pub candidate_contract: String,
    pub status: CompatibilityStatus,
    pub reasons: Vec<String>,
    pub proposed_importer: String,
    pub proposal: String,
    pub proposal_grants_authority: bool,
    pub safety_boundaries: BTreeMap<String, bool>,
}

/// Fail-closed errors for the proposed read-only candidate ledger.
#[derive(Debug, Error)]
pub enum MissionBridgeError {
    #[error("terminal readback schema is unsupported")]
    UnsupportedSchema,
    #[error("terminal readback enables authority boundary `{0}`")]
    AuthorityFlagEnabled(String),
    #[error("terminal readback is semantically contradictory")]
    TerminalContradiction,
    #[error("candidate readback digest conflicts: existing {existing}, observed {observed}")]
    ConflictingDigest { existing: Digest, observed: Digest },
    #[error("strict JSON failure: {0}")]
    StrictJson(#[from] StrictJsonError),
}

/// Assesses the current Mission consumer without manufacturing the lease,
/// lineage, or multi-artifact evidence it requires.
///
/// # Errors
///
/// Returns [`MissionBridgeError`] for an unsupported schema, an enabled
/// authority flag, or a terminal contradiction.
pub fn assess_compatibility(
    readback: &TerminalReadback,
) -> Result<CompatibilityReport, MissionBridgeError> {
    validate_candidate_readback(readback)?;
    Ok(CompatibilityReport {
        schema_version: "ao.next.mission-compatibility.v1".into(),
        current_consumer: "ao.canonical-terminal-index.v1".into(),
        candidate_contract: "ao.next.terminal-readback.v1".into(),
        status: CompatibilityStatus::BoundedIncompatibility,
        reasons: vec![
            "Mission's current consumer requires lease and root artifacts whose counts are independently recomputed; one AO Next run readback does not contain them".into(),
            "Mission's current consumer validates ordered lineage across lease, root, and optional terminal artifacts; synthesizing that lineage would fabricate evidence".into(),
        ],
        proposed_importer: "ao.mission.candidate-terminal-readback.v1".into(),
        proposal: "Add a distinct read-only importer that retains the exact candidate bytes and digest as provenance; do not reinterpret the readback as a canonical terminal index".into(),
        proposal_grants_authority: false,
        safety_boundaries: readback.safety_boundaries.clone(),
    })
}

/// An in-memory model of the proposed Mission-side read-only import rule.
/// Exact reimports are idempotent and digest drift fails closed.
#[derive(Clone, Debug, Default)]
pub struct CandidateReadbackLedger {
    retained: Option<(Digest, TerminalReadback)>,
}

impl CandidateReadbackLedger {
    #[must_use]
    pub const fn new() -> Self {
        Self { retained: None }
    }

    /// Validates and imports a candidate readback by its exact canonical digest.
    ///
    /// # Errors
    ///
    /// Returns [`MissionBridgeError`] for unsafe or contradictory readbacks,
    /// serialization failures, or any digest drift after the first import.
    pub fn import(&mut self, readback: &TerminalReadback) -> Result<Digest, MissionBridgeError> {
        validate_candidate_readback(readback)?;
        let observed = canonical_digest(readback)?;
        if let Some((existing, _)) = &self.retained {
            if existing == &observed {
                return Ok(existing.clone());
            }
            return Err(MissionBridgeError::ConflictingDigest {
                existing: existing.clone(),
                observed,
            });
        }
        self.retained = Some((observed.clone(), readback.clone()));
        Ok(observed)
    }
}

fn validate_candidate_readback(readback: &TerminalReadback) -> Result<(), MissionBridgeError> {
    if readback.schema_version != "ao.next.terminal-readback.v1" {
        return Err(MissionBridgeError::UnsupportedSchema);
    }
    if let Some((boundary, _)) = readback
        .safety_boundaries
        .iter()
        .find(|(_, enabled)| **enabled)
    {
        return Err(MissionBridgeError::AuthorityFlagEnabled(boundary.clone()));
    }
    if !matches!(
        readback.terminal_state,
        RunState::Passed | RunState::Failed | RunState::Denied | RunState::Interrupted
    ) || (readback.terminal_state == RunState::Passed
        && readback.exact_next_action.trim().is_empty())
    {
        return Err(MissionBridgeError::TerminalContradiction);
    }
    Ok(())
}
