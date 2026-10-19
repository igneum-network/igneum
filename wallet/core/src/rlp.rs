//! RLP encoding (the Ethereum yellow paper, appendix B). Encoding only: the wallet builds transactions; it never
//! needs to parse one it did not build. Vectors from the Ethereum wiki in the tests.

use crate::U256;

#[derive(Debug, Clone, PartialEq, Eq)]
pub enum Item {
    Bytes(Vec<u8>),
    List(Vec<Item>),
}

pub fn bytes(b: &[u8]) -> Item {
    Item::Bytes(b.to_vec())
}

/// An unsigned integer: its minimal big-endian bytes, the empty string for zero.
pub fn uint(v: &U256) -> Item {
    Item::Bytes(v.to_be_trimmed().to_vec())
}

pub fn u64(v: u64) -> Item {
    uint(&U256::from_u64(v))
}

pub fn list(items: Vec<Item>) -> Item {
    Item::List(items)
}

pub fn encode(item: &Item) -> Vec<u8> {
    let mut out = Vec::new();
    encode_into(item, &mut out);
    out
}

fn encode_into(item: &Item, out: &mut Vec<u8>) {
    match item {
        Item::Bytes(b) => {
            if b.len() == 1 && b[0] < 0x80 {
                out.push(b[0]);
            } else {
                encode_length(b.len(), 0x80, out);
                out.extend_from_slice(b);
            }
        }
        Item::List(items) => {
            let mut body = Vec::new();
            for it in items {
                encode_into(it, &mut body);
            }
            encode_length(body.len(), 0xc0, out);
            out.extend_from_slice(&body);
        }
    }
}

fn encode_length(len: usize, offset: u8, out: &mut Vec<u8>) {
    if len < 56 {
        out.push(offset + len as u8);
    } else {
        let be = (len as u64).to_be_bytes();
        let start = be.iter().position(|b| *b != 0).unwrap_or(7);
        let len_bytes = &be[start..];
        out.push(offset + 55 + len_bytes.len() as u8);
        out.extend_from_slice(len_bytes);
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use crate::hex;

    fn h(item: &Item) -> String {
        hex::encode(&encode(item))
    }

    #[test]
    fn wiki_vectors() {
        assert_eq!(h(&bytes(b"dog")), "0x83646f67");
        assert_eq!(h(&list(vec![bytes(b"cat"), bytes(b"dog")])), "0xc88363617483646f67");
        assert_eq!(h(&bytes(b"")), "0x80");
        assert_eq!(h(&u64(0)), "0x80");
        assert_eq!(h(&list(vec![])), "0xc0");
        assert_eq!(h(&u64(15)), "0x0f");
        assert_eq!(h(&u64(1024)), "0x820400");
        // the set-theoretic representation of three: [ [], [[]], [ [], [[]] ] ]
        let three = list(vec![
            list(vec![]),
            list(vec![list(vec![])]),
            list(vec![list(vec![]), list(vec![list(vec![])])]),
        ]);
        assert_eq!(h(&three), "0xc7c0c1c0c3c0c1c0");
        // a 56-byte string takes the long form: 0xb8 0x38 then the bytes
        let long = bytes(b"Lorem ipsum dolor sit amet, consectetur adipisicing elit");
        let e = encode(&long);
        assert_eq!(e[0], 0xb8);
        assert_eq!(e[1], 56);
        assert_eq!(e.len(), 58);
        // a single byte below 0x80 is itself
        assert_eq!(h(&bytes(&[0x7f])), "0x7f");
        assert_eq!(h(&bytes(&[0x80])), "0x8180");
    }
}
