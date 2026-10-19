//! The password rule (the project's ruling of 12 October 2026 on wallet-security's ST-4): a minimum of twelve characters
//! and no composition rules (they lower real entropy; a minimum length with a meter raises it), an entropy estimate
//! shown as a plain meter (weak, fair, strong), a refusal under twelve with the count shown, and the common
//! passwords refused by name from a list shipped with the app. The page runs the same arithmetic for the live
//! meter (app/ui/app.js, `passwordMeter`); the signer enforces the minimum where every new password passes; the
//! engine checks the list before the signer sees the password.
//!
//! The estimate is in-house and deliberately simple: the characters' classes give a pool size, the length gives
//! bits as length x log2(pool), and three things that make a password easier than its length says lower it: a
//! run of one character, a stepped run (abcd, 1234, qwerty rows are not modelled), and a word repeated.

use crate::error::{Error, Result};

/// The minimum length in characters (not bytes).
pub const MINIMUM: usize = 12;

/// The sentence shown beside the meter, in the page's words.
pub const SENTENCE: &str = "The password protects the file on this machine only and never the coins on the chain.";

#[derive(Debug, Clone, PartialEq, Eq)]
pub enum Grade {
    Weak,
    Fair,
    Strong,
}

impl Grade {
    pub fn word(&self) -> &'static str {
        match self {
            Grade::Weak => "weak",
            Grade::Fair => "fair",
            Grade::Strong => "strong",
        }
    }
}

/// The estimate's bits (rounded) and its grade: under 40 weak, under 60 fair, 60 and over strong.
pub fn estimate(pw: &str) -> (u32, Grade) {
    let chars: Vec<char> = pw.chars().collect();
    if chars.is_empty() {
        return (0, Grade::Weak);
    }
    let (mut lower, mut upper, mut digit, mut other) = (false, false, false, false);
    for c in &chars {
        if c.is_ascii_lowercase() {
            lower = true
        } else if c.is_ascii_uppercase() {
            upper = true
        } else if c.is_ascii_digit() {
            digit = true
        } else {
            other = true
        }
    }
    let pool: f64 = (if lower { 26.0 } else { 0.0 }) + (if upper { 26.0 } else { 0.0 }) + (if digit { 10.0 } else { 0.0 }) + (if other { 33.0 } else { 0.0 });
    let pool = pool.max(10.0);
    // the effective length: a character that repeats the one before, or steps by one from it, counts a quarter
    let mut effective = 1.0f64;
    for w in chars.windows(2) {
        let (a, b) = (w[0] as i64, w[1] as i64);
        effective += if b == a || b == a + 1 || b == a - 1 { 0.25 } else { 1.0 };
    }
    // a password that is one half repeated (abcabc) counts as its half
    let n = chars.len();
    if n >= 4 && n % 2 == 0 && chars[..n / 2] == chars[n / 2..] {
        effective /= 2.0;
    }
    let bits = effective * pool.log2();
    let bits = bits.round().max(0.0) as u32;
    let grade = if bits < 40 {
        Grade::Weak
    } else if bits < 60 {
        Grade::Fair
    } else {
        Grade::Strong
    };
    (bits, grade)
}

/// The minimum, with the count in the refusal.
pub fn check_minimum(pw: &str) -> Result<()> {
    let n = pw.chars().count();
    if n < MINIMUM {
        return Err(Error::Input(format!("the password is {n} characters; at least {MINIMUM}")));
    }
    Ok(())
}

/// One character repeated for the whole length (the security review's note of 12 October 2026 on ST-4): the cheapest
/// password the minimum allows, which an offline dictionary finds in a first guess whatever the KDF's cost. This
/// is not a composition rule (it requires no character classes); it refuses a single degenerate case. The common
/// list (check_common) refuses the rest of the dictionary's first guesses.
pub fn check_not_trivial(pw: &str) -> Result<()> {
    let mut chars = pw.chars();
    if let Some(first) = chars.next() {
        if chars.all(|c| c == first) {
            return Err(Error::Input("that password is one character repeated; choose a less guessable one".into()));
        }
    }
    Ok(())
}

/// The common list, one password a line, compared whole and case-insensitively; a match is refused by name.
pub fn check_common<'a>(pw: &str, common: impl IntoIterator<Item = &'a str>) -> Result<()> {
    let p = pw.trim().to_lowercase();
    for c in common {
        if c.trim().to_lowercase() == p {
            return Err(Error::Input("That password is on a list of common passwords. Choose another.".into()));
        }
    }
    Ok(())
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn minimum_counts_characters() {
        assert!(check_minimum("elevenchars").is_err());
        assert!(check_minimum("twelve chars").is_ok());
        let e = check_minimum("short").unwrap_err().to_string();
        assert!(e.contains("5 characters") && e.contains("12"), "{e}");
    }

    #[test]
    fn grades_move_with_length_and_pool() {
        assert_eq!(estimate("aaaaaaaaaaaa").1, Grade::Weak);
        assert_eq!(estimate("123456789012").1, Grade::Weak);
        assert_eq!(estimate("correct horse battery staple").1, Grade::Strong);
        let (b1, _) = estimate("twelvechars!");
        let (b2, _) = estimate("twelvecharsmore!");
        assert!(b2 > b1);
        assert_eq!(estimate("abcabc").0, estimate("abc").0);
    }

    #[test]
    fn one_repeated_character_is_refused() {
        assert!(check_not_trivial("aaaaaaaaaaaa").is_err());
        assert!(check_not_trivial("111111111111").is_err());
        assert!(check_not_trivial("a").is_err());
        assert!(check_not_trivial("ababababab").is_ok());
        assert!(check_not_trivial("twelve chars").is_ok());
    }

    #[test]
    fn common_is_refused_by_name() {
        let list = ["password", "123456", "Password1"];
        assert!(check_common("PASSWORD", list).is_err());
        assert!(check_common("password1", list).is_err());
        assert!(check_common("a long and private one", list).is_ok());
    }
}
