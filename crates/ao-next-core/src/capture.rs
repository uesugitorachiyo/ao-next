use std::fs::{File, OpenOptions};
use std::io::{Read, Write};
use std::path::{Path, PathBuf};

#[cfg(unix)]
use rustix::fs::{FileType, Mode, OFlags};
#[cfg(unix)]
use std::os::unix::fs::OpenOptionsExt as _;
#[cfg(windows)]
use std::os::windows::fs::{MetadataExt as _, OpenOptionsExt as _};
use thiserror::Error;

use crate::contracts::Digest;
use crate::evidence::digest_bytes;

const FINAL_NAME: &str = "capture-index.json";
const INCOMPLETE_NAME: &str = "capture-index.json.incomplete";

#[derive(Clone, Debug, Eq, PartialEq)]
pub enum CapturePublication {
    Published(Digest),
    Repaired(Digest),
}

impl CapturePublication {
    #[must_use]
    pub const fn digest(&self) -> &Digest {
        match self {
            Self::Published(digest) | Self::Repaired(digest) => digest,
        }
    }
}

#[derive(Debug, Error)]
pub enum CaptureStoreError {
    #[error("capture store I/O failed: {0}")]
    Io(#[from] std::io::Error),
    #[error("capture root or index path is unsafe")]
    UnsafePath,
    #[error("capture index exceeds {limit} bytes: {actual}")]
    Oversized { actual: u64, limit: u64 },
    #[error("capture index bytes or digest are contradictory")]
    Contradictory,
    #[error("capture index publication is incomplete")]
    Incomplete,
}

#[derive(Clone, Debug)]
pub struct CaptureIndexStore {
    root: PathBuf,
    maximum_bytes: u64,
}

impl CaptureIndexStore {
    /// Opens an existing private capture root.
    ///
    /// # Errors
    ///
    /// Returns [`CaptureStoreError`] when the root is unsafe or the byte limit is zero.
    pub fn open(root: PathBuf, maximum_bytes: u64) -> Result<Self, CaptureStoreError> {
        if maximum_bytes == 0 || !safe_directory(&root)? {
            return Err(CaptureStoreError::UnsafePath);
        }
        Ok(Self {
            root,
            maximum_bytes,
        })
    }

    /// Durably publishes one new capture index without overwriting either name.
    ///
    /// # Errors
    ///
    /// Returns [`CaptureStoreError`] for unsafe paths, existing names, oversized bytes, or I/O
    /// failures.
    pub fn publish(&self, bytes: &[u8]) -> Result<CapturePublication, CaptureStoreError> {
        let digest = self.stage_incomplete(bytes)?;
        self.publish_staged(&digest)
    }

    /// Creates and synchronizes canonical incomplete index bytes without publishing the final name.
    ///
    /// # Errors
    ///
    /// Returns [`CaptureStoreError`] for unsafe paths, existing names, oversized bytes, or I/O
    /// failures.
    pub fn stage_incomplete(&self, bytes: &[u8]) -> Result<Digest, CaptureStoreError> {
        self.validate_root()?;
        let actual = bytes.len() as u64;
        if actual > self.maximum_bytes {
            return Err(CaptureStoreError::Oversized {
                actual,
                limit: self.maximum_bytes,
            });
        }
        let (final_path, incomplete) = self.paths();
        require_absent(&final_path)?;
        require_absent(&incomplete)?;

        let mut options = OpenOptions::new();
        options.write(true).create_new(true);
        #[cfg(unix)]
        options.mode(0o600);
        let mut file = options.open(&incomplete)?;
        file.write_all(bytes)?;
        file.sync_all()?;
        Ok(digest_bytes(bytes))
    }

    /// Publishes previously synchronized incomplete bytes under the final create-new name.
    ///
    /// # Errors
    ///
    /// Returns [`CaptureStoreError`] unless exactly one safe incomplete index matches `expected`
    /// and the final name is absent.
    pub fn publish_staged(
        &self,
        expected: &Digest,
    ) -> Result<CapturePublication, CaptureStoreError> {
        self.validate_root()?;
        let (final_path, incomplete) = self.paths();
        require_absent(&final_path)?;
        if !safe_regular_exists(&incomplete)?
            || digest_bytes(&read_bounded_regular(&incomplete, self.maximum_bytes)?) != *expected
        {
            return Err(CaptureStoreError::Contradictory);
        }
        publish_final(&incomplete, &final_path)?;
        Ok(CapturePublication::Published(expected.clone()))
    }

    /// Repairs or verifies an interrupted capture-index publication.
    ///
    /// # Errors
    ///
    /// Returns [`CaptureStoreError`] when no index exists, either name is unsafe, retained bytes
    /// exceed the limit or contradict the expected digest, or publication repair fails.
    pub fn recover(&self, expected: &Digest) -> Result<CapturePublication, CaptureStoreError> {
        self.validate_root()?;
        let (final_path, incomplete) = self.paths();
        match (
            safe_regular_exists(&final_path)?,
            safe_regular_exists(&incomplete)?,
        ) {
            (false, false) => Err(CaptureStoreError::Incomplete),
            (false, true) => {
                let bytes = read_bounded_regular(&incomplete, self.maximum_bytes)?;
                if digest_bytes(&bytes) != *expected {
                    return Err(CaptureStoreError::Contradictory);
                }
                publish_final(&incomplete, &final_path)?;
                Ok(CapturePublication::Repaired(expected.clone()))
            }
            (true, false) => self
                .verify_final(&final_path, expected)
                .map(CapturePublication::Published),
            (true, true) => {
                let final_bytes = read_bounded_regular(&final_path, self.maximum_bytes)?;
                let incomplete_bytes = read_bounded_regular(&incomplete, self.maximum_bytes)?;
                if final_bytes != incomplete_bytes || digest_bytes(&final_bytes) != *expected {
                    return Err(CaptureStoreError::Contradictory);
                }
                remove_incomplete_and_sync(&incomplete, &final_path)?;
                Ok(CapturePublication::Repaired(expected.clone()))
            }
        }
    }

    fn paths(&self) -> (PathBuf, PathBuf) {
        (self.root.join(FINAL_NAME), self.root.join(INCOMPLETE_NAME))
    }

    fn validate_root(&self) -> Result<(), CaptureStoreError> {
        if safe_directory(&self.root)? {
            Ok(())
        } else {
            Err(CaptureStoreError::UnsafePath)
        }
    }

    fn verify_final(
        &self,
        final_path: &Path,
        expected: &Digest,
    ) -> Result<Digest, CaptureStoreError> {
        let bytes = read_bounded_regular(final_path, self.maximum_bytes)?;
        let observed = digest_bytes(&bytes);
        if observed == *expected {
            Ok(observed)
        } else {
            Err(CaptureStoreError::Contradictory)
        }
    }
}

fn safe_directory(path: &Path) -> Result<bool, CaptureStoreError> {
    let metadata = std::fs::symlink_metadata(path)?;
    Ok(!unsafe_metadata(&metadata) && metadata.is_dir())
}

fn require_absent(path: &Path) -> Result<(), CaptureStoreError> {
    match std::fs::symlink_metadata(path) {
        Err(error) if error.kind() == std::io::ErrorKind::NotFound => Ok(()),
        Ok(metadata) if unsafe_metadata(&metadata) || !metadata.is_file() => {
            Err(CaptureStoreError::UnsafePath)
        }
        Ok(_) => Err(std::io::Error::new(
            std::io::ErrorKind::AlreadyExists,
            "capture index name already exists",
        )
        .into()),
        Err(error) => Err(error.into()),
    }
}

fn safe_regular_exists(path: &Path) -> Result<bool, CaptureStoreError> {
    match std::fs::symlink_metadata(path) {
        Err(error) if error.kind() == std::io::ErrorKind::NotFound => Ok(false),
        Ok(metadata) if unsafe_metadata(&metadata) || !metadata.is_file() => {
            Err(CaptureStoreError::UnsafePath)
        }
        Ok(_) => Ok(true),
        Err(error) => Err(error.into()),
    }
}

#[cfg(unix)]
fn unsafe_metadata(metadata: &std::fs::Metadata) -> bool {
    metadata.file_type().is_symlink()
}

#[cfg(windows)]
fn unsafe_metadata(metadata: &std::fs::Metadata) -> bool {
    metadata.file_attributes() & FILE_ATTRIBUTE_REPARSE_POINT != 0
}

fn read_bounded_regular(path: &Path, maximum_bytes: u64) -> Result<Vec<u8>, CaptureStoreError> {
    let file = open_regular(path)?;
    let metadata = file.metadata()?;
    if metadata.len() > maximum_bytes {
        return Err(CaptureStoreError::Oversized {
            actual: metadata.len(),
            limit: maximum_bytes,
        });
    }
    let mut bytes = Vec::new();
    file.take(maximum_bytes.saturating_add(1))
        .read_to_end(&mut bytes)?;
    if bytes.len() as u64 > maximum_bytes {
        return Err(CaptureStoreError::Oversized {
            actual: bytes.len() as u64,
            limit: maximum_bytes,
        });
    }
    Ok(bytes)
}

#[cfg(unix)]
fn open_regular(path: &Path) -> Result<File, CaptureStoreError> {
    let file = rustix::fs::open(
        path,
        OFlags::RDONLY | OFlags::NOFOLLOW | OFlags::CLOEXEC,
        Mode::empty(),
    )
    .map(File::from)
    .map_err(std::io::Error::from)?;
    let stat = rustix::fs::fstat(&file).map_err(std::io::Error::from)?;
    if !FileType::from_raw_mode(stat.st_mode).is_file() {
        return Err(CaptureStoreError::UnsafePath);
    }
    Ok(file)
}

#[cfg(windows)]
fn open_regular(path: &Path) -> Result<File, CaptureStoreError> {
    let file = OpenOptions::new()
        .read(true)
        .share_mode(FILE_SHARE_READ | FILE_SHARE_WRITE)
        .custom_flags(FILE_FLAG_OPEN_REPARSE_POINT)
        .open(path)?;
    let metadata = file.metadata()?;
    if !metadata.is_file() || metadata.file_attributes() & FILE_ATTRIBUTE_REPARSE_POINT != 0 {
        return Err(CaptureStoreError::UnsafePath);
    }
    Ok(file)
}

#[cfg(unix)]
fn publish_final(incomplete: &Path, final_path: &Path) -> std::io::Result<()> {
    std::fs::hard_link(incomplete, final_path)?;
    File::open(final_path.parent().expect("validated parent"))?.sync_all()?;
    std::fs::remove_file(incomplete)?;
    File::open(final_path.parent().expect("validated parent"))?.sync_all()
}

#[cfg(windows)]
fn publish_final(incomplete: &Path, final_path: &Path) -> std::io::Result<()> {
    std::fs::hard_link(incomplete, final_path)?;
    std::fs::remove_file(incomplete)?;
    sync_windows_regular(final_path)
}

#[cfg(unix)]
fn remove_incomplete_and_sync(incomplete: &Path, final_path: &Path) -> std::io::Result<()> {
    std::fs::remove_file(incomplete)?;
    File::open(final_path.parent().expect("validated parent"))?.sync_all()
}

#[cfg(windows)]
fn remove_incomplete_and_sync(incomplete: &Path, final_path: &Path) -> std::io::Result<()> {
    std::fs::remove_file(incomplete)?;
    sync_windows_regular(final_path)
}

#[cfg(windows)]
const FILE_ATTRIBUTE_REPARSE_POINT: u32 = 0x400;
#[cfg(windows)]
const FILE_FLAG_OPEN_REPARSE_POINT: u32 = 0x0020_0000;
#[cfg(windows)]
const FILE_SHARE_READ: u32 = 0x1;
#[cfg(windows)]
const FILE_SHARE_WRITE: u32 = 0x2;

#[cfg(windows)]
fn sync_windows_regular(path: &Path) -> std::io::Result<()> {
    let file = OpenOptions::new()
        .read(true)
        .write(true)
        .share_mode(FILE_SHARE_READ)
        .custom_flags(FILE_FLAG_OPEN_REPARSE_POINT)
        .open(path)?;
    let metadata = file.metadata()?;
    if !metadata.is_file() || metadata.file_attributes() & FILE_ATTRIBUTE_REPARSE_POINT != 0 {
        return Err(std::io::Error::new(
            std::io::ErrorKind::InvalidInput,
            "capture index is not safe regular data",
        ));
    }
    file.sync_all()
}

#[cfg(test)]
mod tests {
    use super::{CaptureIndexStore, CapturePublication, CaptureStoreError};
    use crate::evidence::digest_bytes;
    use tempfile::TempDir;

    fn store(limit: u64) -> (TempDir, CaptureIndexStore) {
        let root = TempDir::new().expect("capture root");
        let store = CaptureIndexStore::open(root.path().to_path_buf(), limit).expect("store");
        (root, store)
    }

    #[test]
    fn recover_publishes_incomplete_only_state() {
        let (root, store) = store(64);
        std::fs::write(root.path().join(super::INCOMPLETE_NAME), b"capture").expect("incomplete");

        assert_eq!(
            store.recover(&digest_bytes(b"capture")).expect("recover"),
            CapturePublication::Repaired(digest_bytes(b"capture"))
        );
        assert!(!root.path().join(super::INCOMPLETE_NAME).exists());
        assert_eq!(
            std::fs::read(root.path().join(super::FINAL_NAME)).expect("final"),
            b"capture"
        );
    }

    #[test]
    fn recover_accepts_final_only_exact_replay() {
        let (root, store) = store(64);
        std::fs::write(root.path().join(super::FINAL_NAME), b"capture").expect("final");

        assert_eq!(
            store.recover(&digest_bytes(b"capture")).expect("recover"),
            CapturePublication::Published(digest_bytes(b"capture"))
        );
    }

    #[test]
    fn recover_rejects_oversized_bytes_without_removing_them() {
        let (root, store) = store(3);
        let incomplete = root.path().join(super::INCOMPLETE_NAME);
        std::fs::write(&incomplete, b"four").expect("incomplete");

        assert!(matches!(
            store.recover(&digest_bytes(b"four")),
            Err(CaptureStoreError::Oversized {
                actual: 4,
                limit: 3
            })
        ));
        assert_eq!(std::fs::read(incomplete).expect("retained"), b"four");
    }

    #[cfg(unix)]
    #[test]
    fn open_rejects_symlinked_root() {
        let target = TempDir::new().expect("target");
        let parent = TempDir::new().expect("parent");
        let link = parent.path().join("capture-root");
        std::os::unix::fs::symlink(target.path(), &link).expect("root symlink");

        assert!(matches!(
            CaptureIndexStore::open(link, 64),
            Err(CaptureStoreError::UnsafePath)
        ));
    }

    #[cfg(windows)]
    #[test]
    fn open_rejects_symlinked_root() {
        let target = TempDir::new().expect("target");
        let parent = TempDir::new().expect("parent");
        let link = parent.path().join("capture-root");
        std::os::windows::fs::symlink_dir(target.path(), &link).expect("root symlink");

        assert!(matches!(
            CaptureIndexStore::open(link, 64),
            Err(CaptureStoreError::UnsafePath)
        ));
    }

    #[cfg(unix)]
    #[test]
    fn recover_rejects_symlinked_final() {
        let (root, store) = store(64);
        let target = root.path().join("target");
        std::fs::write(&target, b"capture").expect("target");
        std::os::unix::fs::symlink(&target, root.path().join(super::FINAL_NAME))
            .expect("final symlink");

        assert!(matches!(
            store.recover(&digest_bytes(b"capture")),
            Err(CaptureStoreError::UnsafePath)
        ));
    }

    #[cfg(windows)]
    #[test]
    fn recover_rejects_symlinked_final() {
        let (root, store) = store(64);
        let target = root.path().join("target");
        std::fs::write(&target, b"capture").expect("target");
        std::os::windows::fs::symlink_file(&target, root.path().join(super::FINAL_NAME))
            .expect("final symlink");

        assert!(matches!(
            store.recover(&digest_bytes(b"capture")),
            Err(CaptureStoreError::UnsafePath)
        ));
    }

    #[test]
    fn duplicate_publication_preserves_the_existing_final() {
        let (root, store) = store(64);
        store.publish(b"first").expect("first publication");

        assert!(store.publish(b"second").is_err());
        assert_eq!(
            std::fs::read(root.path().join(super::FINAL_NAME)).expect("original final"),
            b"first"
        );
        assert!(!root.path().join(super::INCOMPLETE_NAME).exists());
    }

    #[test]
    fn contradictory_pair_retains_both_original_files() {
        let (root, store) = store(64);
        let final_path = root.path().join(super::FINAL_NAME);
        let incomplete = root.path().join(super::INCOMPLETE_NAME);
        std::fs::write(&final_path, b"final").expect("final");
        std::fs::write(&incomplete, b"different").expect("incomplete");

        assert!(matches!(
            store.recover(&digest_bytes(b"final")),
            Err(CaptureStoreError::Contradictory)
        ));
        assert_eq!(std::fs::read(final_path).expect("final retained"), b"final");
        assert_eq!(
            std::fs::read(incomplete).expect("incomplete retained"),
            b"different"
        );
    }
}
