use std::collections::{BTreeMap, BTreeSet};
use std::fs::OpenOptions;
use std::io::Write as _;
use std::path::{Path, PathBuf};

use ao_next_core::adapter::scripted::ScriptedAdapter;
use ao_next_core::adapter::{AdapterAction, AdapterIdentity, AdapterTurn, TokenUsage};
use ao_next_core::contracts::{
    AuthorityEnvelope, Capability, Digest, EffectKind, EffectRequest, ExternalEffectPolicy,
    ModelProfile, NetworkPolicy, RunLimits, RunRequest, RunState, SourceIdentity, VerifierProfile,
    WorkspaceIdentity,
};
use ao_next_core::effects::{EffectBroker, EffectBrokerError, LocalEffectBroker};
use ao_next_core::engine::{DirectEngine, EngineVerifier, VerificationOutcome};
use ao_next_core::evidence::{
    ArtifactSpec, ArtifactStore, StoreLimits, digest_bytes, verify_evidence,
};
use ao_next_core::policy::PolicyDenial;
use ao_next_core::recovery::{
    Checkpoint, CheckpointIdentity, CheckpointJournal, JournalEvent, JournalEventKind,
    write_durable_event_log,
};
use ao_next_core::strict_json::{canonical_digest, decode_strict_json};
use ao_next_eval::metrics::ExecutionVariant;
use chrono::{Duration, Utc};
use serde::Deserialize;

use super::live::{
    LiveVariant, detect_hidden_material_for_campaign, execute_provider_free_row,
    preflight_provider_free_row, provider_free_capture_root, reject_provider_free_input,
    verify_provider_free_capture,
};
use super::{CommandFailure, CommandOutput, QualifyLiveCampaignArgs, read_bounded_regular};

const ROW_COUNT: usize = 27;
const TASKS: [&str; 3] = [
    "greenfield-engineering-app",
    "bounded-defect-repair",
    "artifact-reconciliation",
];
const SCHEDULE: [(ExecutionVariant, u32, u32); 9] = [
    (ExecutionVariant::N0, 0, 0),
    (ExecutionVariant::N4, 0, 1),
    (ExecutionVariant::N7, 0, 2),
    (ExecutionVariant::N4, 1, 3),
    (ExecutionVariant::N7, 1, 4),
    (ExecutionVariant::N0, 1, 5),
    (ExecutionVariant::N7, 2, 6),
    (ExecutionVariant::N0, 2, 7),
    (ExecutionVariant::N4, 2, 8),
];
const NEGATIVES: [&str; 6] = [
    "duplicate-top-level-key",
    "source-identity-drift",
    "adapter-version-drift",
    "symlink-output-schema",
    "nonempty-raw-capture-root",
    "token-envelope-below-564288",
];
const REQUIRED_SECURITY_COVERAGE: [&str; 13] = [
    "denied-rg",
    "denied-python3",
    "denied-shell",
    "denied-network",
    "denied-traversal",
    "denied-symlink",
    "denied-oversized-content",
    "denied-stale-preimage",
    "rejected-malformed-action",
    "detected-hidden-test-exposure",
    "verified-evidence-recovery",
    "prevented-duplicate-effect",
    "replayed-interrupted-checkpoint",
];

#[derive(serde::Serialize)]
struct SecurityCoverageReceipt {
    schema_version: &'static str,
    names: Vec<&'static str>,
    probe_digests: BTreeMap<&'static str, Digest>,
}

#[derive(Deserialize)]
#[serde(deny_unknown_fields)]
struct CampaignQualification {
    schema_version: String,
    rows: Vec<CampaignRow>,
    normalization_root: PathBuf,
    evidence_root: PathBuf,
    mission_evidence: MissionEvidence,
    correlation_chain: CorrelationChain,
}

#[derive(Deserialize)]
#[serde(deny_unknown_fields)]
struct CampaignRow {
    input: PathBuf,
    variant: ExecutionVariant,
}

#[derive(Deserialize)]
#[serde(deny_unknown_fields)]
struct MissionEvidence {
    evidence_id: String,
    status: String,
}

#[derive(Deserialize)]
#[serde(deny_unknown_fields)]
#[allow(
    clippy::struct_field_names,
    reason = "the external correlation contract uses exact *_id field names"
)]
struct CorrelationChain {
    mission_id: String,
    correlation_id: String,
    evidence_id: String,
}

struct RowIdentity {
    task_id: String,
    run_id: String,
    trial_id: String,
    workspace_instance_id: String,
    capture_root: PathBuf,
}

#[allow(
    clippy::too_many_lines,
    reason = "the fixed 27-row boundary remains linear so preflight/execution adjacency is auditable"
)]
pub fn execute(args: &QualifyLiveCampaignArgs) -> Result<CommandOutput, CommandFailure> {
    if std::env::var_os("AO_NEXT_LIVE_PROVIDER_CALLS").is_some() {
        return Err(CommandFailure::authorization(
            "campaign qualification must remain provider-free",
        ));
    }
    let trusted_corpus = parse_digest(&args.trusted_corpus_digest)?;
    let fake_program_digest = parse_digest(&args.fake_provider_program_digest)?;
    let trusted_verifiers = parse_verifier_bindings(&args.trusted_verifier_profiles)?;
    let bytes = read_bounded_regular(&args.qualification)?;
    let qualification: CampaignQualification = decode_strict_json(&bytes, 1024 * 1024)
        .map_err(|error| CommandFailure::invalid_input(error.to_string()))?;
    validate_header(&qualification)?;
    let identities = scan_rows(&qualification, &trusted_corpus, &trusted_verifiers)?;
    create_private_empty_directory(&qualification.normalization_root)?;
    create_private_empty_directory(&qualification.evidence_root)?;
    for identity in &identities {
        create_private_empty_directory(&identity.capture_root)?;
    }

    let n7_index = qualification
        .rows
        .iter()
        .position(|row| row.variant == ExecutionVariant::N7)
        .expect("fixed schedule contains N7");
    run_negative_matrix(
        &qualification.rows[n7_index].input,
        &qualification.evidence_root,
        &trusted_corpus,
        trusted_verifiers
            .get(&identities[n7_index].task_id)
            .expect("scanned verifier binding"),
    )?;
    let security_coverage = run_security_matrix(&qualification.evidence_root)?;
    let security_coverage_digest = canonical_digest(&security_coverage)
        .map_err(|error| CommandFailure::evidence(error.to_string()))?;
    write_json_new(
        &qualification.evidence_root.join("security-coverage.json"),
        &serde_json::to_value(&security_coverage)
            .map_err(|error| CommandFailure::evidence(error.to_string()))?,
    )?;

    let mut successes = 0_usize;
    let mut valid_failures = 0_usize;
    let mut native_write_successes = 0_usize;
    let mut fake_processes = 0_usize;
    let mut record_digests = Vec::with_capacity(ROW_COUNT);
    let mut row_bindings = Vec::with_capacity(ROW_COUNT);
    for (ordinal, row) in qualification.rows.iter().enumerate() {
        let variant = live_variant(row.variant);
        let trusted_verifier = trusted_verifiers
            .get(&identities[ordinal].task_id)
            .expect("scanned verifier binding");
        let preflight = preflight_provider_free_row(
            &row.input,
            variant,
            &trusted_corpus,
            trusted_verifier,
            &args.fake_provider_program,
            &fake_program_digest,
        )?;
        if preflight.expected_ordinal != ordinal
            || preflight.task_id != identities[ordinal].task_id
            || preflight.variant != row.variant
            || preflight.trial_index != SCHEDULE[ordinal % SCHEDULE.len()].1
            || preflight.schedule_position != SCHEDULE[ordinal % SCHEDULE.len()].2
            || preflight.verifier_profile_digest != *trusted_verifier
            || preflight.capture_root != identities[ordinal].capture_root
        {
            return invalid("provider-free row preflight identity drifted");
        }
        let result = execute_provider_free_row(
            &row.input,
            variant,
            &trusted_corpus,
            trusted_verifier,
            &args.fake_provider_program,
            &fake_program_digest,
            &preflight,
        )?;
        if result.run_id != identities[ordinal].run_id
            || result.trial_id != identities[ordinal].trial_id
            || result.workspace_instance_id != identities[ordinal].workspace_instance_id
            || result.task_id != identities[ordinal].task_id
            || result.capture_root != identities[ordinal].capture_root
            || result.workspace_digest
                != parse_digest(
                    result.output.value["measurement"]["workspace_seed_digest"]
                        .as_str()
                        .ok_or_else(|| {
                            CommandFailure::evidence("row workspace digest is missing")
                        })?,
                )?
        {
            return invalid("executed provider-free row binding drifted");
        }
        validate_record(&result.output.value, row, &identities[ordinal])?;
        let task_success = result.output.value["measurement"]["task_success"]
            .as_bool()
            .ok_or_else(|| CommandFailure::evidence("row task outcome is missing"))?;
        successes += usize::from(task_success);
        valid_failures += usize::from(valid_task_failure(
            &result.output.value,
            result.output.status,
        ));
        native_write_successes +=
            usize::from(native_write_succeeded(&result.output.value, row.variant));
        fake_processes += result.fake_processes;
        let record_digest = result.output.value["record_digest"]
            .as_str()
            .ok_or_else(|| CommandFailure::evidence("row record digest is missing"))?
            .to_owned();
        record_digests.push(record_digest);
        row_bindings.push(serde_json::json!({
            "ordinal": ordinal,
            "input_digest": result.input_digest,
            "workspace_digest": result.workspace_digest,
            "authority_digest": result.authority_digest,
            "record_digest": record_digests.last().expect("record digest"),
        }));
        write_json_new(
            &qualification
                .normalization_root
                .join(format!("{ordinal:02}.json")),
            &result.output.value,
        )?;
    }
    if successes == 0 || valid_failures == 0 || native_write_successes == 0 {
        return invalid(
            "executed fake campaign lacks success, valid failure, or native write coverage",
        );
    }

    Ok(CommandOutput::new(
        serde_json::json!({
            "schema_version": "ao.next.campaign-qualification.v2",
            "corpus_digest": trusted_corpus,
            "evidence_id": qualification.mission_evidence.evidence_id,
            "correlation_chain": {
                "mission_id": qualification.correlation_chain.mission_id,
                "correlation_id": qualification.correlation_chain.correlation_id,
                "evidence_id": qualification.correlation_chain.evidence_id,
            },
            "rows": ROW_COUNT,
            "verified_capture_indexes": ROW_COUNT,
            "local_fake_processes": fake_processes,
            "live_provider_processes": 0,
            "live_provider_calls": 0,
            "task_successes": successes,
            "valid_task_failures": valid_failures,
            "native_write_successes": native_write_successes,
            "negative_mutations": NEGATIVES,
            "parser_regressions": [
                "malformed-top-level-input",
                "oversized-top-level-input-1mib"
            ],
            "security_coverage": security_coverage.names,
            "security_coverage_digest": security_coverage_digest,
            "record_digests": record_digests,
            "row_bindings": row_bindings,
        }),
        "executed and qualified exact provider-free 27-row fake campaign",
        0,
    ))
}

fn validate_header(qualification: &CampaignQualification) -> Result<(), CommandFailure> {
    if qualification.schema_version != "ao.next.provider-free-campaign.v2"
        || qualification.rows.len() != ROW_COUNT
        || qualification.mission_evidence.status != "qualified"
        || !sanitized_id(&qualification.mission_evidence.evidence_id)
        || !sanitized_id(&qualification.correlation_chain.mission_id)
        || !sanitized_id(&qualification.correlation_chain.correlation_id)
        || qualification.correlation_chain.evidence_id != qualification.mission_evidence.evidence_id
    {
        return invalid("provider-free campaign header or Mission binding drifted");
    }
    Ok(())
}

fn scan_rows(
    qualification: &CampaignQualification,
    trusted_corpus: &Digest,
    trusted_verifiers: &BTreeMap<String, Digest>,
) -> Result<Vec<RowIdentity>, CommandFailure> {
    let mut input_paths = BTreeSet::new();
    let mut run_ids = BTreeSet::new();
    let mut trial_ids = BTreeSet::new();
    let mut workspace_ids = BTreeSet::new();
    let mut capture_roots = BTreeSet::new();
    let mut identities = Vec::with_capacity(ROW_COUNT);
    for (ordinal, row) in qualification.rows.iter().enumerate() {
        if !row.input.is_absolute() || !input_paths.insert(row.input.clone()) {
            return invalid("campaign input paths are not unique absolute paths");
        }
        let value: serde_json::Value =
            decode_strict_json(&read_bounded_regular(&row.input)?, 1024 * 1024)
                .map_err(|error| CommandFailure::invalid_input(error.to_string()))?;
        let task_id = string_at(&value, "/task_id")?;
        let expected_task = TASKS[ordinal / SCHEDULE.len()];
        let expected_schedule = SCHEDULE[ordinal % SCHEDULE.len()];
        if task_id != expected_task
            || row.variant != expected_schedule.0
            || u32_at(&value, "/trial_index")? != expected_schedule.1
            || u32_at(&value, "/schedule_position")? != expected_schedule.2
            || string_at(&value, "/corpus/corpus_digest")? != trusted_corpus.as_str()
            || string_at(&value, "/request/verifier_profile/profile_digest")?
                != trusted_verifiers
                    .get(&task_id)
                    .ok_or_else(|| CommandFailure::usage("trusted verifier task is missing"))?
                    .as_str()
            || string_at(&value, "/command_verifier/profile_digest")?
                != trusted_verifiers[&task_id].as_str()
        {
            return invalid("campaign row order, identity, corpus, or verifier binding drifted");
        }
        let corpus_tasks = value
            .pointer("/corpus/tasks")
            .and_then(serde_json::Value::as_array)
            .ok_or_else(|| CommandFailure::invalid_input("corpus task list is missing"))?;
        if corpus_tasks.len() != TASKS.len()
            || corpus_tasks.iter().zip(TASKS).any(|(task, expected)| {
                task.get("task_id").and_then(serde_json::Value::as_str) != Some(expected)
                    || task
                        .get("verifier_profile_digest")
                        .and_then(serde_json::Value::as_str)
                        != trusted_verifiers.get(expected).map(Digest::as_str)
            })
        {
            return invalid("sealed three-task corpus binding drifted");
        }
        let identity = RowIdentity {
            task_id,
            run_id: string_at(&value, "/request/run_id")?,
            trial_id: string_at(&value, "/trial_id")?,
            workspace_instance_id: string_at(&value, "/workspace_instance_id")?,
            capture_root: provider_free_capture_root(&row.input)?,
        };
        if !identity.capture_root.is_absolute()
            || !run_ids.insert(identity.run_id.clone())
            || !trial_ids.insert(identity.trial_id.clone())
            || !workspace_ids.insert(identity.workspace_instance_id.clone())
            || !capture_roots.insert(identity.capture_root.clone())
        {
            return invalid("campaign row run, trial, workspace, or capture identity repeated");
        }
        identities.push(identity);
    }
    Ok(identities)
}

fn run_negative_matrix(
    source: &Path,
    evidence_root: &Path,
    trusted_corpus: &Digest,
    trusted_verifier: &Digest,
) -> Result<(), CommandFailure> {
    let bytes = read_bounded_regular(source)?;
    let value: serde_json::Value = decode_strict_json(&bytes, 1024 * 1024)
        .map_err(|error| CommandFailure::invalid_input(error.to_string()))?;
    let cases = evidence_root.join("negative-cases");
    create_private_empty_directory(&cases)?;

    let mut duplicate = b"{\"schema_version\":\"duplicate\",".to_vec();
    duplicate.extend_from_slice(
        bytes
            .strip_prefix(b"{")
            .ok_or_else(|| CommandFailure::invalid_input("live input is not a JSON object"))?,
    );
    let duplicate_path = cases.join("duplicate-top-level-key.json");
    write_bytes_new(&duplicate_path, &duplicate)?;

    let mut source_drift = value.clone();
    source_drift["request"]["source"]["head"] = serde_json::json!(
        "sha256:0000000000000000000000000000000000000000000000000000000000000000"
    );
    let source_drift_path = cases.join("source-identity-drift.json");
    write_json_new(&source_drift_path, &source_drift)?;

    let mut adapter_drift = value.clone();
    adapter_drift["request"]["model_profile"]["adapter_version"] =
        serde_json::json!("drifted-adapter");
    let adapter_drift_path = cases.join("adapter-version-drift.json");
    write_json_new(&adapter_drift_path, &adapter_drift)?;

    let schema_target = cases.join("schema-target.json");
    write_bytes_new(&schema_target, b"{}")?;
    let schema_link = cases.join("schema-link.json");
    create_symlink(&schema_target, &schema_link)?;
    let mut schema_symlink = value.clone();
    schema_symlink["output_schema"] = serde_json::json!(schema_link);
    let schema_symlink_path = cases.join("symlink-output-schema.json");
    write_json_new(&schema_symlink_path, &schema_symlink)?;

    let nonempty_capture = cases.join("nonempty-capture");
    create_private_empty_directory(&nonempty_capture)?;
    write_bytes_new(&nonempty_capture.join("control.json"), b"{}")?;
    let mut nonempty = value.clone();
    nonempty["raw_capture_root"] = serde_json::json!(nonempty_capture);
    let nonempty_path = cases.join("nonempty-raw-capture-root.json");
    write_json_new(&nonempty_path, &nonempty)?;

    let mut low_tokens = value;
    low_tokens["request"]["limits"]["max_tokens"] = serde_json::json!(564_287);
    let low_tokens_path = cases.join("token-envelope-below-564288.json");
    write_json_new(&low_tokens_path, &low_tokens)?;

    for path in [
        duplicate_path,
        source_drift_path,
        adapter_drift_path,
        schema_symlink_path,
        nonempty_path,
        low_tokens_path,
    ] {
        reject_provider_free_input(&path, LiveVariant::N7, trusted_corpus, trusted_verifier)?;
    }

    let malformed = cases.join("malformed-top-level-input.json");
    write_bytes_new(&malformed, b"{")?;
    reject_provider_free_input(
        &malformed,
        LiveVariant::N7,
        trusted_corpus,
        trusted_verifier,
    )?;
    let oversized = cases.join("oversized-top-level-input-1mib.json");
    write_bytes_new(&oversized, &vec![b' '; 1024 * 1024 + 1])?;
    reject_provider_free_input(
        &oversized,
        LiveVariant::N7,
        trusted_corpus,
        trusted_verifier,
    )?;
    Ok(())
}

#[allow(
    clippy::too_many_lines,
    reason = "the fixed security matrix stays linear so every required derived receipt is visible"
)]
fn run_security_matrix(evidence_root: &Path) -> Result<SecurityCoverageReceipt, CommandFailure> {
    let root = evidence_root.join("security-probes");
    create_private_empty_directory(&root)?;
    let workspace = root.join("workspace");
    create_private_empty_directory(&workspace)?;
    let broker = LocalEffectBroker::new(1_000, 4_096, 4);
    let authority = probe_authority(&workspace);
    let mut probe_digests = BTreeMap::new();

    for (name, program) in [
        ("denied-rg", "rg"),
        ("denied-python3", "/usr/bin/python3"),
        ("denied-shell", "/bin/sh"),
    ] {
        let mut effect = probe_effect(EffectKind::RunProgram);
        effect.program = Some(program.into());
        effect.args = vec!["--version".into()];
        effect.timeout_ms = 1;
        if !matches!(
            broker.authorize(&effect, &authority),
            Err(PolicyDenial::ModelProgramDenied)
        ) {
            return invalid("model program denial coverage probe drifted");
        }
        record_probe(&mut probe_digests, name, serde_json::json!(program))?;
    }

    if !matches!(
        broker.authorize(&probe_effect(EffectKind::Network), &authority),
        Err(PolicyDenial::NetworkDenied)
    ) {
        return invalid("network denial coverage probe drifted");
    }
    record_probe(
        &mut probe_digests,
        "denied-network",
        serde_json::json!("network-policy-denied"),
    )?;

    let mut traversal = probe_effect(EffectKind::ReadFile);
    traversal.paths = vec!["../outside".into()];
    if !matches!(
        broker.authorize(&traversal, &authority),
        Err(PolicyDenial::ParentTraversal(_))
    ) {
        return invalid("traversal denial coverage probe drifted");
    }
    record_probe(
        &mut probe_digests,
        "denied-traversal",
        serde_json::json!(traversal.paths),
    )?;

    let symlink_target = root.join("symlink-target.txt");
    write_bytes_new(&symlink_target, b"outside")?;
    create_symlink(&symlink_target, &workspace.join("link.txt"))?;
    let mut symlink = probe_effect(EffectKind::ReadFile);
    symlink.paths = vec!["link.txt".into()];
    if !matches!(
        broker.authorize(&symlink, &authority),
        Err(PolicyDenial::SymlinkNotAllowed(_))
    ) {
        return invalid("symlink denial coverage probe drifted");
    }
    record_probe(
        &mut probe_digests,
        "denied-symlink",
        serde_json::json!(digest_bytes(b"outside")),
    )?;
    std::fs::remove_file(workspace.join("link.txt"))
        .map_err(|error| CommandFailure::evidence(error.to_string()))?;

    let mut oversized = probe_effect(EffectKind::WriteFile);
    oversized.paths = vec!["oversized.txt".into()];
    oversized.content = Some("12345".into());
    oversized.input_digest = digest_bytes(b"ao.next.file-does-not-exist.v1");
    if !matches!(
        broker.execute(&oversized, &authority),
        Err(EffectBrokerError::OutputTooLarge { limit: 4 })
    ) || workspace.join("oversized.txt").exists()
    {
        return invalid("oversized native content coverage probe drifted");
    }
    record_probe(
        &mut probe_digests,
        "denied-oversized-content",
        serde_json::json!(4),
    )?;

    write_bytes_new(&workspace.join("stale.txt"), b"current")?;
    let mut stale = probe_effect(EffectKind::WriteFile);
    stale.paths = vec!["stale.txt".into()];
    stale.content = Some("next".into());
    stale.input_digest = digest_bytes(b"stale");
    if !matches!(
        broker.execute(&stale, &authority),
        Err(EffectBrokerError::PreimageMismatch)
    ) || std::fs::read(workspace.join("stale.txt"))
        .map_err(|error| CommandFailure::evidence(error.to_string()))?
        != b"current"
    {
        return invalid("stale native preimage coverage probe drifted");
    }
    record_probe(
        &mut probe_digests,
        "denied-stale-preimage",
        serde_json::json!(digest_bytes(b"current")),
    )?;

    let malformed = br#"{"actions":[{"kind":"effect","value":{"effect_id":"missing-fields"}}],"usage":{"input_tokens":0,"cached_input_tokens":0,"reasoning_tokens":0,"output_tokens":0,"output_bytes":0},"model_claimed_success":false,"control_mutations":[]}"#;
    if decode_strict_json::<AdapterTurn>(malformed, 4_096).is_ok() {
        return invalid("malformed action coverage probe drifted");
    }
    record_probe(
        &mut probe_digests,
        "rejected-malformed-action",
        serde_json::json!(digest_bytes(malformed)),
    )?;

    let hidden = b"sealed-hidden-canary".to_vec();
    let mut embedded = b"public-prefix:".to_vec();
    embedded.extend_from_slice(&hidden);
    write_bytes_new(&workspace.join("embedded-hidden.txt"), &embedded)?;
    if !detect_hidden_material_for_campaign(&workspace, std::slice::from_ref(&hidden), 4_096)? {
        return invalid("hidden exposure coverage probe drifted");
    }
    record_probe(
        &mut probe_digests,
        "detected-hidden-test-exposure",
        serde_json::json!(digest_bytes(&hidden)),
    )?;

    let artifact = workspace.join("artifact.txt");
    write_bytes_new(&artifact, b"recoverable-evidence")?;
    let store_root = root.join("evidence-store");
    let store = ArtifactStore::new(
        &store_root,
        vec![workspace.clone()],
        StoreLimits {
            max_artifact_bytes: 4_096,
            max_total_bytes: 4_096,
        },
    )
    .map_err(|error| CommandFailure::evidence(error.to_string()))?;
    let artifact_spec = ArtifactSpec {
        artifact_id: "security-probe-artifact".into(),
        path: artifact,
        original_ref: "workspace/artifact.txt".into(),
        media_type: "text/plain".into(),
        producer: "provider-free-security-matrix".into(),
        input_digests: Vec::new(),
    };
    let entry = store
        .retain(&artifact_spec)
        .map_err(|error| CommandFailure::evidence(error.to_string()))?;
    let request = probe_request(&workspace);
    let manifest = ao_next_core::contracts::ArtifactManifest {
        schema_version: "ao.next.artifact-manifest.v1".into(),
        run_id: request.run_id.clone(),
        source: request.source.clone(),
        entries: vec![entry.clone()],
    };
    let retained = store_root.join(&entry.content_ref);
    std::fs::remove_file(&retained).map_err(|error| CommandFailure::evidence(error.to_string()))?;
    if verify_evidence(&store_root, &manifest, 4_096).is_ok() {
        return invalid("missing retained evidence unexpectedly verified");
    }
    let recovered = store
        .retain(&artifact_spec)
        .map_err(|error| CommandFailure::evidence(error.to_string()))?;
    if recovered != entry {
        return invalid("recovered evidence identity drifted");
    }
    verify_evidence(&store_root, &manifest, 4_096)
        .map_err(|error| CommandFailure::evidence(error.to_string()))?;
    record_probe(
        &mut probe_digests,
        "verified-evidence-recovery",
        serde_json::json!(entry.digest),
    )?;

    let first = probe_write("duplicate-effect", "first.txt", "first");
    let second = probe_write("duplicate-effect", "second.txt", "second");
    let identity = AdapterIdentity {
        runtime: request.model_profile.runtime.clone(),
        model_identifier: request.model_profile.model_identifier.clone(),
        adapter_version: request.model_profile.adapter_version.clone(),
        worker_id: "provider-free-security-worker".into(),
    };
    let mut adapter = ScriptedAdapter::new(
        identity,
        [Ok(AdapterTurn {
            actions: vec![
                AdapterAction::Effect(first),
                AdapterAction::Effect(second),
                AdapterAction::Verify,
            ],
            usage: TokenUsage::default(),
            model_claimed_success: false,
            control_mutations: Vec::new(),
        })],
    );
    let mut verifier = PassingProbeVerifier;
    let outcome = DirectEngine::new(&LocalEffectBroker::new(1_000, 4_096, 4_096)).run(
        &request,
        &mut adapter,
        &mut verifier,
    );
    if outcome.terminal_state != RunState::Denied
        || outcome.failure_code.as_deref() != Some("duplicate_effect")
        || !workspace.join("first.txt").is_file()
        || workspace.join("second.txt").exists()
    {
        return invalid("duplicate effect coverage probe drifted");
    }
    record_probe(
        &mut probe_digests,
        "prevented-duplicate-effect",
        serde_json::json!(outcome.failure_code),
    )?;

    let recovery_root = root.join("recovery");
    let events_path = root.join("interrupted-events.jsonl");
    let events = vec![
        JournalEvent {
            schema_version: "ao.next.journal-event.v1".into(),
            sequence: 0,
            kind: JournalEventKind::EffectCommitted {
                effect_id: "effect-committed-before-interrupt".into(),
            },
        },
        JournalEvent {
            schema_version: "ao.next.journal-event.v1".into(),
            sequence: 1,
            kind: JournalEventKind::VerifierRecorded {
                report_digest: digest_bytes(b"durable-verifier-event"),
            },
        },
    ];
    let events_digest = write_durable_event_log(&events_path, &events, 4_096)
        .map_err(|error| CommandFailure::evidence(error.to_string()))?;
    let checkpoint_identity = CheckpointIdentity::from_request(&request)
        .map_err(|error| CommandFailure::evidence(error.to_string()))?;
    let journal = CheckpointJournal::new(&recovery_root, 4_096)
        .map_err(|error| CommandFailure::evidence(error.to_string()))?;
    journal
        .commit(
            &Checkpoint {
                schema_version: "ao.next.checkpoint.v1".into(),
                run_id: request.run_id,
                sequence: 2,
                identity: checkpoint_identity.clone(),
                committed_effects: BTreeSet::from(["effect-committed-before-interrupt".into()]),
                events_digest: events_digest.clone(),
                recorded_at: Utc::now(),
            },
            &events_path,
        )
        .map_err(|error| CommandFailure::evidence(error.to_string()))?;
    let plan = journal
        .resume(
            &checkpoint_identity,
            &[
                "effect-committed-before-interrupt".into(),
                "effect-after-replay".into(),
            ],
        )
        .map_err(|error| CommandFailure::evidence(error.to_string()))?;
    if plan.skipped_committed_effects != ["effect-committed-before-interrupt"]
        || plan.remaining_effects != ["effect-after-replay"]
    {
        return invalid("interrupted checkpoint replay coverage probe drifted");
    }
    record_probe(
        &mut probe_digests,
        "replayed-interrupted-checkpoint",
        serde_json::json!(events_digest),
    )?;

    let observed = probe_digests.keys().copied().collect::<BTreeSet<_>>();
    let required = REQUIRED_SECURITY_COVERAGE
        .iter()
        .copied()
        .collect::<BTreeSet<_>>();
    if observed != required {
        return invalid("provider-free security coverage matrix is incomplete");
    }
    Ok(SecurityCoverageReceipt {
        schema_version: "ao.next.provider-free-security-coverage.v1",
        names: REQUIRED_SECURITY_COVERAGE.to_vec(),
        probe_digests,
    })
}

fn probe_authority(workspace: &Path) -> AuthorityEnvelope {
    AuthorityEnvelope {
        schema_version: "ao.next.authority-envelope.v1".into(),
        issued_by: "provider-free-security-matrix".into(),
        issued_at: Utc::now() - Duration::hours(1),
        expires_at: Utc::now() + Duration::hours(1),
        capabilities: BTreeSet::from([
            Capability::ReadWorkspace,
            Capability::WriteWorkspace,
            Capability::RunLocalProgram,
            Capability::NetworkAccess,
        ]),
        allowed_roots: vec![workspace.to_path_buf()],
        allowed_programs: BTreeSet::from([
            "rg".into(),
            "/usr/bin/python3".into(),
            "/bin/sh".into(),
        ]),
        network: NetworkPolicy::Denied,
        allowed_network_hosts: BTreeSet::new(),
        external_effects: ExternalEffectPolicy::Denied,
    }
}

fn probe_effect(kind: EffectKind) -> EffectRequest {
    EffectRequest {
        effect_id: "security-probe-effect".into(),
        run_id: "provider-free-security-run".into(),
        kind,
        program: None,
        content: None,
        args: Vec::new(),
        paths: Vec::new(),
        timeout_ms: 0,
        input_digest: digest_bytes(b"security-probe-input"),
    }
}

fn probe_write(effect_id: &str, path: &str, content: &str) -> EffectRequest {
    EffectRequest {
        effect_id: effect_id.into(),
        run_id: "provider-free-security-run".into(),
        kind: EffectKind::WriteFile,
        program: None,
        content: Some(content.into()),
        args: Vec::new(),
        paths: vec![path.into()],
        timeout_ms: 0,
        input_digest: digest_bytes(b"ao.next.file-does-not-exist.v1"),
    }
}

fn probe_request(workspace: &Path) -> RunRequest {
    RunRequest {
        schema_version: "ao.next.run-request.v1".into(),
        run_id: "provider-free-security-run".into(),
        objective: "derive fixed provider-free security coverage".into(),
        source: SourceIdentity {
            repository: "provider-free-security-matrix".into(),
            head: digest_bytes(b"security-probe-source"),
        },
        workspace: WorkspaceIdentity {
            workspace_id: "provider-free-security-workspace".into(),
            root: workspace.to_path_buf(),
            seed_digest: digest_bytes(b"security-probe-workspace"),
        },
        model_profile: ModelProfile {
            runtime: "scripted".into(),
            model_identifier: "provider-free-security-model".into(),
            reasoning_effort: "high".into(),
            system_prompt_digest: digest_bytes(b"security-probe-prompt"),
            tool_contract_digest: digest_bytes(b"security-probe-tools"),
            context_limit: 4_096,
            output_limit: 1_024,
            adapter_version: "scripted-security-v1".into(),
        },
        authority: probe_authority(workspace),
        verifier_profile: VerifierProfile {
            profile_id: "provider-free-security-verifier".into(),
            profile_digest: digest_bytes(b"security-probe-verifier"),
            commands: Vec::new(),
            required_artifacts: Vec::new(),
        },
        policy_digest: digest_bytes(b"security-probe-policy"),
        limits: RunLimits {
            max_input_bytes: 4_096,
            max_turns: 1,
            max_repair_attempts: 0,
            max_run_ms: 1_000,
            max_effect_timeout_ms: 1_000,
            max_output_bytes: 4_096,
            max_tokens: 4_096,
        },
    }
}

struct PassingProbeVerifier;

impl EngineVerifier for PassingProbeVerifier {
    fn verify(&mut self, _: &RunRequest) -> VerificationOutcome {
        VerificationOutcome {
            passed: true,
            report_digest: digest_bytes(b"security-probe-verifier-pass"),
            summary: "provider-free security probe passed".into(),
        }
    }
}

fn record_probe(
    receipts: &mut BTreeMap<&'static str, Digest>,
    name: &'static str,
    evidence: serde_json::Value,
) -> Result<(), CommandFailure> {
    let digest = canonical_digest(&(name, evidence))
        .map_err(|error| CommandFailure::evidence(error.to_string()))?;
    if receipts.insert(name, digest).is_some() {
        return invalid("provider-free security coverage identity repeated");
    }
    Ok(())
}

fn validate_record(
    value: &serde_json::Value,
    row: &CampaignRow,
    identity: &RowIdentity,
) -> Result<(), CommandFailure> {
    if value["schema_version"] != "ao.next.live-run-record.v1"
        || value["variant"] != variant_name(row.variant)
        || value["measurement"]["measurement_origin"] != "offline_fixture"
        || value["measurement"]["task_id"] != identity.task_id
        || value["measurement"]["run_id"] != identity.run_id
        || value["measurement"]["trial_id"] != identity.trial_id
        || value["measurement"]["workspace_instance_id"] != identity.workspace_instance_id
        || value["measurement"]["worker_count"] != 1
        || value["measurement"]["dynamic_fanout"] != false
        || value["capture_digests"]
            .as_array()
            .is_none_or(|digests| digests.len() != 1)
        || !identity.capture_root.join("capture-index.json").is_file()
    {
        return Err(CommandFailure::evidence(
            "executed provider-free row record or capture binding drifted",
        ));
    }
    let index_digest = value["raw_capture_index_digest"]
        .as_str()
        .ok_or_else(|| CommandFailure::evidence("raw capture index digest is missing"))
        .and_then(|value| {
            Digest::new(value).map_err(|error| CommandFailure::evidence(error.to_string()))
        })?;
    verify_provider_free_capture(&row.input, live_variant(row.variant), &index_digest)?;
    Ok(())
}

fn native_write_succeeded(value: &serde_json::Value, variant: ExecutionVariant) -> bool {
    variant == ExecutionVariant::N7
        && value["measurement"]["task_success"] == true
        && value["measurement"]["changed_files"]
            .as_u64()
            .is_some_and(|count| count > 0)
        && value["native_effect_observations"]
            .as_array()
            .is_some_and(|observations| !observations.is_empty())
}

fn valid_task_failure(value: &serde_json::Value, status: u8) -> bool {
    status == 5 && value["measurement"]["task_success"] == false
}

fn parse_verifier_bindings(values: &[String]) -> Result<BTreeMap<String, Digest>, CommandFailure> {
    let mut bindings = BTreeMap::new();
    for value in values {
        let (task, digest) = value.split_once('=').ok_or_else(|| {
            CommandFailure::usage("trusted verifier binding must be TASK_ID=SHA256")
        })?;
        if !TASKS.contains(&task)
            || bindings
                .insert(task.to_owned(), parse_digest(digest)?)
                .is_some()
        {
            return Err(CommandFailure::usage(
                "trusted verifier bindings are duplicate or unknown",
            ));
        }
    }
    if bindings.len() != TASKS.len() {
        return Err(CommandFailure::usage(
            "exactly three trusted verifier bindings are required",
        ));
    }
    Ok(bindings)
}

fn parse_digest(value: &str) -> Result<Digest, CommandFailure> {
    Digest::new(value).map_err(|error| CommandFailure::usage(error.to_string()))
}

fn create_private_empty_directory(path: &Path) -> Result<(), CommandFailure> {
    if !path.is_absolute() {
        return invalid("campaign output directory is not absolute");
    }
    std::fs::create_dir_all(path)
        .map_err(|error| CommandFailure::invalid_input(error.to_string()))?;
    let metadata = std::fs::symlink_metadata(path)
        .map_err(|error| CommandFailure::invalid_input(error.to_string()))?;
    if metadata.file_type().is_symlink() || !metadata.is_dir() {
        return invalid("campaign output path is not a regular directory");
    }
    #[cfg(unix)]
    {
        use std::os::unix::fs::PermissionsExt as _;
        std::fs::set_permissions(path, std::fs::Permissions::from_mode(0o700))
            .map_err(|error| CommandFailure::invalid_input(error.to_string()))?;
    }
    if std::fs::read_dir(path)
        .map_err(|error| CommandFailure::invalid_input(error.to_string()))?
        .next()
        .is_some()
    {
        return invalid("campaign output directory is not empty");
    }
    Ok(())
}

fn write_json_new(path: &Path, value: &serde_json::Value) -> Result<(), CommandFailure> {
    let bytes =
        serde_json::to_vec(value).map_err(|error| CommandFailure::evidence(error.to_string()))?;
    write_bytes_new(path, &bytes)
}

fn write_bytes_new(path: &Path, bytes: &[u8]) -> Result<(), CommandFailure> {
    let mut options = OpenOptions::new();
    options.write(true).create_new(true);
    #[cfg(unix)]
    {
        use std::os::unix::fs::OpenOptionsExt as _;
        options.mode(0o600);
    }
    let mut file = options
        .open(path)
        .map_err(|error| CommandFailure::evidence(error.to_string()))?;
    file.write_all(bytes)
        .and_then(|()| file.sync_all())
        .map_err(|error| CommandFailure::evidence(error.to_string()))
}

#[cfg(unix)]
fn create_symlink(target: &Path, link: &Path) -> Result<(), CommandFailure> {
    std::os::unix::fs::symlink(target, link)
        .map_err(|error| CommandFailure::evidence(error.to_string()))
}

#[cfg(not(unix))]
fn create_symlink(_: &Path, _: &Path) -> Result<(), CommandFailure> {
    Err(CommandFailure::invalid_input(
        "symlink admission regression is unavailable on this platform",
    ))
}

fn string_at(value: &serde_json::Value, pointer: &str) -> Result<String, CommandFailure> {
    value
        .pointer(pointer)
        .and_then(serde_json::Value::as_str)
        .map(ToOwned::to_owned)
        .ok_or_else(|| CommandFailure::invalid_input(format!("{pointer} is missing")))
}

fn u32_at(value: &serde_json::Value, pointer: &str) -> Result<u32, CommandFailure> {
    value
        .pointer(pointer)
        .and_then(serde_json::Value::as_u64)
        .and_then(|value| u32::try_from(value).ok())
        .ok_or_else(|| CommandFailure::invalid_input(format!("{pointer} is missing")))
}

fn sanitized_id(value: &str) -> bool {
    !value.is_empty()
        && value.len() <= 128
        && value
            .bytes()
            .all(|byte| byte.is_ascii_alphanumeric() || matches!(byte, b'-' | b'_' | b'.'))
}

const fn live_variant(variant: ExecutionVariant) -> LiveVariant {
    match variant {
        ExecutionVariant::N0 => LiveVariant::N0,
        ExecutionVariant::N4 => LiveVariant::N4,
        ExecutionVariant::N7 => LiveVariant::N7,
    }
}

const fn variant_name(variant: ExecutionVariant) -> &'static str {
    match variant {
        ExecutionVariant::N0 => "N0",
        ExecutionVariant::N4 => "N4",
        ExecutionVariant::N7 => "N7",
    }
}

fn invalid<T>(message: &str) -> Result<T, CommandFailure> {
    Err(CommandFailure::invalid_input(message))
}

#[cfg(test)]
mod tests {
    use tempfile::TempDir;

    use super::*;

    #[test]
    fn campaign_record_rejects_an_unbound_capture_index() {
        let temporary = TempDir::new().expect("temporary");
        let capture_root = temporary.path().join("captures");
        std::fs::create_dir(&capture_root).expect("capture root");
        std::fs::write(capture_root.join("capture-index.json"), b"{}")
            .expect("malformed capture index");
        let row = CampaignRow {
            input: temporary.path().join("input.json"),
            variant: ExecutionVariant::N7,
        };
        let identity = RowIdentity {
            task_id: "greenfield-engineering-app".into(),
            run_id: "run-01".into(),
            trial_id: "trial-01".into(),
            workspace_instance_id: "workspace-01".into(),
            capture_root,
        };
        let value = serde_json::json!({
            "schema_version": "ao.next.live-run-record.v1",
            "variant": "N7",
            "measurement": {
                "measurement_origin": "offline_fixture",
                "task_id": identity.task_id,
                "run_id": identity.run_id,
                "trial_id": identity.trial_id,
                "workspace_instance_id": identity.workspace_instance_id,
                "worker_count": 1,
                "dynamic_fanout": false
            },
            "capture_digests": [
                "sha256:0000000000000000000000000000000000000000000000000000000000000000"
            ],
            "raw_capture_index_digest":
                "sha256:0000000000000000000000000000000000000000000000000000000000000000"
        });

        validate_record(&value, &row, &identity)
            .expect_err("capture-index existence alone must not qualify a row");
    }

    #[test]
    fn native_write_success_requires_a_passing_changed_n7_workspace() {
        let mut value = serde_json::json!({
            "measurement": {"task_success": true, "changed_files": 0},
            "native_effect_observations": [{"effect_id": "read-source"}]
        });
        assert!(!native_write_succeeded(&value, ExecutionVariant::N7));

        value["measurement"]["changed_files"] = serde_json::json!(1);
        assert!(native_write_succeeded(&value, ExecutionVariant::N7));

        value["measurement"]["task_success"] = serde_json::json!(false);
        assert!(!native_write_succeeded(&value, ExecutionVariant::N7));
        assert!(!native_write_succeeded(&value, ExecutionVariant::N4));
    }

    #[test]
    fn valid_task_failure_requires_the_verifier_failure_status() {
        let failed = serde_json::json!({"measurement": {"task_success": false}});
        assert!(valid_task_failure(&failed, 5));
        assert!(!valid_task_failure(&failed, 4));
        assert!(!valid_task_failure(&failed, 7));

        let passed = serde_json::json!({"measurement": {"task_success": true}});
        assert!(!valid_task_failure(&passed, 5));
    }
}
