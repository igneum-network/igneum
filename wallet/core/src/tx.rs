//! Transactions: legacy with EIP-155 replay protection, and type 2 (EIP-1559). Encoding is this crate's own RLP;
//! the signing hash and the signed encoding follow the EIPs exactly, with the EIP-155 vector in the tests.
//! The access list of a type 2 transaction is always empty here: the wallet never needs one.

use serde::{Deserialize, Serialize};

use crate::{
    keccak::keccak256,
    rlp::{self, Item},
    sign::{hexbytes, Signature},
    Address, U256,
};

#[derive(Clone, Debug, PartialEq, Eq, Serialize, Deserialize)]
#[serde(tag = "type", rename_all = "snake_case")]
pub enum Transaction {
    Legacy(LegacyTx),
    Eip1559(Eip1559Tx),
}

#[derive(Clone, Debug, PartialEq, Eq, Serialize, Deserialize)]
pub struct LegacyTx {
    pub chain_id: u64,
    pub nonce: u64,
    pub gas_price: U256,
    pub gas_limit: u64,
    pub to: Option<Address>,
    pub value: U256,
    #[serde(with = "hexbytes")]
    pub data: Vec<u8>,
}

#[derive(Clone, Debug, PartialEq, Eq, Serialize, Deserialize)]
pub struct Eip1559Tx {
    pub chain_id: u64,
    pub nonce: u64,
    pub max_priority_fee_per_gas: U256,
    pub max_fee_per_gas: U256,
    pub gas_limit: u64,
    pub to: Option<Address>,
    pub value: U256,
    #[serde(with = "hexbytes")]
    pub data: Vec<u8>,
}

fn to_item(to: &Option<Address>) -> Item {
    match to {
        Some(a) => rlp::bytes(&a.0),
        None => rlp::bytes(&[]),
    }
}

impl Transaction {
    pub fn chain_id(&self) -> u64 {
        match self {
            Transaction::Legacy(t) => t.chain_id,
            Transaction::Eip1559(t) => t.chain_id,
        }
    }

    pub fn nonce(&self) -> u64 {
        match self {
            Transaction::Legacy(t) => t.nonce,
            Transaction::Eip1559(t) => t.nonce,
        }
    }

    pub fn to(&self) -> Option<Address> {
        match self {
            Transaction::Legacy(t) => t.to,
            Transaction::Eip1559(t) => t.to,
        }
    }

    pub fn value(&self) -> U256 {
        match self {
            Transaction::Legacy(t) => t.value,
            Transaction::Eip1559(t) => t.value,
        }
    }

    pub fn data(&self) -> &[u8] {
        match self {
            Transaction::Legacy(t) => &t.data,
            Transaction::Eip1559(t) => &t.data,
        }
    }

    pub fn gas_limit(&self) -> u64 {
        match self {
            Transaction::Legacy(t) => t.gas_limit,
            Transaction::Eip1559(t) => t.gas_limit,
        }
    }

    /// The most the fee can be: gas limit times the (max) fee per gas.
    pub fn max_fee(&self) -> Option<U256> {
        let per_gas = match self {
            Transaction::Legacy(t) => t.gas_price,
            Transaction::Eip1559(t) => t.max_fee_per_gas,
        };
        per_gas.checked_mul_u64(self.gas_limit())
    }

    /// The bytes that are hashed for signing.
    pub fn signing_payload(&self) -> Vec<u8> {
        match self {
            Transaction::Legacy(t) => rlp::encode(&rlp::list(vec![
                rlp::u64(t.nonce),
                rlp::uint(&t.gas_price),
                rlp::u64(t.gas_limit),
                to_item(&t.to),
                rlp::uint(&t.value),
                rlp::bytes(&t.data),
                rlp::u64(t.chain_id),
                rlp::u64(0),
                rlp::u64(0),
            ])),
            Transaction::Eip1559(t) => {
                let mut out = vec![0x02];
                out.extend(rlp::encode(&rlp::list(vec![
                    rlp::u64(t.chain_id),
                    rlp::u64(t.nonce),
                    rlp::uint(&t.max_priority_fee_per_gas),
                    rlp::uint(&t.max_fee_per_gas),
                    rlp::u64(t.gas_limit),
                    to_item(&t.to),
                    rlp::uint(&t.value),
                    rlp::bytes(&t.data),
                    rlp::list(vec![]),
                ])));
                out
            }
        }
    }

    pub fn signing_hash(&self) -> [u8; 32] {
        keccak256(&self.signing_payload())
    }

    /// The raw signed transaction, ready for eth_sendRawTransaction.
    pub fn encode_signed(&self, sig: &Signature) -> Vec<u8> {
        match self {
            Transaction::Legacy(t) => {
                let v = t.chain_id * 2 + 35 + sig.recovery_id as u64;
                rlp::encode(&rlp::list(vec![
                    rlp::u64(t.nonce),
                    rlp::uint(&t.gas_price),
                    rlp::u64(t.gas_limit),
                    to_item(&t.to),
                    rlp::uint(&t.value),
                    rlp::bytes(&t.data),
                    rlp::u64(v),
                    rlp::bytes(trim(&sig.r)),
                    rlp::bytes(trim(&sig.s)),
                ]))
            }
            Transaction::Eip1559(t) => {
                let mut out = vec![0x02];
                out.extend(rlp::encode(&rlp::list(vec![
                    rlp::u64(t.chain_id),
                    rlp::u64(t.nonce),
                    rlp::uint(&t.max_priority_fee_per_gas),
                    rlp::uint(&t.max_fee_per_gas),
                    rlp::u64(t.gas_limit),
                    to_item(&t.to),
                    rlp::uint(&t.value),
                    rlp::bytes(&t.data),
                    rlp::list(vec![]),
                    rlp::u64(sig.recovery_id as u64),
                    rlp::bytes(trim(&sig.r)),
                    rlp::bytes(trim(&sig.s)),
                ])));
                out
            }
        }
    }

    /// The transaction hash as the chain will know it.
    pub fn hash_signed(&self, sig: &Signature) -> [u8; 32] {
        keccak256(&self.encode_signed(sig))
    }
}

fn trim(b: &[u8; 32]) -> &[u8] {
    let start = b.iter().position(|x| *x != 0).unwrap_or(32);
    &b[start..]
}

#[cfg(test)]
mod tests {
    use super::*;
    use crate::hex;
    use k256::ecdsa::SigningKey;

    #[test]
    fn eip155_vector() {
        // the example transaction of EIP-155: nonce 9, 20 gwei, 21000 gas, to 0x3535..., 1 ether, chain id 1
        let to = Address::parse("0x3535353535353535353535353535353535353535").unwrap();
        let tx = Transaction::Legacy(LegacyTx {
            chain_id: 1,
            nonce: 9,
            gas_price: U256::from_dec_str("20000000000").unwrap(),
            gas_limit: 21000,
            to: Some(to),
            value: U256::from_dec_str("1000000000000000000").unwrap(),
            data: vec![],
        });
        assert_eq!(
            hex::encode(&tx.signing_payload()),
            "0xec098504a817c800825208943535353535353535353535353535353535353535880de0b6b3a764000080018080"
        );
        assert_eq!(
            hex::encode(&tx.signing_hash()),
            "0xdaf5a779ae972f972197303d7b574746c7ef83eadac0f2791ad23db92e4c8e53"
        );
        let key_bytes = hex::decode("0x4646464646464646464646464646464646464646464646464646464646464646").unwrap();
        let key = SigningKey::from_slice(&key_bytes).unwrap();
        let sig = Signature::sign(&key, &tx.signing_hash()).unwrap();
        assert_eq!(
            hex::encode(&tx.encode_signed(&sig)),
            "0xf86c098504a817c800825208943535353535353535353535353535353535353535880de0b6b3a76400008025a028ef61340bd939bc2195fe537567866003e1a15d3c71ff63e1590620aa636276a067cbe9d8997f761aecb703304b3800ccf555c9f3dc64214b297fb1966a3b6d83"
        );
        assert_eq!(sig.recover(&tx.signing_hash()).unwrap(), Address::from_public_key(key.verifying_key()));
    }

    #[test]
    fn eip1559_shape_and_round_trip() {
        let to = Address::parse("0x3535353535353535353535353535353535353535").unwrap();
        let tx = Transaction::Eip1559(Eip1559Tx {
            chain_id: 7310,
            nonce: 0,
            max_priority_fee_per_gas: U256::from_u64(1_000_000_000),
            max_fee_per_gas: U256::from_u64(30_000_000_000),
            gas_limit: 21000,
            to: Some(to),
            value: U256::from_u64(1),
            data: vec![],
        });
        let payload = tx.signing_payload();
        assert_eq!(payload[0], 0x02);
        assert!(payload[1] >= 0xc0);
        let key = SigningKey::from_slice(&[7u8; 32]).unwrap();
        let sig = Signature::sign(&key, &tx.signing_hash()).unwrap();
        let raw = tx.encode_signed(&sig);
        assert_eq!(raw[0], 0x02);
        assert_eq!(sig.recover(&tx.signing_hash()).unwrap(), Address::from_public_key(key.verifying_key()));
        assert_eq!(tx.max_fee().unwrap(), U256::from_u64(30_000_000_000 * 21000));
        // JSON round trip, the envelope's form
        let json = serde_json::to_string(&tx).unwrap();
        assert!(json.contains("\"type\":\"eip1559\""));
        let back: Transaction = serde_json::from_str(&json).unwrap();
        assert_eq!(back, tx);
    }
}
