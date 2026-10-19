// Package limits holds the bounds every input to the pool must satisfy
// (docs/ecosystem/DESIGN.md, section 2.5). The parser rejects before it
// allocates; nothing a client declares sets a buffer size.
package limits

import "strings"

const (
	// MaxFrameBytes bounds one newline-delimited frame on the wire.
	MaxFrameBytes = 16 * 1024
	// MaxWorkerName bounds the worker name a session authorises with.
	MaxWorkerName = 64
	// MaxJobsRetained is how many jobs a session keeps, and how many the
	// server keeps PER MEMBER on the member-node form; a share on an older
	// job is stale. Eight templates is about two minutes at a 15 s pace and
	// 160 s at the 20 s clock: a slow card's search on a template that was
	// the head when it began still pays (the share's parent is canonical
	// unless the chain moved sideways, and then the sealer says so); the
	// server's retention was four across every member (the project's order of
	// 07:3x UK, 12 October 2026: the mini's eight percent stale).
	MaxJobsRetained = 8
	// MaxSharesPerSecond is the per-session token-bucket rate.
	MaxSharesPerSecond = 50
	// MaxBlockClaimsPerSecond is the per-session rate for submissions that
	// claim to meet the block boundary; separate from the share rate so a
	// block is never lost behind shares, small so a liar pays little.
	MaxBlockClaimsPerSecond = 5
	// MaxSessionsPerIP bounds concurrent sessions from one source address.
	MaxSessionsPerIP = 32
	// MaxWorkersPerAccount bounds distinct worker names under one account.
	MaxWorkersPerAccount = 1024
	// JobIDHexLen is the length of a pool-issued job id in hex digits.
	JobIDHexLen = 16
	// AddressHexLen is the length of a payout address after the 0x prefix.
	AddressHexLen = 40
	// ExtraNonce1Bytes is the per-session extranonce the pool assigns.
	ExtraNonce1Bytes = 4
	// MaxFeeBps is the whole, in basis points: a fee cannot exceed it.
	MaxFeeBps = 10000
	// DefaultMaturityBlocks is the payout maturity horizon: security's
	// SD-15 (240 blocks, about one hour at 15-second blocks, the same
	// number as the wallet's High value band). Credit at this depth, pay
	// only after; re-set only with SD-15.
	DefaultMaturityBlocks = 240
	// NonceRangeBits is the per-session nonce range: a session owns [prefix << NonceRangeBits, +2^NonceRangeBits),
	// the prefix the low (64 - NonceRangeBits) bits of its extranonce1. 44 bits (the project's order of 16 October 2026,
	// the GPU team's read of the owned cohort's fastest 5090 on the live form: at 63 MH/s a card searched a 2^32 range split over the machine's
	// lanes through in about 34 s and idled the rest of every block): 2^44 nonces is 77 hours for one card at that
	// rate and 2.4 hours for a 32-card rig splitting it, above any template life the chains have shown (the longest
	// interval of record 24 minutes); the template is still refreshed on every block. On the share lane each session's
	// header carries its whole extranonce1, so two sessions with one prefix never collide there; on the block form a
	// shared prefix (one pair in a million) only repeats a search.
	NonceRangeBits = 44
	// NonceSessionBits is the width of the session prefix above the range.
	NonceSessionBits = 64 - NonceRangeBits
)

// ValidWorkerName accepts 1 to MaxWorkerName characters from
// A-Z, a-z, 0-9, '.', '_' and '-'.
func ValidWorkerName(s string) bool {
	if len(s) == 0 || len(s) > MaxWorkerName {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9', c == '.', c == '_', c == '-':
		default:
			return false
		}
	}
	return true
}

// IsHex reports whether s is exactly n hex digits.
func IsHex(s string, n int) bool {
	if len(s) != n {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f') || (c >= 'A' && c <= 'F')) {
			return false
		}
	}
	return true
}

// ValidAddress accepts "0x" followed by AddressHexLen hex digits. Checksum
// casing is not enforced here; the payout path lowercases before use.
func ValidAddress(s string) bool {
	if !strings.HasPrefix(s, "0x") {
		return false
	}
	return IsHex(s[2:], AddressHexLen)
}

// ValidJobID accepts a pool-issued job id.
func ValidJobID(s string) bool { return IsHex(s, JobIDHexLen) }
