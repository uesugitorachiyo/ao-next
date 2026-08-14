#[cfg(not(windows))]
use std::ffi::{OsStr, OsString};
#[cfg(not(windows))]
use std::fs::File;
#[cfg(not(windows))]
use std::io::{Read, Seek, Write};
#[cfg(not(windows))]
use std::path::{Path, PathBuf};
#[cfg(not(windows))]
use std::sync::Arc;

#[cfg(not(windows))]
use rustix::fs::{AtFlags, FileType, Mode, OFlags, RenameFlags};
#[cfg(not(windows))]
use rustix::io::Errno;
use thiserror::Error;

#[cfg(not(windows))]
use crate::contracts::Digest;
use crate::contracts::{AuthorityEnvelope, EffectKind, EffectRequest};
#[cfg(not(windows))]
use crate::evidence::digest_bytes;
use crate::policy::{PolicyDenial, resolve_effect_paths, validate_effect_request};

#[cfg(not(windows))]
const NONEXISTENT_FILE_BINDING: &[u8] = b"ao.next.file-does-not-exist.v1";

#[derive(Clone, Debug)]
pub struct AuthorizedEffect {
    request: EffectRequest,
    authority: AuthorityEnvelope,
    #[cfg(not(windows))]
    target: DescriptorTarget,
}

#[cfg(not(windows))]
#[derive(Clone, Debug)]
enum DescriptorTarget {
    Read {
        root: Arc<File>,
        root_path: PathBuf,
        file: Arc<File>,
        relative: PathBuf,
    },
    Write {
        root: Arc<File>,
        root_path: PathBuf,
        parent: Arc<File>,
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
        #[cfg(windows)]
        return Err(EffectBrokerError::UnsupportedEffect(
            authorized.request.kind.clone(),
        ));
        #[cfg(not(windows))]
        {
            verify_descriptor_binding(&authorized.target)?;
            match (&authorized.request.kind, &authorized.target) {
                (EffectKind::ReadFile, DescriptorTarget::Read { file, .. }) => {
                    self.read_file(&authorized.request, file)
                }
                (EffectKind::WriteFile, DescriptorTarget::Write { parent, name, .. }) => {
                    self.write_file(&authorized.request, parent, name)
                }
                _ => Err(EffectBrokerError::UnsupportedEffect(
                    authorized.request.kind.clone(),
                )),
            }
        }
    }

    #[cfg(not(windows))]
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

    #[cfg(not(windows))]
    fn write_file(
        &self,
        request: &EffectRequest,
        parent: &File,
        name: &OsStr,
    ) -> Result<EffectOutput, EffectBrokerError> {
        let content = request.content.as_deref().unwrap_or_default().as_bytes();
        if content.len() > self.output_bytes {
            return Err(EffectBrokerError::OutputTooLarge {
                limit: self.output_bytes,
            });
        }
        let existing = open_regular_at(parent, name)?;
        match &existing {
            Some(file) => require_digest(
                &read_descriptor_utf8(file, self.input_bytes)?,
                &request.input_digest,
            )?,
            None => require_digest(NONEXISTENT_FILE_BINDING, &request.input_digest)?,
        }
        let temporary = temporary_name(name, request);
        let temporary_fd = rustix::fs::openat(
            parent,
            &temporary,
            OFlags::WRONLY | OFlags::CREATE | OFlags::EXCL | OFlags::NOFOLLOW | OFlags::CLOEXEC,
            Mode::from_raw_mode(0o600),
        )
        .map_err(std::io::Error::from)?;
        let mut temporary_file = File::from(temporary_fd);
        let result: Result<(), EffectBrokerError> = (|| {
            temporary_file.write_all(content)?;
            temporary_file.sync_all()?;
            drop(temporary_file);
            if let Some(existing) = existing {
                publish_replace(parent, name, &temporary, &existing)?;
            } else {
                publish_create(parent, name, &temporary)?;
            }
            Ok(())
        })();
        if result.is_err() {
            let _ = rustix::fs::unlinkat(parent, &temporary, AtFlags::empty());
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
        #[cfg(windows)]
        let _ = resolved_paths;
        #[cfg(not(windows))]
        let target = open_descriptor_target(request, authority, &resolved_paths[0])
            .map_err(|_| PolicyDenial::NonRegularFile(request.paths[0].clone()))?;
        Ok(AuthorizedEffect {
            request: request.clone(),
            authority: authority.clone(),
            #[cfg(not(windows))]
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

#[cfg(not(windows))]
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
    let root_fd = rustix::fs::open(
        root,
        OFlags::RDONLY | OFlags::DIRECTORY | OFlags::NOFOLLOW | OFlags::CLOEXEC,
        Mode::empty(),
    )
    .map_err(std::io::Error::from)?;
    let root_file = File::from(root_fd);
    let root_descriptor = Arc::new(root_file);
    let parent_relative = relative.parent().unwrap_or_else(|| Path::new(""));
    let parent = open_parent_at(&root_descriptor, parent_relative)?;
    match request.kind {
        EffectKind::ReadFile => open_regular_at(&parent, &name)?
            .map(|file| DescriptorTarget::Read {
                root: root_descriptor,
                root_path: root.clone(),
                file: Arc::new(file),
                relative: relative.to_path_buf(),
            })
            .ok_or_else(invalid_target),
        EffectKind::WriteFile => Ok(DescriptorTarget::Write {
            root: root_descriptor,
            root_path: root.clone(),
            parent: Arc::new(parent),
            parent_relative: parent_relative.to_path_buf(),
            name,
        }),
        _ => Err(invalid_target()),
    }
}

#[cfg(not(windows))]
fn verify_descriptor_binding(target: &DescriptorTarget) -> Result<(), EffectBrokerError> {
    let (root, root_path) = match target {
        DescriptorTarget::Read {
            root, root_path, ..
        }
        | DescriptorTarget::Write {
            root, root_path, ..
        } => (root, root_path),
    };
    let current_root = File::from(
        rustix::fs::open(
            root_path,
            OFlags::RDONLY | OFlags::DIRECTORY | OFlags::NOFOLLOW | OFlags::CLOEXEC,
            Mode::empty(),
        )
        .map_err(std::io::Error::from)?,
    );
    if !same_file(root, &current_root) {
        return Err(EffectBrokerError::PreimageMismatch);
    }
    match target {
        DescriptorTarget::Read { file, relative, .. } => {
            let parent = open_parent_at(root, relative.parent().unwrap_or_else(|| Path::new("")))?;
            let current =
                open_regular_at(&parent, relative.file_name().ok_or_else(invalid_target)?)?
                    .ok_or(EffectBrokerError::PreimageMismatch)?;
            if !same_file(file, &current) {
                return Err(EffectBrokerError::PreimageMismatch);
            }
        }
        DescriptorTarget::Write {
            parent,
            parent_relative,
            ..
        } => {
            let current = open_parent_at(root, parent_relative)?;
            if !same_file(parent, &current) {
                return Err(EffectBrokerError::PreimageMismatch);
            }
        }
    }
    Ok(())
}

#[cfg(not(windows))]
fn open_parent_at(root: &File, relative: &Path) -> Result<File, std::io::Error> {
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
    Ok(parent)
}

#[cfg(not(windows))]
fn open_regular_at(parent: &File, name: &OsStr) -> Result<Option<File>, std::io::Error> {
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

#[cfg(not(windows))]
fn publish_create(parent: &File, name: &OsStr, temporary: &OsStr) -> Result<(), EffectBrokerError> {
    rustix::fs::renameat_with(parent, temporary, parent, name, RenameFlags::NOREPLACE)
        .map_err(std::io::Error::from)?;
    if let Err(error) = rustix::fs::fsync(parent) {
        let _ = rustix::fs::unlinkat(parent, name, AtFlags::empty());
        let _ = rustix::fs::fsync(parent);
        return Err(std::io::Error::from(error).into());
    }
    Ok(())
}

#[cfg(not(windows))]
fn publish_replace(
    parent: &File,
    name: &OsStr,
    temporary: &OsStr,
    expected: &File,
) -> Result<(), EffectBrokerError> {
    rustix::fs::renameat_with(parent, temporary, parent, name, RenameFlags::EXCHANGE)
        .map_err(std::io::Error::from)?;
    let swapped = open_regular_at(parent, temporary);
    let identity_matches = swapped
        .as_ref()
        .ok()
        .and_then(Option::as_ref)
        .is_some_and(|observed| same_file(expected, observed));
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
    Ok(())
}

#[cfg(not(windows))]
fn rollback_exchange(parent: &File, name: &OsStr, temporary: &OsStr) {
    let _ = rustix::fs::renameat_with(parent, temporary, parent, name, RenameFlags::EXCHANGE);
    let _ = rustix::fs::unlinkat(parent, temporary, AtFlags::empty());
    let _ = rustix::fs::fsync(parent);
}

#[cfg(not(windows))]
fn same_file(expected: &File, observed: &File) -> bool {
    match (rustix::fs::fstat(expected), rustix::fs::fstat(observed)) {
        (Ok(expected), Ok(observed)) => {
            expected.st_dev == observed.st_dev && expected.st_ino == observed.st_ino
        }
        _ => false,
    }
}

#[cfg(not(windows))]
fn invalid_target() -> std::io::Error {
    std::io::Error::new(
        std::io::ErrorKind::InvalidInput,
        "native effect target is not descriptor-bound regular workspace data",
    )
}

#[cfg(not(windows))]
fn read_descriptor_utf8(file: &File, limit: usize) -> Result<Vec<u8>, EffectBrokerError> {
    let stat = rustix::fs::fstat(file).map_err(std::io::Error::from)?;
    if !FileType::from_raw_mode(stat.st_mode).is_file() {
        return Err(invalid_target().into());
    }
    if stat.st_size < 0 || u64::try_from(stat.st_size).unwrap_or(u64::MAX) > limit as u64 {
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

#[cfg(not(windows))]
fn require_digest(bytes: &[u8], expected: &Digest) -> Result<(), EffectBrokerError> {
    if &digest_bytes(bytes) == expected {
        Ok(())
    } else {
        Err(EffectBrokerError::PreimageMismatch)
    }
}

#[cfg(not(windows))]
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
