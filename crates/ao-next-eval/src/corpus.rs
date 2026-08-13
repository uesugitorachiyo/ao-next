use std::collections::BTreeSet;

use ao_next_core::contracts::Digest;
use ao_next_core::strict_json::{StrictJsonError, canonical_digest};
use serde::{Deserialize, Serialize};
use thiserror::Error;

use crate::metrics::ExecutionVariant;

pub const FUNCTIONAL_SENTINEL_TASK_ID: &str = "greenfield-native-write-sentinel";

const LIVE_TASKS: [&str; 3] = [
    "artifact-reconciliation",
    "bounded-defect-repair",
    "greenfield-engineering-app",
];
const FUNCTIONAL_SENTINEL_TASKS: [&str; 3] = [
    "artifact-reconciliation",
    "bounded-defect-repair",
    FUNCTIONAL_SENTINEL_TASK_ID,
];

#[derive(Clone, Copy, Debug, Deserialize, Eq, PartialEq, Serialize)]
#[serde(rename_all = "snake_case")]
pub enum CorpusKind {
    SyntheticUnitTest,
    SealedLive,
}

#[derive(Clone, Copy, Debug, Deserialize, Eq, PartialEq, Serialize)]
#[serde(deny_unknown_fields)]
pub struct ScheduleEntry {
    pub trial_index: u32,
    pub schedule_position: u32,
    pub variant: ExecutionVariant,
}

#[derive(Clone, Debug, Deserialize, Eq, PartialEq, Serialize)]
#[serde(deny_unknown_fields)]
pub struct VariantProfile {
    pub variant: ExecutionVariant,
    pub runtime: String,
    pub runtime_digest: Digest,
    pub model_identifier: String,
    pub model_digest: Digest,
    pub prompt_digest: Digest,
    pub policy_digest: Digest,
    pub adapter_version: String,
    pub adapter_digest: Digest,
}

#[derive(Clone, Debug, Deserialize, Eq, PartialEq, Serialize)]
#[serde(deny_unknown_fields)]
pub struct EvaluationTask {
    pub task_id: String,
    pub task_kind: String,
    pub source_digest: Digest,
    pub objective_digest: Digest,
    pub workspace_seed_digest: Digest,
    pub visible_fixtures_digest: Digest,
    pub hidden_tests_digest: Digest,
    pub verifier_profile_digest: Digest,
    pub variant_profiles: Vec<VariantProfile>,
}

#[derive(Clone, Debug, Deserialize, Eq, PartialEq, Serialize)]
#[serde(deny_unknown_fields)]
pub struct CorpusManifest {
    pub schema_version: String,
    pub corpus_kind: CorpusKind,
    pub corpus_digest: Digest,
    pub required_trial_count: u32,
    pub schedule: Vec<ScheduleEntry>,
    pub tasks: Vec<EvaluationTask>,
}

#[derive(Debug, Error)]
pub enum CorpusError {
    #[error("evaluation corpus schema is unsupported")]
    UnsupportedSchema,
    #[error("evaluation corpus is empty")]
    Empty,
    #[error("evaluation corpus has duplicate or empty task identity: {0}")]
    DuplicateTask(String),
    #[error("evaluation corpus task has an empty kind: {0}")]
    EmptyTaskKind(String),
    #[error("evaluation corpus task has incomplete or duplicate variant profiles: {0}")]
    InvalidVariantProfiles(String),
    #[error("evaluation corpus trial schedule is invalid")]
    InvalidSchedule,
    #[error("live evaluation corpus contains a placeholder identity: {0}")]
    PlaceholderIdentity(String),
    #[error("evaluation corpus digest mismatch: expected {expected}, observed {observed}")]
    DigestMismatch { expected: Digest, observed: Digest },
    #[error("strict JSON failure: {0}")]
    StrictJson(#[from] StrictJsonError),
}

impl CorpusManifest {
    /// Calculates the digest over the corpus classification, trial schedule,
    /// and ordered task list.
    ///
    /// # Errors
    ///
    /// Returns a strict JSON error when canonical serialization fails.
    pub fn calculated_digest(&self) -> Result<Digest, CorpusError> {
        Ok(canonical_digest(&(
            self.corpus_kind,
            self.required_trial_count,
            &self.schedule,
            &self.tasks,
        ))?)
    }

    /// Verifies the schema, task identities, and exact digest of the ordered
    /// sealed task list.
    ///
    /// # Errors
    ///
    /// Returns [`CorpusError`] for schema drift, empty/duplicate tasks, or a
    /// changed corpus digest.
    pub fn validate(&self) -> Result<(), CorpusError> {
        if self.schema_version != "ao.next.evaluation-corpus.v2" {
            return Err(CorpusError::UnsupportedSchema);
        }
        if self.tasks.is_empty() {
            return Err(CorpusError::Empty);
        }
        if self.required_trial_count != 3 || self.schedule != counterbalanced_schedule() {
            return Err(CorpusError::InvalidSchedule);
        }
        let mut ids = BTreeSet::new();
        for task in &self.tasks {
            if task.task_id.trim().is_empty() || !ids.insert(task.task_id.clone()) {
                return Err(CorpusError::DuplicateTask(task.task_id.clone()));
            }
            if task.task_kind.trim().is_empty() {
                return Err(CorpusError::EmptyTaskKind(task.task_id.clone()));
            }
            let profiles = task
                .variant_profiles
                .iter()
                .map(|profile| profile.variant)
                .collect::<BTreeSet<_>>();
            if profiles
                != BTreeSet::from([
                    ExecutionVariant::N0,
                    ExecutionVariant::N4,
                    ExecutionVariant::N7,
                ])
                || task.variant_profiles.len() != 3
                || task.variant_profiles.iter().any(|profile| {
                    profile.runtime.trim().is_empty()
                        || profile.model_identifier.trim().is_empty()
                        || profile.adapter_version.trim().is_empty()
                })
            {
                return Err(CorpusError::InvalidVariantProfiles(task.task_id.clone()));
            }
        }
        let observed = self.calculated_digest()?;
        if observed != self.corpus_digest {
            return Err(CorpusError::DigestMismatch {
                expected: self.corpus_digest.clone(),
                observed,
            });
        }
        Ok(())
    }

    /// Applies the stricter corpus rules required before a live-passed
    /// evaluation is even considered.
    ///
    /// # Errors
    ///
    /// Returns a corpus error for a non-live corpus, placeholder digests or
    /// fixture identities, or a task set other than the three sealed tasks.
    pub fn validate_live(&self) -> Result<(), CorpusError> {
        self.validate_live_task_set(&LIVE_TASKS)
    }

    /// Applies live-input rules to the one-row functional N7 sentinel corpus
    /// without admitting it as an evaluation corpus.
    ///
    /// # Errors
    ///
    /// Returns a corpus error unless the corpus replaces only the greenfield
    /// campaign task with the exact functional sentinel identity.
    pub fn validate_functional_sentinel(&self) -> Result<(), CorpusError> {
        self.validate_live_task_set(&FUNCTIONAL_SENTINEL_TASKS)?;
        let sentinel = self
            .tasks
            .iter()
            .find(|task| task.task_id == FUNCTIONAL_SENTINEL_TASK_ID)
            .ok_or_else(|| CorpusError::PlaceholderIdentity("corpus classification".into()))?;
        if sentinel.task_kind != "functional_native_write_sentinel" {
            return Err(CorpusError::PlaceholderIdentity(
                FUNCTIONAL_SENTINEL_TASK_ID.into(),
            ));
        }
        Ok(())
    }

    fn validate_live_task_set(&self, expected_tasks: &[&str]) -> Result<(), CorpusError> {
        self.validate()?;
        let expected_tasks = expected_tasks.iter().copied().collect::<BTreeSet<_>>();
        let observed_tasks = self
            .tasks
            .iter()
            .map(|task| task.task_id.as_str())
            .collect::<BTreeSet<_>>();
        if self.corpus_kind != CorpusKind::SealedLive || observed_tasks != expected_tasks {
            return Err(CorpusError::PlaceholderIdentity(
                "corpus classification".into(),
            ));
        }
        for task in &self.tasks {
            let task_digests = [
                &task.source_digest,
                &task.objective_digest,
                &task.workspace_seed_digest,
                &task.visible_fixtures_digest,
                &task.hidden_tests_digest,
                &task.verifier_profile_digest,
            ];
            if task_digests.into_iter().any(is_placeholder_digest) {
                return Err(CorpusError::PlaceholderIdentity(task.task_id.clone()));
            }
            for profile in &task.variant_profiles {
                if profile.runtime.contains("fixture")
                    || profile.model_identifier.contains("fixture")
                    || profile.adapter_version.contains("fixture")
                    || [
                        &profile.runtime_digest,
                        &profile.model_digest,
                        &profile.prompt_digest,
                        &profile.policy_digest,
                        &profile.adapter_digest,
                    ]
                    .into_iter()
                    .any(is_placeholder_digest)
                {
                    return Err(CorpusError::PlaceholderIdentity(format!(
                        "{}:{:?}",
                        task.task_id, profile.variant
                    )));
                }
            }
        }
        Ok(())
    }
}

#[must_use]
pub fn counterbalanced_schedule() -> Vec<ScheduleEntry> {
    vec![
        schedule_entry(0, 0, ExecutionVariant::N0),
        schedule_entry(0, 1, ExecutionVariant::N4),
        schedule_entry(0, 2, ExecutionVariant::N7),
        schedule_entry(1, 3, ExecutionVariant::N4),
        schedule_entry(1, 4, ExecutionVariant::N7),
        schedule_entry(1, 5, ExecutionVariant::N0),
        schedule_entry(2, 6, ExecutionVariant::N7),
        schedule_entry(2, 7, ExecutionVariant::N0),
        schedule_entry(2, 8, ExecutionVariant::N4),
    ]
}

const fn schedule_entry(
    trial_index: u32,
    schedule_position: u32,
    variant: ExecutionVariant,
) -> ScheduleEntry {
    ScheduleEntry {
        trial_index,
        schedule_position,
        variant,
    }
}

fn is_placeholder_digest(digest: &Digest) -> bool {
    let bytes = &digest.as_str().as_bytes()[7..];
    bytes
        .first()
        .is_some_and(|first| bytes.iter().all(|byte| byte == first))
}
