//! The OS memory-pressure level, for the running guard on the 8 GB class (security's headroom exception, the
//! Apple auditor's gate-30, 11 October 2026): Ember pauses mining at the warning level, releasing the dataset and
//! keeping the cache, and resumes at normal. macOS reads `kern.memorystatus_vm_pressure_level` (the same source
//! the memory seat's harness reads: 1 normal, 2 warning, 4 critical); Linux and Windows have no reading yet and
//! say so (their equivalents, labelled, follow).

#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub enum Level {
    Normal,
    Warning,
    Critical,
}

impl Level {
    pub fn word(self) -> &'static str {
        match self {
            Level::Normal => "normal",
            Level::Warning => "warning",
            Level::Critical => "critical",
        }
    }
}

/// The level from the OS, or None where there is no reading on this platform (never a guess).
pub fn level() -> Option<Level> {
    read_level()
}

#[cfg(target_os = "macos")]
fn read_level() -> Option<Level> {
    extern "C" {
        fn sysctlbyname(name: *const std::os::raw::c_char, oldp: *mut std::os::raw::c_void, oldlenp: *mut usize, newp: *mut std::os::raw::c_void, newlen: usize) -> std::os::raw::c_int;
    }
    let name = b"kern.memorystatus_vm_pressure_level\0";
    let mut v: i32 = 0;
    let mut len = std::mem::size_of::<i32>();
    // SAFETY: a documented sysctl read into an i32 of the stated length; nothing is written back
    let rc = unsafe { sysctlbyname(name.as_ptr() as *const _, &mut v as *mut i32 as *mut _, &mut len, std::ptr::null_mut(), 0) };
    if rc != 0 {
        return None;
    }
    Some(match v {
        1 => Level::Normal,
        2 => Level::Warning,
        4 => Level::Critical,
        _ => return None,
    })
}

#[cfg(not(target_os = "macos"))]
fn read_level() -> Option<Level> {
    None
}

/// The platform's word for the state line and the record.
pub fn source_name() -> &'static str {
    if cfg!(target_os = "macos") {
        "kern.memorystatus_vm_pressure_level"
    } else {
        "no memory-pressure reading on this platform yet"
    }
}
