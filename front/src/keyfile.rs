//! The iroh secret key kept on disk, private to its owner (0600). Both the
//! front and the computer's link keep one key across restarts: the front's
//! id is what computers accept, and the link's id is the person's hostname.

use std::path::Path;

use anyhow::Context;
use iroh::SecretKey;

/// Reads the key at `path`, or makes one there when it is missing.
pub fn load_or_make_key(path: &Path) -> anyhow::Result<SecretKey> {
    match std::fs::read(path) {
        Ok(bytes) => {
            let bytes: [u8; 32] = bytes
                .try_into()
                .map_err(|_| anyhow::anyhow!("{} is not a 32-byte key", path.display()))?;
            Ok(SecretKey::from_bytes(&bytes))
        }
        Err(err) if err.kind() == std::io::ErrorKind::NotFound => {
            let key = SecretKey::generate();
            write_private(path, &key.to_bytes())?;
            Ok(key)
        }
        Err(err) => Err(err).with_context(|| format!("reading {}", path.display())),
    }
}

#[cfg(unix)]
pub(crate) fn write_private(path: &Path, data: &[u8]) -> anyhow::Result<()> {
    use std::io::Write;
    use std::os::unix::fs::OpenOptionsExt;
    let mut f = std::fs::OpenOptions::new()
        .write(true)
        .create_new(true)
        .mode(0o600)
        .open(path)
        .with_context(|| format!("creating {}", path.display()))?;
    f.write_all(data)?;
    Ok(())
}

#[cfg(not(unix))]
pub(crate) fn write_private(path: &Path, data: &[u8]) -> anyhow::Result<()> {
    std::fs::write(path, data).with_context(|| format!("creating {}", path.display()))
}
