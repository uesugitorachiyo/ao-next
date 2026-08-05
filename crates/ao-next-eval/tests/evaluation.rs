use ao_next_core::contracts::Digest;
use ao_next_core::strict_json::canonical_digest;
use ao_next_core::strict_json::decode_strict_json;
use ao_next_eval::comparison::{
    ComparisonRequest, EvaluationDecision, EvaluationError, evaluate_offline,
};
use ao_next_eval::corpus::{CorpusManifest, EvaluationTask, VariantProfile};
use ao_next_eval::metrics::{ExecutionVariant, RunMeasurement, TokenRow};

const ZERO_DIGEST: &str = "sha256:0000000000000000000000000000000000000000000000000000000000000000";
const ONE_DIGEST: &str = "sha256:1111111111111111111111111111111111111111111111111111111111111111";

fn digest(value: &str) -> Digest {
    Digest::new(value).expect("fixture digest")
}

fn task(id: &str) -> EvaluationTask {
    EvaluationTask {
        task_id: id.into(),
        task_kind: match id {
            "greenfield" => "greenfield_engineering_application",
            "defect" => "bounded_public_defect_repair",
            _ => "ao_compatible_reconciliation",
        }
        .into(),
        source_digest: digest(ZERO_DIGEST),
        objective_digest: digest(ONE_DIGEST),
        workspace_seed_digest: digest(ZERO_DIGEST),
        visible_fixtures_digest: digest(ONE_DIGEST),
        hidden_tests_digest: digest(ZERO_DIGEST),
        verifier_profile_digest: digest(ONE_DIGEST),
        variant_profiles: [
            (ExecutionVariant::N0, "current-ao"),
            (ExecutionVariant::N4, "direct-model"),
            (ExecutionVariant::N7, "ao-next"),
        ]
        .into_iter()
        .map(|(variant, runtime)| VariantProfile {
            variant,
            runtime: runtime.into(),
            model_identifier: "fixture-model".into(),
            prompt_digest: digest(ONE_DIGEST),
            policy_digest: digest(ZERO_DIGEST),
            adapter_version: "fixture-adapter-v1".into(),
        })
        .collect(),
    }
}

fn corpus() -> CorpusManifest {
    let tasks = vec![task("greenfield"), task("defect"), task("reconciliation")];
    CorpusManifest {
        schema_version: "ao.next.evaluation-corpus.v1".into(),
        corpus_digest: canonical_digest(&tasks).expect("corpus digest"),
        tasks,
    }
}

fn measurement(
    corpus: &CorpusManifest,
    task: &EvaluationTask,
    variant: ExecutionVariant,
) -> RunMeasurement {
    let (total_tokens, wall_clock_ms) = match variant {
        ExecutionVariant::N0 => (400, 400),
        ExecutionVariant::N4 => (100, 200),
        ExecutionVariant::N7 => (110, 250),
    };
    RunMeasurement {
        schema_version: "ao.next.run-measurement.v1".into(),
        corpus_digest: corpus.corpus_digest.clone(),
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
        model_identifier: "fixture-model".into(),
        prompt_digest: digest(ONE_DIGEST),
        policy_digest: digest(ZERO_DIGEST),
        adapter_version: "fixture-adapter-v1".into(),
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
    }
}

fn ready_request() -> ComparisonRequest {
    let corpus = corpus();
    let runs = corpus
        .tasks
        .iter()
        .flat_map(|task| {
            [
                measurement(&corpus, task, ExecutionVariant::N0),
                measurement(&corpus, task, ExecutionVariant::N4),
                measurement(&corpus, task, ExecutionVariant::N7),
            ]
        })
        .collect();
    ComparisonRequest {
        schema_version: "ao.next.comparison-request.v1".into(),
        corpus,
        runs,
    }
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
    assert_eq!(report.rows.len(), 9);
    assert_eq!(report.summary.n7_median_total_tokens, 110);
}

#[test]
fn checked_in_three_task_corpus_is_sealed_by_its_exact_ordered_digest() {
    let sealed: CorpusManifest = decode_strict_json(
        include_bytes!("../../../tests/fixtures/evaluation/corpus-v1.json"),
        64 * 1024,
    )
    .expect("strict corpus fixture");
    sealed.validate().expect("sealed corpus");
    assert_eq!(sealed, corpus());
    assert_eq!(sealed.tasks.len(), 3);
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
    corpus_drift.corpus.corpus_digest = digest(ZERO_DIGEST);
    assert!(matches!(
        evaluate_offline(&corpus_drift),
        Err(EvaluationError::CorpusDigestMismatch { .. })
    ));

    let mut hidden_test_drift = ready_request();
    hidden_test_drift.runs[2].hidden_tests_digest = digest(ONE_DIGEST);
    assert!(matches!(
        evaluate_offline(&hidden_test_drift),
        Err(EvaluationError::RunIdentityMismatch { .. })
    ));

    let mut source_drift = ready_request();
    source_drift.runs[2].source_digest = digest(ONE_DIGEST);
    assert!(matches!(
        evaluate_offline(&source_drift),
        Err(EvaluationError::RunIdentityMismatch { .. })
    ));

    let mut prompt_drift = ready_request();
    prompt_drift.runs[2].prompt_digest = digest(ZERO_DIGEST);
    assert!(matches!(
        evaluate_offline(&prompt_drift),
        Err(EvaluationError::RunIdentityMismatch { .. })
    ));
}

#[test]
fn every_task_requires_n0_n4_and_n7_exactly_once() {
    let mut missing = ready_request();
    missing.runs.remove(1);
    assert!(matches!(
        evaluate_offline(&missing),
        Err(EvaluationError::MissingVariant { .. })
    ));

    let mut duplicate = ready_request();
    duplicate.runs.push(duplicate.runs[0].clone());
    assert!(matches!(
        evaluate_offline(&duplicate),
        Err(EvaluationError::DuplicateVariant { .. })
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
