//! F-770: the workspace pins its Rust toolchain (../rust-toolchain.toml), to the version the Money builds used
//! (rustc 1.99.0), because the node's verifier library bytes depend on the compiler.

#[test]
fn the_toolchain_is_pinned() {
    let f = std::fs::read_to_string(concat!(env!("CARGO_MANIFEST_DIR"), "/../rust-toolchain.toml")).expect("rust-toolchain.toml");
    assert!(f.contains("channel = \"1.99.0\""), "{f}");
}
