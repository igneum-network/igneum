// The guest's build and the image id's reproducibility (11 Oct 2026). A native risc0-build embeds the cargo registry's
// absolute path in the guest ELF (the GPU team's finding: one builder's home on a build box and another on a pod gave two ids for one
// guest), so the native id is reproducible only under one recipe: the pinned toolchain (rust 1.97.0, risc0 3.0.6, the
// lock file) with CARGO_HOME under the builder's home, as PI-NOTES.md section 2 records. risc0-build's docker mode (the
// documented host-independent build) needs docker's buildx plugin, absent on the boxes until reliability installs it;
// it is taken when IGNEUM_PI_DOCKER=1 is set, and the id it prints is then a NEW consensus constant (a different
// path inside the container), recorded before any set is generated under it.
use risc0_build::{embed_methods, embed_methods_with_options, DockerOptionsBuilder, GuestOptionsBuilder};
use std::collections::HashMap;

fn main() {
    println!("cargo:rerun-if-env-changed=IGNEUM_PI_DOCKER");
    if std::env::var("IGNEUM_PI_DOCKER").map(|v| v == "1").unwrap_or(false) {
        let docker = DockerOptionsBuilder::default().root_dir("../..".to_string()).build().expect("docker options");
        let guest = GuestOptionsBuilder::default().use_docker(docker).build().expect("guest options");
        embed_methods_with_options(HashMap::from([("pi-guest", guest)]));
    } else {
        embed_methods();
    }
}
