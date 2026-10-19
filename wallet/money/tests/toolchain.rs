//! F-770: the crate pins its Rust toolchain (rust-toolchain.toml), to the version the Money builds used.

#[test]
fn the_toolchain_is_pinned() {
    let f = std::fs::read_to_string(concat!(env!("CARGO_MANIFEST_DIR"), "/rust-toolchain.toml")).expect("rust-toolchain.toml");
    assert!(f.contains("channel = \"1.99.0\""), "{f}");
}
