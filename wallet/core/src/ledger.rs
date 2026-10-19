//! The Ledger path (docs/product-b/DESIGN.md, 1.8): the Ethereum app's APDUs and the HID framing, as pure byte
//! functions over a `Transport` the host supplies. No device is needed to test the layouts; the device run is a
//! separate record. Blind signing is never requested: a transaction the device cannot show in clear is refused
//! by the device itself, and this wallet does not enable the setting for the person.
//!
//! Commands (the Ethereum app): CLA 0xE0; GET PUBLIC KEY 0x02; SIGN TRANSACTION 0x04 (chunked, the first chunk
//! carries the path); SIGN PERSONAL MESSAGE 0x08 (chunked, the first chunk carries the path and the length);
//! SIGN EIP-712 (hashed domain and message) 0x0C. The path is `count || index0 || index1 ...`, 4-byte big-endian
//! indices with the hardened bit set.

use crate::{sign::Signature, Address, Error, Result};

pub const CLA: u8 = 0xE0;
pub const INS_GET_PUBLIC_KEY: u8 = 0x02;
pub const INS_SIGN_TRANSACTION: u8 = 0x04;
pub const INS_SIGN_PERSONAL_MESSAGE: u8 = 0x08;
pub const INS_SIGN_EIP712_HASHED: u8 = 0x0C;
pub const P1_FIRST: u8 = 0x00;
pub const P1_MORE: u8 = 0x80;
pub const CHUNK: usize = 255;

/// The transport the host supplies: one APDU in, the response data and the two status bytes out.
pub trait Transport {
    fn exchange(&mut self, apdu: &[u8]) -> Result<(Vec<u8>, u16)>;
}

/// "m/44'/60'/0'/0/i" to the app's path bytes.
pub fn path_bytes(path: &str) -> Result<Vec<u8>> {
    let t = path.trim().strip_prefix("m/").ok_or_else(|| Error::Input("a path starts with m/".into()))?;
    let parts: Vec<&str> = t.split('/').collect();
    if parts.is_empty() || parts.len() > 10 {
        return Err(Error::Input("a path has 1 to 10 levels".into()));
    }
    let mut out = vec![parts.len() as u8];
    for p in parts {
        let (num, hard) = match p.strip_suffix('\'').or_else(|| p.strip_suffix('h')) {
            Some(n) => (n, true),
            None => (p, false),
        };
        let v: u32 = num.parse().map_err(|_| Error::Input(format!("bad path level {p}")))?;
        if v >= 0x8000_0000 {
            return Err(Error::Input(format!("path level {p} too large")));
        }
        out.extend_from_slice(&(if hard { v | 0x8000_0000 } else { v }).to_be_bytes());
    }
    Ok(out)
}

fn apdu(ins: u8, p1: u8, p2: u8, data: &[u8]) -> Vec<u8> {
    let mut a = vec![CLA, ins, p1, p2, data.len() as u8];
    a.extend_from_slice(data);
    a
}

/// GET PUBLIC KEY: `display` asks the device to show the address for confirmation.
pub fn apdu_get_public_key(path: &str, display: bool) -> Result<Vec<u8>> {
    Ok(apdu(INS_GET_PUBLIC_KEY, if display { 0x01 } else { 0x00 }, 0x00, &path_bytes(path)?))
}

/// The APDUs of a chunked signing command: the path then the payload, 255 bytes per APDU.
fn chunked(ins: u8, path: &str, payload: &[u8]) -> Result<Vec<Vec<u8>>> {
    let mut data = path_bytes(path)?;
    data.extend_from_slice(payload);
    let mut out = Vec::new();
    let mut first = true;
    for chunk in data.chunks(CHUNK) {
        out.push(apdu(ins, if first { P1_FIRST } else { P1_MORE }, 0x00, chunk));
        first = false;
    }
    Ok(out)
}

/// SIGN TRANSACTION over the signing payload (the RLP of the unsigned transaction, with the type byte for a
/// typed transaction).
pub fn apdus_sign_transaction(path: &str, signing_payload: &[u8]) -> Result<Vec<Vec<u8>>> {
    chunked(INS_SIGN_TRANSACTION, path, signing_payload)
}

/// SIGN PERSONAL MESSAGE: the message's length (4 bytes big-endian) then the message.
pub fn apdus_sign_personal_message(path: &str, message: &[u8]) -> Result<Vec<Vec<u8>>> {
    let mut payload = (message.len() as u32).to_be_bytes().to_vec();
    payload.extend_from_slice(message);
    chunked(INS_SIGN_PERSONAL_MESSAGE, path, &payload)
}

/// SIGN EIP-712 with the hashed domain separator and struct hash (every Ethereum app version supports it; the
/// full typed-data flow of newer apps is a later addition).
pub fn apdu_sign_eip712_hashed(path: &str, domain_separator: &[u8; 32], struct_hash: &[u8; 32]) -> Result<Vec<u8>> {
    let mut data = path_bytes(path)?;
    data.extend_from_slice(domain_separator);
    data.extend_from_slice(struct_hash);
    Ok(apdu(INS_SIGN_EIP712_HASHED, 0x00, 0x00, &data))
}

/// The response of GET PUBLIC KEY: `len || pubkey(65) || len || address(40 ascii)`.
pub fn parse_public_key_response(data: &[u8]) -> Result<(Vec<u8>, Address)> {
    if data.len() < 1 + 65 + 1 + 40 {
        return Err(Error::Key("short GET PUBLIC KEY response".into()));
    }
    let klen = data[0] as usize;
    if klen != 65 || data.len() < 1 + klen + 1 {
        return Err(Error::Key("unexpected public key length".into()));
    }
    let key = data[1..1 + klen].to_vec();
    let alen = data[1 + klen] as usize;
    let start = 2 + klen;
    let addr_text = std::str::from_utf8(data.get(start..start + alen).ok_or_else(|| Error::Key("short address".into()))?)
        .map_err(|_| Error::Key("address is not ascii".into()))?;
    let address = Address::parse(&format!("0x{addr_text}"))?;
    Ok((key, address))
}

/// The response of a signing command: `v || r(32) || s(32)`. For a transaction the app returns v as the EIP-155
/// value (or its low byte for large chain ids); `chain_id` recovers the parity.
pub fn parse_signature_response(data: &[u8], chain_id: Option<u64>) -> Result<Signature> {
    if data.len() < 65 {
        return Err(Error::Key("short signature response".into()));
    }
    let v = data[0];
    let mut r = [0u8; 32];
    let mut s = [0u8; 32];
    r.copy_from_slice(&data[1..33]);
    s.copy_from_slice(&data[33..65]);
    let recovery_id = match chain_id {
        // EIP-155: v = chain_id * 2 + 35 + parity, truncated to one byte by the app
        Some(c) => {
            let base = (c.wrapping_mul(2).wrapping_add(35)) & 0xff;
            let parity = v.wrapping_sub(base as u8);
            if parity > 1 {
                // typed transactions return the bare parity
                if v <= 1 { v } else { return Err(Error::Key(format!("unexpected v {v} for chain {c}"))); }
            } else {
                parity
            }
        }
        None => match v {
            0 | 1 => v,
            27 | 28 => v - 27,
            _ => return Err(Error::Key(format!("unexpected v {v}"))),
        },
    };
    Ok(Signature { r, s, recovery_id })
}

/// The status words that matter to the person, in words.
pub fn status_words(sw: u16) -> Result<()> {
    match sw {
        0x9000 => Ok(()),
        0x6985 => Err(Error::Refused("declined on the device".into())),
        0x6a80 | 0x6a84 => Err(Error::Refused("the device could not read this request; it may need blind signing, which this wallet never turns on".into())),
        0x6b00 | 0x6d00 | 0x6e00 => Err(Error::Refused("the Ethereum app is not open on the device".into())),
        0x5515 => Err(Error::Refused("the device is locked".into())),
        0x6511 | 0x6e01 => Err(Error::Refused("open the Ethereum app on the device".into())),
        other => Err(Error::Refused(format!("the device answered {other:#06x}"))),
    }
}

/// HID framing (the Ledger transport): 64-byte reports, channel 0x0101, tag 0x05, a 2-byte sequence, and the
/// payload length in the first report.
pub const HID_REPORT: usize = 64;
pub const HID_CHANNEL: [u8; 2] = [0x01, 0x01];
pub const HID_TAG: u8 = 0x05;

pub fn hid_frames(apdu: &[u8]) -> Vec<[u8; HID_REPORT]> {
    let mut frames = Vec::new();
    let mut seq: u16 = 0;
    let mut payload = (apdu.len() as u16).to_be_bytes().to_vec();
    payload.extend_from_slice(apdu);
    let mut offset = 0;
    while offset < payload.len() || (seq == 0 && payload.is_empty()) {
        let mut f = [0u8; HID_REPORT];
        f[0] = HID_CHANNEL[0];
        f[1] = HID_CHANNEL[1];
        f[2] = HID_TAG;
        f[3..5].copy_from_slice(&seq.to_be_bytes());
        let room = HID_REPORT - 5;
        let end = (offset + room).min(payload.len());
        f[5..5 + (end - offset)].copy_from_slice(&payload[offset..end]);
        frames.push(f);
        offset = end;
        seq += 1;
        if payload.is_empty() {
            break;
        }
    }
    frames
}

/// Reassembles a response from its reports; returns (data, status) once the announced length has arrived.
pub fn hid_unframe(reports: &[[u8; HID_REPORT]]) -> Result<Option<(Vec<u8>, u16)>> {
    let mut payload = Vec::new();
    let mut expected: Option<usize> = None;
    for (i, r) in reports.iter().enumerate() {
        if r[0..2] != HID_CHANNEL || r[2] != HID_TAG {
            return Err(Error::Key("bad HID report header".into()));
        }
        let seq = u16::from_be_bytes([r[3], r[4]]) as usize;
        if seq != i {
            return Err(Error::Key("HID reports out of order".into()));
        }
        if i == 0 {
            expected = Some(u16::from_be_bytes([r[5], r[6]]) as usize);
            payload.extend_from_slice(&r[7..]);
        } else {
            payload.extend_from_slice(&r[5..]);
        }
    }
    let n = match expected {
        Some(n) => n,
        None => return Ok(None),
    };
    if payload.len() < n {
        return Ok(None);
    }
    let body = &payload[..n];
    if body.len() < 2 {
        return Err(Error::Key("short APDU response".into()));
    }
    let sw = u16::from_be_bytes([body[body.len() - 2], body[body.len() - 1]]);
    Ok(Some((body[..body.len() - 2].to_vec(), sw)))
}

/// A signer over a transport: the address from the device, every signature from the device.
pub struct LedgerSigner<T: Transport> {
    pub transport: T,
    pub path: String,
    pub address: Address,
}

impl<T: Transport> LedgerSigner<T> {
    /// Reads the address at `path` (shown on the device when `display`).
    pub fn open(mut transport: T, path: &str, display: bool) -> Result<Self> {
        let (data, sw) = transport.exchange(&apdu_get_public_key(path, display)?)?;
        status_words(sw)?;
        let (_, address) = parse_public_key_response(&data)?;
        Ok(LedgerSigner { transport, path: path.to_string(), address })
    }

    fn run_chunks(&mut self, apdus: Vec<Vec<u8>>) -> Result<Vec<u8>> {
        let mut last = (Vec::new(), 0x9000u16);
        for a in apdus {
            last = self.transport.exchange(&a)?;
            status_words(last.1)?;
        }
        Ok(last.0)
    }

    pub fn sign_transaction(&mut self, signing_payload: &[u8], chain_id: u64) -> Result<Signature> {
        let data = self.run_chunks(apdus_sign_transaction(&self.path, signing_payload)?)?;
        let typed = signing_payload.first().map(|b| *b < 0x80).unwrap_or(false);
        parse_signature_response(&data, if typed { None } else { Some(chain_id) })
    }

    pub fn sign_personal_message(&mut self, message: &[u8]) -> Result<Signature> {
        let data = self.run_chunks(apdus_sign_personal_message(&self.path, message)?)?;
        parse_signature_response(&data, None)
    }

    pub fn sign_eip712_hashed(&mut self, domain_separator: &[u8; 32], struct_hash: &[u8; 32]) -> Result<Signature> {
        let (data, sw) = self.transport.exchange(&apdu_sign_eip712_hashed(&self.path, domain_separator, struct_hash)?)?;
        status_words(sw)?;
        parse_signature_response(&data, None)
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use crate::hex;

    #[test]
    fn path_layout() {
        let b = path_bytes("m/44'/60'/0'/0/0").unwrap();
        assert_eq!(hex::encode(&b), "0x058000002c8000003c800000000000000000000000");
        assert_eq!(path_bytes("m/44h/60h/0h/0/7").unwrap()[17..], [0, 0, 0, 7]);
        assert!(path_bytes("44'/60'").is_err());
        assert!(path_bytes("m/x").is_err());
    }

    #[test]
    fn apdus() {
        let a = apdu_get_public_key("m/44'/60'/0'/0/0", true).unwrap();
        assert_eq!(a[..5], [0xE0, 0x02, 0x01, 0x00, 21]);
        assert_eq!(a.len(), 26);
        let big = vec![0xabu8; 600];
        let chunks = apdus_sign_transaction("m/44'/60'/0'/0/0", &big).unwrap();
        assert_eq!(chunks.len(), 3);
        assert_eq!(chunks[0][2], P1_FIRST);
        assert_eq!(chunks[1][2], P1_MORE);
        assert_eq!(chunks[0][4], 255);
        assert_eq!(chunks[2][4] as usize, 21 + 600 - 510);
        let m = apdus_sign_personal_message("m/44'/60'/0'/0/0", b"hello").unwrap();
        assert_eq!(m.len(), 1);
        assert_eq!(&m[0][5 + 21..5 + 25], &[0, 0, 0, 5]);
        assert_eq!(&m[0][5 + 25..], b"hello");
        let e = apdu_sign_eip712_hashed("m/44'/60'/0'/0/0", &[1u8; 32], &[2u8; 32]).unwrap();
        assert_eq!(e[1], INS_SIGN_EIP712_HASHED);
        assert_eq!(e.len(), 5 + 21 + 64);
    }

    #[test]
    fn responses() {
        let mut data = vec![65u8];
        data.extend_from_slice(&[4u8; 65]);
        data.push(40);
        data.extend_from_slice(b"f39Fd6e51aad88F6F4ce6aB8827279cffFb92266");
        let (key, addr) = parse_public_key_response(&data).unwrap();
        assert_eq!(key.len(), 65);
        assert_eq!(addr.to_checksum(), "0xf39Fd6e51aad88F6F4ce6aB8827279cffFb92266");
        // a legacy signature on chain 7310: v = (7310*2+35) & 0xff = 14655 & 0xff = 0x3f (63) + parity
        let mut sig = vec![64u8];
        sig.extend_from_slice(&[7u8; 32]);
        sig.extend_from_slice(&[9u8; 32]);
        let s = parse_signature_response(&sig, Some(7310)).unwrap();
        assert_eq!(s.recovery_id, 1);
        assert_eq!(s.r, [7u8; 32]);
        sig[0] = 28;
        assert_eq!(parse_signature_response(&sig, None).unwrap().recovery_id, 1);
        sig[0] = 0;
        assert_eq!(parse_signature_response(&sig, None).unwrap().recovery_id, 0);
        assert!(status_words(0x9000).is_ok());
        assert!(status_words(0x6985).unwrap_err().to_string().contains("declined"));
    }

    #[test]
    fn hid_round_trip() {
        let apdu = apdu_get_public_key("m/44'/60'/0'/0/0", false).unwrap();
        let frames = hid_frames(&apdu);
        assert_eq!(frames.len(), 1);
        assert_eq!(&frames[0][..5], &[1, 1, 5, 0, 0]);
        assert_eq!(u16::from_be_bytes([frames[0][5], frames[0][6]]) as usize, apdu.len());
        let big = vec![0x11u8; 200];
        let frames = hid_frames(&big);
        assert_eq!(frames.len(), 4);
        assert_eq!(frames[3][3..5], [0, 3]);
        // a response: data || 0x9000, framed the same way
        let mut resp = vec![0xaa; 70];
        resp.extend_from_slice(&[0x90, 0x00]);
        let rf = hid_frames(&resp);
        let (data, sw) = hid_unframe(&rf).unwrap().unwrap();
        assert_eq!(data, vec![0xaa; 70]);
        assert_eq!(sw, 0x9000);
        assert!(hid_unframe(&rf[..1]).unwrap().is_none());
    }

    struct Fake;
    impl Transport for Fake {
        fn exchange(&mut self, apdu: &[u8]) -> Result<(Vec<u8>, u16)> {
            match apdu[1] {
                INS_GET_PUBLIC_KEY => {
                    let mut data = vec![65u8];
                    data.extend_from_slice(&[4u8; 65]);
                    data.push(40);
                    data.extend_from_slice(b"f39Fd6e51aad88F6F4ce6aB8827279cffFb92266");
                    Ok((data, 0x9000))
                }
                INS_SIGN_PERSONAL_MESSAGE => {
                    let mut sig = vec![27u8];
                    sig.extend_from_slice(&[1u8; 32]);
                    sig.extend_from_slice(&[2u8; 32]);
                    Ok((sig, 0x9000))
                }
                _ => Ok((vec![], 0x6985)),
            }
        }
    }

    #[test]
    fn signer_over_a_fake_transport() {
        let mut l = LedgerSigner::open(Fake, "m/44'/60'/0'/0/0", true).unwrap();
        assert_eq!(l.address.to_checksum(), "0xf39Fd6e51aad88F6F4ce6aB8827279cffFb92266");
        let s = l.sign_personal_message(b"hi").unwrap();
        assert_eq!(s.recovery_id, 0);
        let e = l.sign_transaction(&[0x02, 0xc0], 7310).unwrap_err();
        assert!(e.to_string().contains("declined"));
    }
}
