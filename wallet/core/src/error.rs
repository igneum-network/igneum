use std::fmt;

/// Every failure in the core, in words the window can show.
#[derive(Debug)]
pub enum Error {
    /// malformed input (hex, address, number, path)
    Input(String),
    /// a key, signature or derivation problem
    Key(String),
    /// the vault could not be sealed or opened (wrong password, corrupt file)
    Vault(String),
    /// the node answered badly or not at all
    Rpc(String),
    /// the signer refused: watch-only, a display mismatch, a disallowed request
    Refused(String),
    Io(std::io::Error),
    Json(serde_json::Error),
}

impl fmt::Display for Error {
    fn fmt(&self, f: &mut fmt::Formatter<'_>) -> fmt::Result {
        match self {
            Error::Input(s) => write!(f, "{s}"),
            Error::Key(s) => write!(f, "{s}"),
            Error::Vault(s) => write!(f, "{s}"),
            Error::Rpc(s) => write!(f, "node: {s}"),
            Error::Refused(s) => write!(f, "refused: {s}"),
            Error::Io(e) => write!(f, "{e}"),
            Error::Json(e) => write!(f, "{e}"),
        }
    }
}

impl std::error::Error for Error {}

impl From<std::io::Error> for Error {
    fn from(e: std::io::Error) -> Self {
        Error::Io(e)
    }
}

impl From<serde_json::Error> for Error {
    fn from(e: serde_json::Error) -> Self {
        Error::Json(e)
    }
}

pub type Result<T> = std::result::Result<T, Error>;
