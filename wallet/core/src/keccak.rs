//! Keccak-256 as Ethereum uses it (the pre-FIPS padding), through the `sha3` crate.

use sha3::{Digest, Keccak256};

pub fn keccak256(data: &[u8]) -> [u8; 32] {
    let mut out = [0u8; 32];
    out.copy_from_slice(&Keccak256::digest(data));
    out
}

#[cfg(test)]
mod tests {
    use super::*;
    use crate::hex;

    #[test]
    fn known_vectors() {
        // keccak256 of the empty string: the value every EVM tool agrees on
        assert_eq!(
            hex::encode(&keccak256(b"")),
            "0xc5d2460186f7233c927e7db2dcc703c0e500b653ca82273b7bfad8045d85a470"
        );
        // the function selectors of transfer(address,uint256) and approve(address,uint256)
        assert_eq!(&hex::encode(&keccak256(b"transfer(address,uint256)"))[..10], "0xa9059cbb");
        assert_eq!(&hex::encode(&keccak256(b"approve(address,uint256)"))[..10], "0x095ea7b3");
    }
}
