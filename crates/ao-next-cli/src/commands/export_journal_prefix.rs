use std::fs::File;
use std::io::{Read as _, Seek as _, SeekFrom, Write as _};
use std::path::{Component, Path, PathBuf};

#[cfg(unix)]
use std::ffi::{OsStr, OsString};
#[cfg(unix)]
use std::os::unix::ffi::OsStrExt as _;
#[cfg(windows)]
use std::os::windows::ffi::OsStrExt as _;
#[cfg(windows)]
use std::os::windows::fs::{MetadataExt as _, OpenOptionsExt as _};

use ao_next_core::contracts::{IntakeExpectation, RunRequest, validate_intake_identity};
use ao_next_core::mission_exchange::{
    build_execution_journal_prefix, verify_execution_journal_prefix,
};
use ao_next_core::recovery::CheckpointJournal;
use ao_next_core::strict_json::{canonical_json_bytes, decode_strict_json};
#[cfg(unix)]
use rustix::fs::{AtFlags, Mode, OFlags};
#[cfg(unix)]
use rustix::io::Errno;

use super::{CommandFailure, CommandOutput, ExportJournalPrefixArgs};

const MAXIMUM_PREFIX_BYTES: u64 = 16 * 1024 * 1024;

pub fn execute(args: &ExportJournalPrefixArgs) -> Result<CommandOutput, CommandFailure> {
    let request_path = clean_absolute_local(&args.request)?;
    let journal_root = clean_absolute_local(&args.journal_root)?;
    let output_path = clean_absolute_local(&args.out)?;
    if lexically_contains(&journal_root, &output_path) {
        return Err(invalid_path("output must be outside the journal root"));
    }

    let mut request_input = BoundedInput::open(&request_path)?;
    let request_bytes = request_input.read(MAXIMUM_PREFIX_BYTES)?;
    let request: RunRequest = decode_strict_json(
        &request_bytes,
        usize::try_from(MAXIMUM_PREFIX_BYTES).unwrap_or(usize::MAX),
    )
    .map_err(|error| CommandFailure::invalid_input(error.to_string()))?;
    validate_intake_identity(
        &request,
        &IntakeExpectation {
            run_id: request.run_id.clone(),
            source: request.source.clone(),
            workspace: request.workspace.clone(),
            now: request.authority.issued_at,
        },
    )
    .map_err(|error| CommandFailure::invalid_input(error.to_string()))?;

    let journal_anchor = SafeDirectory::open(&journal_root)?;
    let output = SafeOutput::new(&output_path)?;
    #[cfg(unix)]
    let journal = CheckpointJournal::open_bound_from_unix_root(
        &journal_root,
        journal_anchor
            .file
            .try_clone()
            .map_err(|error| invalid_path(error.to_string()))?,
        MAXIMUM_PREFIX_BYTES,
        &request,
    )
    .map_err(|error| CommandFailure::evidence(error.to_string()))?;
    #[cfg(not(unix))]
    let journal = CheckpointJournal::open_bound(&journal_root, MAXIMUM_PREFIX_BYTES, &request)
        .map_err(|error| CommandFailure::evidence(error.to_string()))?;
    let prefix = build_execution_journal_prefix(&journal, &request)
        .map_err(|error| CommandFailure::evidence(error.to_string()))?;
    verify_execution_journal_prefix(&prefix, &request)
        .map_err(|error| CommandFailure::evidence(error.to_string()))?;
    journal_anchor.verify()?;
    if request_input.read(MAXIMUM_PREFIX_BYTES)? != request_bytes {
        return Err(CommandFailure::invalid_input(
            "request changed during journal-prefix export",
        ));
    }
    output.publish(
        &canonical_json_bytes(&prefix)
            .map_err(|error| CommandFailure::evidence(error.to_string()))?,
    )?;

    Ok(CommandOutput::new(
        serde_json::json!({
            "schema_version": "ao.next.journal-prefix-export-readback.v1",
            "run_id": prefix.run_id,
            "prefix_digest": prefix.prefix_digest,
            "event_count": prefix.events.len(),
            "safe_to_execute": false,
            "executes_work": false,
            "approves_work": false
        }),
        "exported one verified read-only execution journal prefix",
        0,
    ))
}

fn clean_absolute_local(path: &Path) -> Result<PathBuf, CommandFailure> {
    if path.as_os_str().is_empty() || !path.is_absolute() || path.file_name().is_none() {
        return Err(invalid_path("locator must be a clean absolute local path"));
    }
    #[cfg(unix)]
    if path.as_os_str().as_bytes().contains(&0) {
        return Err(invalid_path("locator contains NUL"));
    }
    #[cfg(windows)]
    if path.as_os_str().encode_wide().any(|value| value == 0) {
        return Err(invalid_path("locator contains NUL"));
    }
    let rendered = path.as_os_str().to_string_lossy();
    if rendered.starts_with("//") || rendered.starts_with(r"\\") {
        return Err(invalid_path("UNC and device namespace paths are denied"));
    }
    if has_lexical_non_normal_component(path)
        || path.components().any(|component| match component {
            Component::RootDir | Component::Normal(_) => false,
            Component::Prefix(prefix) => {
                #[cfg(windows)]
                {
                    !matches!(prefix.kind(), std::path::Prefix::Disk(_))
                }
                #[cfg(not(windows))]
                {
                    let _ = prefix;
                    true
                }
            }
            Component::CurDir | Component::ParentDir => true,
        })
    {
        return Err(invalid_path("locator contains a non-normal component"));
    }
    Ok(path.to_path_buf())
}

#[cfg(unix)]
fn has_lexical_non_normal_component(path: &Path) -> bool {
    path.as_os_str()
        .as_bytes()
        .split(|byte| *byte == b'/')
        .skip(1)
        .any(|component| component.is_empty() || component == b"." || component == b"..")
}

#[cfg(windows)]
fn has_lexical_non_normal_component(path: &Path) -> bool {
    path.as_os_str()
        .to_string_lossy()
        .split(['/', '\\'])
        .skip(1)
        .any(|component| component.is_empty() || component == "." || component == "..")
}

#[cfg(not(any(unix, windows)))]
fn has_lexical_non_normal_component(_: &Path) -> bool {
    false
}

#[cfg(windows)]
fn lexically_contains(root: &Path, candidate: &Path) -> bool {
    let root = root.as_os_str().to_string_lossy().to_lowercase();
    let candidate = candidate.as_os_str().to_string_lossy().to_lowercase();
    let root = root.trim_end_matches(['/', '\\']);
    candidate == root
        || candidate
            .strip_prefix(root)
            .is_some_and(|suffix| suffix.starts_with(['/', '\\']))
}

#[cfg(not(windows))]
fn lexically_contains(root: &Path, candidate: &Path) -> bool {
    candidate.starts_with(root)
}

fn invalid_path(message: impl Into<String>) -> CommandFailure {
    CommandFailure::invalid_input(message)
}

fn read_bounded(file: &mut File, limit: u64) -> Result<Vec<u8>, CommandFailure> {
    let metadata = file
        .metadata()
        .map_err(|error| invalid_path(error.to_string()))?;
    if !metadata.is_file() || metadata.len() > limit {
        return Err(invalid_path("input is not a bounded regular file"));
    }
    file.seek(SeekFrom::Start(0))
        .map_err(|error| invalid_path(error.to_string()))?;
    let mut bytes = Vec::new();
    file.take(limit.saturating_add(1))
        .read_to_end(&mut bytes)
        .map_err(|error| invalid_path(error.to_string()))?;
    if u64::try_from(bytes.len()).unwrap_or(u64::MAX) > limit {
        return Err(invalid_path("input exceeds the journal-prefix byte bound"));
    }
    Ok(bytes)
}

#[cfg(unix)]
fn open_unix_existing(path: &Path, directory: bool) -> Result<File, CommandFailure> {
    let relative = path
        .strip_prefix("/")
        .map_err(|error| invalid_path(error.to_string()))?;
    let components = relative
        .components()
        .map(|component| match component {
            Component::Normal(name) => Ok(name.to_os_string()),
            _ => Err(invalid_path("locator contains a non-normal component")),
        })
        .collect::<Result<Vec<_>, _>>()?;
    let mut opened = rustix::fs::open(
        "/",
        OFlags::RDONLY | OFlags::DIRECTORY | OFlags::NOFOLLOW | OFlags::CLOEXEC,
        Mode::empty(),
    )
    .map(File::from)
    .map_err(|error| invalid_path(error.to_string()))?;
    for (index, component) in components.iter().enumerate() {
        let is_leaf = index + 1 == components.len();
        let mut flags = OFlags::RDONLY | OFlags::NOFOLLOW | OFlags::CLOEXEC | OFlags::NONBLOCK;
        if !is_leaf || directory {
            flags |= OFlags::DIRECTORY;
        }
        opened = rustix::fs::openat(&opened, component, flags, Mode::empty())
            .map(File::from)
            .map_err(|error| {
                invalid_path(format!(
                    "open component {} (leaf={is_leaf}, directory={directory}): {error}",
                    component.to_string_lossy()
                ))
            })?;
    }
    let metadata = opened
        .metadata()
        .map_err(|error| invalid_path(error.to_string()))?;
    if metadata.file_type().is_symlink()
        || (directory && !metadata.is_dir())
        || (!directory && !metadata.is_file())
    {
        return Err(invalid_path("locator leaf has the wrong file type"));
    }
    Ok(opened)
}

#[cfg(unix)]
struct BoundedInput {
    file: File,
}

#[cfg(unix)]
impl BoundedInput {
    fn open(path: &Path) -> Result<Self, CommandFailure> {
        Ok(Self {
            file: open_unix_existing(path, false)?,
        })
    }

    fn read(&mut self, limit: u64) -> Result<Vec<u8>, CommandFailure> {
        read_bounded(&mut self.file, limit)
    }
}

#[cfg(unix)]
struct SafeDirectory {
    path: PathBuf,
    file: File,
}

#[cfg(unix)]
impl SafeDirectory {
    fn open(path: &Path) -> Result<Self, CommandFailure> {
        Ok(Self {
            path: path.to_path_buf(),
            file: open_unix_existing(path, true)?,
        })
    }

    fn verify(&self) -> Result<(), CommandFailure> {
        use std::os::unix::fs::MetadataExt as _;

        let observed = open_unix_existing(&self.path, true)?;
        let expected = self
            .file
            .metadata()
            .map_err(|error| invalid_path(error.to_string()))?;
        let observed = observed
            .metadata()
            .map_err(|error| invalid_path(error.to_string()))?;
        if expected.dev() != observed.dev() || expected.ino() != observed.ino() {
            return Err(invalid_path("journal root changed during export"));
        }
        Ok(())
    }
}

#[cfg(unix)]
struct SafeOutput {
    root: File,
    parent: File,
    parent_relative: PathBuf,
    name: OsString,
}

#[cfg(unix)]
impl SafeOutput {
    fn new(path: &Path) -> Result<Self, CommandFailure> {
        let name = path
            .file_name()
            .ok_or_else(|| invalid_path("output name is missing"))?
            .to_os_string();
        let parent_path = path
            .parent()
            .ok_or_else(|| invalid_path("output parent is missing"))?;
        let root = rustix::fs::open(
            "/",
            OFlags::RDONLY | OFlags::DIRECTORY | OFlags::NOFOLLOW | OFlags::CLOEXEC,
            Mode::empty(),
        )
        .map(File::from)
        .map_err(|error| {
            invalid_path(format!(
                "open output parent component {}: {error}",
                name.to_string_lossy()
            ))
        })?;
        let parent_relative = parent_path
            .strip_prefix("/")
            .map_err(|error| invalid_path(error.to_string()))?
            .to_path_buf();
        let parent = open_unix_relative_directory(&root, &parent_relative)?;
        require_unix_absent(&parent, &name)?;
        Ok(Self {
            root,
            parent,
            parent_relative,
            name,
        })
    }

    fn publish(self, bytes: &[u8]) -> Result<(), CommandFailure> {
        self.verify_parent()?;
        let mut file = rustix::fs::openat(
            &self.parent,
            &self.name,
            OFlags::WRONLY | OFlags::CREATE | OFlags::EXCL | OFlags::NOFOLLOW | OFlags::CLOEXEC,
            Mode::from_raw_mode(0o600),
        )
        .map(File::from)
        .map_err(|error| invalid_path(error.to_string()))?;
        if let Err(error) = file.write_all(bytes).and_then(|()| file.sync_all()) {
            remove_unix_output(&self.parent, &self.name);
            return Err(CommandFailure::evidence(error.to_string()));
        }
        if let Err(error) = self.verify_parent() {
            remove_unix_output(&self.parent, &self.name);
            return Err(error);
        }
        if let Err(error) = rustix::fs::fsync(&self.parent) {
            remove_unix_output(&self.parent, &self.name);
            return Err(CommandFailure::evidence(error.to_string()));
        }
        Ok(())
    }

    fn verify_parent(&self) -> Result<(), CommandFailure> {
        use std::os::unix::fs::MetadataExt as _;

        let observed = open_unix_relative_directory(&self.root, &self.parent_relative)?;
        let expected = self
            .parent
            .metadata()
            .map_err(|error| invalid_path(error.to_string()))?;
        let observed = observed
            .metadata()
            .map_err(|error| invalid_path(error.to_string()))?;
        if expected.dev() != observed.dev() || expected.ino() != observed.ino() {
            return Err(invalid_path("output parent changed during export"));
        }
        Ok(())
    }
}

#[cfg(unix)]
fn open_unix_relative_directory(root: &File, relative: &Path) -> Result<File, CommandFailure> {
    let mut directory = root
        .try_clone()
        .map_err(|error| invalid_path(error.to_string()))?;
    for component in relative.components() {
        let Component::Normal(name) = component else {
            return Err(invalid_path("output path contains a non-normal component"));
        };
        directory = rustix::fs::openat(
            &directory,
            name,
            OFlags::RDONLY | OFlags::DIRECTORY | OFlags::NOFOLLOW | OFlags::CLOEXEC,
            Mode::empty(),
        )
        .map(File::from)
        .map_err(|error| invalid_path(error.to_string()))?;
    }
    Ok(directory)
}

#[cfg(unix)]
fn require_unix_absent(parent: &File, name: &OsStr) -> Result<(), CommandFailure> {
    match rustix::fs::statat(parent, name, AtFlags::SYMLINK_NOFOLLOW) {
        Err(Errno::NOENT) => Ok(()),
        Ok(_) => Err(invalid_path("output already exists")),
        Err(error) => Err(invalid_path(error.to_string())),
    }
}

#[cfg(unix)]
fn remove_unix_output(parent: &File, name: &OsStr) {
    let _ = rustix::fs::unlinkat(parent, name, AtFlags::empty());
    let _ = rustix::fs::fsync(parent);
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
fn open_windows_path(path: &Path, directory: bool) -> Result<File, CommandFailure> {
    let flags = FILE_FLAG_OPEN_REPARSE_POINT
        | if directory {
            FILE_FLAG_BACKUP_SEMANTICS
        } else {
            0
        };
    let file = std::fs::OpenOptions::new()
        .read(true)
        .share_mode(FILE_SHARE_READ | FILE_SHARE_WRITE)
        .custom_flags(flags)
        .open(path)
        .map_err(|error| invalid_path(error.to_string()))?;
    let metadata = file
        .metadata()
        .map_err(|error| invalid_path(error.to_string()))?;
    if metadata.file_attributes() & FILE_ATTRIBUTE_REPARSE_POINT != 0
        || (directory && !metadata.is_dir())
        || (!directory && !metadata.is_file())
    {
        return Err(invalid_path(
            "Windows locator is a reparse point or wrong type",
        ));
    }
    Ok(file)
}

#[cfg(windows)]
fn open_windows_ancestors(path: &Path) -> Result<Vec<File>, CommandFailure> {
    let mut ancestors = path
        .ancestors()
        .filter(|ancestor| !ancestor.as_os_str().is_empty())
        .collect::<Vec<_>>();
    ancestors.reverse();
    ancestors
        .into_iter()
        .map(|ancestor| open_windows_path(ancestor, true))
        .collect()
}

#[cfg(windows)]
struct BoundedInput {
    file: File,
    _ancestors: Vec<File>,
}

#[cfg(windows)]
impl BoundedInput {
    fn open(path: &Path) -> Result<Self, CommandFailure> {
        let parent = path
            .parent()
            .ok_or_else(|| invalid_path("request parent is missing"))?;
        Ok(Self {
            file: open_windows_path(path, false)?,
            _ancestors: open_windows_ancestors(parent)?,
        })
    }

    fn read(&mut self, limit: u64) -> Result<Vec<u8>, CommandFailure> {
        read_bounded(&mut self.file, limit)
    }
}

#[cfg(windows)]
struct SafeDirectory {
    path: PathBuf,
    _ancestors: Vec<File>,
}

#[cfg(windows)]
impl SafeDirectory {
    fn open(path: &Path) -> Result<Self, CommandFailure> {
        Ok(Self {
            path: path.to_path_buf(),
            _ancestors: open_windows_ancestors(path)?,
        })
    }

    fn verify(&self) -> Result<(), CommandFailure> {
        let _ = open_windows_ancestors(&self.path)?;
        Ok(())
    }
}

#[cfg(windows)]
struct SafeOutput {
    path: PathBuf,
    _ancestors: Vec<File>,
}

#[cfg(windows)]
impl SafeOutput {
    fn new(path: &Path) -> Result<Self, CommandFailure> {
        let parent = path
            .parent()
            .ok_or_else(|| invalid_path("output parent is missing"))?;
        let ancestors = open_windows_ancestors(parent)?;
        match std::fs::OpenOptions::new()
            .read(true)
            .share_mode(FILE_SHARE_READ | FILE_SHARE_WRITE)
            .custom_flags(FILE_FLAG_OPEN_REPARSE_POINT)
            .open(path)
        {
            Ok(_) => return Err(invalid_path("output already exists")),
            Err(error) if error.kind() == std::io::ErrorKind::NotFound => {}
            Err(error) => return Err(invalid_path(error.to_string())),
        }
        Ok(Self {
            path: path.to_path_buf(),
            _ancestors: ancestors,
        })
    }

    fn publish(self, bytes: &[u8]) -> Result<(), CommandFailure> {
        let mut file = std::fs::OpenOptions::new()
            .write(true)
            .create_new(true)
            .share_mode(FILE_SHARE_READ)
            .custom_flags(FILE_FLAG_OPEN_REPARSE_POINT)
            .open(&self.path)
            .map_err(|error| invalid_path(error.to_string()))?;
        let metadata = file
            .metadata()
            .map_err(|error| invalid_path(error.to_string()))?;
        if !metadata.is_file() || metadata.file_attributes() & FILE_ATTRIBUTE_REPARSE_POINT != 0 {
            drop(file);
            let _ = std::fs::remove_file(&self.path);
            return Err(invalid_path("output is not a regular non-reparse file"));
        }
        if let Err(error) = file.write_all(bytes).and_then(|()| file.sync_all()) {
            drop(file);
            let _ = std::fs::remove_file(&self.path);
            return Err(CommandFailure::evidence(error.to_string()));
        }
        Ok(())
    }
}

#[cfg(not(any(unix, windows)))]
struct BoundedInput {
    file: File,
}

#[cfg(not(any(unix, windows)))]
impl BoundedInput {
    fn open(path: &Path) -> Result<Self, CommandFailure> {
        let metadata =
            std::fs::symlink_metadata(path).map_err(|error| invalid_path(error.to_string()))?;
        if metadata.file_type().is_symlink() || !metadata.is_file() {
            return Err(invalid_path("request is not a regular non-link file"));
        }
        Ok(Self {
            file: File::open(path).map_err(|error| invalid_path(error.to_string()))?,
        })
    }

    fn read(&mut self, limit: u64) -> Result<Vec<u8>, CommandFailure> {
        read_bounded(&mut self.file, limit)
    }
}

#[cfg(not(any(unix, windows)))]
struct SafeDirectory(PathBuf);

#[cfg(not(any(unix, windows)))]
impl SafeDirectory {
    fn open(path: &Path) -> Result<Self, CommandFailure> {
        let metadata =
            std::fs::symlink_metadata(path).map_err(|error| invalid_path(error.to_string()))?;
        if metadata.file_type().is_symlink() || !metadata.is_dir() {
            return Err(invalid_path("journal root is not a regular directory"));
        }
        Ok(Self(path.to_path_buf()))
    }

    fn verify(&self) -> Result<(), CommandFailure> {
        Self::open(&self.0).map(|_| ())
    }
}

#[cfg(not(any(unix, windows)))]
struct SafeOutput(PathBuf);

#[cfg(not(any(unix, windows)))]
impl SafeOutput {
    fn new(path: &Path) -> Result<Self, CommandFailure> {
        let parent = path
            .parent()
            .ok_or_else(|| invalid_path("output parent is missing"))?;
        let metadata =
            std::fs::symlink_metadata(parent).map_err(|error| invalid_path(error.to_string()))?;
        if metadata.file_type().is_symlink() || !metadata.is_dir() || path.exists() {
            return Err(invalid_path("output path is unsafe or already exists"));
        }
        Ok(Self(path.to_path_buf()))
    }

    fn publish(self, bytes: &[u8]) -> Result<(), CommandFailure> {
        let mut file = std::fs::OpenOptions::new()
            .write(true)
            .create_new(true)
            .open(self.0)
            .map_err(|error| invalid_path(error.to_string()))?;
        file.write_all(bytes)
            .and_then(|()| file.sync_all())
            .map_err(|error| CommandFailure::evidence(error.to_string()))
    }
}

#[cfg(test)]
mod tests {
    #[cfg(any(unix, windows))]
    use super::clean_absolute_local;

    #[cfg(unix)]
    #[test]
    fn clean_locator_rejects_nul() {
        use std::ffi::OsString;
        use std::os::unix::ffi::OsStringExt as _;

        let path = std::path::PathBuf::from(OsString::from_vec(b"/tmp/bad\0path".to_vec()));
        assert!(clean_absolute_local(&path).is_err());
    }

    #[cfg(windows)]
    #[test]
    fn clean_windows_locator_rejects_nul() {
        use std::ffi::OsString;
        use std::os::windows::ffi::OsStringExt as _;

        let path = std::path::PathBuf::from(OsString::from_wide(&[
            b'C' as u16,
            b':' as u16,
            b'\\' as u16,
            b'b' as u16,
            0,
            b'd' as u16,
        ]));
        assert!(clean_absolute_local(&path).is_err());
    }
}
