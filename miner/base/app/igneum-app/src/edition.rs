//! The two builds (review B F14, the founder's ruling of 19:57 UK, 8 October 2026): the PUBLIC miner and the LAB build.
//!
//! The public miner runs no remote-execution jobs at all: no jobs file is fetched, no signing root for jobs exists in
//! it, the Settings switch is not shown, and `POST /api/jobs/allow` is refused. Its update choice is honoured: with
//! automatic updates off, nothing installs until the user presses Install now, an urgent manifest included; an urgent
//! manifest that says this version is unsupported pauses mining and says so (src/ota.rs, src/engine.rs). A manifest's
//! `consensus.override` is never written to the node on the public build: a compromised update key cannot activate a
//! consensus change there; the node's rules come from the node. The lab build (our own fleet) runs the remote jobs
//! under a SEPARATE signing root, `JOBS_LAB_PUBLIC_KEY_HEX`, never the OTA release key, so the update key alone
//! signs no job even on the fleet.
//!
//! The build is chosen at compile time by the cargo feature `lab` (`cargo build --features lab`); the default is the
//! public miner. The lab build's signing root is read from the build environment, never from the repo: the relay
//! lane keeps the pair under $IGNEUM_SECRETS_DIR/lab-signing/ and the lab build is compiled with
//! `IGNEUM_LAB_PUBLIC_KEY=<hex of key.pub>`; the build fails without it. The lab build verifies BOTH its OTA manifest
//! and its jobs feed against that root and refuses a release-signed one; the public build carries the release root
//! only and refuses a lab-signed manifest. Each build reads its own channel (`channel()`) and refuses a manifest of
//! the other, so a lab build never reaches a public machine or the reverse. The engine reports the build as
//! `state.edition` ("public" | "lab") and the window shows it on the About line.

/// True on the lab build, false on the public miner.
pub const LAB: bool = cfg!(feature = "lab");

/// The lab signing root, compiled in from the build environment under the `lab` feature only.
#[cfg(feature = "lab")]
pub const LAB_PUBLIC_KEY_HEX: &str = env!("IGNEUM_LAB_PUBLIC_KEY", "the lab build needs IGNEUM_LAB_PUBLIC_KEY (the hex of $IGNEUM_SECRETS_DIR/lab-signing/key.pub)");

/// "public" or "lab", for state.edition and the log.
pub fn name() -> &'static str {
    if LAB { "lab" } else { "public" }
}

/// The product name the window and the installers show: both builds can sit on one machine.
pub fn product_name() -> &'static str {
    if LAB { "Ember Lab" } else { "Ember" }
}

/// The manifest channel this build reads; a manifest of another channel is refused (src/ota.rs).
pub fn channel() -> &'static str {
    if LAB { "ember-lab" } else { "ember" }
}

/// The channel names this build accepts: its own, and for the public build the older plain name the an earlier release and an earlier release
/// manifests carried, so a public machine on them keeps updating until the publisher renames the channel.
pub fn channel_accepted(manifest_channel: &str) -> bool {
    // "public" is the release store's channel word (the signed pointer of 12 October 2026 carries it)
    manifest_channel == channel() || (!LAB && (manifest_channel.is_empty() || manifest_channel == "devnet" || manifest_channel == "public"))
}

/// The signing root this build verifies its OTA manifest and its interface bundles against.
pub fn ota_key() -> &'static str {
    #[cfg(feature = "lab")]
    {
        LAB_PUBLIC_KEY_HEX
    }
    #[cfg(not(feature = "lab"))]
    {
        crate::manifest::OTA_PUBLIC_KEY_HEX
    }
}

/// The signing root remote jobs are checked against: the lab root on the lab build, none on the public miner (which
/// runs no jobs).
pub fn jobs_key() -> Option<&'static str> {
    #[cfg(feature = "lab")]
    {
        Some(LAB_PUBLIC_KEY_HEX)
    }
    #[cfg(not(feature = "lab"))]
    {
        None
    }
}

/// Whether remote jobs can run on this build at all.
pub fn remote_jobs_possible() -> bool {
    jobs_key().is_some()
}

/// Whether a manifest's consensus override may reach the node: the lab build only.
pub fn manifest_override_allowed() -> bool {
    LAB
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn the_public_build_has_no_jobs_root_and_the_lab_build_has_its_own() {
        if LAB {
            assert_eq!(name(), "lab");
            assert_eq!(product_name(), "Ember Lab");
            assert_eq!(channel(), "ember-lab");
            assert!(channel_accepted("ember-lab"));
            assert!(!channel_accepted("ember"), "a lab build never takes the public channel");
            assert!(!channel_accepted("devnet"));
            assert_ne!(ota_key(), crate::manifest::OTA_PUBLIC_KEY_HEX, "the lab root is not the release root");
            assert_eq!(jobs_key(), Some(ota_key()));
            assert!(crate::manifest::public_key(ota_key()).is_ok());
            assert!(manifest_override_allowed());
        } else {
            assert_eq!(name(), "public");
            assert_eq!(product_name(), "Ember");
            assert_eq!(channel(), "ember");
            assert!(channel_accepted("ember"));
            assert!(channel_accepted("devnet"), "the 0.2.0 and 0.2.1 manifests' name, until the publisher renames it");
            assert!(channel_accepted(""));
            assert!(!channel_accepted("ember-lab"), "a public build never takes the lab channel");
            assert_eq!(ota_key(), crate::manifest::OTA_PUBLIC_KEY_HEX);
            assert_eq!(jobs_key(), None, "the public miner has no signing root for remote jobs");
            assert!(!remote_jobs_possible());
            assert!(!manifest_override_allowed(), "a manifest never activates a consensus change on the public miner");
        }
    }
}
