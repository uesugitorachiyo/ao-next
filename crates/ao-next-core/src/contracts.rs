use std::collections::{BTreeMap, BTreeSet};
use std::fmt;
use std::path::PathBuf;

use chrono::{DateTime, Utc};
use schemars::{JsonSchema, schema_for};
use serde::de::Error as _;
use serde::{Deserialize, Deserializer, Serialize};
use serde_json::Value;
use thiserror::Error;

#[derive(Clone, Debug, PartialEq, Eq, PartialOrd, Ord, Hash, Serialize, JsonSchema)]
#[serde(transparent)]
#[schemars(transparent)]
pub struct Digest(String);

impl Digest {
    /// Creates a validated lowercase SHA-256 digest.
    ///
    /// # Errors
    ///
    /// Returns [`DigestError`] when the value is not `sha256:` followed by 64
    /// lowercase hexadecimal characters.
    pub fn new(value: impl Into<String>) -> Result<Self, DigestError> {
        let value = value.into();
        if is_sha256_digest(&value) {
            Ok(Self(value))
        } else {
            Err(DigestError(value))
        }
    }

    #[must_use]
    pub fn as_str(&self) -> &str {
        &self.0
    }

    pub(crate) fn from_sha256_hex(hex: &str) -> Self {
        debug_assert!(hex.len() == 64 && hex.bytes().all(|byte| byte.is_ascii_hexdigit()));
        Self(format!("sha256:{hex}"))
    }
}

impl fmt::Display for Digest {
    fn fmt(&self, formatter: &mut fmt::Formatter<'_>) -> fmt::Result {
        self.0.fmt(formatter)
    }
}

impl<'de> Deserialize<'de> for Digest {
    fn deserialize<D>(deserializer: D) -> Result<Self, D::Error>
    where
        D: Deserializer<'de>,
    {
        let value = String::deserialize(deserializer)?;
        Self::new(value).map_err(D::Error::custom)
    }
}

fn is_sha256_digest(value: &str) -> bool {
    value.len() == 71
        && value.starts_with("sha256:")
        && value[7..]
            .bytes()
            .all(|byte| byte.is_ascii_digit() || (b'a'..=b'f').contains(&byte))
}

#[derive(Clone, Debug, Error, PartialEq, Eq)]
#[error("invalid digest `{0}`; expected sha256 followed by 64 lowercase hex characters")]
pub struct DigestError(String);

#[derive(Clone, Debug, Deserialize, Eq, JsonSchema, PartialEq, Serialize)]
#[serde(deny_unknown_fields)]
pub struct SourceIdentity {
    pub repository: String,
    pub head: Digest,
}

#[derive(Clone, Debug, Deserialize, Eq, JsonSchema, PartialEq, Serialize)]
#[serde(deny_unknown_fields)]
pub struct WorkspaceIdentity {
    pub workspace_id: String,
    pub root: PathBuf,
    pub seed_digest: Digest,
}

#[derive(Clone, Debug, Deserialize, Eq, JsonSchema, PartialEq, Serialize)]
#[serde(deny_unknown_fields)]
pub struct ModelProfile {
    pub runtime: String,
    pub model_identifier: String,
    pub reasoning_effort: String,
    pub system_prompt_digest: Digest,
    pub tool_contract_digest: Digest,
    pub context_limit: u64,
    pub output_limit: u64,
    pub adapter_version: String,
}

#[derive(Clone, Debug, Deserialize, Eq, JsonSchema, PartialEq, Serialize)]
#[serde(deny_unknown_fields)]
pub struct AdapterIdentity {
    pub runtime: String,
    pub model_identifier: String,
    pub adapter_version: String,
    pub worker_id: String,
}

#[derive(Clone, Debug, Deserialize, Eq, JsonSchema, Ord, PartialEq, PartialOrd, Serialize)]
#[serde(rename_all = "snake_case")]
pub enum Capability {
    ReadWorkspace,
    WriteWorkspace,
    RunLocalProgram,
    NetworkAccess,
    CredentialAccess,
    RemoteMutation,
    Release,
    Deployment,
    Publication,
}

#[derive(Clone, Debug, Deserialize, Eq, JsonSchema, PartialEq, Serialize)]
#[serde(rename_all = "snake_case")]
pub enum NetworkPolicy {
    Denied,
    LoopbackOnly,
    Allowlisted,
}

#[derive(Clone, Debug, Deserialize, Eq, JsonSchema, PartialEq, Serialize)]
#[serde(rename_all = "snake_case")]
pub enum ExternalEffectPolicy {
    Denied,
    AuthorizedCapabilitiesOnly,
}

#[derive(Clone, Debug, Deserialize, Eq, JsonSchema, PartialEq, Serialize)]
#[serde(deny_unknown_fields)]
pub struct AuthorityEnvelope {
    pub schema_version: String,
    pub issued_by: String,
    pub issued_at: DateTime<Utc>,
    pub expires_at: DateTime<Utc>,
    pub capabilities: BTreeSet<Capability>,
    pub allowed_roots: Vec<PathBuf>,
    pub allowed_programs: BTreeSet<String>,
    pub network: NetworkPolicy,
    pub allowed_network_hosts: BTreeSet<String>,
    pub external_effects: ExternalEffectPolicy,
}

#[derive(Clone, Debug, Deserialize, Eq, JsonSchema, PartialEq, Serialize)]
#[serde(deny_unknown_fields)]
pub struct StructuredCommand {
    pub program: String,
    pub args: Vec<String>,
    pub timeout_ms: u64,
}

#[derive(Clone, Debug, Deserialize, Eq, JsonSchema, PartialEq, Serialize)]
#[serde(deny_unknown_fields)]
pub struct VerifierProfile {
    pub profile_id: String,
    pub profile_digest: Digest,
    pub commands: Vec<StructuredCommand>,
    pub required_artifacts: Vec<PathBuf>,
}

#[derive(Clone, Debug, Deserialize, Eq, JsonSchema, PartialEq, Serialize)]
#[serde(deny_unknown_fields)]
pub struct RunLimits {
    pub max_input_bytes: u64,
    pub max_turns: u32,
    pub max_repair_attempts: u32,
    pub max_run_ms: u64,
    pub max_effect_timeout_ms: u64,
    pub max_output_bytes: u64,
    pub max_tokens: u64,
}

#[derive(Clone, Debug, Deserialize, Eq, JsonSchema, PartialEq, Serialize)]
#[serde(deny_unknown_fields)]
pub struct RunRequest {
    pub schema_version: String,
    pub run_id: String,
    pub objective: String,
    pub source: SourceIdentity,
    pub workspace: WorkspaceIdentity,
    pub model_profile: ModelProfile,
    pub authority: AuthorityEnvelope,
    pub verifier_profile: VerifierProfile,
    pub policy_digest: Digest,
    pub limits: RunLimits,
}

#[derive(Clone, Debug, Deserialize, Eq, JsonSchema, PartialEq, Serialize)]
#[serde(rename_all = "snake_case")]
pub enum EffectKind {
    ReadFile,
    WriteFile,
    #[schemars(skip)]
    RunProgram,
    Network,
    Credential,
    RemoteMutation,
    Release,
    Deployment,
    Publication,
}

#[derive(Clone, Debug, Deserialize, Eq, JsonSchema, PartialEq, Serialize)]
#[serde(deny_unknown_fields)]
pub struct EffectRequest {
    pub effect_id: String,
    pub run_id: String,
    pub kind: EffectKind,
    pub program: Option<String>,
    pub content: Option<String>,
    pub args: Vec<String>,
    pub paths: Vec<PathBuf>,
    pub timeout_ms: u64,
    pub input_digest: Digest,
}

#[derive(Clone, Debug, Deserialize, Eq, JsonSchema, PartialEq, Serialize)]
#[serde(rename_all = "snake_case")]
pub enum EffectDecision {
    Admitted,
    Denied,
}

#[derive(Clone, Debug, Deserialize, Eq, JsonSchema, PartialEq, Serialize)]
#[serde(deny_unknown_fields)]
pub struct EffectEvent {
    pub schema_version: String,
    pub request: EffectRequest,
    pub decision: EffectDecision,
    pub policy_digest: Digest,
    pub recorded_at: DateTime<Utc>,
    pub output_digest: Option<Digest>,
}

#[derive(Clone, Debug, Deserialize, Eq, JsonSchema, PartialEq, Serialize)]
#[serde(deny_unknown_fields)]
pub struct VerifierResult {
    pub verifier_id: String,
    pub passed: bool,
    pub exit_status: Option<i32>,
    pub output_digest: Digest,
    pub message: String,
}

#[derive(Clone, Debug, Deserialize, Eq, JsonSchema, PartialEq, Serialize)]
#[serde(deny_unknown_fields)]
pub struct VerifierReport {
    pub schema_version: String,
    pub run_id: String,
    pub verifier_profile_digest: Digest,
    pub started_at: DateTime<Utc>,
    pub completed_at: DateTime<Utc>,
    pub passed: bool,
    pub results: Vec<VerifierResult>,
}

#[derive(Clone, Debug, Deserialize, Eq, JsonSchema, PartialEq, Serialize)]
#[serde(deny_unknown_fields)]
pub struct ArtifactEntry {
    pub artifact_id: String,
    pub media_type: String,
    pub digest: Digest,
    pub content_ref: String,
    pub original_ref: String,
    pub size_bytes: u64,
    pub producer: String,
    pub input_digests: Vec<Digest>,
}

#[derive(Clone, Debug, Deserialize, Eq, JsonSchema, PartialEq, Serialize)]
#[serde(deny_unknown_fields)]
pub struct ArtifactManifest {
    pub schema_version: String,
    pub run_id: String,
    pub source: SourceIdentity,
    pub entries: Vec<ArtifactEntry>,
}

#[derive(Clone, Debug, Deserialize, Eq, JsonSchema, PartialEq, Serialize)]
#[serde(rename_all = "snake_case")]
pub enum RunState {
    Received,
    Validated,
    Running,
    Verifying,
    Passed,
    Failed,
    Denied,
    Interrupted,
}

#[derive(Clone, Debug, Deserialize, Eq, JsonSchema, PartialEq, Serialize)]
#[serde(deny_unknown_fields)]
pub struct TerminalReadback {
    pub schema_version: String,
    pub run_id: String,
    pub source: SourceIdentity,
    pub workspace: WorkspaceIdentity,
    pub adapter: AdapterIdentity,
    pub request_digest: Digest,
    pub policy_digest: Digest,
    pub verifier_report_digest: Digest,
    pub artifact_manifest_digest: Digest,
    pub terminal_state: RunState,
    pub completed_at: DateTime<Utc>,
    pub safety_boundaries: BTreeMap<String, bool>,
    pub exact_next_action: String,
}

#[derive(Clone, Debug, Eq, PartialEq)]
pub struct IntakeExpectation {
    pub run_id: String,
    pub source: SourceIdentity,
    pub workspace: WorkspaceIdentity,
    pub now: DateTime<Utc>,
}

#[derive(Clone, Debug, Error, Eq, PartialEq)]
pub enum IntakeError {
    #[error("run identity mismatch")]
    RunIdentityMismatch,
    #[error("source identity mismatch")]
    SourceIdentityMismatch,
    #[error("workspace identity mismatch")]
    WorkspaceIdentityMismatch,
    #[error("authority is not current")]
    AuthorityNotCurrent,
    #[error("authority envelope schema mismatch")]
    AuthoritySchemaMismatch,
    #[error("run request schema mismatch")]
    RequestSchemaMismatch,
    #[error("workspace root is not authority-bound")]
    WorkspaceRootNotAllowed,
}

/// Validates request freshness and exact run, source, workspace, and root bindings.
///
/// # Errors
///
/// Returns [`IntakeError`] when a schema, identity, freshness, or authority-root
/// binding differs from the operator-authored expectation.
pub fn validate_intake(
    request: &RunRequest,
    expectation: &IntakeExpectation,
) -> Result<(), IntakeError> {
    if request.schema_version != "ao.next.run-request.v1" {
        return Err(IntakeError::RequestSchemaMismatch);
    }
    if request.authority.schema_version != "ao.next.authority-envelope.v1" {
        return Err(IntakeError::AuthoritySchemaMismatch);
    }
    if request.run_id != expectation.run_id {
        return Err(IntakeError::RunIdentityMismatch);
    }
    if request.source != expectation.source {
        return Err(IntakeError::SourceIdentityMismatch);
    }
    if request.workspace != expectation.workspace {
        return Err(IntakeError::WorkspaceIdentityMismatch);
    }
    if request.authority.issued_at > expectation.now
        || request.authority.expires_at <= expectation.now
        || request.authority.issued_at >= request.authority.expires_at
    {
        return Err(IntakeError::AuthorityNotCurrent);
    }
    if !request
        .authority
        .allowed_roots
        .contains(&request.workspace.root)
    {
        return Err(IntakeError::WorkspaceRootNotAllowed);
    }
    Ok(())
}

#[must_use]
/// Generates the checked-in public JSON Schemas from their Rust types.
///
/// # Panics
///
/// Panics only if a `schemars` root schema cannot be represented as JSON,
/// which would indicate a programming error in a derived schema.
pub fn generated_contract_schemas() -> BTreeMap<&'static str, Value> {
    BTreeMap::from([
        (
            "adapter-turn-v1.schema.json",
            serde_json::to_value(schema_for!(crate::adapter::AdapterTurn))
                .expect("schema serialization"),
        ),
        (
            "command-verifier-profile-v1.schema.json",
            serde_json::to_value(schema_for!(crate::verifier::CommandVerifierProfile))
                .expect("schema serialization"),
        ),
        (
            "run-request-v1.schema.json",
            serde_json::to_value(schema_for!(RunRequest)).expect("schema serialization"),
        ),
        (
            "authority-envelope-v1.schema.json",
            serde_json::to_value(schema_for!(AuthorityEnvelope)).expect("schema serialization"),
        ),
        (
            "effect-event-v1.schema.json",
            serde_json::to_value(schema_for!(EffectEvent)).expect("schema serialization"),
        ),
        (
            "verifier-report-v1.schema.json",
            serde_json::to_value(schema_for!(VerifierReport)).expect("schema serialization"),
        ),
        (
            "artifact-manifest-v1.schema.json",
            serde_json::to_value(schema_for!(ArtifactManifest)).expect("schema serialization"),
        ),
        (
            "terminal-readback-v1.schema.json",
            serde_json::to_value(schema_for!(TerminalReadback)).expect("schema serialization"),
        ),
    ])
}
