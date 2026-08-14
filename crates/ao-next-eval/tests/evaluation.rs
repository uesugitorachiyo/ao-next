use std::collections::BTreeSet;

use ao_next_core::evidence::digest_bytes;
use ao_next_core::strict_json::{canonical_digest, decode_strict_json};
use ao_next_eval::comparison::{
    ComparisonRequest, EvaluationDecision, EvaluationError, RecoveryQualification,
    evaluate_live_authorized, evaluate_offline,
};
use ao_next_eval::corpus::{
    CorpusKind, CorpusManifest, EvaluationTask, ScheduleEntry, VariantProfile,
};
use ao_next_eval::metrics::{ExecutionVariant, MeasurementOrigin, RunMeasurement, TokenRow};

fn task(id: &str) -> EvaluationTask {
    EvaluationTask {
        task_id: id.into(),
        task_kind: match id {
            "greenfield" => "greenfield_engineering_application",
            "defect" => "bounded_public_defect_repair",
            _ => "ao_compatible_reconciliation",
        }
        .into(),
        source_digest: digest_bytes(format!("{id}:source").as_bytes()),
        objective_digest: digest_bytes(format!("{id}:objective").as_bytes()),
        workspace_seed_digest: digest_bytes(format!("{id}:workspace").as_bytes()),
        visible_fixtures_digest: digest_bytes(format!("{id}:visible").as_bytes()),
        hidden_tests_digest: digest_bytes(format!("{id}:hidden").as_bytes()),
        verifier_profile_digest: digest_bytes(format!("{id}:verifier").as_bytes()),
        variant_profiles: [
            (ExecutionVariant::N0, "current-ao"),
            (ExecutionVariant::N4, "direct-model"),
            (ExecutionVariant::N7, "ao-next"),
        ]
        .into_iter()
        .map(|(variant, runtime)| VariantProfile {
            variant,
            runtime: runtime.into(),
            runtime_digest: digest_bytes(format!("{id}:{runtime}:runtime").as_bytes()),
            model_identifier: "offline-evaluation-model".into(),
            model_digest: digest_bytes(format!("{id}:{runtime}:model").as_bytes()),
            prompt_digest: digest_bytes(format!("{id}:{runtime}:prompt").as_bytes()),
            policy_digest: digest_bytes(format!("{id}:{runtime}:policy").as_bytes()),
            adapter_version: "offline-adapter-v1".into(),
            adapter_digest: digest_bytes(format!("{id}:{runtime}:adapter").as_bytes()),
        })
        .collect(),
    }
}

fn corpus() -> CorpusManifest {
    let tasks = vec![task("greenfield"), task("defect"), task("reconciliation")];
    let schedule = schedule();
    let mut corpus = CorpusManifest {
        schema_version: "ao.next.evaluation-corpus.v2".into(),
        corpus_kind: CorpusKind::SyntheticUnitTest,
        corpus_digest: digest_bytes(b"pending corpus digest"),
        required_trial_count: 3,
        schedule,
        tasks,
    };
    corpus.corpus_digest = corpus.calculated_digest().expect("corpus digest");
    corpus
}

fn measurement(
    corpus: &CorpusManifest,
    task: &EvaluationTask,
    variant: ExecutionVariant,
    trial_index: u32,
) -> RunMeasurement {
    let (total_tokens, wall_clock_ms) = match variant {
        ExecutionVariant::N0 => (400, 400),
        ExecutionVariant::N4 => (100, 200),
        ExecutionVariant::N7 => (110, 250),
    };
    let raw_capture_digests = vec![digest_bytes(
        format!("capture:{}:{variant:?}:{trial_index}", task.task_id).as_bytes(),
    )];
    RunMeasurement {
        schema_version: "ao.next.run-measurement.v2".into(),
        corpus_digest: corpus.corpus_digest.clone(),
        run_id: format!("run-{}-{variant:?}-{trial_index}", task.task_id),
        trial_id: format!("trial-{}-{variant:?}-{trial_index}", task.task_id),
        trial_index,
        schedule_position: schedule_position(trial_index, variant),
        raw_capture_digest: canonical_digest(&raw_capture_digests).expect("capture manifest"),
        raw_capture_digests,
        workspace_instance_id: format!(
            "workspace-instance-{}-{variant:?}-{trial_index}",
            task.task_id
        ),
        task_id: task.task_id.clone(),
        variant,
        source_digest: task.source_digest.clone(),
        objective_digest: task.objective_digest.clone(),
        workspace_seed_digest: task.workspace_seed_digest.clone(),
        visible_fixtures_digest: task.visible_fixtures_digest.clone(),
        hidden_tests_digest: task.hidden_tests_digest.clone(),
        verifier_profile_digest: task.verifier_profile_digest.clone(),
        runtime: match variant {
            ExecutionVariant::N0 => "current-ao",
            ExecutionVariant::N4 => "direct-model",
            ExecutionVariant::N7 => "ao-next",
        }
        .into(),
        runtime_digest: profile(task, variant).runtime_digest.clone(),
        model_identifier: "offline-evaluation-model".into(),
        model_digest: profile(task, variant).model_digest.clone(),
        prompt_digest: profile(task, variant).prompt_digest.clone(),
        policy_digest: profile(task, variant).policy_digest.clone(),
        adapter_version: "offline-adapter-v1".into(),
        adapter_digest: profile(task, variant).adapter_digest.clone(),
        measurement_origin: MeasurementOrigin::OfflineFixture,
        provider_usage_trusted: true,
        tokens: TokenRow {
            input_tokens: Some(total_tokens),
            cached_input_tokens: Some(0),
            reasoning_tokens: Some(0),
            output_tokens: Some(0),
            reported_total_tokens: total_tokens,
        },
        wall_clock_ms,
        model_wait_ms: wall_clock_ms / 2,
        worker_turns: 1,
        repair_attempts: 0,
        operator_interventions: 0,
        changed_files: 2,
        accepted_changed_files: 2,
        task_success: true,
        hidden_tests_passed: 10,
        hidden_tests_total: 10,
        regressions: 0,
        unauthorized_effects: 0,
        evidence_complete: true,
        evidence_digest_valid: true,
        recovery_attempted: variant == ExecutionVariant::N7 && task.task_id == "greenfield",
        recovery_no_duplicate_effect: true,
        cross_runtime_agreement: true,
        worker_count: 1,
        dynamic_fanout: false,
        hidden_test_exposure: false,
    }
}

fn ready_request() -> ComparisonRequest {
    let corpus = corpus();
    let runs = corpus
        .tasks
        .iter()
        .flat_map(|task| {
            schedule()
                .into_iter()
                .map(|entry| measurement(&corpus, task, entry.variant, entry.trial_index))
        })
        .collect();
    ComparisonRequest {
        schema_version: "ao.next.comparison-request.v2".into(),
        corpus,
        runs,
        recovery_qualification: None,
        recovery_qualification_digest: None,
    }
}

fn qualified_recovery_request() -> ComparisonRequest {
    let mut request = ready_request();
    for run in &mut request.runs {
        run.recovery_attempted = false;
        run.recovery_no_duplicate_effect = false;
    }
    let qualification = RecoveryQualification {
        schema_version: "ao.next.recovery-qualification.v1".into(),
        corpus_digest: request.corpus.corpus_digest.clone(),
        n7_adapter_digests: request
            .corpus
            .tasks
            .iter()
            .flat_map(|task| &task.variant_profiles)
            .filter(|profile| profile.variant == ExecutionVariant::N7)
            .map(|profile| profile.adapter_digest.clone())
            .collect::<BTreeSet<_>>(),
        replayed_checkpoint_probe_digest: digest_bytes(b"replayed-checkpoint"),
        prevented_duplicate_effect_probe_digest: digest_bytes(b"prevented-duplicate-effect"),
        recovery_attempted: true,
        recovery_no_duplicate_effect: true,
        live_provider_processes: 0,
    };
    request.recovery_qualification_digest =
        Some(canonical_digest(&qualification).expect("recovery qualification digest"));
    request.recovery_qualification = Some(qualification);
    request
}

fn recovery_gate_passed(request: &ComparisonRequest) -> bool {
    evaluate_offline(request)
        .expect("comparison")
        .gates
        .into_iter()
        .find(|gate| gate.gate_id == "recovery_without_duplicate_effect")
        .expect("recovery gate")
        .passed
}

#[test]
fn provider_free_recovery_qualification_satisfies_recovery_without_repair_attempts() {
    let request = qualified_recovery_request();

    assert!(recovery_gate_passed(&request));
}

#[test]
fn recovery_qualification_rejects_missing_altered_or_mismatched_evidence() {
    let valid = qualified_recovery_request();
    let mut cases = Vec::new();

    let mut missing_digest = valid.clone();
    missing_digest.recovery_qualification_digest = None;
    cases.push(missing_digest);

    let mut altered_probe = valid.clone();
    altered_probe
        .recovery_qualification
        .as_mut()
        .expect("qualification")
        .replayed_checkpoint_probe_digest = digest_bytes(b"altered");
    cases.push(altered_probe);

    let mut wrong_corpus = valid.clone();
    wrong_corpus
        .recovery_qualification
        .as_mut()
        .expect("qualification")
        .corpus_digest = digest_bytes(b"wrong corpus");
    wrong_corpus.recovery_qualification_digest = Some(
        canonical_digest(
            wrong_corpus
                .recovery_qualification
                .as_ref()
                .expect("qualification"),
        )
        .expect("digest"),
    );
    cases.push(wrong_corpus);

    let mut wrong_adapters = valid.clone();
    wrong_adapters
        .recovery_qualification
        .as_mut()
        .expect("qualification")
        .n7_adapter_digests = BTreeSet::from([digest_bytes(b"wrong adapter")]);
    wrong_adapters.recovery_qualification_digest = Some(
        canonical_digest(
            wrong_adapters
                .recovery_qualification
                .as_ref()
                .expect("qualification"),
        )
        .expect("digest"),
    );
    cases.push(wrong_adapters);

    let mut reused_probe = valid.clone();
    let qualification = reused_probe
        .recovery_qualification
        .as_mut()
        .expect("qualification");
    qualification.prevented_duplicate_effect_probe_digest =
        qualification.replayed_checkpoint_probe_digest.clone();
    reused_probe.recovery_qualification_digest =
        Some(canonical_digest(qualification).expect("digest"));
    cases.push(reused_probe);

    for field in ["recovery_attempted", "recovery_no_duplicate_effect"] {
        let mut incomplete = valid.clone();
        let qualification = incomplete
            .recovery_qualification
            .as_mut()
            .expect("qualification");
        if field == "recovery_attempted" {
            qualification.recovery_attempted = false;
        } else {
            qualification.recovery_no_duplicate_effect = false;
        }
        incomplete.recovery_qualification_digest =
            Some(canonical_digest(qualification).expect("digest"));
        cases.push(incomplete);
    }

    let mut provider_claim = valid;
    let qualification = provider_claim
        .recovery_qualification
        .as_mut()
        .expect("qualification");
    qualification.live_provider_processes = 1;
    provider_claim.recovery_qualification_digest =
        Some(canonical_digest(qualification).expect("digest"));
    cases.push(provider_claim);

    assert!(cases.iter().all(|request| !recovery_gate_passed(request)));
}

fn schedule() -> Vec<ScheduleEntry> {
    [
        [
            ExecutionVariant::N0,
            ExecutionVariant::N4,
            ExecutionVariant::N7,
        ],
        [
            ExecutionVariant::N4,
            ExecutionVariant::N7,
            ExecutionVariant::N0,
        ],
        [
            ExecutionVariant::N7,
            ExecutionVariant::N0,
            ExecutionVariant::N4,
        ],
    ]
    .into_iter()
    .enumerate()
    .flat_map(|(trial_index, variants)| {
        variants
            .into_iter()
            .enumerate()
            .map(move |(within_trial, variant)| ScheduleEntry {
                trial_index: u32::try_from(trial_index).expect("trial index"),
                schedule_position: u32::try_from(trial_index * 3 + within_trial)
                    .expect("schedule position"),
                variant,
            })
    })
    .collect()
}

fn schedule_position(trial_index: u32, variant: ExecutionVariant) -> u32 {
    schedule()
        .into_iter()
        .find(|entry| entry.trial_index == trial_index && entry.variant == variant)
        .expect("scheduled variant")
        .schedule_position
}

fn profile(task: &EvaluationTask, variant: ExecutionVariant) -> &VariantProfile {
    task.variant_profiles
        .iter()
        .find(|profile| profile.variant == variant)
        .expect("variant profile")
}

#[test]
fn complete_identity_matched_rows_can_reach_only_live_evaluation_readiness() {
    let report = evaluate_offline(&ready_request()).expect("offline comparison");
    assert_eq!(
        report.decision,
        EvaluationDecision::AoNextReadyForLiveEvaluation
    );
    assert_ne!(
        report.decision,
        EvaluationDecision::AoNextLiveEvaluationPassed
    );
    assert!(report.gates.iter().all(|gate| gate.passed));
    assert_eq!(report.rows.len(), 27);
    assert_eq!(report.summary.n7_median_total_tokens, 110);
    assert_eq!(report.summary.task_variant_medians.len(), 9);
}

#[test]
fn checked_in_placeholder_corpus_is_explicitly_synthetic_and_never_live() {
    let bytes = include_bytes!("../../../tests/fixtures/evaluation/synthetic-corpus-v1.json");
    let synthetic: serde_json::Value =
        decode_strict_json(bytes, 64 * 1024).expect("strict synthetic corpus fixture");
    assert_eq!(
        synthetic["schema_version"],
        "ao.next.synthetic-evaluation-corpus.v1"
    );
    assert!(
        serde_json::to_string(&synthetic)
            .expect("synthetic corpus JSON")
            .contains("fixture-model")
    );
    assert!(
        decode_strict_json::<CorpusManifest>(bytes, 64 * 1024).is_err(),
        "legacy synthetic fixture must not satisfy the repeated-trial live corpus contract"
    );
}

#[test]
fn incomplete_token_rows_and_reported_metric_manipulation_are_rejected() {
    let mut incomplete = ready_request();
    incomplete.runs[2].tokens.reasoning_tokens = None;
    assert!(matches!(
        evaluate_offline(&incomplete),
        Err(EvaluationError::IncompleteTokens { .. })
    ));

    let mut manipulated = ready_request();
    manipulated.runs[2].tokens.reported_total_tokens += 1;
    assert!(matches!(
        evaluate_offline(&manipulated),
        Err(EvaluationError::ReportedMetricMismatch { .. })
    ));
}

#[test]
fn corpus_identity_hidden_tests_and_all_exact_bindings_fail_closed() {
    let mut corpus_drift = ready_request();
    corpus_drift.corpus.corpus_digest = digest_bytes(b"drifted corpus");
    assert!(matches!(
        evaluate_offline(&corpus_drift),
        Err(EvaluationError::CorpusDigestMismatch { .. })
    ));

    let mut hidden_test_drift = ready_request();
    hidden_test_drift.runs[2].hidden_tests_digest = digest_bytes(b"drifted hidden tests");
    assert!(matches!(
        evaluate_offline(&hidden_test_drift),
        Err(EvaluationError::RunIdentityMismatch { .. })
    ));

    let mut source_drift = ready_request();
    source_drift.runs[2].source_digest = digest_bytes(b"drifted source");
    assert!(matches!(
        evaluate_offline(&source_drift),
        Err(EvaluationError::RunIdentityMismatch { .. })
    ));

    let mut prompt_drift = ready_request();
    prompt_drift.runs[2].prompt_digest = digest_bytes(b"drifted prompt");
    assert!(matches!(
        evaluate_offline(&prompt_drift),
        Err(EvaluationError::RunIdentityMismatch { .. })
    ));

    let mut workspace_drift = ready_request();
    workspace_drift.runs[2].workspace_seed_digest = digest_bytes(b"drifted workspace");
    assert!(matches!(
        evaluate_offline(&workspace_drift),
        Err(EvaluationError::RunIdentityMismatch { .. })
    ));

    let mut objective_drift = ready_request();
    objective_drift.runs[2].objective_digest = digest_bytes(b"drifted objective");
    assert!(matches!(
        evaluate_offline(&objective_drift),
        Err(EvaluationError::RunIdentityMismatch { .. })
    ));

    let mut visible_drift = ready_request();
    visible_drift.runs[2].visible_fixtures_digest = digest_bytes(b"drifted visible fixtures");
    assert!(matches!(
        evaluate_offline(&visible_drift),
        Err(EvaluationError::RunIdentityMismatch { .. })
    ));

    let mut verifier_drift = ready_request();
    verifier_drift.runs[2].verifier_profile_digest = digest_bytes(b"drifted verifier");
    assert!(matches!(
        evaluate_offline(&verifier_drift),
        Err(EvaluationError::RunIdentityMismatch { .. })
    ));

    let mut runtime_drift = ready_request();
    runtime_drift.runs[2].runtime_digest = digest_bytes(b"drifted runtime");
    assert!(matches!(
        evaluate_offline(&runtime_drift),
        Err(EvaluationError::RunIdentityMismatch { .. })
    ));

    let mut model_drift = ready_request();
    model_drift.runs[2].model_digest = digest_bytes(b"drifted model");
    assert!(matches!(
        evaluate_offline(&model_drift),
        Err(EvaluationError::RunIdentityMismatch { .. })
    ));

    let mut policy_drift = ready_request();
    policy_drift.runs[2].policy_digest = digest_bytes(b"drifted policy");
    assert!(matches!(
        evaluate_offline(&policy_drift),
        Err(EvaluationError::RunIdentityMismatch { .. })
    ));

    let mut adapter_drift = ready_request();
    adapter_drift.runs[2].adapter_digest = digest_bytes(b"drifted adapter");
    assert!(matches!(
        evaluate_offline(&adapter_drift),
        Err(EvaluationError::RunIdentityMismatch { .. })
    ));

    let mut task_drift = ready_request();
    task_drift.runs[2].task_id = "unknown-task".into();
    assert!(matches!(
        evaluate_offline(&task_drift),
        Err(EvaluationError::RunIdentityMismatch { .. })
    ));
}

#[test]
fn every_task_requires_three_unique_scheduled_trials_per_variant() {
    let mut missing = ready_request();
    missing.runs.remove(1);
    assert!(matches!(
        evaluate_offline(&missing),
        Err(EvaluationError::MissingTrial { .. })
    ));

    let mut duplicate = ready_request();
    duplicate.runs.push(duplicate.runs[0].clone());
    assert!(matches!(
        evaluate_offline(&duplicate),
        Err(EvaluationError::DuplicateTrial { .. })
    ));
}

#[test]
fn a_missed_promotion_gate_is_not_yet_superior_and_never_expands_scope() {
    let mut request = ready_request();
    for run in &mut request.runs {
        if run.variant == ExecutionVariant::N7 {
            run.tokens.input_tokens = Some(121);
            run.tokens.reported_total_tokens = 121;
        }
    }
    let report = evaluate_offline(&request).expect("valid but non-superior comparison");
    assert_eq!(report.decision, EvaluationDecision::AoNextNotYetSuperior);
    assert!(!report.dynamic_fanout_authorized);
    assert!(!report.promotion_authorized);
    assert!(report.gates.iter().any(|gate| !gate.passed));
}

#[test]
fn wrong_schedule_reused_capture_and_workspace_reuse_fail_closed() {
    let mut wrong_schedule = ready_request();
    wrong_schedule.runs[0].schedule_position = 8;
    assert!(matches!(
        evaluate_offline(&wrong_schedule),
        Err(EvaluationError::TrialIdentityMismatch { .. })
    ));

    let mut reused_capture = ready_request();
    reused_capture.runs[1].raw_capture_digest = reused_capture.runs[0].raw_capture_digest.clone();
    assert!(matches!(
        evaluate_offline(&reused_capture),
        Err(EvaluationError::ReusedProvenance)
    ));

    let mut reused_workspace = ready_request();
    reused_workspace.runs[1].workspace_instance_id =
        reused_workspace.runs[0].workspace_instance_id.clone();
    assert!(matches!(
        evaluate_offline(&reused_workspace),
        Err(EvaluationError::ReusedProvenance)
    ));
}

#[test]
fn raw_capture_integrity_and_runtime_safety_boundaries_fail_closed() {
    let mut capture_drift = ready_request();
    capture_drift.runs[0].raw_capture_digest = digest_bytes(b"drifted capture manifest");
    assert!(matches!(
        evaluate_offline(&capture_drift),
        Err(EvaluationError::InvalidMetrics { .. })
    ));

    let mut unauthorized = ready_request();
    unauthorized.runs[0].unauthorized_effects = 1;
    assert!(matches!(
        evaluate_offline(&unauthorized),
        Err(EvaluationError::InvalidMetrics { .. })
    ));

    let mut workers = ready_request();
    let n7 = workers
        .runs
        .iter_mut()
        .find(|run| run.variant == ExecutionVariant::N7)
        .expect("N7 row");
    n7.worker_count = 2;
    assert!(matches!(
        evaluate_offline(&workers),
        Err(EvaluationError::InvalidMetrics { .. })
    ));

    let mut fanout = ready_request();
    let n7 = fanout
        .runs
        .iter_mut()
        .find(|run| run.variant == ExecutionVariant::N7)
        .expect("N7 row");
    n7.dynamic_fanout = true;
    assert!(matches!(
        evaluate_offline(&fanout),
        Err(EvaluationError::InvalidMetrics { .. })
    ));

    let mut timing = ready_request();
    timing.runs[0].model_wait_ms = timing.runs[0].wall_clock_ms + 1;
    assert!(matches!(
        evaluate_offline(&timing),
        Err(EvaluationError::InvalidMetrics { .. })
    ));

    let mut hidden_exposure = ready_request();
    hidden_exposure.runs[0].hidden_test_exposure = true;
    assert!(matches!(
        evaluate_offline(&hidden_exposure),
        Err(EvaluationError::InvalidMetrics { .. })
    ));
}

#[test]
fn cross_task_summary_uses_per_task_variant_medians_before_aggregation() {
    let mut request = ready_request();
    let values = [
        ("greenfield", [1, 2, 100]),
        ("defect", [3, 4, 5]),
        ("reconciliation", [6, 7, 8]),
    ];
    for (task_id, tokens) in values {
        for run in request
            .runs
            .iter_mut()
            .filter(|run| run.task_id == task_id && run.variant == ExecutionVariant::N7)
        {
            let value = tokens[usize::try_from(run.trial_index).expect("trial index")];
            run.tokens.input_tokens = Some(value);
            run.tokens.reported_total_tokens = value;
        }
    }

    let report = evaluate_offline(&request).expect("repeated comparison");

    assert_eq!(report.summary.n7_median_total_tokens, 4);
}

#[test]
fn synthetic_or_unauthorized_rows_cannot_emit_live_evaluation_passed() {
    let request = ready_request();
    let offline = evaluate_offline(&request).expect("offline comparison");
    assert_ne!(
        offline.decision,
        EvaluationDecision::AoNextLiveEvaluationPassed
    );
    assert!(matches!(
        evaluate_live_authorized(&request),
        Err(EvaluationError::LiveAuthorityMissing | EvaluationError::LiveCorpusRequired)
    ));
}
