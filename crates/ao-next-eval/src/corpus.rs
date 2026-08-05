use std::collections::BTreeSet;

use ao_next_core::contracts::Digest;
use ao_next_core::strict_json::{StrictJsonError, canonical_digest};
use serde::{Deserialize, Serialize};
use thiserror::Error;

use crate::metrics::ExecutionVariant;

#[derive(Clone, Debug, Deserialize, Eq, PartialEq, Serialize)]
#[serde(deny_unknown_fields)]
pub struct VariantProfile {
    pub variant: ExecutionVariant,
    pub runtime: String,
    pub model_identifier: String,
    pub prompt_digest: Digest,
    pub policy_digest: Digest,
    pub adapter_version: String,
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
    pub corpus_digest: Digest,
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
    #[error("evaluation corpus digest mismatch: expected {expected}, observed {observed}")]
    DigestMismatch { expected: Digest, observed: Digest },
    #[error("strict JSON failure: {0}")]
    StrictJson(#[from] StrictJsonError),
}

impl CorpusManifest {
    /// Verifies the schema, task identities, and exact digest of the ordered
    /// sealed task list.
    ///
    /// # Errors
    ///
    /// Returns [`CorpusError`] for schema drift, empty/duplicate tasks, or a
    /// changed corpus digest.
    pub fn validate(&self) -> Result<(), CorpusError> {
        if self.schema_version != "ao.next.evaluation-corpus.v1" {
            return Err(CorpusError::UnsupportedSchema);
        }
        if self.tasks.is_empty() {
            return Err(CorpusError::Empty);
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
        let observed = canonical_digest(&self.tasks)?;
        if observed != self.corpus_digest {
            return Err(CorpusError::DigestMismatch {
                expected: self.corpus_digest.clone(),
                observed,
            });
        }
        Ok(())
    }
}
