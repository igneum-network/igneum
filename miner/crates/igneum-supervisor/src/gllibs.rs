//! The raster lane's GL libraries on Linux (the project's order of 15:2x UK, 15 October 2026: the rig carries NVIDIA's kernel
//! driver and CUDA but no EGL or GL vendor library, and the raster lane cannot open a context without one; a Linux
//! miner's machine will often be the same). Before a card is relaunched on the GL raster worker the engine looks for
//! the GL vendor library and the EGL loader; when one is absent it names the package to install in the card's state
//! words and the log and keeps the card on the other lanes (the coordinate refused for the card, never a fatal close).
//! On Windows and macOS the libraries ship with the driver and nothing is checked.

/// The library directories a Linux host's loader searches (the multiarch and the classic ones).
pub const LIB_DIRS: &[&str] = &["/usr/lib/x86_64-linux-gnu", "/usr/lib64", "/usr/lib", "/lib/x86_64-linux-gnu", "/lib64", "/lib", "/usr/lib/aarch64-linux-gnu", "/usr/local/lib"];

/// What the raster worker needs: the EGL loader and the GL dispatch (glvnd), and the vendor's EGL ICD.
#[derive(Debug, PartialEq, Eq)]
pub struct GlNeed {
    /// the files looked for (any directory of LIB_DIRS, or the ICD json under /usr/share/glvnd/egl_vendor.d)
    pub missing: Vec<&'static str>,
    /// the package words for the host's distribution
    pub package: String,
}

/// The distribution id from /etc/os-release text ("ubuntu", "debian", "fedora", "arch", ...).
pub fn distro_id(os_release: &str) -> String {
    for line in os_release.lines() {
        if let Some(v) = line.strip_prefix("ID=") {
            return v.trim().trim_matches('"').to_ascii_lowercase();
        }
    }
    String::new()
}

/// The package that carries the GL vendor library for the card's vendor on the distribution; the driver's major
/// version names NVIDIA's Debian and Ubuntu package (libnvidia-gl-<major>).
pub fn package_for(vendor: &str, driver: &str, distro: &str) -> String {
    let major: String = driver.trim().chars().take_while(|c| c.is_ascii_digit()).collect();
    let debian_like = matches!(distro, "ubuntu" | "debian" | "linuxmint" | "pop" | "hiveos" | "neon" | "zorin" | "elementary") || distro.is_empty();
    match vendor {
        "nvidia" => {
            if debian_like {
                if major.is_empty() { "libnvidia-gl-<driver major> libegl1 libgl1 libglvnd0 (the driver's own version; nvidia-smi names it)".into() } else { format!("libnvidia-gl-{major} libegl1 libgl1 libglvnd0") }
            } else if matches!(distro, "fedora" | "rhel" | "centos" | "rocky" | "almalinux") {
                "xorg-x11-drv-nvidia-libs libglvnd-egl libglvnd-gles".into()
            } else if matches!(distro, "arch" | "manjaro" | "endeavouros") {
                "nvidia-utils libglvnd".into()
            } else {
                "the NVIDIA GL and EGL libraries of the driver (libEGL_nvidia.so.0 with its glvnd ICD) and libglvnd".into()
            }
        }
        "amd" | "intel" => {
            if debian_like { "libegl-mesa0 libgl1-mesa-dri libegl1 libgl1".into() }
            else if matches!(distro, "fedora" | "rhel" | "centos" | "rocky" | "almalinux") { "mesa-libEGL mesa-dri-drivers".into() }
            else if matches!(distro, "arch" | "manjaro" | "endeavouros") { "mesa".into() }
            else { "Mesa's EGL and GL libraries (libEGL_mesa.so.0 with its glvnd ICD)".into() }
        }
        _ => "the card vendor's EGL and GL libraries with their glvnd ICD".into(),
    }
}

/// The files the raster worker needs for the vendor: the glvnd loader pair and the vendor ICD (either the library
/// or its json in /usr/share/glvnd/egl_vendor.d). The vendor ICD is one entry naming both forms, found when either is.
pub fn needed_files(vendor: &str) -> Vec<(&'static str, &'static [&'static str])> {
    let mut v: Vec<(&'static str, &'static [&'static str])> = vec![("libEGL.so.1", &["libEGL.so.1"]), ("libGL.so.1", &["libGL.so.1"])];
    match vendor {
        "nvidia" => v.push(("the NVIDIA EGL ICD (libEGL_nvidia.so.0, 10_nvidia.json)", &["libEGL_nvidia.so.0", "/usr/share/glvnd/egl_vendor.d/10_nvidia.json"])),
        "amd" | "intel" => v.push(("Mesa's EGL ICD (libEGL_mesa.so.0, 50_mesa.json)", &["libEGL_mesa.so.0", "/usr/share/glvnd/egl_vendor.d/50_mesa.json"])),
        _ => {}
    }
    v
}

/// The check against a probe (`exists(path)` for an absolute path, `exists(name)` searched over LIB_DIRS for a bare
/// name): None when every needed file is present, else the missing names and the package words.
pub fn check_with<F: Fn(&str) -> bool>(vendor: &str, driver: &str, os_release: &str, exists: F) -> Option<GlNeed> {
    let found = |name: &str| -> bool {
        if name.starts_with('/') { return exists(name); }
        LIB_DIRS.iter().any(|d| exists(&format!("{d}/{name}")))
    };
    let missing: Vec<&'static str> = needed_files(vendor).into_iter().filter(|(_, forms)| !forms.iter().any(|f| found(f))).map(|(label, _)| label).collect();
    if missing.is_empty() { return None; }
    Some(GlNeed { missing, package: package_for(vendor, driver, &distro_id(os_release)) })
}

/// The host check on Linux (the files on this machine); always None elsewhere (the libraries ship with the driver).
pub fn check(vendor: &str, driver: &str) -> Option<GlNeed> {
    if !cfg!(target_os = "linux") { return None; }
    let os_release = std::fs::read_to_string("/etc/os-release").unwrap_or_default();
    check_with(vendor, driver, &os_release, |p| std::path::Path::new(p).exists())
}

/// The words for the card's state and the log.
pub fn words(need: &GlNeed) -> String {
    format!("the raster lane needs the GL and EGL libraries ({} missing): install {}; the card mines the other lanes meanwhile", need.missing.join(", "), need.package)
}

#[cfg(test)]
mod tests {
    use super::*;
    const UBUNTU: &str = "NAME=\"Ubuntu\"\nID=ubuntu\nVERSION_ID=\"24.04\"\n";
    #[test]
    fn rig_1_s_case_names_the_nvidia_package_with_the_driver_major_and_keeps_the_card_on_the_other_lanes() {
        // the kernel driver and CUDA present, no GL vendor library, no glvnd: all three missing
        let need = check_with("nvidia", "570.133.07", UBUNTU, |_| false).unwrap();
        assert_eq!(need.missing.len(), 3);
        assert_eq!(need.package, "libnvidia-gl-570 libegl1 libgl1 libglvnd0");
        assert!(words(&need).starts_with("the raster lane needs the GL and EGL libraries ("));
        assert!(words(&need).contains("install libnvidia-gl-570 libegl1 libgl1 libglvnd0; the card mines the other lanes meanwhile"));
        // glvnd present, the vendor ICD absent: one missing, the same package
        let need = check_with("nvidia", "570.133.07", UBUNTU, |p| p.ends_with("/libEGL.so.1") || p.ends_with("/libGL.so.1")).unwrap();
        assert_eq!(need.missing, vec!["the NVIDIA EGL ICD (libEGL_nvidia.so.0, 10_nvidia.json)"]);
        // the ICD json alone satisfies the vendor entry
        assert!(check_with("nvidia", "570.1", UBUNTU, |p| p.ends_with("/libEGL.so.1") || p.ends_with("/libGL.so.1") || p == "/usr/share/glvnd/egl_vendor.d/10_nvidia.json").is_none());
        // everything present: nothing to say
        assert!(check_with("nvidia", "570.1", UBUNTU, |_| true).is_none());
    }
    #[test]
    fn the_package_follows_the_distribution_and_the_vendor() {
        assert_eq!(package_for("nvidia", "550.54", "debian"), "libnvidia-gl-550 libegl1 libgl1 libglvnd0");
        assert_eq!(package_for("nvidia", "", "ubuntu"), "libnvidia-gl-<driver major> libegl1 libgl1 libglvnd0 (the driver's own version; nvidia-smi names it)");
        assert_eq!(package_for("nvidia", "550.54", "fedora"), "xorg-x11-drv-nvidia-libs libglvnd-egl libglvnd-gles");
        assert_eq!(package_for("nvidia", "550.54", "arch"), "nvidia-utils libglvnd");
        assert_eq!(package_for("amd", "", "ubuntu"), "libegl-mesa0 libgl1-mesa-dri libegl1 libgl1");
        assert_eq!(package_for("intel", "", "fedora"), "mesa-libEGL mesa-dri-drivers");
        assert_eq!(distro_id("ID=\"fedora\"\nVERSION=40"), "fedora");
        assert_eq!(distro_id(""), "");
    }
}
