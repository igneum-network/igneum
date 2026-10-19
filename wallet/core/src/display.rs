//! The words a person sees before signing, computed from the payload and nothing else, so the signer can recompute
//! them and refuse a mismatch. Amounts to four decimals in sentences (the kept look's rule), addresses as their
//! first 6 and last 4 characters, the network by name. Known calls (Igneum Pay, the three token standards) are
//! named; anything else is "a call with N bytes of data".

use crate::{hex, keccak::keccak256, tx::Transaction, Address, Error, Result, U256};

pub const DECIMALS: u32 = 18;

/// Wei to IGN with up to `places` decimals, trailing zeros cut, never scientific.
pub fn ign(wei: &U256, places: usize) -> String {
    let unit = U256::pow10(DECIMALS).expect("10^18 fits");
    let (whole, frac) = div_rem(wei, &unit);
    let mut frac_s = frac.to_dec_string();
    while frac_s.len() < DECIMALS as usize {
        frac_s.insert(0, '0');
    }
    let cut: String = frac_s.chars().take(places).collect();
    let cut = cut.trim_end_matches('0');
    if cut.is_empty() {
        whole.to_dec_string()
    } else {
        format!("{}.{}", whole.to_dec_string(), cut)
    }
}

/// The amount in a sentence: four decimals, or every decimal when four would show a non-zero amount as 0, so a
/// dust amount is never read as nothing (and a change below the fourth decimal always changes the sentence).
pub fn amount_words(wei: &U256) -> String {
    let four = ign(wei, 4);
    if four == "0" && !wei.is_zero() {
        ign(wei, DECIMALS as usize)
    } else {
        four
    }
}

/// "1.5", "0.25", "12" to wei. More than 18 decimals is an error, not a rounding.
pub fn parse_ign(text: &str) -> Result<U256> {
    let t = text.trim();
    if t.is_empty() {
        return Err(Error::Input("no amount".into()));
    }
    let (whole, frac) = match t.split_once('.') {
        Some((w, f)) => (w, f),
        None => (t, ""),
    };
    let whole = if whole.is_empty() { "0" } else { whole };
    if frac.len() > DECIMALS as usize {
        return Err(Error::Input(format!("IGN has {DECIMALS} decimals; {} given", frac.len())));
    }
    if !frac.bytes().all(|c| c.is_ascii_digit()) {
        return Err(Error::Input(format!("not an amount: {text:?}")));
    }
    let mut padded = frac.to_string();
    while padded.len() < DECIMALS as usize {
        padded.push('0');
    }
    let whole_v = U256::from_dec_str(whole)?;
    let frac_v = if padded.is_empty() { U256::ZERO } else { U256::from_dec_str(&padded)? };
    let unit = U256::pow10(DECIMALS).expect("10^18 fits");
    mul(&whole_v, &unit)
        .checked_add(&frac_v)
        .ok_or_else(|| Error::Input("amount too large".into()))
}

fn mul(a: &U256, b: &U256) -> U256 {
    // schoolbook over bytes: enough for amounts (a * 10^18 for any a that fits)
    let mut out = [0u16; 64];
    for i in (0..32).rev() {
        if a.0[i] == 0 {
            continue;
        }
        let mut carry = 0u32;
        for j in (0..32).rev() {
            let idx = i + j + 1;
            let p = out[idx] as u32 + a.0[i] as u32 * b.0[j] as u32 + carry;
            out[idx] = (p & 0xff) as u16;
            carry = p >> 8;
        }
        let mut k = i;
        while carry != 0 {
            let p = out[k] as u32 + carry;
            out[k] = (p & 0xff) as u16;
            carry = p >> 8;
            if k == 0 {
                break;
            }
            k -= 1;
        }
    }
    let mut r = [0u8; 32];
    for i in 0..32 {
        r[i] = out[i + 32] as u8;
    }
    U256(r)
}

fn div_rem(a: &U256, b: &U256) -> (U256, U256) {
    // long division by repeated subtraction of shifted b: fine for one call per display
    if b.is_zero() {
        return (U256::ZERO, *a);
    }
    let mut q = U256::ZERO;
    let mut r = U256::ZERO;
    for i in 0..256 {
        // r = r << 1 | bit i of a (from the top)
        r = r.checked_mul_u64(2).unwrap_or(U256::MAX);
        let byte = a.0[i / 8];
        let bit = (byte >> (7 - (i % 8))) & 1;
        if bit == 1 {
            r.0[31] |= 1;
        }
        q = q.checked_mul_u64(2).unwrap_or(U256::MAX);
        if r >= *b {
            r = r.checked_sub(b).unwrap();
            q.0[31] |= 1;
        }
    }
    (q, r)
}

/// A decoded call, for the sentences and the permission screen.
#[derive(Clone, Debug, PartialEq, Eq)]
pub enum Call {
    /// a plain transfer of native coin
    Transfer,
    /// a transfer of native coin carrying exactly 32 bytes of data: a memo (a payout intent's hash, an invoice id)
    Memo { memo: [u8; 32] },
    /// ERC-20 transfer(address,uint256)
    TokenTransfer { to: Address, amount: U256 },
    /// ERC-20 approve(address,uint256); `unlimited` when the amount is the maximum
    Approve { spender: Address, amount: U256, unlimited: bool },
    /// ERC-721 and ERC-1155 setApprovalForAll(address,bool)
    ApproveAll { operator: Address, approved: bool },
    /// Igneum Pay pay(bytes32)
    PayInvoice { id: [u8; 32] },
    /// Igneum Pay payRequest((payee, token, amount, expiry, memo, payer, nonce), signature)
    PayRequest { payee: Address, token: Address, amount: U256, expiry: U256, payer: Address },
    /// Igneum Pay createInvoice(token, amount, expiry, memo, payer)
    CreateInvoice { token: Address, amount: U256, expiry: U256, payer: Address },
    /// Igneum Recurring authorise(payee, token, amountPerPeriod, operatorFee, period, start, end, totalCap)
    Authorise { payee: Address, token: Address, per_period: U256, operator_fee: U256, period: U256, start: U256, end: U256, cap: U256 },
    /// Igneum Escrow open(payee, arbiter, token, amount, deadline, onTimeout)
    EscrowOpen { payee: Address, arbiter: Address, token: Address, amount: U256, deadline: U256, payee_claims: bool },
    /// a known Igneum Pay function by name
    Pay { function: &'static str },
    /// Igneum Recurring withdraw(id, amount): the payer takes part of the deposit back
    PayWithdraw { id: [u8; 32], amount: U256 },
    /// Igneum Recurring revoke(id): no further collection, the remaining deposit returns
    PayRevoke { id: [u8; 32] },
    /// Igneum Escrow timeout(id): after the deadline, the whole amount to the side the open named
    PayTimeout { id: [u8; 32] },
    /// Igneum Escrow resolve(id, payeeShareBps): the arbiter's split
    PayResolve { id: [u8; 32], payee_share_bps: U256 },
    /// a contract deployment
    Create { bytes: usize },
    /// a call to the chain's bond registry (work authority), read by `authority::decode`
    Registry { call: crate::authority::RegistryCall },
    /// anything else
    Unknown { selector: [u8; 4], bytes: usize },
}

fn selector(sig: &str) -> [u8; 4] {
    let h = keccak256(sig.as_bytes());
    [h[0], h[1], h[2], h[3]]
}

fn word_address(data: &[u8], i: usize) -> Option<Address> {
    let start = 4 + 32 * i;
    let w = data.get(start..start + 32)?;
    Address::from_slice(&w[12..]).ok()
}

fn word_bytes32(data: &[u8], i: usize) -> Option<[u8; 32]> {
    let start = 4 + 32 * i;
    let w = data.get(start..start + 32)?;
    let mut out = [0u8; 32];
    out.copy_from_slice(w);
    Some(out)
}

fn word_uint(data: &[u8], i: usize) -> Option<U256> {
    let start = 4 + 32 * i;
    let w = data.get(start..start + 32)?;
    U256::from_be_bytes(w).ok()
}

/// Decodes what the data says, from the selector and the static words that follow. A call to the network's bond
/// registry is read as a registry call (the registry has no ABI: its calldata's length is the operation), so a
/// 32-byte memo to the registry is a bond, never a memo.
pub fn decode_call_on(to: Option<Address>, data: &[u8], value: &U256, registry: Option<Address>) -> Call {
    if let (Some(t), Some(r)) = (to, registry) {
        if t == r {
            return Call::Registry { call: crate::authority::decode(data, value) };
        }
    }
    decode_call(to, data)
}

/// `decode_call_on` without a registry: the plain reading.
pub fn decode_call(to: Option<Address>, data: &[u8]) -> Call {
    if to.is_none() {
        return Call::Create { bytes: data.len() };
    }
    if data.is_empty() {
        return Call::Transfer;
    }
    if data.len() == 32 {
        let mut memo = [0u8; 32];
        memo.copy_from_slice(data);
        return Call::Memo { memo };
    }
    if data.len() < 4 {
        return Call::Unknown { selector: [0; 4], bytes: data.len() };
    }
    let sel = [data[0], data[1], data[2], data[3]];
    if sel == selector("transfer(address,uint256)") {
        if let (Some(to), Some(amount)) = (word_address(data, 0), word_uint(data, 1)) {
            return Call::TokenTransfer { to, amount };
        }
    }
    if sel == selector("approve(address,uint256)") {
        if let (Some(spender), Some(amount)) = (word_address(data, 0), word_uint(data, 1)) {
            return Call::Approve { spender, amount, unlimited: amount == U256::MAX };
        }
    }
    if sel == selector("setApprovalForAll(address,bool)") {
        if let (Some(operator), Some(flag)) = (word_address(data, 0), word_uint(data, 1)) {
            return Call::ApproveAll { operator, approved: !flag.is_zero() };
        }
    }
    if sel == selector("pay(bytes32)") {
        if let Some(w) = data.get(4..36) {
            let mut id = [0u8; 32];
            id.copy_from_slice(w);
            return Call::PayInvoice { id };
        }
    }
    // the Pay calls that move money are decoded in full so the screen can show gross, allocations, fees and net
    // (ECONOMICS-RESTART.md 3.3a); the PaymentRequest tuple is static, so its seven words sit inline before the
    // signature's offset
    if sel == selector("payRequest((address,address,uint256,uint64,bytes32,address,uint256),bytes)") {
        if let (Some(payee), Some(token), Some(amount), Some(expiry), Some(payer)) =
            (word_address(data, 0), word_address(data, 1), word_uint(data, 2), word_uint(data, 3), word_address(data, 5))
        {
            return Call::PayRequest { payee, token, amount, expiry, payer };
        }
    }
    if sel == selector("createInvoice(address,uint256,uint64,bytes32,address)") {
        if let (Some(token), Some(amount), Some(expiry), Some(payer)) =
            (word_address(data, 0), word_uint(data, 1), word_uint(data, 2), word_address(data, 4))
        {
            return Call::CreateInvoice { token, amount, expiry, payer };
        }
    }
    if sel == selector("authorise(address,address,uint256,uint256,uint64,uint64,uint64,uint256)") {
        if let (Some(payee), Some(token), Some(per_period), Some(operator_fee), Some(period), Some(start), Some(end), Some(cap)) = (
            word_address(data, 0),
            word_address(data, 1),
            word_uint(data, 2),
            word_uint(data, 3),
            word_uint(data, 4),
            word_uint(data, 5),
            word_uint(data, 6),
            word_uint(data, 7),
        ) {
            return Call::Authorise { payee, token, per_period, operator_fee, period, start, end, cap };
        }
    }
    if sel == selector("open(address,address,address,uint256,uint64,uint8)") {
        if let (Some(payee), Some(arbiter), Some(token), Some(amount), Some(deadline), Some(on_timeout)) = (
            word_address(data, 0),
            word_address(data, 1),
            word_address(data, 2),
            word_uint(data, 3),
            word_uint(data, 4),
            word_uint(data, 5),
        ) {
            return Call::EscrowOpen { payee, arbiter, token, amount, deadline, payee_claims: on_timeout.is_zero() };
        }
    }
    // the four calls whose arguments move value or name a side (wallet-security's P8, 12 October 2026): decoded
    // whole, never the generic line
    if sel == selector("withdraw(bytes32,uint256)") {
        if let (Some(id), Some(amount)) = (word_bytes32(data, 0), word_uint(data, 1)) {
            return Call::PayWithdraw { id, amount };
        }
    }
    if sel == selector("revoke(bytes32)") {
        if let Some(id) = word_bytes32(data, 0) {
            return Call::PayRevoke { id };
        }
    }
    if sel == selector("timeout(bytes32)") {
        if let Some(id) = word_bytes32(data, 0) {
            return Call::PayTimeout { id };
        }
    }
    if sel == selector("resolve(bytes32,uint256)") {
        if let (Some(id), Some(bps)) = (word_bytes32(data, 0), word_uint(data, 1)) {
            return Call::PayResolve { id, payee_share_bps: bps };
        }
    }
    let pay_functions: [(&str, &'static str); 15] = [
        ("resolve(bytes32,uint256)", "resolve"),
        ("payRequest((address,address,uint256,uint64,bytes32,address,uint256),bytes)", "payRequest"),
        ("createInvoice(address,uint256,uint64,bytes32,address)", "createInvoice"),
        ("cancelInvoice(bytes32)", "cancelInvoice"),
        ("claim()", "claim"),
        ("collect(bytes32)", "collect"),
        ("authorise(address,address,uint256,uint256,uint64,uint64,uint64,uint256)", "authorise"),
        ("revoke(bytes32)", "revoke"),
        ("deposit(bytes32)", "deposit"),
        ("withdraw(bytes32,uint256)", "withdraw"),
        ("open(address,address,address,uint256,uint64,uint8)", "open"),
        ("release(bytes32)", "release"),
        ("refund(bytes32)", "refund"),
        ("timeout(bytes32)", "timeout"),
        ("dispute(bytes32)", "dispute"),
    ];
    for (sig, name) in pay_functions {
        if sel == selector(sig) {
            return Call::Pay { function: name };
        }
    }
    Call::Unknown { selector: sel, bytes: data.len() }
}

/// An amount in a Pay sentence: IGN to four decimals for the native coin (`address(0)`), or the token's raw units
/// with the token's address (the wallet does not read a token's decimals before signing: the words come from the
/// payload alone, so the signer can recompute them).
fn asset_words(token: &Address, amount: &U256) -> String {
    if token.is_zero() {
        format!("{} IGN", amount_words(amount))
    } else {
        format!("{} units of the token at {}", amount.to_dec_string(), token.short())
    }
}

/// The sentences for a transaction, in order. The first is what the hardware device shows too. The network's name
/// selects the chain (`Chain::by_name`), and with it the bond registry whose calls get their own sentences; the
/// signing side recomputes the sentences from the same name, so the two always agree.
pub fn sentences(tx: &Transaction, from: &Address, network: &str) -> Vec<String> {
    use crate::authority::RegistryCall;
    let mut out = Vec::new();
    let value = tx.value();
    let to = tx.to();
    let registry = crate::chain::Chain::by_name(network).and_then(|c| c.registry());
    let at = to.map(|a| a.short()).unwrap_or_default();
    match decode_call_on(to, tx.data(), &value, registry) {
        Call::Registry { call: RegistryCall::Bond { key, exit } } => {
            out.push(match exit {
                None => format!(
                    "You bond {} IGN to the identity key {} in the registry at {}: the bond belongs to this account, is locked by the chain's rule, and only this account can add to it or rotate its signer",
                    amount_words(&value),
                    short_hash(&key),
                    at
                ),
                Some(exit) if crate::chain::Chain::by_name(network).and_then(|c| c.authority.map(|a| a.registry_version)).unwrap_or(3) >= 4 => format!(
                    "You bond {} IGN to the identity key {} with the exit address {}: a fresh key records this account and that exit and the post height starts the lock; on a bonded key this changes the exit address only after the lock (the post height plus the chain's lock in blocks); before it the chain refuses the call and nothing moves; a claim to any other address burns a quarter",
                    amount_words(&value),
                    short_hash(&key),
                    exit.short()
                ),
                Some(exit) => format!(
                    "You bond {} IGN to the identity key {} with the exit address {}: a fresh key records this account and that exit; on a bonded key this changes the exit address at once (the lab build models no lock) and a claim to any other address burns a quarter",
                    amount_words(&value),
                    short_hash(&key),
                    exit.short()
                ),
            });
        }
        Call::Registry { call: RegistryCall::ClaimReveal { to: claimant } } => {
            out.push(format!(
                "DEV-ONLY (the lab registry's stand-in): You reveal a bond's SECRET on the chain (the first 32 bytes of the data) to claim its whole bond to {}: to the registered exit address everything is paid (your own exit), to any other address one quarter is burnt to the fund; the secret becomes readable in the transaction and a sealer that sees it first could claim ahead of you",
                claimant.short()
            ));
        }
        Call::Registry { call: RegistryCall::Read { key } } => {
            out.push(format!(
                "You send a read of the identity key {} to the registry at {} as a transaction: it changes nothing and costs its fee",
                short_hash(&key),
                at
            ));
        }
        Call::Registry { call: RegistryCall::Release { key } } => {
            out.push(format!(
                "You release the whole bond of the identity {} to its registered exit address through the registry at {}: the chain refuses this before the lock (the post height plus the chain's lock in blocks); after it the bond leaves the table in full and the key never bonds again{}",
                short_hash(&key),
                at,
                if value.is_zero() { "" } else { "; WARNING: this call carries IGN the registry refuses" }
            ));
        }
        Call::Registry { call: RegistryCall::SetSigner { key, signer } } => {
            out.push(format!(
                "You set the signing key of the identity {} to {} in the registry at {}: from the next block, templates under this identity are signed by that key and no other; the bond does not move{}",
                short_hash(&key),
                short_hash(&signer),
                at,
                if value.is_zero() { "" } else { "; WARNING: this call carries IGN the registry refuses" }
            ));
        }
        Call::Registry { call: RegistryCall::Claim { key, to: claimant, pi_bytes, .. } } => {
            out.push(format!(
                "DEV-ONLY (registry version 3, not on any chain yet): You present the proof pi ({} bytes) under the identity {}'s secret to claim its whole bond to {}: to the registered exit address this is your own release after the lock; to any other address it is hostile, one quarter is burnt to the fund, the key leaves the next epoch's table and its pending rewards are burnt",
                pi_bytes,
                short_hash(&key),
                claimant.short()
            ));
        }
        Call::Registry { call: RegistryCall::Unknown { bytes } } => {
            out.push(format!(
                "You call the registry at {} with {} bytes the registry has no operation for, and {} IGN: the chain will refuse this",
                at,
                bytes,
                amount_words(&value)
            ));
        }
        Call::Transfer => {
            // the payee in full, checksummed (wallet-security CL-3, 12 October 2026): a clipboard swapper that keeps the
            // ends passes a person who compares them; the short form stays on the home and history cards
            out.push(format!("You send {} IGN to {}", amount_words(&value), to.map(|a| a.to_checksum()).unwrap_or_default()));
        }
        Call::Memo { memo } => {
            out.push(format!(
                "You send {} IGN to {} with the 32-byte memo {}; a contract at that address would receive the memo as call data",
                amount_words(&value),
                to.map(|a| a.to_checksum()).unwrap_or_default(),
                hex::encode(&memo)
            ));
        }
        Call::TokenTransfer { to: dest, amount } => {
            out.push(format!(
                "You send {} units of the token at {} to {}",
                amount.to_dec_string(),
                to.map(|a| a.short()).unwrap_or_default(),
                dest.to_checksum()
            ));
        }
        Call::Approve { spender, amount, unlimited } => {
            if unlimited {
                out.push(format!(
                    "You allow {} to spend an UNLIMITED amount of the token at {}",
                    spender.short(),
                    to.map(|a| a.short()).unwrap_or_default()
                ));
            } else {
                out.push(format!(
                    "You allow {} to spend up to {} units of the token at {} (not unlimited)",
                    spender.short(),
                    amount.to_dec_string(),
                    to.map(|a| a.short()).unwrap_or_default()
                ));
            }
        }
        Call::ApproveAll { operator, approved } => {
            out.push(if approved {
                format!(
                    "You allow {} to move EVERY item of the collection at {}",
                    operator.short(),
                    to.map(|a| a.short()).unwrap_or_default()
                )
            } else {
                format!(
                    "You withdraw {}'s permission over the collection at {}",
                    operator.short(),
                    to.map(|a| a.short()).unwrap_or_default()
                )
            });
        }
        Call::PayInvoice { id } => {
            out.push(format!(
                "You pay invoice {} through Igneum Pay at {}: {} IGN",
                short_hash(&id),
                to.map(|a| a.short()).unwrap_or_default(),
                amount_words(&value)
            ));
            if value.is_zero() {
                out.push("Gross: the invoice's token amount as recorded on the chain for this id (this call sends 0 IGN); fees: none, the contract takes nothing and Igneum Labs is paid nothing; the payee receives the whole amount; the network fee is the line below".to_string());
            } else {
                out.push(format!(
                    "Gross {} IGN; fees: none, the contract takes nothing and Igneum Labs is paid nothing; the payee receives {} IGN net; the network fee is the line below",
                    amount_words(&value),
                    amount_words(&value)
                ));
            }
        }
        Call::PayRequest { payee, token, amount, expiry, payer } => {
            out.push(format!(
                "You pay a request of {} to {} through Igneum Pay at {}{}",
                asset_words(&token, &amount),
                payee.short(),
                to.map(|a| a.short()).unwrap_or_default(),
                if payer.is_zero() { String::new() } else { format!(" (the request names {} as the payer)", payer.short()) }
            ));
            out.push(format!(
                "Gross {}; fees: none, the contract takes nothing and Igneum Labs is paid nothing; {} receives {} net; the request expires at unix second {}; the network fee is the line below",
                asset_words(&token, &amount),
                payee.short(),
                asset_words(&token, &amount),
                expiry.to_dec_string()
            ));
            if token.is_zero() && value != amount {
                out.push(format!("This call sends {} IGN, which is not the request's amount: the contract will refuse it", amount_words(&value)));
            }
        }
        Call::CreateInvoice { token, amount, expiry, payer } => {
            out.push(format!(
                "You create an invoice for {} on Igneum Pay at {}, payable by {}, expiring at unix second {}",
                asset_words(&token, &amount),
                to.map(|a| a.short()).unwrap_or_default(),
                if payer.is_zero() { "anyone".to_string() } else { payer.short() },
                expiry.to_dec_string()
            ));
            out.push(format!(
                "When it is paid: gross {}; fees: none, the contract takes nothing and Igneum Labs is paid nothing; you receive {} net",
                asset_words(&token, &amount),
                asset_words(&token, &amount)
            ));
        }
        Call::Authorise { payee, token, per_period, operator_fee, period, start, end, cap } => {
            let per_collection = per_period.checked_add(&operator_fee);
            out.push(format!(
                "You authorise {} to be paid {} every {} seconds from unix second {} to {} through Igneum Pay at {}, up to {} in all",
                payee.short(),
                asset_words(&token, &per_period),
                period.to_dec_string(),
                start.to_dec_string(),
                end.to_dec_string(),
                to.map(|a| a.short()).unwrap_or_default(),
                asset_words(&token, &cap)
            ));
            out.push(format!(
                "Each collection: gross {}; {} to {}; the operator fee {} to whoever submits the collection (set by you, paid from your deposit); the contract takes nothing and Igneum Labs is paid nothing; the cap counts both",
                per_collection.map(|g| asset_words(&token, &g)).unwrap_or_else(|| "more than the chain can hold".to_string()),
                asset_words(&token, &per_period),
                payee.short(),
                asset_words(&token, &operator_fee)
            ));
            if !value.is_zero() {
                out.push(format!("Deposited now: {} IGN, yours to withdraw while unspent", amount_words(&value)));
            }
        }
        Call::EscrowOpen { payee, arbiter, token, amount, deadline, payee_claims } => {
            out.push(format!(
                "You lock {} in escrow for {} through Igneum Pay at {} until unix second {}; after that {}; {}",
                asset_words(&token, &amount),
                payee.short(),
                to.map(|a| a.short()).unwrap_or_default(),
                deadline.to_dec_string(),
                if payee_claims { "the payee may claim it" } else { "you may take it back" },
                if arbiter.is_zero() {
                    "no arbiter: there is no dispute, the timeout rule is the whole agreement".to_string()
                } else {
                    format!("on a dispute before then only the arbiter {} decides the split", arbiter.short())
                }
            ));
            out.push(format!(
                "Gross {}; fees: none, the contract takes nothing, the arbiter is paid nothing by it and Igneum Labs is paid nothing; released to {} in full, returned to you in full, or split by the arbiter's share; the network fee is the line below",
                asset_words(&token, &amount),
                payee.short()
            ));
        }
        Call::Pay { function } => {
            out.push(format!(
                "You call {} on Igneum Pay at {} with {} IGN",
                function,
                to.map(|a| a.short()).unwrap_or_default(),
                amount_words(&value)
            ));
            out.push(match function {
                "collect" => "You collect a due period: the payee is paid the period's amount and you receive the operator fee the payer set, both from the payer's deposit as recorded on the chain for this id; the contract takes nothing and Igneum Labs is paid nothing".to_string(),
                "deposit" => format!("Deposited: {} IGN into your authorisation, yours to withdraw while unspent; the contract takes nothing", amount_words(&value)),
                "claim" => "You take the balance credited to you by the contract, in full; the contract takes nothing".to_string(),
                "release" => "You release the escrow to the payee in full; the contract takes nothing and the arbiter is paid nothing by it".to_string(),
                "refund" => "The escrow is returned to the payer in full; the contract takes nothing".to_string(),
                "resolve" => "The arbiter splits the escrow by the share given, nothing kept by the contract or the arbiter".to_string(),
                _ => "Fees: none, the contract takes nothing and Igneum Labs is paid nothing; the network fee is the line below".to_string(),
            });
        }
        Call::PayWithdraw { id, amount } => {
            out.push(format!(
                "You withdraw {} IGN of your deposit from authorisation {} on Igneum Pay at {} back to this account; the authorisation stays, with less behind it; the contract takes nothing",
                amount_words(&amount),
                short_hash(&id),
                to.map(|a| a.short()).unwrap_or_default()
            ));
        }
        Call::PayRevoke { id } => {
            out.push(format!(
                "You revoke authorisation {} on Igneum Pay at {}: no further collection; your remaining deposit, as recorded on the chain, returns to this account in full; the contract takes nothing",
                short_hash(&id),
                to.map(|a| a.short()).unwrap_or_default()
            ));
        }
        Call::PayTimeout { id } => {
            out.push(format!(
                "You close escrow {} on Igneum Pay at {} after its deadline: the escrow pays its whole amount to the side the open named (the payee where the open said the payee claims on timeout, else back to the payer); the chain's record of the open decides, nothing is kept by the contract or the arbiter",
                short_hash(&id),
                to.map(|a| a.short()).unwrap_or_default()
            ));
        }
        Call::PayResolve { id, payee_share_bps } => {
            let bps = payee_share_bps.to_u64().unwrap_or(u64::MAX);
            let share = if bps <= 10_000 { format!("{}.{:02} percent", bps / 100, bps % 100) } else { format!("{} basis points (more than the whole: the contract will refuse it)", payee_share_bps.to_dec_string()) };
            out.push(format!(
                "As the arbiter you resolve escrow {} on Igneum Pay at {}: {} of the escrow to the payee, the rest to the payer; nothing kept by the contract or the arbiter",
                short_hash(&id),
                to.map(|a| a.short()).unwrap_or_default(),
                share
            ));
        }
        Call::Create { bytes } => {
            out.push(format!("You deploy a contract ({bytes} bytes of code) with {} IGN", amount_words(&value)));
        }
        Call::Unknown { selector, bytes } => {
            out.push(format!(
                "You call {} with {} bytes of data (selector {}) and {} IGN: this wallet cannot read this call",
                to.map(|a| a.short()).unwrap_or_default(),
                bytes,
                hex::encode(&selector),
                amount_words(&value)
            ));
        }
    }
    match tx.max_fee() {
        Some(fee) => out.push(format!("Fee up to {} IGN ({} gas)", ign(&fee, 6), tx.gas_limit())),
        None => out.push("Fee: not computable".to_string()),
    }
    out.push(format!("From {} on {} (chain {}), nonce {}", from.short(), crate::chain::Chain::public_word(network), tx.chain_id(), tx.nonce()));
    out
}

pub fn short_hash(h: &[u8; 32]) -> String {
    let s = hex::encode(h);
    format!("{}\u{2026}{}", &s[..6], &s[62..])
}

#[cfg(test)]
mod tests {
    use super::*;
    use crate::tx::Eip1559Tx;

    #[test]
    fn amounts() {
        assert_eq!(ign(&U256::from_dec_str("1500000000000000000").unwrap(), 4), "1.5");
        assert_eq!(ign(&U256::from_dec_str("1000000000000000000").unwrap(), 4), "1");
        assert_eq!(ign(&U256::from_dec_str("123456789000000000").unwrap(), 4), "0.1234");
        assert_eq!(ign(&U256::from_dec_str("123456789000000000").unwrap(), 18), "0.123456789");
        assert_eq!(ign(&U256::ZERO, 4), "0");
        assert_eq!(ign(&U256::from_dec_str("1").unwrap(), 4), "0");
        assert_eq!(ign(&U256::from_dec_str("1").unwrap(), 18), "0.000000000000000001");
        assert_eq!(parse_ign("1.5").unwrap().to_dec_string(), "1500000000000000000");
        assert_eq!(parse_ign("12").unwrap().to_dec_string(), "12000000000000000000");
        assert_eq!(parse_ign(".25").unwrap().to_dec_string(), "250000000000000000");
        assert_eq!(parse_ign("0.000000000000000001").unwrap().to_dec_string(), "1");
        assert!(parse_ign("1.0000000000000000001").is_err());
        assert!(parse_ign("abc").is_err());
        assert!(parse_ign("").is_err());
        let big = parse_ign("123456789012345678901234567890.5").unwrap();
        assert_eq!(ign(&big, 4), "123456789012345678901234567890.5");
        assert_eq!(amount_words(&U256::from_u64(5)), "0.000000000000000005");
        assert_eq!(amount_words(&U256::from_u64(5_000_000)), "0.000000000005");
        assert_eq!(amount_words(&parse_ign("1.5").unwrap()), "1.5");
        assert_eq!(amount_words(&U256::ZERO), "0");
    }

    fn tx(to: Option<Address>, value: U256, data: Vec<u8>) -> Transaction {
        Transaction::Eip1559(Eip1559Tx {
            chain_id: 7310,
            nonce: 3,
            max_priority_fee_per_gas: U256::from_u64(1_000_000_000),
            max_fee_per_gas: U256::from_u64(2_000_000_000),
            gas_limit: 21000,
            to,
            value,
            data,
        })
    }

    #[test]
    fn transfer_sentences() {
        let from = Address::parse("0xf39Fd6e51aad88F6F4ce6aB8827279cffFb92266").unwrap();
        let to = Address::parse("0x70997970C51812dc3A010C7d01b50e0d17dc79C8").unwrap();
        let s = sentences(&tx(Some(to), parse_ign("1.5").unwrap(), vec![]), &from, "Igneum development chain 7310");
        assert_eq!(s[0], "You send 1.5 IGN to 0x70997970C51812dc3A010C7d01b50e0d17dc79C8");
        assert_eq!(s[1], "Fee up to 0.000042 IGN (21000 gas)");
        assert_eq!(s[2], "From 0xf39Fd6\u{2026}2266 on Igneum development chain 7310 (chain 7310), nonce 3");
    }

    #[test]
    fn memo_send_sentences() {
        let from = Address::parse("0xf39Fd6e51aad88F6F4ce6aB8827279cffFb92266").unwrap();
        let to = Address::parse("0x70997970C51812dc3A010C7d01b50e0d17dc79C8").unwrap();
        let memo = [0xabu8; 32];
        assert_eq!(decode_call(Some(to), &memo), Call::Memo { memo });
        let s = sentences(&tx(Some(to), parse_ign("2").unwrap(), memo.to_vec()), &from, "Igneum development chain 7310");
        assert_eq!(s.len(), 3);
        assert_eq!(
            s[0],
            format!("You send 2 IGN to 0x70997970C51812dc3A010C7d01b50e0d17dc79C8 with the 32-byte memo {}; a contract at that address would receive the memo as call data", hex::encode(&memo))
        );
        // 31 or 33 bytes are not a memo: an unknown call
        assert!(matches!(decode_call(Some(to), &[0u8; 31]), Call::Unknown { .. }));
        assert!(matches!(decode_call(Some(to), &[0u8; 33]), Call::Unknown { .. }));
    }

    #[test]
    fn registry_sentences_on_an_authority_chain() {
        use crate::authority::{self, RegistryCall};
        let from = Address::parse("0xf39Fd6e51aad88F6F4ce6aB8827279cffFb92266").unwrap();
        let registry = crate::chain::Chain::igneum_devnet4().registry().unwrap();
        let key = [0x4bu8; 32];
        // 32 bytes with value to the registry on devnet4: a bond, not a memo
        let s = sentences(&tx(Some(registry), parse_ign("1").unwrap(), authority::bond_data(&key)), &from, "Igneum development chain 7310, fourth cut");
        assert!(s[0].starts_with("You bond 1 IGN to the identity key 0x4b4b\u{2026}4b4b in the registry at"), "{}", s[0]);
        assert_eq!(s[2], "From 0xf39Fd6\u{2026}2266 on Igneum development chain 7310, fourth cut (chain 7310), nonce 3");
        // the same bytes on builder one's devnet (no registry) are a memo send
        let m = sentences(&tx(Some(registry), parse_ign("1").unwrap(), authority::bond_data(&key)), &from, "Igneum development chain 7310");
        assert!(m[0].starts_with("You send 1 IGN to"), "{}", m[0]);
        // the same bytes on an unpinned chain 7310 are a memo send too: no registry is assumed without a pin
        let u = sentences(&tx(Some(registry), parse_ign("1").unwrap(), authority::bond_data(&key)), &from, "chain 7310");
        assert!(u[0].starts_with("You send 1 IGN to"), "{}", u[0]);
        // value 0: a read
        let r = sentences(&tx(Some(registry), U256::ZERO, authority::bond_data(&key)), &from, "Igneum development chain 7310, fourth cut");
        assert!(r[0].starts_with("You send a read of the identity key"), "{}", r[0]);
        // the signer rotation
        let signer = [0xa8u8; 32];
        let s2 = sentences(&tx(Some(registry), U256::ZERO, authority::set_signer_data(&key, &signer)), &from, "Igneum development chain 7310, fourth cut");
        assert!(s2[0].starts_with("You set the signing key of the identity 0x4b4b\u{2026}4b4b to 0xa8a8\u{2026}a8a8"), "{}", s2[0]);
        // the version 3 forms carry the DEV-ONLY label first
        let exit = Address::parse("0x70997970C51812dc3A010C7d01b50e0d17dc79C8").unwrap();
        let mut pi = vec![authority::PI_SYSTEM];
        pi.extend_from_slice(&[7u8; 500]);
        let claim = authority::claim_data(&key, &exit, &[1u8; 32], &pi).unwrap();
        let v3 = sentences(&tx(Some(registry), U256::ZERO, claim.clone()), &from, "Igneum development chain 7310, fourth cut");
        assert!(v3[0].starts_with("DEV-ONLY (registry version 3, not on any chain yet): You "), "{}", v3[0]);
        // the bond with its exit word, with value: the lab's form, its own sentence
        let we = sentences(&tx(Some(registry), parse_ign("1").unwrap(), authority::set_exit_data(&key, &exit)), &from, "Igneum development chain 7315");
        assert!(we[0].starts_with("You bond 1 IGN to the identity key 0x4b4b\u{2026}4b4b with the exit address 0x709979\u{2026}79C8"), "{}", we[0]);
        // the helper builds every test transaction at chain id 7310; the row checks the network's name, which is what
        // selects the registry's sentences
        assert!(we[2].contains("on Igneum development chain 7315 (chain 7310)"), "{}", we[2]);
        // the lab's stand-in claim says the secret goes on the chain
        let rv = sentences(&tx(Some(registry), U256::ZERO, authority::claim_reveal_data(&[8u8; 32], &exit)), &from, "Igneum development chain 7315");
        assert!(rv[0].starts_with("DEV-ONLY (the lab registry's stand-in): You reveal a bond's SECRET"), "{}", rv[0]);
        let c = sentences(&tx(Some(registry), U256::ZERO, claim.clone()), &from, "Igneum development chain 7310, fourth cut");
        assert!(c[0].contains("the proof pi (501 bytes)"), "{}", c[0]);
        assert!(matches!(
            decode_call_on(Some(registry), &claim, &U256::ZERO, Some(registry)),
            Call::Registry { call: RegistryCall::Claim { pi_bytes: 501, .. } }
        ));
        // an unknown length to the registry says the chain will refuse it
        let bad = sentences(&tx(Some(registry), U256::ZERO, vec![0u8; 40]), &from, "Igneum development chain 7310, fourth cut");
        assert!(bad[0].contains("40 bytes the registry has no operation for"), "{}", bad[0]);
    }

    #[test]
    fn decodes_approvals_and_pay() {
        let token = Address::parse("0x70997970C51812dc3A010C7d01b50e0d17dc79C8").unwrap();
        let spender = Address::parse("0x3C44CdDdB6a900fa2b585dd299e03d12FA4293BC").unwrap();
        let mut data = selector("approve(address,uint256)").to_vec();
        data.extend_from_slice(&[0u8; 12]);
        data.extend_from_slice(&spender.0);
        data.extend_from_slice(&[0xffu8; 32]);
        let call = decode_call(Some(token), &data);
        assert_eq!(call, Call::Approve { spender, amount: U256::MAX, unlimited: true });
        let from = Address::default();
        let s = sentences(&tx(Some(token), U256::ZERO, data), &from, "Igneum development chain 7310");
        assert!(s[0].contains("UNLIMITED"));

        let mut data = selector("pay(bytes32)").to_vec();
        data.extend_from_slice(&[0xabu8; 32]);
        match decode_call(Some(token), &data) {
            Call::PayInvoice { id } => assert_eq!(id, [0xabu8; 32]),
            other => panic!("{other:?}"),
        }
        let mut data = selector("collect(bytes32)").to_vec();
        data.extend_from_slice(&[0u8; 32]);
        assert_eq!(decode_call(Some(token), &data), Call::Pay { function: "collect" });
        let c = sentences(&tx(Some(token), U256::ZERO, data), &from, "Igneum development chain 7310");
        assert!(c[1].contains("you receive the operator fee the payer set"), "{}", c[1]);
        assert_eq!(decode_call(None, &[1, 2, 3]), Call::Create { bytes: 3 });
    }

    /// Every Pay screen shows gross, allocations, fees and net before authorisation (ECONOMICS-RESTART.md 3.3a):
    /// the operator fee of an authorisation as its own line with its recipient, an escrow's arbiter paid nothing,
    /// the contract's fee none on every call.
    #[test]
    fn pay_argument_sentences_p8() {
        // wallet-security's P8 (12 October 2026): withdraw, revoke, timeout and resolve decode their arguments
        let from = Address::parse("0xf39Fd6e51aad88F6F4ce6aB8827279cffFb92266").unwrap();
        let pay = Address::parse("0x90F79bf6EB2c4f870365E785982E1f101E93b906").unwrap();
        let id = [0x11u8; 32];
        let mut amount = [0u8; 32];
        amount[24..].copy_from_slice(&2_500_000_000_000_000_000u64.to_be_bytes());
        let mut data = selector("withdraw(bytes32,uint256)").to_vec();
        data.extend_from_slice(&id);
        data.extend_from_slice(&amount);
        let s = sentences(&tx(Some(pay), U256::ZERO, data), &from, "Igneum development chain 7310");
        assert!(s[0].starts_with("You withdraw 2.5 IGN of your deposit from authorisation 0x1111"), "{}", s[0]);
        let mut data = selector("revoke(bytes32)").to_vec();
        data.extend_from_slice(&id);
        let s = sentences(&tx(Some(pay), U256::ZERO, data), &from, "Igneum development chain 7310");
        assert!(s[0].contains("no further collection; your remaining deposit, as recorded on the chain, returns to this account in full"), "{}", s[0]);
        let mut data = selector("timeout(bytes32)").to_vec();
        data.extend_from_slice(&id);
        let s = sentences(&tx(Some(pay), U256::ZERO, data), &from, "Igneum development chain 7310");
        assert!(s[0].contains("the escrow pays its whole amount to the side the open named"), "{}", s[0]);
        let mut bps = [0u8; 32];
        bps[30..].copy_from_slice(&3_250u16.to_be_bytes());
        let mut data = selector("resolve(bytes32,uint256)").to_vec();
        data.extend_from_slice(&id);
        data.extend_from_slice(&bps);
        let s = sentences(&tx(Some(pay), U256::ZERO, data), &from, "Igneum development chain 7310");
        assert!(s[0].contains("32.50 percent of the escrow to the payee, the rest to the payer"), "{}", s[0]);
    }

    #[test]
    fn bond_with_exit_sentence_by_registry_version() {
        // wallet-security's W-5: the lock form on a version-4 chain (devnet8), the lab form on version 3 (devnet7)
        use crate::authority::set_exit_data;
        let from = Address::parse("0xf39Fd6e51aad88F6F4ce6aB8827279cffFb92266").unwrap();
        let key = [7u8; 32];
        let exit = Address::parse("0x70997970C51812dc3A010C7d01b50e0d17dc79C8").unwrap();
        let data = set_exit_data(&key, &exit);
        let reg8 = crate::chain::Chain::igneum_devnet8().registry().unwrap();
        let s8 = sentences(&tx(Some(reg8), parse_ign("100").unwrap(), data.clone()), &from, "Igneum development chain 7318");
        assert!(s8[0].contains("only after the lock"), "{}", s8[0]);
        assert!(s8[0].contains("before it the chain refuses the call and nothing moves"), "{}", s8[0]);
        let reg7 = crate::chain::Chain::igneum_devnet7().registry().unwrap();
        let s7 = sentences(&tx(Some(reg7), parse_ign("100").unwrap(), data), &from, "Igneum development chain 7317");
        assert!(s7[0].contains("changes the exit address at once"), "{}", s7[0]);
    }

    #[test]
    fn pay_money_lines() {
        fn word(v: u64) -> [u8; 32] {
            let mut w = [0u8; 32];
            w[24..].copy_from_slice(&v.to_be_bytes());
            w
        }
        fn addr_word(a: &Address) -> [u8; 32] {
            let mut w = [0u8; 32];
            w[12..].copy_from_slice(&a.0);
            w
        }
        let pay = Address::parse("0x90F79bf6EB2c4f870365E785982E1f101E93b906").unwrap();
        let payee = Address::parse("0x70997970C51812dc3A010C7d01b50e0d17dc79C8").unwrap();
        let arbiter = Address::parse("0x3C44CdDdB6a900fa2b585dd299e03d12FA4293BC").unwrap();
        let from = Address::default();
        let ign = |n: u64| U256::from_u64(n).checked_mul_u64(1_000_000_000_000_000).unwrap();
        // authorise(payee, native, 1.5 IGN a period, 0.01 IGN operator fee, every 2592000 s, 1 to 100, cap 20 IGN)
        let mut data = selector("authorise(address,address,uint256,uint256,uint64,uint64,uint64,uint256)").to_vec();
        data.extend_from_slice(&addr_word(&payee));
        data.extend_from_slice(&[0u8; 32]);
        data.extend_from_slice(&ign(1500).0);
        data.extend_from_slice(&ign(10).0);
        data.extend_from_slice(&word(2_592_000));
        data.extend_from_slice(&word(1));
        data.extend_from_slice(&word(100));
        data.extend_from_slice(&ign(20_000).0);
        match decode_call(Some(pay), &data) {
            Call::Authorise { per_period, operator_fee, cap, .. } => {
                assert_eq!(per_period, ign(1500));
                assert_eq!(operator_fee, ign(10));
                assert_eq!(cap, ign(20_000));
            }
            other => panic!("{other:?}"),
        }
        let s = sentences(&tx(Some(pay), ign(5000), data), &from, "Igneum development chain 7310");
        assert!(s[0].starts_with("You authorise 0x709979…79C8 to be paid 1.5 IGN every 2592000 seconds"), "{}", s[0]);
        assert!(s[1].starts_with("Each collection: gross 1.51 IGN; 1.5 IGN to 0x709979…79C8; the operator fee 0.01 IGN to whoever submits the collection"), "{}", s[1]);
        assert!(s[1].contains("Igneum Labs is paid nothing"), "{}", s[1]);
        assert_eq!(s[2], "Deposited now: 5 IGN, yours to withdraw while unspent");
        assert!(s[3].starts_with("Fee up to"), "{}", s[3]);

        // open(payee, arbiter, native, 3 IGN, deadline 1700000000, PayerRefunds)
        let mut data = selector("open(address,address,address,uint256,uint64,uint8)").to_vec();
        data.extend_from_slice(&addr_word(&payee));
        data.extend_from_slice(&addr_word(&arbiter));
        data.extend_from_slice(&[0u8; 32]);
        data.extend_from_slice(&ign(3000).0);
        data.extend_from_slice(&word(1_700_000_000));
        data.extend_from_slice(&word(1));
        let s = sentences(&tx(Some(pay), ign(3000), data), &from, "Igneum development chain 7310");
        assert!(s[0].contains("You lock 3 IGN in escrow for 0x709979…79C8"), "{}", s[0]);
        assert!(s[0].contains("you may take it back; on a dispute before then only the arbiter 0x3C44Cd…93BC decides"), "{}", s[0]);
        assert!(s[1].contains("the arbiter is paid nothing by it"), "{}", s[1]);

        // createInvoice(token, 250 units, expiry, memo, anyone)
        let mut data = selector("createInvoice(address,uint256,uint64,bytes32,address)").to_vec();
        data.extend_from_slice(&addr_word(&arbiter));
        data.extend_from_slice(&word(250));
        data.extend_from_slice(&word(1_700_000_000));
        data.extend_from_slice(&[0xabu8; 32]);
        data.extend_from_slice(&[0u8; 32]);
        let s = sentences(&tx(Some(pay), U256::ZERO, data), &from, "Igneum development chain 7310");
        assert!(s[0].starts_with("You create an invoice for 250 units of the token at 0x3C44Cd…93BC on Igneum Pay at 0x90F79b…b906, payable by anyone"), "{}", s[0]);
        assert!(s[1].contains("you receive 250 units of the token at 0x3C44Cd…93BC net"), "{}", s[1]);

        // payRequest((payee, native, 2 IGN, expiry, memo, payer 0, nonce), sig) sent with the wrong value
        let mut data = selector("payRequest((address,address,uint256,uint64,bytes32,address,uint256),bytes)").to_vec();
        data.extend_from_slice(&addr_word(&payee));
        data.extend_from_slice(&[0u8; 32]);
        data.extend_from_slice(&ign(2000).0);
        data.extend_from_slice(&word(1_700_000_000));
        data.extend_from_slice(&[0xcdu8; 32]);
        data.extend_from_slice(&[0u8; 32]);
        data.extend_from_slice(&word(7));
        data.extend_from_slice(&word(0x100));
        data.extend_from_slice(&word(0));
        let s = sentences(&tx(Some(pay), ign(1000), data), &from, "Igneum development chain 7310");
        assert!(s[0].starts_with("You pay a request of 2 IGN to 0x709979…79C8"), "{}", s[0]);
        assert!(s[1].starts_with("Gross 2 IGN; fees: none"), "{}", s[1]);
        assert_eq!(s[2], "This call sends 1 IGN, which is not the request's amount: the contract will refuse it");

        // pay(bytes32) with value: the net line beside the first sentence
        let mut data = selector("pay(bytes32)").to_vec();
        data.extend_from_slice(&[0xabu8; 32]);
        let s = sentences(&tx(Some(pay), ign(1000), data), &from, "Igneum development chain 7310");
        assert!(s[1].starts_with("Gross 1 IGN; fees: none, the contract takes nothing and Igneum Labs is paid nothing; the payee receives 1 IGN net"), "{}", s[1]);
    }

    #[test]
    fn unknown_selector_is_named_by_its_bytes() {
        let token = Address::parse("0x70997970C51812dc3A010C7d01b50e0d17dc79C8").unwrap();
        match decode_call(Some(token), &[1, 2, 3, 4, 5]) {
            Call::Unknown { selector: s, bytes } => {
                assert_eq!(s, [1, 2, 3, 4]);
                assert_eq!(bytes, 5);
            }
            other => panic!("{other:?}"),
        }
    }
}
