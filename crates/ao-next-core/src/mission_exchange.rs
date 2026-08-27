use std::fs::OpenOptions;
use std::io::Write as _;
use std::path::Path;

use schemars::JsonSchema;
use schemars::r#gen::SchemaGenerator;
use schemars::schema::{InstanceType, Schema, SchemaObject, SingleOrVec};
use serde::de::Error as _;
use serde::ser::SerializeTuple as _;
use serde::{Deserialize, Deserializer, Serialize};
use thiserror::Error;

use crate::contracts::{Digest, RunRequest};
use crate::evidence::digest_bytes;
use crate::recovery::{
    CheckpointIdentity, CheckpointJournal, JournalEvent, JournalEventKind, RecoveryError,
    validate_execution_prefix_lifecycle,
};
use crate::strict_json::{
    StrictJsonError, canonical_digest, canonical_json_bytes, decode_strict_json,
};

const PREFIX_SCHEMA: &str = "ao.next.execution-journal-prefix.v1";
const MAXIMUM_PREFIX_BYTES: usize = 16 * 1024 * 1024;
const MAXIMUM_PREFIX_EVENTS: usize = 4096;

#[derive(Clone, Debug, Deserialize, Eq, JsonSchema, PartialEq, Serialize)]
#[serde(deny_unknown_fields)]
#[allow(
    clippy::struct_excessive_bools,
    reason = "each denied authority boundary is an independent public contract field"
)]
pub struct ExecutionJournalPrefix {
    pub schema_version: String,
    pub run_id: String,
    pub request_digest: Digest,
    pub journal_identity: CheckpointIdentity,
    pub worker_count: u32,
    pub dynamic_fanout: bool,
    pub first_sequence: u64,
    #[serde(deserialize_with = "deserialize_required_nullable")]
    #[schemars(required, schema_with = "nullable_u64_schema")]
    pub last_sequence: Option<u64>,
    #[serde(deserialize_with = "deserialize_required_nullable")]
    #[schemars(required, schema_with = "nullable_digest_schema")]
    pub preceding_prefix_digest: Option<Digest>,
    pub events_digest: Digest,
    #[schemars(length(max = 4096))]
    pub events: Vec<JournalEvent>,
    #[serde(deserialize_with = "deserialize_required_nullable")]
    #[schemars(required, schema_with = "nullable_digest_schema")]
    pub terminal_digest: Option<Digest>,
    #[serde(deserialize_with = "deserialize_required_nullable_object")]
    #[schemars(required, schema_with = "nullable_object_schema")]
    pub terminal_record: Option<serde_json::Value>,
    pub safe_to_execute: bool,
    pub executes_work: bool,
    pub approves_work: bool,
    pub mutates_repositories: bool,
    pub grants_provider_access: bool,
    pub publishes_artifacts: bool,
    pub releases: bool,
    pub deploys: bool,
    pub advances_authority: bool,
    pub prefix_digest: Digest,
}

#[derive(Debug, Error)]
pub enum MissionExchangeError {
    #[error("execution journal prefix schema is unsupported")]
    UnsupportedSchema,
    #[error("execution journal prefix run identity mismatched")]
    RunIdentityMismatch,
    #[error("execution journal prefix request digest mismatched")]
    RequestDigestMismatch,
    #[error("execution journal prefix journal identity mismatched")]
    JournalIdentityMismatch,
    #[error("execution journal prefix worker boundary is invalid")]
    WorkerBoundary,
    #[error("execution journal prefix enables authority boundary `{0}`")]
    AuthorityEnabled(&'static str),
    #[error("execution journal prefix preceding digest is unsupported")]
    PrecedingPrefixUnsupported,
    #[error("execution journal prefix event sequence is invalid")]
    EventSequenceInvalid,
    #[error("execution journal prefix has more than {limit} events")]
    EventLimitExceeded { actual: usize, limit: usize },
    #[error("execution journal prefix event digest mismatched")]
    EventsDigestMismatch,
    #[error("execution journal prefix terminal record is contradictory")]
    TerminalContradiction,
    #[error("execution journal prefix digest mismatched")]
    PrefixDigestMismatch,
    #[error("execution journal prefix is oversized: {actual} bytes exceeds {limit}")]
    Oversized { actual: usize, limit: usize },
    #[error("execution journal prefix I/O failed: {0}")]
    Io(String),
    #[error("recovery validation failed: {0}")]
    Recovery(#[from] RecoveryError),
    #[error("strict JSON failure: {0}")]
    StrictJson(#[from] StrictJsonError),
}

impl From<std::io::Error> for MissionExchangeError {
    fn from(error: std::io::Error) -> Self {
        Self::Io(error.to_string())
    }
}

fn deserialize_required_nullable<'de, D, T>(deserializer: D) -> Result<Option<T>, D::Error>
where
    D: Deserializer<'de>,
    T: Deserialize<'de>,
{
    Option::<T>::deserialize(deserializer)
}

fn deserialize_required_nullable_object<'de, D>(
    deserializer: D,
) -> Result<Option<serde_json::Value>, D::Error>
where
    D: Deserializer<'de>,
{
    let value = Option::<serde_json::Value>::deserialize(deserializer)?;
    if value.as_ref().is_none_or(serde_json::Value::is_object) {
        Ok(value)
    } else {
        Err(D::Error::custom(
            "terminal_record must be an object or null",
        ))
    }
}

fn nullable_object_schema(_generator: &mut SchemaGenerator) -> Schema {
    SchemaObject {
        instance_type: Some(SingleOrVec::Vec(vec![
            InstanceType::Object,
            InstanceType::Null,
        ])),
        ..SchemaObject::default()
    }
    .into()
}

fn nullable_u64_schema(generator: &mut SchemaGenerator) -> Schema {
    nullable_schema::<u64>(generator)
}

fn nullable_digest_schema(generator: &mut SchemaGenerator) -> Schema {
    nullable_schema::<Digest>(generator)
}

fn nullable_schema<T: JsonSchema>(generator: &mut SchemaGenerator) -> Schema {
    let mut schema: SchemaObject = T::json_schema(generator).into();
    let mut types = match schema.instance_type.take() {
        Some(SingleOrVec::Single(instance)) => vec![*instance],
        Some(SingleOrVec::Vec(instances)) => instances,
        None => Vec::new(),
    };
    types.push(InstanceType::Null);
    schema.instance_type = Some(SingleOrVec::Vec(types));
    schema.into()
}

#[allow(
    clippy::struct_excessive_bools,
    reason = "the digest material must preserve every public contract field separately"
)]
struct PrefixDigestMaterial<'a> {
    schema_version: &'a str,
    run_id: &'a str,
    request_digest: &'a Digest,
    journal_identity: &'a CheckpointIdentity,
    worker_count: u32,
    dynamic_fanout: bool,
    first_sequence: u64,
    last_sequence: Option<u64>,
    preceding_prefix_digest: Option<&'a Digest>,
    events_digest: &'a Digest,
    events: &'a [JournalEvent],
    terminal_digest: Option<&'a Digest>,
    terminal_record: Option<&'a serde_json::Value>,
    safe_to_execute: bool,
    executes_work: bool,
    approves_work: bool,
    mutates_repositories: bool,
    grants_provider_access: bool,
    publishes_artifacts: bool,
    releases: bool,
    deploys: bool,
    advances_authority: bool,
}

impl Serialize for PrefixDigestMaterial<'_> {
    fn serialize<S>(&self, serializer: S) -> Result<S::Ok, S::Error>
    where
        S: serde::Serializer,
    {
        let mut tuple = serializer.serialize_tuple(22)?;
        tuple.serialize_element(self.schema_version)?;
        tuple.serialize_element(self.run_id)?;
        tuple.serialize_element(self.request_digest)?;
        tuple.serialize_element(self.journal_identity)?;
        tuple.serialize_element(&self.worker_count)?;
        tuple.serialize_element(&self.dynamic_fanout)?;
        tuple.serialize_element(&self.first_sequence)?;
        tuple.serialize_element(&self.last_sequence)?;
        tuple.serialize_element(&self.preceding_prefix_digest)?;
        tuple.serialize_element(self.events_digest)?;
        tuple.serialize_element(self.events)?;
        tuple.serialize_element(&self.terminal_digest)?;
        tuple.serialize_element(&self.terminal_record)?;
        tuple.serialize_element(&self.safe_to_execute)?;
        tuple.serialize_element(&self.executes_work)?;
        tuple.serialize_element(&self.approves_work)?;
        tuple.serialize_element(&self.mutates_repositories)?;
        tuple.serialize_element(&self.grants_provider_access)?;
        tuple.serialize_element(&self.publishes_artifacts)?;
        tuple.serialize_element(&self.releases)?;
        tuple.serialize_element(&self.deploys)?;
        tuple.serialize_element(&self.advances_authority)?;
        tuple.end()
    }
}

fn calculated_prefix_digest(
    prefix: &ExecutionJournalPrefix,
) -> Result<Digest, MissionExchangeError> {
    Ok(canonical_digest(&PrefixDigestMaterial {
        schema_version: &prefix.schema_version,
        run_id: &prefix.run_id,
        request_digest: &prefix.request_digest,
        journal_identity: &prefix.journal_identity,
        worker_count: prefix.worker_count,
        dynamic_fanout: prefix.dynamic_fanout,
        first_sequence: prefix.first_sequence,
        last_sequence: prefix.last_sequence,
        preceding_prefix_digest: prefix.preceding_prefix_digest.as_ref(),
        events_digest: &prefix.events_digest,
        events: &prefix.events,
        terminal_digest: prefix.terminal_digest.as_ref(),
        terminal_record: prefix.terminal_record.as_ref(),
        safe_to_execute: prefix.safe_to_execute,
        executes_work: prefix.executes_work,
        approves_work: prefix.approves_work,
        mutates_repositories: prefix.mutates_repositories,
        grants_provider_access: prefix.grants_provider_access,
        publishes_artifacts: prefix.publishes_artifacts,
        releases: prefix.releases,
        deploys: prefix.deploys,
        advances_authority: prefix.advances_authority,
    })?)
}

fn bounded_prefix_bytes(prefix: &ExecutionJournalPrefix) -> Result<Vec<u8>, MissionExchangeError> {
    let bytes = canonical_json_bytes(prefix)?;
    if bytes.len() > MAXIMUM_PREFIX_BYTES {
        return Err(MissionExchangeError::Oversized {
            actual: bytes.len(),
            limit: MAXIMUM_PREFIX_BYTES,
        });
    }
    Ok(bytes)
}

/// Builds one immutable, request-bound execution journal prefix.
///
/// # Errors
///
/// Returns [`MissionExchangeError`] for journal, request, lifecycle, terminal,
/// canonicalization, or digest contradictions.
pub fn build_execution_journal_prefix(
    journal: &CheckpointJournal,
    request: &RunRequest,
) -> Result<ExecutionJournalPrefix, MissionExchangeError> {
    let (journal_identity, events, terminal_bytes) = journal.verified_execution_prefix(request)?;
    let terminal_record = terminal_bytes
        .as_deref()
        .map(|bytes| {
            let value = decode_strict_json(bytes, MAXIMUM_PREFIX_BYTES)?;
            if canonical_json_bytes(&value)? != bytes {
                return Err(MissionExchangeError::TerminalContradiction);
            }
            Ok(value)
        })
        .transpose()?;
    let terminal_digest = events.last().and_then(|event| match &event.kind {
        JournalEventKind::TerminalPublished { record_digest } => Some(record_digest.clone()),
        _ => None,
    });
    let last_sequence = events
        .len()
        .checked_sub(1)
        .map(|value| u64::try_from(value).unwrap_or(u64::MAX));
    let mut prefix = ExecutionJournalPrefix {
        schema_version: PREFIX_SCHEMA.into(),
        run_id: request.run_id.clone(),
        request_digest: canonical_digest(request)?,
        journal_identity,
        worker_count: 1,
        dynamic_fanout: false,
        first_sequence: 0,
        last_sequence,
        preceding_prefix_digest: None,
        events_digest: canonical_digest(&events)?,
        events,
        terminal_digest,
        terminal_record,
        safe_to_execute: false,
        executes_work: false,
        approves_work: false,
        mutates_repositories: false,
        grants_provider_access: false,
        publishes_artifacts: false,
        releases: false,
        deploys: false,
        advances_authority: false,
        prefix_digest: digest_bytes(&[]),
    };
    prefix.prefix_digest = calculated_prefix_digest(&prefix)?;
    verify_execution_journal_prefix(&prefix, request)?;
    bounded_prefix_bytes(&prefix)?;
    Ok(prefix)
}

/// Verifies a detached prefix against one exact expected request.
///
/// # Errors
///
/// Returns [`MissionExchangeError`] at the first schema, identity, authority,
/// lifecycle, terminal, or digest contradiction.
pub fn verify_execution_journal_prefix(
    prefix: &ExecutionJournalPrefix,
    expected_request: &RunRequest,
) -> Result<(), MissionExchangeError> {
    if prefix.schema_version != PREFIX_SCHEMA {
        return Err(MissionExchangeError::UnsupportedSchema);
    }
    if prefix.run_id.is_empty() || prefix.run_id != expected_request.run_id {
        return Err(MissionExchangeError::RunIdentityMismatch);
    }
    if prefix.request_digest != canonical_digest(expected_request)? {
        return Err(MissionExchangeError::RequestDigestMismatch);
    }
    if prefix.journal_identity != CheckpointIdentity::from_request(expected_request)? {
        return Err(MissionExchangeError::JournalIdentityMismatch);
    }
    if prefix.worker_count != 1 || prefix.dynamic_fanout {
        return Err(MissionExchangeError::WorkerBoundary);
    }
    if prefix.events.len() > MAXIMUM_PREFIX_EVENTS {
        return Err(MissionExchangeError::EventLimitExceeded {
            actual: prefix.events.len(),
            limit: MAXIMUM_PREFIX_EVENTS,
        });
    }
    for (name, enabled) in [
        ("safe_to_execute", prefix.safe_to_execute),
        ("executes_work", prefix.executes_work),
        ("approves_work", prefix.approves_work),
        ("mutates_repositories", prefix.mutates_repositories),
        ("grants_provider_access", prefix.grants_provider_access),
        ("publishes_artifacts", prefix.publishes_artifacts),
        ("releases", prefix.releases),
        ("deploys", prefix.deploys),
        ("advances_authority", prefix.advances_authority),
    ] {
        if enabled {
            return Err(MissionExchangeError::AuthorityEnabled(name));
        }
    }
    if prefix.preceding_prefix_digest.is_some() {
        return Err(MissionExchangeError::PrecedingPrefixUnsupported);
    }
    let expected_last = prefix
        .events
        .len()
        .checked_sub(1)
        .map(|value| u64::try_from(value).unwrap_or(u64::MAX));
    if prefix.first_sequence != 0 || prefix.last_sequence != expected_last {
        return Err(MissionExchangeError::EventSequenceInvalid);
    }
    for (sequence, event) in prefix.events.iter().enumerate() {
        if event.schema_version != "ao.next.journal-event.v1"
            || event.sequence != u64::try_from(sequence).unwrap_or(u64::MAX)
        {
            return Err(MissionExchangeError::EventSequenceInvalid);
        }
    }
    validate_execution_prefix_lifecycle(&prefix.events)?;
    if prefix.events_digest != canonical_digest(&prefix.events)? {
        return Err(MissionExchangeError::EventsDigestMismatch);
    }
    let terminal_event_digest = prefix.events.last().and_then(|event| match &event.kind {
        JournalEventKind::TerminalPublished { record_digest } => Some(record_digest),
        _ => None,
    });
    if prefix
        .terminal_record
        .as_ref()
        .is_some_and(|record| !record.is_object())
    {
        return Err(MissionExchangeError::TerminalContradiction);
    }
    match (
        terminal_event_digest,
        prefix.terminal_digest.as_ref(),
        prefix.terminal_record.as_ref(),
    ) {
        (None, None, None) => {}
        (Some(event_digest), Some(terminal_digest), Some(record))
            if event_digest == terminal_digest
                && &digest_bytes(&canonical_json_bytes(record)?) == terminal_digest => {}
        _ => return Err(MissionExchangeError::TerminalContradiction),
    }
    if calculated_prefix_digest(prefix)? != prefix.prefix_digest {
        return Err(MissionExchangeError::PrefixDigestMismatch);
    }
    Ok(())
}

/// Writes one canonical prefix to a new file and never overwrites an existing leaf.
///
/// # Errors
///
/// Returns [`MissionExchangeError`] when serialization, exclusive creation,
/// writing, or synchronization fails.
pub fn write_execution_journal_prefix(
    path: &Path,
    prefix: &ExecutionJournalPrefix,
) -> Result<(), MissionExchangeError> {
    let bytes = bounded_prefix_bytes(prefix)?;
    let mut file = OpenOptions::new().write(true).create_new(true).open(path)?;
    file.write_all(&bytes)?;
    file.sync_all()?;
    Ok(())
}
