use std::collections::{BTreeMap, BTreeSet};
#[cfg(unix)]
use std::ffi::{OsStr, OsString};
use std::fs::{File, OpenOptions};
use std::io::{Read, Seek, SeekFrom, Write};
use std::path::{Path, PathBuf};
use std::sync::{Arc, Mutex};

#[cfg(unix)]
use rustix::fs::{Mode, OFlags};
#[cfg(unix)]
use std::os::unix::ffi::OsStrExt as _;
#[cfg(unix)]
use std::os::unix::fs::MetadataExt as _;
#[cfg(windows)]
use std::os::windows::fs::{MetadataExt as _, OpenOptionsExt as _};

use chrono::{DateTime, Utc};
use schemars::JsonSchema;
use serde::{Deserialize, Serialize};
use thiserror::Error;

use crate::adapter::EffectObservation;
use crate::contracts::{Digest, EffectRequest, RunRequest};
use crate::evidence::{EvidenceError, digest_bytes};
use crate::strict_json::{
    StrictJsonError, canonical_digest, canonical_json_bytes, decode_strict_json,
};

#[derive(Clone, Debug, Deserialize, Eq, JsonSchema, PartialEq, Serialize)]
#[serde(deny_unknown_fields)]
pub struct CheckpointIdentity {
    pub request_digest: Digest,
    pub source_digest: Digest,
    pub workspace_digest: Digest,
    pub policy_digest: Digest,
    pub model_profile_digest: Digest,
    pub verifier_profile_digest: Digest,
}

impl CheckpointIdentity {
    /// Derives every recovery identity from one exact run request.
    ///
    /// # Errors
    ///
    /// Returns [`RecoveryError`] when a request component cannot be canonically
    /// serialized.
    pub fn from_request(request: &RunRequest) -> Result<Self, RecoveryError> {
        Ok(Self {
            request_digest: canonical_digest(request)?,
            source_digest: canonical_digest(&request.source)?,
            workspace_digest: canonical_digest(&request.workspace)?,
            policy_digest: request.policy_digest.clone(),
            model_profile_digest: canonical_digest(&request.model_profile)?,
            verifier_profile_digest: request.verifier_profile.profile_digest.clone(),
        })
    }
}

#[derive(Clone, Debug, Deserialize, Eq, JsonSchema, PartialEq, Serialize)]
#[serde(deny_unknown_fields, rename_all = "snake_case", tag = "kind")]
pub enum JournalEventKind {
    ProviderRequestIntent {
        prepared_run_digest: Digest,
        execution_authority_digest: Digest,
    },
    ProviderProcessStarted {
        invocation_digest: Digest,
    },
    ProviderOutputRetained {
        raw_capture_digest: Digest,
    },
    ProviderCaptureIndexPublished {
        index_digest: Digest,
    },
    ProviderCaptureVerified {
        index_digest: Digest,
    },
    AdapterTurnNormalized {
        turn_digest: Digest,
    },
    EffectIntent {
        effect_id: String,
        effect_digest: Digest,
    },
    EffectCommitted {
        effect_id: String,
    },
    EffectCompleted {
        observation: EffectObservation,
    },
    VerificationStarted {
        attempt: u32,
    },
    VerifierRecorded {
        report_digest: Digest,
    },
    TerminalPublished {
        record_digest: Digest,
    },
}

#[derive(Clone, Debug, Deserialize, Eq, JsonSchema, PartialEq, Serialize)]
#[serde(deny_unknown_fields)]
pub struct JournalEvent {
    pub schema_version: String,
    pub sequence: u64,
    pub kind: JournalEventKind,
}

#[derive(Clone, Debug, Deserialize, Eq, JsonSchema, PartialEq, Serialize)]
#[serde(deny_unknown_fields)]
pub struct Checkpoint {
    pub schema_version: String,
    pub run_id: String,
    pub sequence: u64,
    pub identity: CheckpointIdentity,
    pub committed_effects: BTreeSet<String>,
    pub events_digest: Digest,
    pub recorded_at: DateTime<Utc>,
}

#[derive(Clone, Debug, Eq, PartialEq)]
pub struct ResumePlan {
    pub skipped_committed_effects: Vec<String>,
    pub remaining_effects: Vec<String>,
}

#[derive(Clone, Debug, Eq, PartialEq)]
pub enum JournalEffectState {
    Fresh,
    Unknown,
    Completed(EffectObservation),
}

#[derive(Clone, Debug, Eq, PartialEq)]
pub struct ProviderJournalState {
    pub prepared_run_digest: Option<Digest>,
    pub execution_authority_digest: Option<Digest>,
    pub provider_process_started: bool,
    pub invocation_digest: Option<Digest>,
    pub raw_capture_digest: Option<Digest>,
    pub capture_index_digest: Option<Digest>,
    pub capture_verified: bool,
    pub adapter_turn_digest: Option<Digest>,
}

impl ProviderJournalState {
    #[must_use]
    pub const fn outcome_unknown(&self) -> bool {
        self.prepared_run_digest.is_some() && self.raw_capture_digest.is_none()
    }
}

#[derive(Debug, Error)]
pub enum RecoveryError {
    #[error("recovery I/O failed: {0}")]
    Io(String),
    #[error("recovery artifact is unsafe: {0}")]
    UnsafePath(PathBuf),
    #[error("recovery artifact is oversized: {actual} bytes exceeds {limit}")]
    Oversized { actual: u64, limit: u64 },
    #[error("checkpoint event digest mismatch")]
    EventDigestMismatch,
    #[error("checkpoint digest mismatch: expected {expected}, observed {observed}")]
    CheckpointDigestMismatch { expected: Digest, observed: Digest },
    #[error("checkpoint identity mismatch")]
    IdentityMismatch,
    #[error("committed effect is absent from the durable event log: {0}")]
    CommittedEffectMissing(String),
    #[error("durable verifier event is missing")]
    VerifierEventMissing,
    #[error("journal event sequence is invalid")]
    EventSequenceInvalid,
    #[error("journal effect identity or digest drifted")]
    EffectIdentityMismatch,
    #[error("checkpoint schema is invalid")]
    InvalidSchema,
    #[error("strict JSON failure: {0}")]
    StrictJson(#[from] StrictJsonError),
}

impl From<std::io::Error> for RecoveryError {
    fn from(error: std::io::Error) -> Self {
        Self::Io(error.to_string())
    }
}

impl From<EvidenceError> for RecoveryError {
    fn from(error: EvidenceError) -> Self {
        Self::Io(error.to_string())
    }
}

fn provider_state_from_events(
    events: &[JournalEvent],
) -> Result<ProviderJournalState, RecoveryError> {
    Ok(validate_journal_lifecycle(events)?.provider)
}

struct JournalLifecycle {
    provider: ProviderJournalState,
    effects: BTreeMap<String, (Digest, Option<EffectObservation>)>,
    legacy_committed_effects: BTreeSet<String>,
    verifier_records: u32,
    verification_open: bool,
    terminal_digest: Option<Digest>,
}

impl JournalLifecycle {
    const fn verification_seen(&self) -> bool {
        self.verification_open || self.verifier_records > 0 || self.terminal_digest.is_some()
    }
}

#[allow(
    clippy::too_many_lines,
    reason = "the durable lifecycle stays linear so every legal prefix transition is visible"
)]
fn validate_journal_lifecycle(events: &[JournalEvent]) -> Result<JournalLifecycle, RecoveryError> {
    let mut state = ProviderJournalState {
        prepared_run_digest: None,
        execution_authority_digest: None,
        provider_process_started: false,
        invocation_digest: None,
        raw_capture_digest: None,
        capture_index_digest: None,
        capture_verified: false,
        adapter_turn_digest: None,
    };
    let mut effect_seen = false;
    let mut verification_seen = false;
    let mut provider_step = 0_u8;
    let mut effects = BTreeMap::<String, (Digest, Option<EffectObservation>)>::new();
    let mut legacy_committed_effects = BTreeSet::new();
    let mut verifier_records = 0_u32;
    let mut verification_open = false;
    let mut terminal_digest = None;
    for (expected_sequence, event) in events.iter().enumerate() {
        if event.schema_version != "ao.next.journal-event.v1"
            || event.sequence != u64::try_from(expected_sequence).unwrap_or(u64::MAX)
            || terminal_digest.is_some()
        {
            return Err(RecoveryError::EventSequenceInvalid);
        }
        match &event.kind {
            JournalEventKind::ProviderRequestIntent {
                prepared_run_digest,
                execution_authority_digest,
            } if !effect_seen && !verification_seen && provider_step == 0 => {
                state.prepared_run_digest = Some(prepared_run_digest.clone());
                state.execution_authority_digest = Some(execution_authority_digest.clone());
                provider_step = 1;
            }
            JournalEventKind::ProviderProcessStarted { invocation_digest }
                if !effect_seen && !verification_seen && provider_step == 1 =>
            {
                state.provider_process_started = true;
                state.invocation_digest = Some(invocation_digest.clone());
                provider_step = 2;
            }
            JournalEventKind::ProviderOutputRetained { raw_capture_digest }
                if !effect_seen && !verification_seen && provider_step == 2 =>
            {
                state.raw_capture_digest = Some(raw_capture_digest.clone());
                provider_step = 3;
            }
            JournalEventKind::ProviderCaptureIndexPublished { index_digest }
                if !effect_seen && !verification_seen && provider_step == 3 =>
            {
                state.capture_index_digest = Some(index_digest.clone());
                provider_step = 4;
            }
            JournalEventKind::ProviderCaptureVerified { index_digest }
                if !effect_seen
                    && !verification_seen
                    && provider_step == 4
                    && state.capture_index_digest.as_ref() == Some(index_digest) =>
            {
                state.capture_verified = true;
                provider_step = 5;
            }
            JournalEventKind::AdapterTurnNormalized { turn_digest }
                if !effect_seen && !verification_seen && provider_step == 5 =>
            {
                state.adapter_turn_digest = Some(turn_digest.clone());
                provider_step = 6;
            }
            JournalEventKind::EffectIntent {
                effect_id,
                effect_digest,
            } => {
                if verification_seen
                    || (provider_step != 0 && provider_step != 6)
                    || legacy_committed_effects.contains(effect_id)
                    || effects
                        .insert(effect_id.clone(), (effect_digest.clone(), None))
                        .is_some()
                {
                    return Err(RecoveryError::EventSequenceInvalid);
                }
                effect_seen = true;
            }
            JournalEventKind::EffectCommitted { effect_id } => {
                if verification_seen
                    || (provider_step != 0 && provider_step != 6)
                    || effects.contains_key(effect_id)
                    || !legacy_committed_effects.insert(effect_id.clone())
                {
                    return Err(RecoveryError::EventSequenceInvalid);
                }
                effect_seen = true;
            }
            JournalEventKind::EffectCompleted { observation } => {
                let Some((_, completion)) = effects.get_mut(&observation.effect_id) else {
                    return Err(RecoveryError::EventSequenceInvalid);
                };
                if verification_seen || completion.replace(observation.clone()).is_some() {
                    return Err(RecoveryError::EventSequenceInvalid);
                }
                effect_seen = true;
            }
            JournalEventKind::VerificationStarted { attempt } => {
                if (provider_step != 0 && provider_step != 6)
                    || verification_open
                    || *attempt != verifier_records
                    || effects.values().any(|(_, completion)| completion.is_none())
                {
                    return Err(RecoveryError::EventSequenceInvalid);
                }
                verification_seen = true;
                verification_open = true;
            }
            JournalEventKind::VerifierRecorded { .. } => {
                if !verification_open {
                    return Err(RecoveryError::EventSequenceInvalid);
                }
                verification_open = false;
                verifier_records = verifier_records
                    .checked_add(1)
                    .ok_or(RecoveryError::EventSequenceInvalid)?;
            }
            JournalEventKind::TerminalPublished { record_digest } => {
                if verification_open || verifier_records == 0 {
                    return Err(RecoveryError::EventSequenceInvalid);
                }
                terminal_digest = Some(record_digest.clone());
            }
            JournalEventKind::ProviderRequestIntent { .. }
            | JournalEventKind::ProviderProcessStarted { .. }
            | JournalEventKind::ProviderOutputRetained { .. }
            | JournalEventKind::ProviderCaptureIndexPublished { .. }
            | JournalEventKind::ProviderCaptureVerified { .. }
            | JournalEventKind::AdapterTurnNormalized { .. } => {
                return Err(RecoveryError::EventSequenceInvalid);
            }
        }
    }
    Ok(JournalLifecycle {
        provider: state,
        effects,
        legacy_committed_effects,
        verifier_records,
        verification_open,
        terminal_digest,
    })
}

pub(crate) fn validate_execution_prefix_lifecycle(
    events: &[JournalEvent],
) -> Result<(), RecoveryError> {
    validate_journal_lifecycle(events).map(|_| ())
}

fn require_provider_ready(state: &ProviderJournalState) -> Result<(), RecoveryError> {
    if state.prepared_run_digest.is_some() && state.adapter_turn_digest.is_none() {
        return Err(RecoveryError::EventSequenceInvalid);
    }
    Ok(())
}

fn require_verification_complete(events: &[JournalEvent]) -> Result<(), RecoveryError> {
    let lifecycle = validate_journal_lifecycle(events)?;
    if lifecycle.verifier_records == 0 || lifecycle.verification_open {
        return Err(RecoveryError::VerifierEventMissing);
    }
    Ok(())
}

#[cfg(windows)]
const FILE_FLAG_BACKUP_SEMANTICS: u32 = 0x0200_0000;
#[cfg(windows)]
const FILE_FLAG_OPEN_REPARSE_POINT: u32 = 0x0020_0000;
#[cfg(windows)]
const FILE_SHARE_READ: u32 = 0x1;
#[cfg(windows)]
const FILE_SHARE_WRITE: u32 = 0x2;

#[cfg(any(test, windows))]
const fn windows_reparse_point(attributes: u32) -> bool {
    attributes & 0x400 != 0
}

#[derive(Clone, Debug)]
pub struct CheckpointJournal {
    root: PathBuf,
    maximum_bytes: u64,
    mode: JournalMode,
}

#[derive(Clone, Debug)]
enum JournalMode {
    CreateCapable,
    ExistingOnly(Arc<Mutex<ExistingJournalBinding>>),
}

#[derive(Debug)]
struct ExistingJournalBinding {
    request_identity: Vec<u8>,
    root: OpenedJournalPath,
    identity: OpenedJournalPath,
    events: OpenedJournalPath,
    event_files: BTreeMap<PathBuf, OpenedJournalPath>,
    #[cfg(unix)]
    terminal_files: BTreeMap<PathBuf, OpenedJournalPath>,
    #[cfg(unix)]
    descriptor_reads: bool,
    initializing: bool,
}

#[derive(Clone, Debug)]
struct OpenedJournalPath {
    fingerprint: JournalPathFingerprint,
    anchor: Arc<File>,
}

#[derive(Clone, Debug, Eq, PartialEq)]
struct JournalPathFingerprint {
    #[cfg(unix)]
    device: u64,
    #[cfg(unix)]
    inode: u64,
    #[cfg(windows)]
    handle: Arc<same_file::Handle>,
    #[cfg(not(any(unix, windows)))]
    length: u64,
}

impl CheckpointJournal {
    /// Creates a checkpoint journal beneath an existing regular directory.
    ///
    /// # Errors
    ///
    /// Returns [`RecoveryError`] when the root cannot be created or is a symlink
    /// or non-directory entry.
    pub fn new(root: impl AsRef<Path>, maximum_bytes: u64) -> Result<Self, RecoveryError> {
        let root = root.as_ref().to_path_buf();
        std::fs::create_dir_all(&root)?;
        open_journal_path(&root, true)?;
        Ok(Self {
            root,
            maximum_bytes,
            mode: JournalMode::CreateCapable,
        })
    }

    /// Opens an existing journal only when it is already bound to one exact request.
    ///
    /// # Errors
    ///
    /// Returns [`RecoveryError`] without creating files or directories when the
    /// root or exact request identity is missing, unsafe, malformed, or drifted.
    pub fn open_bound(
        root: impl AsRef<Path>,
        maximum_bytes: u64,
        request: &RunRequest,
    ) -> Result<Self, RecoveryError> {
        let root = root.as_ref().to_path_buf();
        let root_opened = open_journal_path(&root, true)?;
        Self::open_bound_from_opened(root, maximum_bytes, request, root_opened, false)
    }

    /// Opens an existing Unix journal through one already accepted root descriptor.
    ///
    /// # Errors
    ///
    /// Returns [`RecoveryError`] when descriptor-relative identity, event, or
    /// terminal reads fail validation.
    #[cfg(unix)]
    pub fn open_bound_from_unix_root(
        root: impl AsRef<Path>,
        root_anchor: File,
        maximum_bytes: u64,
        request: &RunRequest,
    ) -> Result<Self, RecoveryError> {
        let root = root.as_ref().to_path_buf();
        let root_opened = opened_unix_journal_path(root_anchor, &root, true)?;
        Self::open_bound_from_opened(root, maximum_bytes, request, root_opened, true)
    }

    fn open_bound_from_opened(
        root: PathBuf,
        maximum_bytes: u64,
        request: &RunRequest,
        root_opened: OpenedJournalPath,
        #[allow(unused_variables)] descriptor_reads: bool,
    ) -> Result<Self, RecoveryError> {
        let provisional = Self {
            root: root.clone(),
            maximum_bytes,
            mode: JournalMode::CreateCapable,
        };
        let (identity, identity_bytes) = provisional.request_identity(request)?;
        #[cfg(unix)]
        let identity_opened = open_existing_journal_entry(
            &root_opened,
            &root,
            OsStr::new("execution-identity.json"),
            false,
            !descriptor_reads,
        )?;
        #[cfg(not(unix))]
        let identity_opened = open_journal_path(&root.join("execution-identity.json"), false)?;
        validate_identity_bytes(&identity_opened, &identity, &identity_bytes, maximum_bytes)?;
        #[cfg(unix)]
        let events_opened = open_existing_journal_entry(
            &root_opened,
            &root,
            OsStr::new("execution-events"),
            true,
            !descriptor_reads,
        )?;
        #[cfg(not(unix))]
        let events_opened = open_journal_path(&root.join("execution-events"), true)?;
        let binding = Arc::new(Mutex::new(ExistingJournalBinding {
            request_identity: identity_bytes,
            root: root_opened,
            identity: identity_opened,
            events: events_opened,
            event_files: BTreeMap::new(),
            #[cfg(unix)]
            terminal_files: BTreeMap::new(),
            #[cfg(unix)]
            descriptor_reads,
            initializing: true,
        }));
        let journal = Self {
            root,
            maximum_bytes,
            mode: JournalMode::ExistingOnly(binding.clone()),
        };
        let events = journal.load_execution_events()?;
        journal.terminal_record(&events)?;
        binding
            .lock()
            .map_err(|error| RecoveryError::Io(error.to_string()))?
            .initializing = false;
        Ok(journal)
    }

    /// Binds the append-only execution journal to one exact run request before
    /// worker dispatch.
    ///
    /// # Errors
    ///
    /// Returns [`RecoveryError`] when the identity path is unsafe, unreadable,
    /// oversized, malformed, or already bound to a different request.
    pub fn bind_request(&self, request: &RunRequest) -> Result<(), RecoveryError> {
        if matches!(self.mode, JournalMode::ExistingOnly(_)) {
            return self.require_bound_request(request);
        }
        let path = self.root.join("execution-identity.json");
        if path.exists() || path.is_symlink() {
            return self.require_bound_request(request);
        }
        let (_, bytes) = self.request_identity(request)?;
        durable_create_new(&path, &bytes)
    }

    fn request_identity(
        &self,
        request: &RunRequest,
    ) -> Result<(CheckpointIdentity, Vec<u8>), RecoveryError> {
        let identity = CheckpointIdentity::from_request(request)?;
        let bytes = canonical_json_bytes(&identity)?;
        if bytes.len() as u64 > self.maximum_bytes {
            return Err(RecoveryError::Oversized {
                actual: bytes.len() as u64,
                limit: self.maximum_bytes,
            });
        }
        Ok((identity, bytes))
    }

    fn require_bound_request(&self, request: &RunRequest) -> Result<(), RecoveryError> {
        let (identity, bytes) = self.request_identity(request)?;
        if let JournalMode::ExistingOnly(binding) = &self.mode {
            let expected = binding
                .lock()
                .map_err(|error| RecoveryError::Io(error.to_string()))?;
            if expected.request_identity != bytes {
                return Err(RecoveryError::IdentityMismatch);
            }
            #[cfg(unix)]
            if expected.descriptor_reads {
                return validate_identity_bytes(
                    &expected.identity,
                    &identity,
                    &bytes,
                    self.maximum_bytes,
                );
            }
            let root = expected.root.clone();
            let identity_path = expected.identity.clone();
            let events = expected.events.clone();
            drop(expected);
            require_same_journal_path(&self.root, true, &root)?;
            let opened_identity = require_same_journal_path(
                &self.root.join("execution-identity.json"),
                false,
                &identity_path,
            )?;
            require_same_journal_path(&self.root.join("execution-events"), true, &events)?;
            validate_identity_bytes(&opened_identity, &identity, &bytes, self.maximum_bytes)?;
        } else {
            let opened = open_journal_path(&self.root.join("execution-identity.json"), false)?;
            validate_identity_bytes(&opened, &identity, &bytes, self.maximum_bytes)?;
        }
        Ok(())
    }

    /// Binds one exact request only when its execution-event prefix is empty.
    ///
    /// # Errors
    ///
    /// Returns [`RecoveryError`] for ordinary request-binding failures or when
    /// any valid, malformed, or unsafe execution-event entry already exists.
    pub fn bind_pristine_request(&self, request: &RunRequest) -> Result<(), RecoveryError> {
        self.bind_request(request)?;
        let directory = self.root.join("execution-events");
        match std::fs::symlink_metadata(&directory) {
            Ok(_) => {
                open_journal_path(&directory, true)?;
            }
            Err(error) if error.kind() == std::io::ErrorKind::NotFound => return Ok(()),
            Err(error) => return Err(error.into()),
        }
        if std::fs::read_dir(&directory)?.next().transpose()?.is_none() {
            return Ok(());
        }
        self.load_execution_events()?;
        Err(RecoveryError::EventSequenceInvalid)
    }

    /// Returns the durable provider-capture lifecycle for one exact request.
    ///
    /// # Errors
    ///
    /// Returns [`RecoveryError`] for identity drift, malformed or reordered
    /// events, unsafe paths, size violations, or I/O failure.
    pub fn provider_state(
        &self,
        request: &RunRequest,
    ) -> Result<ProviderJournalState, RecoveryError> {
        self.bind_request(request)?;
        provider_state_from_events(&self.load_execution_events()?)
    }

    /// Returns provider state from an existing request-bound journal without
    /// creating the journal root, identity, or event directory.
    ///
    /// # Errors
    ///
    /// Returns [`RecoveryError`] for any ordinary request, identity, event-log,
    /// path, size, or I/O failure.
    pub fn provider_state_read_only(
        &self,
        request: &RunRequest,
    ) -> Result<ProviderJournalState, RecoveryError> {
        if !matches!(self.mode, JournalMode::ExistingOnly(_)) {
            return Err(RecoveryError::EventSequenceInvalid);
        }
        self.require_bound_request(request)?;
        provider_state_from_events(&self.load_execution_events()?)
    }

    /// Reads one exact, already retained journal prefix without creating,
    /// repairing, or appending any journal artifact.
    ///
    /// # Errors
    ///
    /// Returns [`RecoveryError`] unless this journal was opened existing-only
    /// and its request identity, event lifecycle, paths, and terminal bytes are
    /// unchanged and valid.
    #[allow(
        clippy::type_complexity,
        reason = "the task-owned read-only interface returns the exact identity, events, and terminal bytes"
    )]
    pub fn verified_execution_prefix(
        &self,
        request: &RunRequest,
    ) -> Result<(CheckpointIdentity, Vec<JournalEvent>, Option<Vec<u8>>), RecoveryError> {
        if !matches!(self.mode, JournalMode::ExistingOnly(_)) {
            return Err(RecoveryError::EventSequenceInvalid);
        }
        self.require_bound_request(request)?;
        let events = self.load_execution_events()?;
        validate_journal_lifecycle(&events)?;
        let terminal = self.terminal_record(&events)?.map(|(_, bytes, _)| bytes);
        Ok((CheckpointIdentity::from_request(request)?, events, terminal))
    }

    /// Confirms that no provider attempt or effect is already durable.
    ///
    /// # Errors
    ///
    /// Returns [`RecoveryError`] when starting could repeat an unknown provider
    /// outcome or place provider events after effects.
    pub fn provider_may_start(&self, request: &RunRequest) -> Result<(), RecoveryError> {
        self.bind_request(request)?;
        let events = self.load_execution_events()?;
        let state = provider_state_from_events(&events)?;
        if state.prepared_run_digest.is_some()
            || events.iter().any(|event| {
                matches!(
                    event.kind,
                    JournalEventKind::EffectIntent { .. }
                        | JournalEventKind::EffectCommitted { .. }
                        | JournalEventKind::EffectCompleted { .. }
                )
            })
        {
            return Err(RecoveryError::EventSequenceInvalid);
        }
        Ok(())
    }

    /// Durably records provider request intent before process execution.
    ///
    /// # Errors
    ///
    /// Returns [`RecoveryError`] unless no provider attempt or effect is
    /// already durable.
    pub fn record_provider_request_intent(
        &self,
        request: &RunRequest,
        prepared: &Digest,
        execution_authority: &Digest,
    ) -> Result<(), RecoveryError> {
        self.provider_may_start(request)?;
        self.append_execution_event(JournalEventKind::ProviderRequestIntent {
            prepared_run_digest: prepared.clone(),
            execution_authority_digest: execution_authority.clone(),
        })
    }

    /// Durably records that the provider process transition was reached.
    ///
    /// # Errors
    ///
    /// Returns [`RecoveryError`] unless provider request intent is the exact
    /// current lifecycle state.
    pub fn record_provider_process_started(
        &self,
        request: &RunRequest,
        invocation: &Digest,
    ) -> Result<(), RecoveryError> {
        let state = self.provider_state(request)?;
        if state.prepared_run_digest.is_none() || state.invocation_digest.is_some() {
            return Err(RecoveryError::EventSequenceInvalid);
        }
        self.append_execution_event(JournalEventKind::ProviderProcessStarted {
            invocation_digest: invocation.clone(),
        })
    }

    /// Durably records the retained raw provider capture.
    ///
    /// # Errors
    ///
    /// Returns [`RecoveryError`] unless provider process start is the exact
    /// current lifecycle state.
    pub fn record_provider_output_retained(
        &self,
        request: &RunRequest,
        raw: &Digest,
    ) -> Result<(), RecoveryError> {
        let state = self.provider_state(request)?;
        if state.invocation_digest.is_none() || state.raw_capture_digest.is_some() {
            return Err(RecoveryError::EventSequenceInvalid);
        }
        self.append_execution_event(JournalEventKind::ProviderOutputRetained {
            raw_capture_digest: raw.clone(),
        })
    }

    /// Durably records the published provider capture index.
    ///
    /// # Errors
    ///
    /// Returns [`RecoveryError`] unless retained provider output is the exact
    /// current lifecycle state.
    pub fn record_provider_capture_published(
        &self,
        request: &RunRequest,
        index: &Digest,
    ) -> Result<(), RecoveryError> {
        let state = self.provider_state(request)?;
        if state.raw_capture_digest.is_none() || state.capture_index_digest.is_some() {
            return Err(RecoveryError::EventSequenceInvalid);
        }
        self.append_execution_event(JournalEventKind::ProviderCaptureIndexPublished {
            index_digest: index.clone(),
        })
    }

    /// Durably records verification of the published provider capture index.
    ///
    /// # Errors
    ///
    /// Returns [`RecoveryError`] unless the same index digest was published
    /// exactly once.
    pub fn record_provider_capture_verified(
        &self,
        request: &RunRequest,
        index: &Digest,
    ) -> Result<(), RecoveryError> {
        let state = self.provider_state(request)?;
        if state.capture_index_digest.as_ref() != Some(index) || state.capture_verified {
            return Err(RecoveryError::EventSequenceInvalid);
        }
        self.append_execution_event(JournalEventKind::ProviderCaptureVerified {
            index_digest: index.clone(),
        })
    }

    /// Durably records the trusted normalized adapter turn.
    ///
    /// # Errors
    ///
    /// Returns [`RecoveryError`] unless provider capture verification is the
    /// exact current lifecycle state.
    pub fn record_adapter_turn_normalized(
        &self,
        request: &RunRequest,
        turn: &Digest,
    ) -> Result<(), RecoveryError> {
        let state = self.provider_state(request)?;
        if !state.capture_verified {
            return Err(RecoveryError::EventSequenceInvalid);
        }
        if let Some(recorded) = state.adapter_turn_digest {
            return if recorded == *turn {
                Ok(())
            } else {
                Err(RecoveryError::EventDigestMismatch)
            };
        }
        self.append_execution_event(JournalEventKind::AdapterTurnNormalized {
            turn_digest: turn.clone(),
        })
    }

    /// Returns the durable state of one exact effect without executing it.
    ///
    /// # Errors
    ///
    /// Returns [`RecoveryError`] for run/effect identity drift, malformed or
    /// reordered events, unsafe paths, size violations, or I/O failure.
    pub fn effect_state(
        &self,
        request: &RunRequest,
        effect: &EffectRequest,
    ) -> Result<JournalEffectState, RecoveryError> {
        self.bind_request(request)?;
        let effect_digest = canonical_digest(effect)?;
        let events = self.load_execution_events()?;
        let lifecycle = validate_journal_lifecycle(&events)?;
        require_provider_ready(&lifecycle.provider)?;
        let Some((recorded_digest, completion)) = lifecycle.effects.get(&effect.effect_id) else {
            if lifecycle.verification_seen() {
                return Err(RecoveryError::EventSequenceInvalid);
            }
            return Ok(JournalEffectState::Fresh);
        };
        if recorded_digest != &effect_digest {
            return Err(RecoveryError::EffectIdentityMismatch);
        }
        Ok(completion
            .clone()
            .map_or(JournalEffectState::Unknown, JournalEffectState::Completed))
    }

    /// Returns durable states only when the journal contains no effect outside
    /// the exact normalized turn effect set.
    ///
    /// # Errors
    ///
    /// Returns [`RecoveryError`] for duplicate expected identities, any
    /// journal-only effect, effect digest drift, malformed sequencing, or
    /// ordinary request and path validation failures.
    pub fn effect_states(
        &self,
        request: &RunRequest,
        effects: &[EffectRequest],
    ) -> Result<Vec<JournalEffectState>, RecoveryError> {
        self.bind_request(request)?;
        let events = self.load_execution_events()?;
        let lifecycle = validate_journal_lifecycle(&events)?;
        require_provider_ready(&lifecycle.provider)?;
        let recorded = &lifecycle.effects;
        let mut expected = BTreeMap::new();
        for effect in effects {
            if effect.run_id != request.run_id
                || expected
                    .insert(effect.effect_id.clone(), canonical_digest(effect)?)
                    .is_some()
            {
                return Err(RecoveryError::EffectIdentityMismatch);
            }
        }
        if recorded
            .keys()
            .any(|effect_id| !expected.contains_key(effect_id))
            || (lifecycle.verification_seen()
                && expected
                    .keys()
                    .any(|effect_id| !recorded.contains_key(effect_id)))
        {
            return Err(RecoveryError::EffectIdentityMismatch);
        }
        effects
            .iter()
            .map(|effect| match recorded.get(&effect.effect_id) {
                None => Ok(JournalEffectState::Fresh),
                Some((digest, _)) if Some(digest) != expected.get(&effect.effect_id) => {
                    Err(RecoveryError::EffectIdentityMismatch)
                }
                Some((_, completion)) => Ok(completion
                    .clone()
                    .map_or(JournalEffectState::Unknown, JournalEffectState::Completed)),
            })
            .collect()
    }

    /// Durably records exact effect intent before native execution.
    ///
    /// # Errors
    ///
    /// Returns [`RecoveryError`] unless the effect is fresh and the next event
    /// can be created and synced without replacing prior journal bytes.
    pub fn record_effect_intent(
        &self,
        request: &RunRequest,
        effect: &EffectRequest,
    ) -> Result<(), RecoveryError> {
        if self.effect_state(request, effect)? != JournalEffectState::Fresh {
            return Err(RecoveryError::EventSequenceInvalid);
        }
        self.append_execution_event(JournalEventKind::EffectIntent {
            effect_id: effect.effect_id.clone(),
            effect_digest: canonical_digest(effect)?,
        })
    }

    /// Durably records exact effect completion after native execution.
    ///
    /// # Errors
    ///
    /// Returns [`RecoveryError`] unless matching intent is durable and the next
    /// event can be created and synced without replacing prior journal bytes.
    pub fn record_effect_completion(
        &self,
        request: &RunRequest,
        effect: &EffectRequest,
        observation: &EffectObservation,
    ) -> Result<(), RecoveryError> {
        if observation.effect_id != effect.effect_id
            || self.effect_state(request, effect)? != JournalEffectState::Unknown
        {
            return Err(RecoveryError::EffectIdentityMismatch);
        }
        self.append_execution_event(JournalEventKind::EffectCompleted {
            observation: observation.clone(),
        })
    }

    /// Durably marks verification as started, or resumes one unmatched start.
    ///
    /// # Errors
    ///
    /// Returns [`RecoveryError`] for identity drift or contradictory journal
    /// sequencing.
    pub fn begin_verification(&self, request: &RunRequest) -> Result<(), RecoveryError> {
        self.bind_request(request)?;
        let events = self.load_execution_events()?;
        let lifecycle = validate_journal_lifecycle(&events)?;
        require_provider_ready(&lifecycle.provider)?;
        if lifecycle.terminal_digest.is_some() {
            return Err(RecoveryError::EventSequenceInvalid);
        }
        if lifecycle.verification_open {
            return Ok(());
        }
        self.append_execution_event(JournalEventKind::VerificationStarted {
            attempt: lifecycle.verifier_records,
        })
    }

    /// Durably records the verifier report for one started attempt.
    ///
    /// # Errors
    ///
    /// Returns [`RecoveryError`] unless exactly one verification start remains
    /// unmatched.
    pub fn record_verifier(
        &self,
        request: &RunRequest,
        report_digest: &Digest,
    ) -> Result<(), RecoveryError> {
        self.bind_request(request)?;
        let events = self.load_execution_events()?;
        let lifecycle = validate_journal_lifecycle(&events)?;
        require_provider_ready(&lifecycle.provider)?;
        if !lifecycle.verification_open || lifecycle.terminal_digest.is_some() {
            return Err(RecoveryError::EventSequenceInvalid);
        }
        self.append_execution_event(JournalEventKind::VerifierRecorded {
            report_digest: report_digest.clone(),
        })
    }

    /// Reads one exact content-addressed terminal file after completed
    /// verification without changing journal state.
    ///
    /// # Errors
    ///
    /// Returns [`RecoveryError`] when verification is incomplete, terminal
    /// files or events are missing, duplicated, unsafe, oversized, or
    /// contradictory, or the terminal event cannot be appended durably.
    pub fn retained_terminal_record(
        &self,
        request: &RunRequest,
    ) -> Result<Option<Vec<u8>>, RecoveryError> {
        self.bind_request(request)?;
        let events = self.load_execution_events()?;
        require_provider_ready(&provider_state_from_events(&events)?)?;
        let terminal = self.terminal_record(&events)?;
        if terminal.is_some() {
            require_verification_complete(&events)?;
        }
        Ok(terminal.map(|(_, bytes, _)| bytes))
    }

    /// Completes a terminal event from one exact pre-existing
    /// content-addressed terminal file, or returns the already published bytes.
    ///
    /// # Errors
    ///
    /// Returns the same failures as [`Self::retained_terminal_record`] or
    /// [`Self::publish_terminal_record`].
    pub fn recover_terminal_record(
        &self,
        request: &RunRequest,
    ) -> Result<Option<Vec<u8>>, RecoveryError> {
        let Some(bytes) = self.retained_terminal_record(request)? else {
            return Ok(None);
        };
        self.publish_terminal_record(request, &bytes)?;
        Ok(Some(bytes))
    }

    /// Publishes canonical terminal bytes at a content-addressed create-only
    /// path and then appends their durable journal event.
    ///
    /// # Errors
    ///
    /// Returns [`RecoveryError`] when verification is incomplete, a terminal
    /// contradicts an existing terminal, or retained bytes/path integrity fail.
    pub fn publish_terminal_record(
        &self,
        request: &RunRequest,
        bytes: &[u8],
    ) -> Result<Digest, RecoveryError> {
        self.bind_request(request)?;
        if bytes.len() as u64 > self.maximum_bytes {
            return Err(RecoveryError::Oversized {
                actual: bytes.len() as u64,
                limit: self.maximum_bytes,
            });
        }
        let digest = digest_bytes(bytes);
        let events = self.load_execution_events()?;
        require_provider_ready(&provider_state_from_events(&events)?)?;
        require_verification_complete(&events)?;
        if let Some((recorded_digest, recorded_bytes, recorded)) = self.terminal_record(&events)? {
            if recorded_digest != digest || recorded_bytes != bytes {
                return Err(RecoveryError::EventDigestMismatch);
            }
            if !recorded {
                self.append_execution_event(JournalEventKind::TerminalPublished {
                    record_digest: digest.clone(),
                })?;
            }
            return Ok(digest);
        }
        let digest_hex = digest
            .as_str()
            .strip_prefix("sha256:")
            .ok_or(RecoveryError::EventDigestMismatch)?;
        self.create_terminal_path(digest_hex, bytes)?;
        self.append_execution_event(JournalEventKind::TerminalPublished {
            record_digest: digest.clone(),
        })?;
        Ok(digest)
    }

    fn terminal_record(
        &self,
        events: &[JournalEvent],
    ) -> Result<Option<(Digest, Vec<u8>, bool)>, RecoveryError> {
        let recorded = events
            .iter()
            .filter_map(|event| match &event.kind {
                JournalEventKind::TerminalPublished { record_digest } => {
                    Some(record_digest.clone())
                }
                _ => None,
            })
            .collect::<Vec<_>>();
        if recorded.len() > 1 {
            return Err(RecoveryError::EventSequenceInvalid);
        }
        let mut files = self.terminal_files()?;
        files.sort_by(|left, right| left.0.cmp(&right.0));
        if files.len() > 1 {
            return Err(RecoveryError::EventDigestMismatch);
        }
        let Some((path, opened)) = files.pop() else {
            return if recorded.is_empty() {
                Ok(None)
            } else {
                Err(RecoveryError::EventDigestMismatch)
            };
        };
        let name = path
            .file_name()
            .and_then(|name| name.to_str())
            .ok_or_else(|| RecoveryError::UnsafePath(path.clone()))?;
        let digest_hex = name
            .strip_prefix("terminal-")
            .and_then(|name| name.strip_suffix(".json"))
            .filter(|hex| {
                hex.len() == 64
                    && hex
                        .bytes()
                        .all(|byte| byte.is_ascii_digit() || (b'a'..=b'f').contains(&byte))
            })
            .ok_or(RecoveryError::EventDigestMismatch)?;
        let digest = Digest::new(format!("sha256:{digest_hex}"))
            .map_err(|_| RecoveryError::EventDigestMismatch)?;
        let bytes = opened.map_or_else(
            || read_journal_regular(&path, self.maximum_bytes),
            |opened| read_opened_regular(&opened, self.maximum_bytes),
        )?;
        if digest_bytes(&bytes) != digest
            || recorded.first().is_some_and(|recorded| recorded != &digest)
        {
            return Err(RecoveryError::EventDigestMismatch);
        }
        Ok(Some((digest, bytes, !recorded.is_empty())))
    }

    fn terminal_files(&self) -> Result<Vec<(PathBuf, Option<OpenedJournalPath>)>, RecoveryError> {
        #[cfg(unix)]
        if let JournalMode::ExistingOnly(binding) = &self.mode {
            let (root, descriptor_reads) = {
                let binding = binding
                    .lock()
                    .map_err(|error| RecoveryError::Io(error.to_string()))?;
                (binding.root.clone(), binding.descriptor_reads)
            };
            if !descriptor_reads {
                require_same_journal_path(&self.root, true, &root)?;
            }
            let mut directory =
                rustix::fs::Dir::read_from(root.anchor.as_ref()).map_err(std::io::Error::from)?;
            let mut names = Vec::<OsString>::new();
            for entry in &mut directory {
                let entry = entry.map_err(std::io::Error::from)?;
                let name = entry.file_name().to_bytes();
                if name.starts_with(b"terminal-") {
                    names.push(OsStr::from_bytes(name).to_os_string());
                }
            }
            names.sort();
            let mut files = Vec::with_capacity(names.len());
            let mut observed = BTreeSet::new();
            for name in names {
                let path = self.root.join(&name);
                let opened =
                    open_existing_terminal_file(&root, &self.root, &name, !descriptor_reads)?;
                self.validate_terminal_path(&path, &opened)?;
                observed.insert(path.clone());
                files.push((path, Some(opened)));
            }
            if !descriptor_reads {
                require_same_journal_path(&self.root, true, &root)?;
            }
            if binding
                .lock()
                .map_err(|error| RecoveryError::Io(error.to_string()))?
                .terminal_files
                .keys()
                .cloned()
                .collect::<BTreeSet<_>>()
                != observed
            {
                return Err(RecoveryError::EventDigestMismatch);
            }
            return Ok(files);
        }

        Ok(std::fs::read_dir(&self.root)?
            .map(|entry| entry.map(|value| value.path()))
            .collect::<Result<Vec<_>, _>>()?
            .into_iter()
            .filter(|path| {
                path.file_name()
                    .and_then(|name| name.to_str())
                    .is_some_and(|name| name.starts_with("terminal-"))
            })
            .map(|path| (path, None))
            .collect())
    }

    #[cfg(unix)]
    fn validate_terminal_path(
        &self,
        path: &Path,
        opened: &OpenedJournalPath,
    ) -> Result<(), RecoveryError> {
        let JournalMode::ExistingOnly(binding) = &self.mode else {
            return Ok(());
        };
        let mut binding = binding
            .lock()
            .map_err(|error| RecoveryError::Io(error.to_string()))?;
        if let Some(expected) = binding.terminal_files.get(path) {
            if opened.fingerprint != expected.fingerprint {
                return Err(RecoveryError::IdentityMismatch);
            }
        } else if binding.initializing {
            binding
                .terminal_files
                .insert(path.to_path_buf(), opened.clone());
        } else {
            return Err(RecoveryError::EventDigestMismatch);
        }
        Ok(())
    }

    fn create_terminal_path(&self, digest_hex: &str, bytes: &[u8]) -> Result<(), RecoveryError> {
        let path = self.root.join(format!("terminal-{digest_hex}.json"));
        #[cfg(unix)]
        if let JournalMode::ExistingOnly(binding) = &self.mode {
            let root = binding
                .lock()
                .map_err(|error| RecoveryError::Io(error.to_string()))?
                .root
                .clone();
            let name = path
                .file_name()
                .ok_or_else(|| RecoveryError::UnsafePath(path.clone()))?;
            let opened = create_existing_terminal_file(&root, &self.root, name, bytes)?;
            let mut binding = binding
                .lock()
                .map_err(|error| RecoveryError::Io(error.to_string()))?;
            if binding
                .terminal_files
                .insert(path.clone(), opened)
                .is_some()
            {
                return Err(RecoveryError::EventDigestMismatch);
            }
            return Ok(());
        }
        durable_create_new(&path, bytes)
    }

    fn append_execution_event(&self, kind: JournalEventKind) -> Result<(), RecoveryError> {
        let mut events = self.load_execution_events()?;
        let event = JournalEvent {
            schema_version: "ao.next.journal-event.v1".into(),
            sequence: events.len() as u64,
            kind,
        };
        events.push(event.clone());
        validate_journal_lifecycle(&events)?;
        let bytes = canonical_json_bytes(&event)?;
        if bytes.len() as u64 > self.maximum_bytes {
            return Err(RecoveryError::Oversized {
                actual: bytes.len() as u64,
                limit: self.maximum_bytes,
            });
        }
        let directory = self.execution_event_directory()?;
        let digest = digest_bytes(&bytes);
        let digest_hex = digest
            .as_str()
            .strip_prefix("sha256:")
            .ok_or(RecoveryError::EventDigestMismatch)?;
        let path = directory.join(format!("{:020}-{digest_hex}.json", event.sequence));
        let opened = self.create_event_path(&path, &bytes)?;
        self.register_event_path(&path, opened)
    }

    fn load_execution_events(&self) -> Result<Vec<JournalEvent>, RecoveryError> {
        let directory = self.execution_event_directory()?;
        self.load_execution_events_from(&directory)
    }

    fn load_execution_events_from(
        &self,
        directory: &Path,
    ) -> Result<Vec<JournalEvent>, RecoveryError> {
        let mut paths = self.execution_event_paths(directory)?;
        paths.sort();
        let observed_paths = paths.iter().cloned().collect::<BTreeSet<_>>();
        let mut events = Vec::with_capacity(paths.len());
        let mut total_bytes = 0_u64;
        for (expected_sequence, path) in paths.into_iter().enumerate() {
            let name = path
                .file_name()
                .and_then(|value| value.to_str())
                .ok_or_else(|| RecoveryError::UnsafePath(path.clone()))?;
            let name_bytes = name.as_bytes();
            let expected_prefix = format!("{expected_sequence:020}-");
            if name_bytes.len() != 90
                || !name_bytes.starts_with(expected_prefix.as_bytes())
                || &name_bytes[85..] != b".json"
            {
                return Err(RecoveryError::EventSequenceInvalid);
            }
            let opened = self.open_execution_event_path(directory, &path)?;
            self.validate_event_path(&path, &opened)?;
            let metadata = opened.anchor.metadata()?;
            total_bytes = total_bytes.saturating_add(metadata.len());
            if total_bytes > self.maximum_bytes {
                return Err(RecoveryError::Oversized {
                    actual: total_bytes,
                    limit: self.maximum_bytes,
                });
            }
            let bytes = read_opened_regular(&opened, self.maximum_bytes)?;
            let observed_digest = digest_bytes(&bytes);
            let observed_hex = observed_digest
                .as_str()
                .strip_prefix("sha256:")
                .ok_or(RecoveryError::EventDigestMismatch)?;
            if &name_bytes[21..85] != observed_hex.as_bytes() {
                return Err(RecoveryError::EventDigestMismatch);
            }
            let maximum_bytes = usize::try_from(self.maximum_bytes).unwrap_or(usize::MAX);
            let event: JournalEvent = decode_strict_json(&bytes, maximum_bytes)?;
            if event.schema_version != "ao.next.journal-event.v1"
                || event.sequence != expected_sequence as u64
            {
                return Err(RecoveryError::EventSequenceInvalid);
            }
            if canonical_json_bytes(&event)? != bytes {
                return Err(RecoveryError::EventDigestMismatch);
            }
            events.push(event);
        }
        if let JournalMode::ExistingOnly(binding) = &self.mode {
            let binding = binding
                .lock()
                .map_err(|error| RecoveryError::Io(error.to_string()))?;
            if binding.event_files.keys().cloned().collect::<BTreeSet<_>>() != observed_paths {
                return Err(RecoveryError::EventSequenceInvalid);
            }
        }
        validate_journal_lifecycle(&events)?;
        Ok(events)
    }

    #[cfg_attr(not(unix), allow(clippy::unused_self))]
    fn execution_event_paths(&self, directory: &Path) -> Result<Vec<PathBuf>, RecoveryError> {
        #[cfg(unix)]
        if let JournalMode::ExistingOnly(binding) = &self.mode {
            let (events, descriptor_reads) = {
                let binding = binding
                    .lock()
                    .map_err(|error| RecoveryError::Io(error.to_string()))?;
                (binding.events.clone(), binding.descriptor_reads)
            };
            if !descriptor_reads {
                require_same_journal_path(directory, true, &events)?;
            }
            let mut entries =
                rustix::fs::Dir::read_from(events.anchor.as_ref()).map_err(std::io::Error::from)?;
            let mut paths = Vec::new();
            for entry in &mut entries {
                let entry = entry.map_err(std::io::Error::from)?;
                let name = entry.file_name().to_bytes();
                if name != b"." && name != b".." {
                    paths.push(directory.join(OsStr::from_bytes(name)));
                }
            }
            if !descriptor_reads {
                require_same_journal_path(directory, true, &events)?;
            }
            return Ok(paths);
        }

        Ok(std::fs::read_dir(directory)?
            .map(|entry| entry.map(|value| value.path()))
            .collect::<Result<Vec<_>, _>>()?)
    }

    #[cfg_attr(not(unix), allow(clippy::unused_self))]
    fn open_execution_event_path(
        &self,
        #[allow(unused_variables)] directory: &Path,
        path: &Path,
    ) -> Result<OpenedJournalPath, RecoveryError> {
        #[cfg(unix)]
        if let JournalMode::ExistingOnly(binding) = &self.mode {
            let (events, descriptor_reads) = {
                let binding = binding
                    .lock()
                    .map_err(|error| RecoveryError::Io(error.to_string()))?;
                (binding.events.clone(), binding.descriptor_reads)
            };
            let name = path
                .file_name()
                .ok_or_else(|| RecoveryError::UnsafePath(path.to_path_buf()))?;
            return open_existing_journal_entry(&events, directory, name, false, !descriptor_reads);
        }

        open_journal_path(path, false)
    }

    fn execution_event_directory(&self) -> Result<PathBuf, RecoveryError> {
        let directory = self.root.join("execution-events");
        if matches!(self.mode, JournalMode::CreateCapable) {
            std::fs::create_dir_all(&directory)?;
        }
        self.existing_execution_event_directory()
    }

    fn existing_execution_event_directory(&self) -> Result<PathBuf, RecoveryError> {
        let directory = self.root.join("execution-events");
        if let JournalMode::ExistingOnly(binding) = &self.mode {
            let binding = binding
                .lock()
                .map_err(|error| RecoveryError::Io(error.to_string()))?;
            #[cfg(unix)]
            if binding.descriptor_reads {
                return Ok(directory);
            }
            let expected = binding.events.clone();
            drop(binding);
            let opened = open_journal_path(&directory, true)?;
            if opened.fingerprint != expected.fingerprint {
                return Err(RecoveryError::IdentityMismatch);
            }
        } else {
            open_journal_path(&directory, true)?;
        }
        Ok(directory)
    }

    fn validate_event_path(
        &self,
        path: &Path,
        opened: &OpenedJournalPath,
    ) -> Result<(), RecoveryError> {
        let JournalMode::ExistingOnly(binding) = &self.mode else {
            return Ok(());
        };
        let mut binding = binding
            .lock()
            .map_err(|error| RecoveryError::Io(error.to_string()))?;
        if let Some(expected) = binding.event_files.get(path) {
            if opened.fingerprint != expected.fingerprint {
                return Err(RecoveryError::IdentityMismatch);
            }
        } else if binding.initializing {
            binding
                .event_files
                .insert(path.to_path_buf(), opened.clone());
        } else {
            return Err(RecoveryError::EventSequenceInvalid);
        }
        Ok(())
    }

    fn create_event_path(
        &self,
        path: &Path,
        bytes: &[u8],
    ) -> Result<OpenedJournalPath, RecoveryError> {
        if let JournalMode::ExistingOnly(binding) = &self.mode {
            let events = binding
                .lock()
                .map_err(|error| RecoveryError::Io(error.to_string()))?
                .events
                .clone();
            let name = path
                .file_name()
                .ok_or_else(|| RecoveryError::UnsafePath(path.to_path_buf()))?;
            return create_existing_event_file(
                &events,
                path.parent()
                    .ok_or_else(|| RecoveryError::UnsafePath(path.to_path_buf()))?,
                name,
                bytes,
            );
        }
        durable_create_new(path, bytes)?;
        open_journal_path(path, false)
    }

    fn register_event_path(
        &self,
        path: &Path,
        opened: OpenedJournalPath,
    ) -> Result<(), RecoveryError> {
        let JournalMode::ExistingOnly(binding) = &self.mode else {
            return Ok(());
        };
        let mut binding = binding
            .lock()
            .map_err(|error| RecoveryError::Io(error.to_string()))?;
        if binding
            .event_files
            .insert(path.to_path_buf(), opened)
            .is_some()
        {
            return Err(RecoveryError::EventSequenceInvalid);
        }
        Ok(())
    }

    /// Commits a checkpoint only after its effect and verifier events are durable.
    ///
    /// # Errors
    ///
    /// Returns [`RecoveryError`] for event digest drift, missing committed effect
    /// or verifier events, invalid sequence/schema, size violations, or I/O failure.
    pub fn commit(&self, checkpoint: &Checkpoint, event_log: &Path) -> Result<(), RecoveryError> {
        if checkpoint.schema_version != "ao.next.checkpoint.v1" {
            return Err(RecoveryError::InvalidSchema);
        }
        let event_bytes = read_journal_regular(event_log, self.maximum_bytes)?;
        if digest_bytes(&event_bytes) != checkpoint.events_digest {
            return Err(RecoveryError::EventDigestMismatch);
        }
        let events = decode_event_log(&event_bytes, self.maximum_bytes)?;
        let lifecycle = validate_journal_lifecycle(&events)?;
        require_provider_ready(&lifecycle.provider)?;
        let mut committed = lifecycle.legacy_committed_effects;
        committed.extend(
            lifecycle
                .effects
                .iter()
                .filter_map(|(effect_id, (_, completion))| {
                    completion.as_ref().map(|_| effect_id.clone())
                }),
        );
        for (expected_sequence, event) in events.iter().enumerate() {
            if event.sequence != expected_sequence as u64
                || event.schema_version != "ao.next.journal-event.v1"
            {
                return Err(RecoveryError::EventSequenceInvalid);
            }
        }
        for effect_id in &checkpoint.committed_effects {
            if !committed.contains(effect_id) {
                return Err(RecoveryError::CommittedEffectMissing(effect_id.clone()));
            }
        }
        if lifecycle.verifier_records == 0 || lifecycle.verification_open {
            return Err(RecoveryError::VerifierEventMissing);
        }
        if checkpoint.sequence != events.len() as u64 {
            return Err(RecoveryError::EventSequenceInvalid);
        }

        let bytes = canonical_json_bytes(checkpoint)?;
        if bytes.len() as u64 > self.maximum_bytes {
            return Err(RecoveryError::Oversized {
                actual: bytes.len() as u64,
                limit: self.maximum_bytes,
            });
        }
        let digest = digest_bytes(&bytes);
        durable_replace(&self.root.join("checkpoint.json"), &bytes)?;
        durable_replace(
            &self.root.join("checkpoint.sha256"),
            digest.as_str().as_bytes(),
        )?;
        Ok(())
    }

    /// Loads a checkpoint and returns only effects not already committed.
    ///
    /// # Errors
    ///
    /// Returns [`RecoveryError`] for digest or identity drift, unsafe/oversized
    /// checkpoint artifacts, strict JSON failure, or invalid schema.
    pub fn resume(
        &self,
        identity: &CheckpointIdentity,
        pending_effects: &[String],
    ) -> Result<ResumePlan, RecoveryError> {
        let checkpoint = self.load()?;
        if &checkpoint.identity != identity {
            return Err(RecoveryError::IdentityMismatch);
        }
        let mut skipped = Vec::new();
        let mut remaining = Vec::new();
        for effect_id in pending_effects {
            if checkpoint.committed_effects.contains(effect_id) {
                skipped.push(effect_id.clone());
            } else {
                remaining.push(effect_id.clone());
            }
        }
        Ok(ResumePlan {
            skipped_committed_effects: skipped,
            remaining_effects: remaining,
        })
    }

    fn load(&self) -> Result<Checkpoint, RecoveryError> {
        let digest_bytes_raw = read_journal_regular(&self.root.join("checkpoint.sha256"), 128)?;
        let expected = Digest::new(
            std::str::from_utf8(&digest_bytes_raw)
                .map_err(|error| RecoveryError::Io(error.to_string()))?
                .trim(),
        )
        .map_err(|error| RecoveryError::Io(error.to_string()))?;
        let bytes = read_journal_regular(&self.root.join("checkpoint.json"), self.maximum_bytes)?;
        let observed = digest_bytes(&bytes);
        if observed != expected {
            return Err(RecoveryError::CheckpointDigestMismatch { expected, observed });
        }
        let maximum_bytes = usize::try_from(self.maximum_bytes).unwrap_or(usize::MAX);
        let checkpoint: Checkpoint = decode_strict_json(&bytes, maximum_bytes)?;
        if checkpoint.schema_version != "ao.next.checkpoint.v1" {
            return Err(RecoveryError::InvalidSchema);
        }
        Ok(checkpoint)
    }
}

/// Writes an ordered canonical JSONL event log and returns its exact digest.
///
/// # Errors
///
/// Returns [`RecoveryError`] when the encoded log exceeds `maximum_bytes`, its
/// parent is unavailable, or durable writing fails.
pub fn write_durable_event_log(
    path: &Path,
    events: &[JournalEvent],
    maximum_bytes: u64,
) -> Result<Digest, RecoveryError> {
    let mut bytes = Vec::new();
    for event in events {
        bytes.extend_from_slice(&canonical_json_bytes(event)?);
        bytes.push(b'\n');
    }
    if bytes.len() as u64 > maximum_bytes {
        return Err(RecoveryError::Oversized {
            actual: bytes.len() as u64,
            limit: maximum_bytes,
        });
    }
    durable_replace(path, &bytes)?;
    Ok(digest_bytes(&bytes))
}

fn decode_event_log(bytes: &[u8], maximum_bytes: u64) -> Result<Vec<JournalEvent>, RecoveryError> {
    let mut events = Vec::new();
    for line in bytes.split(|byte| *byte == b'\n') {
        if line.is_empty() {
            continue;
        }
        let maximum_bytes = usize::try_from(maximum_bytes).unwrap_or(usize::MAX);
        events.push(decode_strict_json(line, maximum_bytes)?);
    }
    Ok(events)
}

fn require_same_journal_path(
    path: &Path,
    directory: bool,
    expected: &OpenedJournalPath,
) -> Result<OpenedJournalPath, RecoveryError> {
    let opened = open_journal_path(path, directory)?;
    if opened.fingerprint != expected.fingerprint {
        return Err(RecoveryError::IdentityMismatch);
    }
    Ok(opened)
}

fn validate_identity_bytes(
    opened: &OpenedJournalPath,
    identity: &CheckpointIdentity,
    expected: &[u8],
    maximum_bytes: u64,
) -> Result<(), RecoveryError> {
    let existing = read_opened_regular(opened, maximum_bytes)?;
    if existing != expected {
        return Err(RecoveryError::IdentityMismatch);
    }
    let maximum_bytes = usize::try_from(maximum_bytes).unwrap_or(usize::MAX);
    let recorded: CheckpointIdentity = decode_strict_json(&existing, maximum_bytes)?;
    if &recorded != identity {
        return Err(RecoveryError::IdentityMismatch);
    }
    Ok(())
}

fn read_opened_regular(
    opened: &OpenedJournalPath,
    maximum_bytes: u64,
) -> Result<Vec<u8>, RecoveryError> {
    let metadata = opened.anchor.metadata()?;
    if metadata.len() > maximum_bytes {
        return Err(RecoveryError::Oversized {
            actual: metadata.len(),
            limit: maximum_bytes,
        });
    }
    let mut file = opened.anchor.try_clone()?;
    file.seek(SeekFrom::Start(0))?;
    let mut bytes = Vec::new();
    file.take(maximum_bytes.saturating_add(1))
        .read_to_end(&mut bytes)?;
    if bytes.len() as u64 > maximum_bytes {
        return Err(RecoveryError::Oversized {
            actual: bytes.len() as u64,
            limit: maximum_bytes,
        });
    }
    Ok(bytes)
}

fn read_journal_regular(path: &Path, maximum_bytes: u64) -> Result<Vec<u8>, RecoveryError> {
    read_opened_regular(&open_journal_path(path, false)?, maximum_bytes)
}

#[cfg(all(test, unix))]
std::thread_local! {
    static BEFORE_EXISTING_APPEND: std::cell::RefCell<Option<Box<dyn FnOnce()>>> =
        std::cell::RefCell::new(None);
    static BEFORE_EXISTING_TERMINAL_CREATE: std::cell::RefCell<Option<Box<dyn FnOnce()>>> =
        std::cell::RefCell::new(None);
}

#[cfg(all(test, unix))]
fn set_existing_append_test_hook(hook: Box<dyn FnOnce()>) {
    BEFORE_EXISTING_APPEND.with(|slot| {
        assert!(slot.borrow_mut().replace(hook).is_none());
    });
}

#[cfg(all(test, unix))]
fn run_existing_append_test_hook() {
    BEFORE_EXISTING_APPEND.with(|slot| {
        if let Some(hook) = slot.borrow_mut().take() {
            hook();
        }
    });
}

#[cfg(all(test, unix))]
fn set_existing_terminal_create_test_hook(hook: Box<dyn FnOnce()>) {
    BEFORE_EXISTING_TERMINAL_CREATE.with(|slot| {
        assert!(slot.borrow_mut().replace(hook).is_none());
    });
}

#[cfg(all(test, unix))]
fn run_existing_terminal_create_test_hook() {
    BEFORE_EXISTING_TERMINAL_CREATE.with(|slot| {
        if let Some(hook) = slot.borrow_mut().take() {
            hook();
        }
    });
}

#[cfg(all(not(test), unix))]
const fn run_existing_terminal_create_test_hook() {}

#[cfg(all(not(test), unix))]
const fn run_existing_append_test_hook() {}

#[cfg(unix)]
fn open_existing_journal_entry(
    retained: &OpenedJournalPath,
    public_root: &Path,
    name: &OsStr,
    directory: bool,
    verify_public_path: bool,
) -> Result<OpenedJournalPath, RecoveryError> {
    if verify_public_path {
        require_same_journal_path(public_root, true, retained)?;
    }
    let mut flags = OFlags::RDONLY | OFlags::NOFOLLOW | OFlags::CLOEXEC;
    if directory {
        flags |= OFlags::DIRECTORY;
    }
    let file = rustix::fs::openat(retained.anchor.as_ref(), name, flags, Mode::empty())
        .map(File::from)
        .map_err(std::io::Error::from)?;
    let opened = opened_unix_journal_path(file, &public_root.join(name), directory)?;
    if verify_public_path {
        require_same_journal_path(public_root, true, retained)?;
    }
    Ok(opened)
}

#[cfg(unix)]
fn open_existing_terminal_file(
    retained: &OpenedJournalPath,
    public_root: &Path,
    name: &OsStr,
    verify_public_path: bool,
) -> Result<OpenedJournalPath, RecoveryError> {
    open_existing_journal_entry(retained, public_root, name, false, verify_public_path)
}

#[cfg(unix)]
fn create_existing_terminal_file(
    retained: &OpenedJournalPath,
    public_root: &Path,
    name: &OsStr,
    bytes: &[u8],
) -> Result<OpenedJournalPath, RecoveryError> {
    require_same_journal_path(public_root, true, retained)?;
    run_existing_terminal_create_test_hook();
    let mut file = rustix::fs::openat(
        retained.anchor.as_ref(),
        name,
        OFlags::WRONLY | OFlags::CREATE | OFlags::EXCL | OFlags::NOFOLLOW | OFlags::CLOEXEC,
        Mode::from_raw_mode(0o600),
    )
    .map(File::from)
    .map_err(std::io::Error::from)?;
    file.write_all(bytes)?;
    file.sync_all()?;
    let opened = opened_unix_journal_path(file, &public_root.join(name), false)?;
    require_same_journal_path(public_root, true, retained)?;
    Ok(opened)
}

#[cfg(unix)]
fn create_existing_event_file(
    retained: &OpenedJournalPath,
    public_directory: &Path,
    name: &OsStr,
    bytes: &[u8],
) -> Result<OpenedJournalPath, RecoveryError> {
    require_same_journal_path(public_directory, true, retained)?;
    run_existing_append_test_hook();
    let mut file = rustix::fs::openat(
        retained.anchor.as_ref(),
        name,
        OFlags::WRONLY | OFlags::CREATE | OFlags::EXCL | OFlags::NOFOLLOW | OFlags::CLOEXEC,
        Mode::from_raw_mode(0o600),
    )
    .map(File::from)
    .map_err(std::io::Error::from)?;
    file.write_all(bytes)?;
    file.sync_all()?;
    let opened = opened_unix_journal_path(file, &public_directory.join(name), false)?;
    require_same_journal_path(public_directory, true, retained)?;
    Ok(opened)
}

#[cfg(not(unix))]
fn create_existing_event_file(
    retained: &OpenedJournalPath,
    public_directory: &Path,
    name: &std::ffi::OsStr,
    bytes: &[u8],
) -> Result<OpenedJournalPath, RecoveryError> {
    require_same_journal_path(public_directory, true, retained)?;
    let path = public_directory.join(name);
    durable_create_new(&path, bytes)?;
    let opened = open_journal_path(&path, false)?;
    require_same_journal_path(public_directory, true, retained)?;
    Ok(opened)
}

#[cfg(unix)]
fn open_journal_path(path: &Path, directory: bool) -> Result<OpenedJournalPath, RecoveryError> {
    let mut flags = OFlags::RDONLY | OFlags::CLOEXEC | OFlags::NOFOLLOW;
    if directory {
        flags |= OFlags::DIRECTORY;
    }
    let file = rustix::fs::open(path, flags, Mode::empty())
        .map(File::from)
        .map_err(std::io::Error::from)?;
    opened_unix_journal_path(file, path, directory)
}

#[cfg(unix)]
fn opened_unix_journal_path(
    file: File,
    path: &Path,
    directory: bool,
) -> Result<OpenedJournalPath, RecoveryError> {
    let metadata = file.metadata()?;
    if metadata.file_type().is_symlink()
        || (directory && !metadata.is_dir())
        || (!directory && !metadata.is_file())
    {
        return Err(RecoveryError::UnsafePath(path.to_path_buf()));
    }
    Ok(OpenedJournalPath {
        fingerprint: JournalPathFingerprint {
            device: metadata.dev(),
            inode: metadata.ino(),
        },
        anchor: Arc::new(file),
    })
}

#[cfg(windows)]
fn open_journal_path(path: &Path, directory: bool) -> Result<OpenedJournalPath, RecoveryError> {
    let flags = FILE_FLAG_OPEN_REPARSE_POINT
        | if directory {
            FILE_FLAG_BACKUP_SEMANTICS
        } else {
            0
        };
    let file = OpenOptions::new()
        .read(true)
        .share_mode(FILE_SHARE_READ | FILE_SHARE_WRITE)
        .custom_flags(flags)
        .open(path)?;
    let metadata = file.metadata()?;
    if metadata.file_type().is_symlink()
        || windows_reparse_point(metadata.file_attributes())
        || (directory && !metadata.is_dir())
        || (!directory && !metadata.is_file())
    {
        return Err(RecoveryError::UnsafePath(path.to_path_buf()));
    }
    let handle = same_file::Handle::from_file(file.try_clone()?)?;
    Ok(OpenedJournalPath {
        fingerprint: JournalPathFingerprint {
            handle: Arc::new(handle),
        },
        anchor: Arc::new(file),
    })
}

#[cfg(not(any(unix, windows)))]
fn open_journal_path(path: &Path, directory: bool) -> Result<OpenedJournalPath, RecoveryError> {
    let metadata = std::fs::symlink_metadata(path)?;
    if metadata.file_type().is_symlink()
        || (directory && !metadata.is_dir())
        || (!directory && !metadata.is_file())
    {
        return Err(RecoveryError::UnsafePath(path.to_path_buf()));
    }
    Ok(OpenedJournalPath {
        fingerprint: JournalPathFingerprint {
            length: metadata.len(),
        },
        anchor: Arc::new(OpenOptions::new().read(true).open(path)?),
    })
}

fn durable_replace(path: &Path, bytes: &[u8]) -> Result<(), RecoveryError> {
    let file_name = path
        .file_name()
        .and_then(|value| value.to_str())
        .ok_or_else(|| RecoveryError::UnsafePath(path.to_path_buf()))?;
    let temporary = path.with_file_name(format!(".{file_name}.tmp"));
    {
        let mut file = File::create(&temporary)?;
        file.write_all(bytes)?;
        file.sync_all()?;
    }
    std::fs::rename(temporary, path)?;
    Ok(())
}

fn durable_create_new(path: &Path, bytes: &[u8]) -> Result<(), RecoveryError> {
    let mut file = OpenOptions::new().write(true).create_new(true).open(path)?;
    file.write_all(bytes)?;
    file.sync_all()?;
    Ok(())
}

#[cfg(test)]
mod reparse_tests {
    #[cfg(unix)]
    fn test_request(root: &std::path::Path) -> crate::contracts::RunRequest {
        use std::collections::BTreeSet;

        use crate::contracts::{
            AuthorityEnvelope, Capability, Digest, ExternalEffectPolicy, ModelProfile,
            NetworkPolicy, RunLimits, SourceIdentity, StructuredCommand, VerifierProfile,
            WorkspaceIdentity,
        };

        let zero = Digest::new(format!("sha256:{}", "0".repeat(64))).expect("zero digest");
        let one = Digest::new(format!("sha256:{}", "1".repeat(64))).expect("one digest");
        let verifier = StructuredCommand {
            program: "/usr/bin/true".into(),
            args: Vec::new(),
            timeout_ms: 1_000,
        };
        crate::contracts::RunRequest {
            schema_version: "ao.next.run-request.v1".into(),
            run_id: "terminal-root-swap".into(),
            objective: "reject a swapped terminal root".into(),
            source: SourceIdentity {
                repository: "fixture".into(),
                head: zero.clone(),
            },
            workspace: WorkspaceIdentity {
                workspace_id: "workspace-terminal-root-swap".into(),
                root: root.to_path_buf(),
                seed_digest: one.clone(),
            },
            model_profile: ModelProfile {
                runtime: "scripted".into(),
                model_identifier: "fixture-model".into(),
                reasoning_effort: "high".into(),
                system_prompt_digest: zero.clone(),
                tool_contract_digest: one.clone(),
                context_limit: 32_000,
                output_limit: 4_000,
                adapter_version: "scripted-v1".into(),
            },
            authority: AuthorityEnvelope {
                schema_version: "ao.next.authority-envelope.v1".into(),
                issued_by: "operator".into(),
                issued_at: "2026-08-05T00:00:00Z".parse().expect("issued at"),
                expires_at: "2026-08-06T00:00:00Z".parse().expect("expires at"),
                capabilities: BTreeSet::from([Capability::RunLocalProgram]),
                allowed_roots: vec![root.to_path_buf()],
                allowed_programs: BTreeSet::from([verifier.program.clone()]),
                network: NetworkPolicy::Denied,
                allowed_network_hosts: BTreeSet::new(),
                external_effects: ExternalEffectPolicy::Denied,
            },
            verifier_profile: VerifierProfile {
                profile_id: "complete-local".into(),
                profile_digest: one,
                commands: vec![verifier],
                required_artifacts: Vec::new(),
            },
            policy_digest: zero,
            limits: RunLimits {
                max_input_bytes: 64 * 1024,
                max_turns: 4,
                max_repair_attempts: 1,
                max_run_ms: 10_000,
                max_effect_timeout_ms: 1_000,
                max_output_bytes: 4_096,
                max_tokens: 1_000,
            },
        }
    }

    #[test]
    fn windows_reparse_attribute_is_unsafe() {
        assert!(super::windows_reparse_point(0x400));
        assert!(!super::windows_reparse_point(0));
    }

    #[cfg(unix)]
    #[test]
    fn existing_only_append_uses_retained_directory_handle() {
        use std::ffi::OsStr;
        use std::os::unix::fs::PermissionsExt as _;

        let root = tempfile::TempDir::new().expect("root");
        let events = root.path().join("execution-events");
        std::fs::create_dir(&events).expect("events");
        let retained = super::open_journal_path(&events, true).expect("retained events");
        let name = OsStr::new(
            "00000000000000000000-aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa.json",
        );

        let opened = super::create_existing_event_file(&retained, &events, name, b"event")
            .expect("anchored append");

        let path = events.join(name);
        assert_eq!(std::fs::read(&path).expect("event bytes"), b"event");
        assert_eq!(
            std::fs::metadata(&path)
                .expect("event metadata")
                .permissions()
                .mode()
                & 0o777,
            0o600
        );
        assert_eq!(
            opened.fingerprint,
            super::open_journal_path(&path, false)
                .expect("event path")
                .fingerprint
        );
    }

    #[cfg(unix)]
    #[test]
    fn existing_only_append_fails_when_public_directory_is_replaced_before_open() {
        use std::ffi::OsStr;

        let root = tempfile::TempDir::new().expect("root");
        let events = root.path().join("execution-events");
        let original = root.path().join("retained-events");
        std::fs::create_dir(&events).expect("events");
        let retained = super::open_journal_path(&events, true).expect("retained events");
        let name = OsStr::new(
            "00000000000000000000-bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb.json",
        );
        let hook_events = events.clone();
        let hook_original = original.clone();
        super::set_existing_append_test_hook(Box::new(move || {
            std::fs::rename(&hook_events, &hook_original).expect("move retained directory");
            std::fs::create_dir(&hook_events).expect("replacement directory");
        }));

        assert!(
            super::create_existing_event_file(&retained, &events, name, b"event").is_err(),
            "replaced public locator authorized append"
        );
        assert_eq!(
            std::fs::read(original.join(name)).expect("retained event bytes"),
            b"event"
        );
        assert!(
            !events.join(name).exists(),
            "event was written through substitute directory"
        );
    }

    #[cfg(unix)]
    #[test]
    fn existing_only_terminal_creation_rejects_journal_root_swap() {
        let recovery = tempfile::TempDir::new().expect("recovery");
        let root = recovery.path().join("journal");
        let original = recovery.path().join("retained-journal");
        let substitute = tempfile::TempDir::new().expect("substitute");
        let request = test_request(recovery.path());
        let journal = super::CheckpointJournal::new(&root, 16 * 1024).expect("journal");
        journal
            .begin_verification(&request)
            .expect("verification start");
        journal
            .record_verifier(&request, &crate::evidence::digest_bytes(b"report"))
            .expect("verifier record");
        let journal = super::CheckpointJournal::open_bound(&root, 16 * 1024, &request)
            .expect("bound journal");
        let events_before = std::fs::read_dir(root.join("execution-events"))
            .expect("execution events")
            .count();
        let hook_root = root.clone();
        let hook_original = original.clone();
        let hook_substitute = substitute.path().to_path_buf();
        super::set_existing_terminal_create_test_hook(Box::new(move || {
            std::fs::rename(&hook_root, &hook_original).expect("move retained root");
            std::fs::rename(&hook_substitute, &hook_root).expect("install substitute root");
        }));

        assert!(
            journal
                .publish_terminal_record(&request, br#"{"terminal":"passed"}"#)
                .is_err(),
            "swapped journal root authorized terminal publication"
        );
        std::fs::rename(&root, substitute.path()).expect("remove substitute root");
        std::fs::rename(&original, &root).expect("restore retained root");
        assert_eq!(
            std::fs::read_dir(root.join("execution-events"))
                .expect("execution events")
                .count(),
            events_before,
            "terminal event was appended after root replacement"
        );
        assert!(
            std::fs::read_dir(substitute.path())
                .expect("substitute root")
                .all(|entry| !entry
                    .expect("substitute entry")
                    .file_name()
                    .to_string_lossy()
                    .starts_with("terminal-")),
            "terminal bytes were created in the substitute root"
        );
    }
}
