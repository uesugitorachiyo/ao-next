use std::collections::BTreeMap;
use std::path::PathBuf;

use ao_next_core::contracts::{
    AdapterIdentity, Digest, RunState, SourceIdentity, TerminalReadback, WorkspaceIdentity,
};
use ao_next_core::mission::{
    CandidateReadbackLedger, CompatibilityReport, CompatibilityStatus, MissionBridgeError,
    assess_compatibility,
};
use ao_next_core::strict_json::decode_strict_json;
use chrono::{DateTime, Utc};

const ZERO_DIGEST: &str = "sha256:0000000000000000000000000000000000000000000000000000000000000000";
const ONE_DIGEST: &str = "sha256:1111111111111111111111111111111111111111111111111111111111111111";

fn timestamp(value: &str) -> DateTime<Utc> {
    DateTime::parse_from_rfc3339(value)
        .expect("fixture timestamp")
        .with_timezone(&Utc)
}

fn digest(value: &str) -> Digest {
    Digest::new(value).expect("fixture digest")
}

fn readback() -> TerminalReadback {
    TerminalReadback {
        schema_version: "ao.next.terminal-readback.v1".into(),
        run_id: "run-bridge-01".into(),
        source: SourceIdentity {
            repository: "fixture".into(),
            head: digest(ZERO_DIGEST),
        },
        workspace: WorkspaceIdentity {
            workspace_id: "workspace-bridge-01".into(),
            root: PathBuf::from("/tmp/ao-next-bridge"),
            seed_digest: digest(ONE_DIGEST),
        },
        adapter: AdapterIdentity {
            runtime: "scripted".into(),
            model_identifier: "fixture-model".into(),
            adapter_version: "scripted-v1".into(),
            worker_id: "worker-01".into(),
        },
        request_digest: digest(ZERO_DIGEST),
        policy_digest: digest(ONE_DIGEST),
        verifier_report_digest: digest(ZERO_DIGEST),
        artifact_manifest_digest: digest(ONE_DIGEST),
        terminal_state: RunState::Passed,
        completed_at: timestamp("2026-08-05T12:00:00Z"),
        safety_boundaries: BTreeMap::from([
            ("approves_work".into(), false),
            ("executes_mission_work".into(), false),
            ("grants_authority".into(), false),
            ("publishes".into(), false),
        ]),
        exact_next_action: "Await separately authorized live evaluation".into(),
    }
}

#[test]
fn current_mission_consumer_gets_an_exact_bounded_incompatibility_report() {
    let report = assess_compatibility(&readback()).expect("compatibility report");
    assert_eq!(report.status, CompatibilityStatus::BoundedIncompatibility);
    assert_eq!(report.current_consumer, "ao.canonical-terminal-index.v1");
    assert_eq!(report.candidate_contract, "ao.next.terminal-readback.v1");
    assert!(report.reasons.iter().any(|reason| reason.contains("lease")));
    assert!(
        report
            .reasons
            .iter()
            .any(|reason| reason.contains("lineage"))
    );
    assert!(report.safety_boundaries.values().all(|value| !value));
    assert!(!report.proposal_grants_authority);
}

#[test]
fn compatibility_report_matches_the_checked_in_mission_mapping_fixture() {
    let expected: CompatibilityReport = decode_strict_json(
        include_bytes!("../../../tests/fixtures/mission/compatibility-report-v1.json"),
        16 * 1024,
    )
    .expect("strict fixture");
    assert_eq!(assess_compatibility(&readback()).expect("report"), expected);
}

#[test]
fn proposed_readback_ledger_is_idempotent_by_exact_digest_and_rejects_drift() {
    let mut ledger = CandidateReadbackLedger::new();
    let first = ledger.import(&readback()).expect("first import");
    let second = ledger.import(&readback()).expect("idempotent import");
    assert_eq!(first, second);

    let mut drifted = readback();
    drifted.exact_next_action = "Different next action".into();
    assert!(matches!(
        ledger.import(&drifted),
        Err(MissionBridgeError::ConflictingDigest { .. })
    ));
}

#[test]
fn proposed_readback_ledger_rejects_authority_and_terminal_contradictions() {
    let mut ledger = CandidateReadbackLedger::new();
    let mut unsafe_readback = readback();
    unsafe_readback
        .safety_boundaries
        .insert("publishes".into(), true);
    assert!(matches!(
        ledger.import(&unsafe_readback),
        Err(MissionBridgeError::AuthorityFlagEnabled(_))
    ));

    let mut contradictory = readback();
    contradictory.exact_next_action.clear();
    assert!(matches!(
        ledger.import(&contradictory),
        Err(MissionBridgeError::TerminalContradiction)
    ));
}
