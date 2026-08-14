use std::ffi::{OsStr, OsString};
use std::fs::File;
#[cfg(windows)]
use std::fs::OpenOptions;
use std::io::{Read, Seek, Write};
use std::path::{Path, PathBuf};
use std::sync::Arc;

#[cfg(unix)]
use rustix::fs::{AtFlags, FileType, Mode, OFlags, RenameFlags};
#[cfg(unix)]
use rustix::io::Errno;
#[cfg(windows)]
use std::os::windows::fs::{MetadataExt as _, OpenOptionsExt as _};
use thiserror::Error;

use crate::contracts::{AuthorityEnvelope, Digest, EffectKind, EffectRequest};
use crate::evidence::digest_bytes;
use crate::policy::{PolicyDenial, resolve_effect_paths, validate_effect_request};

const NONEXISTENT_FILE_BINDING: &[u8] = b"ao.next.file-does-not-exist.v1";

#[derive(Clone, Debug)]
pub struct AuthorizedEffect {
    request: EffectRequest,
    authority: AuthorityEnvelope,
    target: DescriptorTarget,
}

#[derive(Clone, Debug)]
enum DescriptorTarget {
    Read {
        root: Arc<File>,
        root_path: PathBuf,
        anchors: Vec<Arc<File>>,
        file: Arc<File>,
        relative: PathBuf,
    },
    Write {
        root: Arc<File>,
        root_path: PathBuf,
        anchors: Vec<Arc<File>>,
        parent: Arc<File>,
        parent_path: PathBuf,
        parent_relative: PathBuf,
        name: OsString,
    },
}

impl AuthorizedEffect {
    #[must_use]
    pub fn request(&self) -> &EffectRequest {
        &self.request
    }
}

#[derive(Clone, Debug, Eq, PartialEq)]
pub struct EffectOutput {
    pub status: i32,
    pub stdout: Vec<u8>,
    pub stderr: Vec<u8>,
}

#[derive(Debug, Error)]
pub enum EffectBrokerError {
    #[error("effect denied: {0}")]
    Denied(#[from] PolicyDenial),
    #[error("effect I/O failed: {0}")]
    Io(#[from] std::io::Error),
    #[error("effect input exceeded {limit} bytes")]
    InputTooLarge { limit: usize },
    #[error("effect output exceeded {limit} bytes")]
    OutputTooLarge { limit: usize },
    #[error("native file content is not UTF-8")]
    InvalidUtf8,
    #[error("native file preimage does not match the admitted digest")]
    PreimageMismatch,
    #[error("the admitted effect kind has no local executor: {0:?}")]
    UnsupportedEffect(EffectKind),
}

pub trait EffectBroker {
    /// Admits a request only when deterministic policy accepts every binding.
    ///
    /// # Errors
    ///
    /// Returns [`PolicyDenial`] when any capability, path, byte-bound shape, or
    /// external-effect rule fails.
    fn authorize(
        &self,
        request: &EffectRequest,
        authority: &AuthorityEnvelope,
    ) -> Result<AuthorizedEffect, PolicyDenial>;

    /// Executes a request only after the exact authorized value is returned by
    /// [`EffectBroker::authorize`].
    ///
    /// # Errors
    ///
    /// Returns [`EffectBrokerError`] for I/O, byte bounds, UTF-8, preimage, or
    /// an admitted effect kind without a native executor.
    fn execute_authorized(
        &self,
        authorized: &AuthorizedEffect,
    ) -> Result<EffectOutput, EffectBrokerError>;
}

#[derive(Clone, Debug)]
pub struct LocalEffectBroker {
    timeout_ms: u64,
    input_bytes: usize,
    output_bytes: usize,
}

impl LocalEffectBroker {
    #[must_use]
    pub const fn new(
        maximum_timeout_ms: u64,
        maximum_input_bytes: usize,
        maximum_output_bytes: usize,
    ) -> Self {
        Self {
            timeout_ms: maximum_timeout_ms,
            input_bytes: maximum_input_bytes,
            output_bytes: maximum_output_bytes,
        }
    }

    /// Authorizes and executes one bounded native workspace effect.
    ///
    /// # Errors
    ///
    /// Returns [`EffectBrokerError`] when admission or native execution fails.
    pub fn execute(
        &self,
        request: &EffectRequest,
        authority: &AuthorityEnvelope,
    ) -> Result<EffectOutput, EffectBrokerError> {
        let authorized = self.authorize(request, authority)?;
        <Self as EffectBroker>::execute_authorized(self, &authorized)
    }

    fn run_authorized(
        &self,
        authorized: &AuthorizedEffect,
    ) -> Result<EffectOutput, EffectBrokerError> {
        validate_effect_request(&authorized.request, &authorized.authority, self.timeout_ms)?;
        verify_descriptor_binding(&authorized.target)?;
        match (&authorized.request.kind, &authorized.target) {
            (EffectKind::ReadFile, DescriptorTarget::Read { file, .. }) => {
                self.read_file(&authorized.request, file)
            }
            (
                EffectKind::WriteFile,
                DescriptorTarget::Write {
                    parent,
                    parent_path,
                    name,
                    ..
                },
            ) => self.write_file(&authorized.request, parent, parent_path, name),
            _ => Err(EffectBrokerError::UnsupportedEffect(
                authorized.request.kind.clone(),
            )),
        }
    }

    fn read_file(
        &self,
        request: &EffectRequest,
        file: &File,
    ) -> Result<EffectOutput, EffectBrokerError> {
        let bytes = read_descriptor_utf8(file, self.input_bytes)?;
        require_digest(&bytes, &request.input_digest)?;
        Ok(EffectOutput {
            status: 0,
            stdout: bytes,
            stderr: Vec::new(),
        })
    }

    fn write_file(
        &self,
        request: &EffectRequest,
        parent: &File,
        parent_path: &Path,
        name: &OsStr,
    ) -> Result<EffectOutput, EffectBrokerError> {
        let content = request.content.as_deref().unwrap_or_default().as_bytes();
        if content.len() > self.output_bytes {
            return Err(EffectBrokerError::OutputTooLarge {
                limit: self.output_bytes,
            });
        }
        let existing = open_regular_at(parent, parent_path, name)?;
        match &existing {
            Some(file) => require_digest(
                &read_descriptor_utf8(file, self.input_bytes)?,
                &request.input_digest,
            )?,
            None => require_digest(NONEXISTENT_FILE_BINDING, &request.input_digest)?,
        }
        let temporary = temporary_name(name, request);
        let mut temporary_file = create_temporary(parent, parent_path, &temporary)?;
        let result: Result<(), EffectBrokerError> = (|| {
            temporary_file.write_all(content)?;
            temporary_file.sync_all()?;
            drop(temporary_file);
            if let Some(existing) = existing {
                publish_replace(parent, parent_path, name, &temporary, existing)?;
            } else {
                publish_create(parent, parent_path, name, &temporary)?;
            }
            Ok(())
        })();
        if result.is_err() {
            remove_temporary(parent, parent_path, &temporary);
        }
        result?;
        Ok(EffectOutput {
            status: 0,
            stdout: Vec::new(),
            stderr: Vec::new(),
        })
    }
}

impl EffectBroker for LocalEffectBroker {
    fn authorize(
        &self,
        request: &EffectRequest,
        authority: &AuthorityEnvelope,
    ) -> Result<AuthorizedEffect, PolicyDenial> {
        validate_effect_request(request, authority, self.timeout_ms)?;
        let resolved_paths =
            resolve_effect_paths(request, authority, request.kind == EffectKind::ReadFile)?;
        let target = open_descriptor_target(request, authority, &resolved_paths[0])
            .map_err(|_| PolicyDenial::NonRegularFile(request.paths[0].clone()))?;
        Ok(AuthorizedEffect {
            request: request.clone(),
            authority: authority.clone(),
            target,
        })
    }

    fn execute_authorized(
        &self,
        authorized: &AuthorizedEffect,
    ) -> Result<EffectOutput, EffectBrokerError> {
        self.run_authorized(authorized)
    }
}

fn open_descriptor_target(
    request: &EffectRequest,
    authority: &AuthorityEnvelope,
    resolved: &Path,
) -> Result<DescriptorTarget, std::io::Error> {
    let root = authority
        .allowed_roots
        .iter()
        .find(|root| resolved.starts_with(root))
        .ok_or_else(invalid_target)?;
    let relative = request.paths[0].as_path();
    let name = relative
        .file_name()
        .ok_or_else(invalid_target)?
        .to_os_string();
    let root_file = open_root(root)?;
    let root_descriptor = Arc::new(root_file);
    let parent_relative = relative.parent().unwrap_or_else(|| Path::new(""));
    let (parent, anchors) = open_parent_at(&root_descriptor, root, parent_relative)?;
    let parent_path = root.join(parent_relative);
    match request.kind {
        EffectKind::ReadFile => open_regular_at(&parent, &parent_path, &name)?
            .map(|file| DescriptorTarget::Read {
                root: root_descriptor,
                root_path: root.clone(),
                anchors,
                file: Arc::new(file),
                relative: relative.to_path_buf(),
            })
            .ok_or_else(invalid_target),
        EffectKind::WriteFile => Ok(DescriptorTarget::Write {
            root: root_descriptor,
            root_path: root.clone(),
            anchors,
            parent: Arc::new(parent),
            parent_path,
            parent_relative: parent_relative.to_path_buf(),
            name,
        }),
        _ => Err(invalid_target()),
    }
}

fn verify_descriptor_binding(target: &DescriptorTarget) -> Result<(), EffectBrokerError> {
    let (root, root_path) = match target {
        DescriptorTarget::Read {
            root, root_path, ..
        }
        | DescriptorTarget::Write {
            root, root_path, ..
        } => (root, root_path),
    };
    let current_root = open_root(root_path)?;
    if !same_file(root, &current_root) {
        return Err(EffectBrokerError::PreimageMismatch);
    }
    match target {
        DescriptorTarget::Read {
            anchors,
            file,
            relative,
            ..
        } => {
            let _ = anchors;
            let parent_relative = relative.parent().unwrap_or_else(|| Path::new(""));
            let (parent, current_anchors) = open_parent_at(root, root_path, parent_relative)?;
            let _ = current_anchors;
            let current = open_regular_at(
                &parent,
                &root_path.join(parent_relative),
                relative.file_name().ok_or_else(invalid_target)?,
            )?
            .ok_or(EffectBrokerError::PreimageMismatch)?;
            if !same_file(file, &current) {
                return Err(EffectBrokerError::PreimageMismatch);
            }
        }
        DescriptorTarget::Write {
            parent,
            anchors,
            parent_relative,
            ..
        } => {
            let _ = anchors;
            let (current, current_anchors) = open_parent_at(root, root_path, parent_relative)?;
            let _ = current_anchors;
            if !same_file(parent, &current) {
                return Err(EffectBrokerError::PreimageMismatch);
            }
        }
    }
    Ok(())
}

#[cfg(unix)]
fn open_root(path: &Path) -> Result<File, std::io::Error> {
    rustix::fs::open(
        path,
        OFlags::RDONLY | OFlags::DIRECTORY | OFlags::NOFOLLOW | OFlags::CLOEXEC,
        Mode::empty(),
    )
    .map(File::from)
    .map_err(std::io::Error::from)
}

#[cfg(unix)]
fn open_parent_at(
    root: &File,
    _root_path: &Path,
    relative: &Path,
) -> Result<(File, Vec<Arc<File>>), std::io::Error> {
    let mut parent = root.try_clone()?;
    for component in relative.components() {
        let std::path::Component::Normal(segment) = component else {
            return Err(invalid_target());
        };
        let next = rustix::fs::openat(
            &parent,
            segment,
            OFlags::RDONLY | OFlags::DIRECTORY | OFlags::NOFOLLOW | OFlags::CLOEXEC,
            Mode::empty(),
        )
        .map_err(std::io::Error::from)?;
        parent = File::from(next);
    }
    Ok((parent, Vec::new()))
}

#[cfg(unix)]
fn open_regular_at(
    parent: &File,
    _parent_path: &Path,
    name: &OsStr,
) -> Result<Option<File>, std::io::Error> {
    match rustix::fs::openat(
        parent,
        name,
        OFlags::RDONLY | OFlags::NOFOLLOW | OFlags::CLOEXEC,
        Mode::empty(),
    ) {
        Ok(fd) => {
            let file = File::from(fd);
            let stat = rustix::fs::fstat(&file).map_err(std::io::Error::from)?;
            if !FileType::from_raw_mode(stat.st_mode).is_file() {
                return Err(invalid_target());
            }
            Ok(Some(file))
        }
        Err(Errno::NOENT) => Ok(None),
        Err(error) => Err(std::io::Error::from(error)),
    }
}

#[cfg(unix)]
fn create_temporary(
    parent: &File,
    _parent_path: &Path,
    temporary: &OsStr,
) -> Result<File, std::io::Error> {
    rustix::fs::openat(
        parent,
        temporary,
        OFlags::WRONLY | OFlags::CREATE | OFlags::EXCL | OFlags::NOFOLLOW | OFlags::CLOEXEC,
        Mode::from_raw_mode(0o600),
    )
    .map(File::from)
    .map_err(std::io::Error::from)
}

#[cfg(unix)]
fn remove_temporary(parent: &File, _parent_path: &Path, temporary: &OsStr) {
    let _ = rustix::fs::unlinkat(parent, temporary, AtFlags::empty());
}

#[cfg(unix)]
fn publish_create(
    parent: &File,
    _parent_path: &Path,
    name: &OsStr,
    temporary: &OsStr,
) -> Result<(), EffectBrokerError> {
    rustix::fs::renameat_with(parent, temporary, parent, name, RenameFlags::NOREPLACE)
        .map_err(std::io::Error::from)?;
    if let Err(error) = rustix::fs::fsync(parent) {
        let _ = rustix::fs::unlinkat(parent, name, AtFlags::empty());
        let _ = rustix::fs::fsync(parent);
        return Err(std::io::Error::from(error).into());
    }
    Ok(())
}

#[cfg(unix)]
fn publish_replace(
    parent: &File,
    _parent_path: &Path,
    name: &OsStr,
    temporary: &OsStr,
    expected: File,
) -> Result<(), EffectBrokerError> {
    rustix::fs::renameat_with(parent, temporary, parent, name, RenameFlags::EXCHANGE)
        .map_err(std::io::Error::from)?;
    let swapped = open_regular_at(parent, Path::new(""), temporary);
    let identity_matches = swapped
        .as_ref()
        .ok()
        .and_then(Option::as_ref)
        .is_some_and(|observed| same_file(&expected, observed));
    if !identity_matches {
        rollback_exchange(parent, name, temporary);
        return Err(EffectBrokerError::PreimageMismatch);
    }
    if let Err(error) = rustix::fs::unlinkat(parent, temporary, AtFlags::empty()) {
        rollback_exchange(parent, name, temporary);
        return Err(std::io::Error::from(error).into());
    }
    if let Err(error) = rustix::fs::fsync(parent) {
        return Err(std::io::Error::from(error).into());
    }
    drop(expected);
    Ok(())
}

#[cfg(unix)]
fn rollback_exchange(parent: &File, name: &OsStr, temporary: &OsStr) {
    let _ = rustix::fs::renameat_with(parent, temporary, parent, name, RenameFlags::EXCHANGE);
    let _ = rustix::fs::unlinkat(parent, temporary, AtFlags::empty());
    let _ = rustix::fs::fsync(parent);
}

#[cfg(unix)]
fn same_file(expected: &File, observed: &File) -> bool {
    match (rustix::fs::fstat(expected), rustix::fs::fstat(observed)) {
        (Ok(expected), Ok(observed)) => {
            expected.st_dev == observed.st_dev && expected.st_ino == observed.st_ino
        }
        _ => false,
    }
}

#[cfg(windows)]
const FILE_ATTRIBUTE_REPARSE_POINT: u32 = 0x400;
#[cfg(windows)]
const FILE_FLAG_BACKUP_SEMANTICS: u32 = 0x0200_0000;
#[cfg(windows)]
const FILE_FLAG_OPEN_REPARSE_POINT: u32 = 0x0020_0000;
#[cfg(windows)]
const FILE_SHARE_READ: u32 = 0x1;
#[cfg(windows)]
const FILE_SHARE_WRITE: u32 = 0x2;

#[cfg(windows)]
fn open_root(path: &Path) -> Result<File, std::io::Error> {
    open_windows_directory(path)
}

#[cfg(windows)]
fn open_windows_directory(path: &Path) -> Result<File, std::io::Error> {
    let file = OpenOptions::new()
        .read(true)
        .share_mode(FILE_SHARE_READ | FILE_SHARE_WRITE)
        .custom_flags(FILE_FLAG_BACKUP_SEMANTICS | FILE_FLAG_OPEN_REPARSE_POINT)
        .open(path)?;
    let metadata = file.metadata()?;
    if !metadata.is_dir() || metadata.file_attributes() & FILE_ATTRIBUTE_REPARSE_POINT != 0 {
        return Err(invalid_target());
    }
    Ok(file)
}

#[cfg(windows)]
fn open_parent_at(
    root: &File,
    root_path: &Path,
    relative: &Path,
) -> Result<(File, Vec<Arc<File>>), std::io::Error> {
    let mut parent = root.try_clone()?;
    let mut path = root_path.to_path_buf();
    let mut anchors = Vec::new();
    for component in relative.components() {
        let std::path::Component::Normal(segment) = component else {
            return Err(invalid_target());
        };
        path.push(segment);
        let next = open_windows_directory(&path)?;
        anchors.push(Arc::new(next.try_clone()?));
        parent = next;
    }
    Ok((parent, anchors))
}

#[cfg(windows)]
fn open_regular_at(
    _parent: &File,
    parent_path: &Path,
    name: &OsStr,
) -> Result<Option<File>, std::io::Error> {
    let path = parent_path.join(name);
    let file = match OpenOptions::new()
        .read(true)
        .share_mode(FILE_SHARE_READ | FILE_SHARE_WRITE)
        .custom_flags(FILE_FLAG_OPEN_REPARSE_POINT)
        .open(path)
    {
        Ok(file) => file,
        Err(error) if error.kind() == std::io::ErrorKind::NotFound => return Ok(None),
        Err(error) => return Err(error),
    };
    let metadata = file.metadata()?;
    if !metadata.is_file() || metadata.file_attributes() & FILE_ATTRIBUTE_REPARSE_POINT != 0 {
        return Err(invalid_target());
    }
    Ok(Some(file))
}

#[cfg(windows)]
fn create_temporary(
    _parent: &File,
    parent_path: &Path,
    temporary: &OsStr,
) -> Result<File, std::io::Error> {
    OpenOptions::new()
        .write(true)
        .create_new(true)
        .share_mode(FILE_SHARE_READ)
        .custom_flags(FILE_FLAG_OPEN_REPARSE_POINT)
        .open(parent_path.join(temporary))
}

#[cfg(windows)]
fn remove_temporary(_parent: &File, parent_path: &Path, temporary: &OsStr) {
    let _ = std::fs::remove_file(parent_path.join(temporary));
}

#[cfg(windows)]
fn publish_create(
    _parent: &File,
    parent_path: &Path,
    name: &OsStr,
    temporary: &OsStr,
) -> Result<(), EffectBrokerError> {
    let temporary_path = parent_path.join(temporary);
    let target_path = parent_path.join(name);
    std::fs::hard_link(&temporary_path, &target_path)?;
    std::fs::remove_file(temporary_path)?;
    if let Err(error) = sync_windows_file(&target_path) {
        let _ = std::fs::remove_file(&target_path);
        return Err(error);
    }
    Ok(())
}

#[cfg(windows)]
fn publish_replace(
    parent: &File,
    parent_path: &Path,
    name: &OsStr,
    temporary: &OsStr,
    expected: File,
) -> Result<(), EffectBrokerError> {
    let target = parent_path.join(name);
    let current =
        open_regular_at(parent, parent_path, name)?.ok_or(EffectBrokerError::PreimageMismatch)?;
    if !same_file(&expected, &current) {
        return Err(EffectBrokerError::PreimageMismatch);
    }
    drop(current);
    drop(expected);
    std::fs::rename(parent_path.join(temporary), &target)?;
    sync_windows_file(&target)?;
    Ok(())
}

#[cfg(windows)]
fn sync_windows_file(path: &Path) -> Result<(), EffectBrokerError> {
    let file = OpenOptions::new()
        .read(true)
        .write(true)
        .share_mode(FILE_SHARE_READ)
        .custom_flags(FILE_FLAG_OPEN_REPARSE_POINT)
        .open(path)?;
    let metadata = file.metadata()?;
    if !metadata.is_file() || metadata.file_attributes() & FILE_ATTRIBUTE_REPARSE_POINT != 0 {
        return Err(invalid_target().into());
    }
    file.sync_all()?;
    Ok(())
}

#[cfg(windows)]
fn same_file(expected: &File, observed: &File) -> bool {
    match (
        expected.try_clone().and_then(same_file::Handle::from_file),
        observed.try_clone().and_then(same_file::Handle::from_file),
    ) {
        (Ok(expected), Ok(observed)) => expected == observed,
        _ => false,
    }
}

fn invalid_target() -> std::io::Error {
    std::io::Error::new(
        std::io::ErrorKind::InvalidInput,
        "native effect target is not descriptor-bound regular workspace data",
    )
}

fn read_descriptor_utf8(file: &File, limit: usize) -> Result<Vec<u8>, EffectBrokerError> {
    let metadata = file.metadata()?;
    if !metadata.is_file() {
        return Err(invalid_target().into());
    }
    if metadata.len() > limit as u64 {
        return Err(EffectBrokerError::InputTooLarge { limit });
    }
    let mut file = file.try_clone()?;
    file.rewind()?;
    let mut bytes = Vec::new();
    file.take(u64::try_from(limit).unwrap_or(u64::MAX).saturating_add(1))
        .read_to_end(&mut bytes)?;
    if bytes.len() > limit {
        return Err(EffectBrokerError::InputTooLarge { limit });
    }
    std::str::from_utf8(&bytes).map_err(|_| EffectBrokerError::InvalidUtf8)?;
    Ok(bytes)
}

fn require_digest(bytes: &[u8], expected: &Digest) -> Result<(), EffectBrokerError> {
    if &digest_bytes(bytes) == expected {
        Ok(())
    } else {
        Err(EffectBrokerError::PreimageMismatch)
    }
}

fn temporary_name(target: &OsStr, request: &EffectRequest) -> OsString {
    let binding = digest_bytes(
        format!(
            "{}\0{}\0{}",
            request.run_id,
            request.effect_id,
            target.to_string_lossy()
        )
        .as_bytes(),
    );
    let suffix = &binding.as_str()[binding.as_str().len().saturating_sub(16)..];
    format!(".ao-next-{}-{suffix}.tmp", std::process::id()).into()
}
