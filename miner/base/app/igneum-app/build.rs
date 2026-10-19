// Build script for igneum-app. On a Windows target it compiles resources/igneum-app.rc (the coin icon Explorer shows
// and the version block under Properties > Details) with windres and links the object into igneum-app.exe. Other
// targets: nothing. the founder's rule (4 October 2026): every shipped exe carries the coin icon and a version block, like the
// Mac app and DMG. No crate dependency: windres is called directly (x86_64-w64-mingw32-windres from Homebrew mingw-w64
// on the Mac, windres from MSYS2 on a PC; IGNEUM_WINDRES names another one).
use std::env;
use std::fs;
use std::path::PathBuf;
use std::process::Command;

fn main() {
    let manifest = PathBuf::from(env::var("CARGO_MANIFEST_DIR").expect("CARGO_MANIFEST_DIR"));
    let rc = manifest.join("resources").join("igneum-app.rc");
    let icons = manifest.join("..").join("..").join("brand").join("icons");
    let ico = icons.join("igneum.ico");
    println!("cargo:rerun-if-changed=build.rs");
    println!("cargo:rerun-if-changed={}", rc.display());
    println!("cargo:rerun-if-changed={}", ico.display());
    println!("cargo:rerun-if-env-changed=IGNEUM_WINDRES");
    if env::var("CARGO_CFG_TARGET_OS").as_deref() != Ok("windows") {
        return;
    }
    // The .rc carries the version by hand; it must agree with Cargo.toml so Properties shows the number the app reports.
    let version = env::var("CARGO_PKG_VERSION").expect("CARGO_PKG_VERSION");
    let text = fs::read_to_string(&rc).unwrap_or_else(|e| panic!("cannot read {}: {e}", rc.display()));
    let numeric = format!("{},0", version.replace('.', ","));
    if !text.contains(&format!("\"{version}\"")) || !text.contains(&numeric) {
        panic!(
            "{} must carry version {version} (FILEVERSION/PRODUCTVERSION {numeric}, FileVersion/ProductVersion \"{version}\")",
            rc.display()
        );
    }
    if !ico.is_file() {
        panic!("no {}; run python3 brand/icons/make-icons.py first", ico.display());
    }
    let windres = match find_windres() {
        Some(w) => w,
        None => {
            if env::var("CARGO_CFG_TARGET_ENV").as_deref() == Ok("msvc") {
                println!("cargo:warning=no windres found: igneum-app.exe is built without the coin icon and version block (set IGNEUM_WINDRES)");
                return;
            }
            panic!("no windres found (brew install mingw-w64, or set IGNEUM_WINDRES): igneum-app.exe must carry the coin icon and version block");
        }
    };
    let out = PathBuf::from(env::var("OUT_DIR").expect("OUT_DIR")).join("igneum-app.res.o");
    let status = Command::new(&windres)
        .arg("-I").arg(&icons)
        .arg("-i").arg(&rc)
        .arg("-O").arg("coff")
        .arg("-o").arg(&out)
        .status()
        .unwrap_or_else(|e| panic!("cannot run {windres}: {e}"));
    if !status.success() {
        panic!("{windres} failed on {} ({status})", rc.display());
    }
    println!("cargo:rustc-link-arg-bins={}", out.display());
}

fn find_windres() -> Option<String> {
    let mut candidates = Vec::new();
    if let Ok(w) = env::var("IGNEUM_WINDRES") {
        if !w.is_empty() {
            candidates.push(w);
        }
    }
    let prefix = match env::var("CARGO_CFG_TARGET_ARCH").unwrap_or_default().as_str() {
        "x86_64" => Some("x86_64-w64-mingw32"),
        "x86" => Some("i686-w64-mingw32"),
        "aarch64" => Some("aarch64-w64-mingw32"),
        _ => None,
    };
    if let Some(p) = prefix {
        candidates.push(format!("{p}-windres"));
    }
    candidates.push("windres".to_string());
    candidates
        .into_iter()
        .find(|c| Command::new(c).arg("--version").output().map(|o| o.status.success()).unwrap_or(false))
}
