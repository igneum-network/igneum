//! Envelopes carry a transaction between a watch-only wallet and the place where the key is: out unsigned, back
//! signed. JSON now; the same fields as animated QR (UR) later. The unsigned envelope carries the sentences so the
//! signing side shows and checks the same words.

use serde::{Deserialize, Serialize};

use crate::{
    display,
    sign::Signature,
    signer::{sign_transaction, SignedTransaction, Signer},
    tx::Transaction,
    vault::now_unix,
    Address, Error, Result,
};

pub const VERSION: u32 = 1;

#[derive(Clone, Debug, PartialEq, Eq, Serialize, Deserialize)]
pub struct UnsignedEnvelope {
    pub version: u32,
    pub network: String,
    pub chain_id: u64,
    pub from: Address,
    pub transaction: Transaction,
    pub sentences: Vec<String>,
    pub created_unix: u64,
}

#[derive(Clone, Debug, Serialize, Deserialize)]
pub struct SignedEnvelope {
    pub version: u32,
    pub chain_id: u64,
    pub from: Address,
    pub signature: Signature,
    #[serde(with = "crate::sign::hexbytes")]
    pub raw: Vec<u8>,
    #[serde(with = "crate::sign::hex32")]
    pub hash: [u8; 32],
    pub sentences: Vec<String>,
}

impl UnsignedEnvelope {
    pub fn new(network: &str, from: Address, transaction: Transaction) -> Self {
        let sentences = display::sentences(&transaction, &from, network);
        UnsignedEnvelope {
            version: VERSION,
            network: network.to_string(),
            chain_id: transaction.chain_id(),
            from,
            transaction,
            sentences,
            created_unix: now_unix(),
        }
    }

    /// Checks the envelope is consistent with itself before anything is shown or signed.
    pub fn check(&self) -> Result<()> {
        if self.version != VERSION {
            return Err(Error::Input(format!("envelope version {} is not supported", self.version)));
        }
        if self.chain_id != self.transaction.chain_id() {
            return Err(Error::Input("the envelope's chain id does not match its transaction".into()));
        }
        let expected = display::sentences(&self.transaction, &self.from, &self.network);
        if expected != self.sentences {
            return Err(Error::Refused("the envelope's sentences do not describe its transaction".into()));
        }
        Ok(())
    }

    pub fn sign(&self, signer: &dyn Signer) -> Result<SignedEnvelope> {
        self.check()?;
        let SignedTransaction { signature, raw, hash } =
            sign_transaction(signer, &self.transaction, &self.from, &self.network, &self.sentences)?;
        Ok(SignedEnvelope {
            version: VERSION,
            chain_id: self.chain_id,
            from: self.from,
            signature,
            raw,
            hash,
            sentences: self.sentences.clone(),
        })
    }
}

impl SignedEnvelope {
    /// Checks the signature recovers to `from` over the unsigned envelope it answers.
    pub fn check_against(&self, unsigned: &UnsignedEnvelope) -> Result<()> {
        if self.chain_id != unsigned.chain_id || self.from != unsigned.from {
            return Err(Error::Input("the signed envelope answers a different transaction".into()));
        }
        let hash = unsigned.transaction.signing_hash();
        if self.signature.recover(&hash)? != unsigned.from {
            return Err(Error::Refused("the signature is not from the wallet's address".into()));
        }
        if unsigned.transaction.encode_signed(&self.signature) != self.raw {
            return Err(Error::Input("the raw bytes do not match the transaction and signature".into()));
        }
        Ok(())
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use crate::signer::SoftwareSigner;
    use crate::tx::Eip1559Tx;
    use crate::U256;

    const PHRASE: &str = "test test test test test test test test test test test junk";

    #[test]
    fn watch_only_round_trip() {
        let signer = SoftwareSigner::from_phrase(PHRASE, 0).unwrap();
        let to = Address::parse("0x70997970C51812dc3A010C7d01b50e0d17dc79C8").unwrap();
        let tx = Transaction::Eip1559(Eip1559Tx {
            chain_id: 7310,
            nonce: 4,
            max_priority_fee_per_gas: U256::from_u64(1_000_000_000),
            max_fee_per_gas: U256::from_u64(2_000_000_000),
            gas_limit: 21000,
            to: Some(to),
            value: U256::from_u64(5),
            data: vec![],
        });
        // the watch-only side builds the envelope
        let unsigned = UnsignedEnvelope::new("Igneum development chain 7310", signer.address(), tx);
        let json = serde_json::to_string_pretty(&unsigned).unwrap();
        // the signing side reads it, checks it, signs it
        let parsed: UnsignedEnvelope = serde_json::from_str(&json).unwrap();
        parsed.check().unwrap();
        let signed = parsed.sign(&signer).unwrap();
        let json2 = serde_json::to_string(&signed).unwrap();
        // the watch-only side verifies before broadcasting
        let back: SignedEnvelope = serde_json::from_str(&json2).unwrap();
        back.check_against(&unsigned).unwrap();
        assert_eq!(back.hash, unsigned.transaction.hash_signed(&back.signature));
    }

    #[test]
    fn tampered_envelope_is_refused() {
        let signer = SoftwareSigner::from_phrase(PHRASE, 0).unwrap();
        let to = Address::parse("0x70997970C51812dc3A010C7d01b50e0d17dc79C8").unwrap();
        let tx = Transaction::Eip1559(Eip1559Tx {
            chain_id: 7310,
            nonce: 4,
            max_priority_fee_per_gas: U256::from_u64(1),
            max_fee_per_gas: U256::from_u64(2),
            gas_limit: 21000,
            to: Some(to),
            value: U256::from_u64(5),
            data: vec![],
        });
        let mut unsigned = UnsignedEnvelope::new("Igneum development chain 7310", signer.address(), tx);
        if let Transaction::Eip1559(t) = &mut unsigned.transaction {
            t.value = U256::from_u64(5_000_000);
        }
        assert!(unsigned.check().is_err());
        assert!(unsigned.sign(&signer).is_err());
    }
}
