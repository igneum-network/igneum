// Builds pow/ref with CMake (GPU engineer A's recipe: configure, build, install) and links libignpow.a and
// librvn_ethash.a in that order plus the C++ standard library. The source is `pow/ref` at the repository root
// (three levels above this crate), or `IGNPOW_SRC` when set (a checkout of gpu-a's branch on a build box).
//
use std::path::PathBuf;

fn main() {
    println!("cargo:rerun-if-env-changed=IGNPOW_SRC");
    let src = std::env::var("IGNPOW_SRC").map(PathBuf::from).unwrap_or_else(|_| {
        let here = PathBuf::from(std::env::var("CARGO_MANIFEST_DIR").expect("manifest dir"));
        here.join("../../../pow/ref").canonicalize().unwrap_or_else(|_| here.join("../../../pow/ref"))
    });
    if !src.join("CMakeLists.txt").is_file() {
        panic!("igneum-pow-sys: the proof-of-work reference is not at {} (pow/ref at the repository root, or IGNPOW_SRC)", src.display());
    }
    println!("cargo:rerun-if-changed={}", src.display());
    let target = std::env::var("TARGET").unwrap_or_default();
    // IGNPOW_PREBUILT: a directory holding libignpow.a (and any other static library of the reference) built
    // outside cargo, for a host without cmake: the mini's command line tools build the reference with clang alone
    // (pow/harness/orchard-build.sh's recipe, reused by pow/harness/ember-mac-build.sh, 11 October 2026); the
    // link order and the C++ runtime line are the same as the cmake path's
    println!("cargo:rerun-if-env-changed=IGNPOW_PREBUILT");
    if let Ok(pre) = std::env::var("IGNPOW_PREBUILT") {
        let dir = PathBuf::from(&pre);
        if !dir.join("libignpow.a").is_file() {
            panic!("igneum-pow-sys: IGNPOW_PREBUILT={pre} holds no libignpow.a");
        }
        println!("cargo:rerun-if-changed={}", dir.join("libignpow.a").display());
        println!("cargo:rustc-link-search=native={}", dir.display());
        let mut libs = static_libs_in(&[dir.clone()]);
        libs.sort();
        libs.sort_by_key(|l| if l == "ignpow" { 0 } else { 1 });
        for l in &libs {
            println!("cargo:rustc-link-lib=static={l}");
        }
        link_cxx_runtime(&target);
        println!("cargo:include={}", src.join("include").display());
        return;
    }
    let mut cfg = cmake::Config::new(&src);
    cfg.profile("Release");
    if target.contains("windows") {
        // the reference's tools (benches, vector generators) are POSIX-only (sys/resource.h, open_memstream);
        // a Windows build wants the library and its dependencies alone (the MinGW cross-build of ember.exe,
        // 11 October 2026)
        cfg.build_target("ignpow");
    }
    let dst = cfg.build();
    // every static library the reference produces: libignpow.a first, then its dependencies (rvn_ethash, blake3,
    // and whatever a later revision adds), from the install directory with the build directory as the fallback
    println!("cargo:rustc-link-search=native={}", dst.join("lib").display());
    println!("cargo:rustc-link-search=native={}", dst.join("build").display());
    let mut libs = static_libs_in(&[dst.join("lib"), dst.join("build")]);
    // a fixed link order: ignpow first, the rest by name (read_dir's order differs between filesystems, and a
    // different link order is a different binary: the two-builder rule, the Linux builder against the second Linux builder, 11 October 2026)
    libs.sort();
    libs.sort_by_key(|l| if l == "ignpow" { 0 } else { 1 });
    if !libs.iter().any(|l| l == "ignpow") {
        panic!("igneum-pow-sys: the reference build produced no libignpow.a under {}", dst.display());
    }
    for l in &libs {
        println!("cargo:rustc-link-lib=static={l}");
    }
    link_cxx_runtime(&target);
    println!("cargo:include={}", dst.join("include").display());
}

/// The stems of every `lib<stem>.a` under the directories given, each once, in the order found (the caller sorts).
fn static_libs_in(dirs: &[PathBuf]) -> Vec<String> {
    let mut libs: Vec<String> = Vec::new();
    for dir in dirs {
        if let Ok(rd) = std::fs::read_dir(dir) {
            for e in rd.flatten() {
                let name = e.file_name().to_string_lossy().to_string();
                if let Some(stem) = name.strip_prefix("lib").and_then(|n| n.strip_suffix(".a")) {
                    if !libs.iter().any(|l| l == stem) {
                        libs.push(stem.to_string());
                    }
                }
            }
        }
    }
    libs
}

/// The C++ runtime the reference's objects want on this target.
fn link_cxx_runtime(target: &str) {
    if target.contains("apple") {
        println!("cargo:rustc-link-lib=c++");
    } else if target.contains("windows-msvc") {
        // the MSVC runtime links the C++ standard library itself
    } else if target.contains("windows-gnu") {
        // the MinGW cross-build: libstdc++ linked static, so ember.exe wants no libstdc++-6.dll on the user's
        // machine (the second Windows machine, 11 October 2026: STATUS_DLL_NOT_FOUND on the first launch); the archive lives in the
        // cross compiler's own library directory, which the compiler names
        let cc = std::env::var("CC_x86_64_pc_windows_gnu").unwrap_or_else(|_| "x86_64-w64-mingw32-gcc".into());
        if let Ok(out) = std::process::Command::new(&cc).arg("-print-file-name=libstdc++.a").output() {
            let p = String::from_utf8_lossy(&out.stdout).trim().to_string();
            if let Some(dir) = std::path::Path::new(&p).parent() {
                println!("cargo:rustc-link-search=native={}", dir.display());
            }
        }
        println!("cargo:rustc-link-lib=static=stdc++");
    } else {
        println!("cargo:rustc-link-lib=stdc++");
    }
}
