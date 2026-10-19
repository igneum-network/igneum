//! The saved block card (miner-ui-5): the page draws the 1200x630 PNG on a canvas from the same words the card
//! shows (no network call), posts it as a data URL to POST /api/card, and the engine writes it under
//! `<data root>/cards/`. This module is the pure part: the base64 decode (no crate for it) and the file name.
use std::path::{Path, PathBuf};

/// `data:image/png;base64,....` -> the PNG bytes; None for anything that is not a base64 PNG data URL.
pub fn decode_data_url(s: &str) -> Option<Vec<u8>> {
    let rest = s.strip_prefix("data:image/png;base64,")?;
    let bytes = decode_base64(rest)?;
    if bytes.len() < 8 || bytes[..8] != [0x89, b'P', b'N', b'G', 0x0D, 0x0A, 0x1A, 0x0A] {
        return None;
    }
    Some(bytes)
}

/// Standard base64 (RFC 4648, with or without `=` padding, whitespace ignored). None on a bad character.
pub fn decode_base64(s: &str) -> Option<Vec<u8>> {
    let mut out = Vec::with_capacity(s.len() * 3 / 4);
    let mut acc: u32 = 0;
    let mut bits = 0u32;
    for c in s.bytes() {
        let v = match c {
            b'A'..=b'Z' => c - b'A',
            b'a'..=b'z' => c - b'a' + 26,
            b'0'..=b'9' => c - b'0' + 52,
            b'+' | b'-' => 62,
            b'/' | b'_' => 63,
            b'=' | b'\n' | b'\r' | b' ' | b'\t' => continue,
            _ => return None,
        } as u32;
        acc = (acc << 6) | v;
        bits += 6;
        if bits >= 8 {
            bits -= 8;
            out.push(((acc >> bits) & 0xFF) as u8);
        }
    }
    Some(out)
}

/// `<root>/cards/igneum-block-<name>-<unix>.png`, the name reduced to [a-z0-9-] and at most 40 characters.
pub fn card_path(root: &Path, name: &str, unix: u64) -> PathBuf {
    let mut clean = String::new();
    for c in name.to_ascii_lowercase().chars() {
        if c.is_ascii_alphanumeric() { clean.push(c); } else if !clean.ends_with('-') { clean.push('-'); }
    }
    let clean: String = clean.trim_matches('-').chars().take(40).collect::<String>().trim_end_matches('-').to_string();
    let clean = if clean.is_empty() { "block".to_string() } else { clean };
    root.join("cards").join(format!("igneum-block-{clean}-{unix}.png"))
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn base64_decodes_with_and_without_padding() {
        assert_eq!(decode_base64("aGVsbG8=").unwrap(), b"hello");
        assert_eq!(decode_base64("aGVsbG8").unwrap(), b"hello");
        assert_eq!(decode_base64("aGVs\nbG8gd29ybGQ=").unwrap(), b"hello world");
        assert!(decode_base64("aGV$sbG8=").is_none());
    }

    #[test]
    fn a_png_data_url_decodes_and_anything_else_is_refused() {
        // the 8-byte PNG signature, base64
        let sig = "iVBORw0KGgo=";
        let png = decode_data_url(&format!("data:image/png;base64,{sig}")).unwrap();
        assert_eq!(&png[..4], b"\x89PNG");
        assert!(decode_data_url("data:image/jpeg;base64,/9j/").is_none());
        assert!(decode_data_url("data:image/png;base64,aGVsbG8=").is_none(), "not a PNG");
    }

    #[test]
    fn the_file_name_is_safe_and_dated() {
        let p = card_path(Path::new("/data"), "Block 1,284,117 / RTX 4070", 1_791_362_116);
        assert_eq!(p, PathBuf::from("/data/cards/igneum-block-block-1-284-117-rtx-4070-1791362116.png"));
        assert_eq!(card_path(Path::new("/d"), "///", 1).file_name().unwrap(), "igneum-block-block-1.png");
    }
}
