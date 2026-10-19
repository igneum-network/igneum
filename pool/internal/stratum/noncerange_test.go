package stratum

import (
	"encoding/binary"
	"testing"

	"igneum.network/pool/internal/limits"
)

// TestNonceRangeOutlastsATemplate pins 0.1.66 (the project's order of 16 October 2026, the GPU team's read of a 5090 searching its
// 2^32 range through in 34 s and idling the rest of the block): a session's range is 2^44 nonces, never overflowing
// the 64-bit nonce for any extranonce1, and two sessions with different prefixes own disjoint ranges.
func TestNonceRangeOutlastsATemplate(t *testing.T) {
	if limits.NonceRangeBits < 44 {
		t.Fatalf("NonceRangeBits %d: a 5090 at 63 MH/s searches 2^%d through in %.0f s", limits.NonceRangeBits, limits.NonceRangeBits, float64(uint64(1)<<limits.NonceRangeBits)/63e6)
	}
	var e [limits.ExtraNonce1Bytes]byte
	for _, v := range []uint32{0, 1, 0x00ffffff, 0x01000000, 0xffffffff, 0x8e796a02} {
		binary.BigEndian.PutUint32(e[:], v)
		start, count := nonceRangeOf(e)
		if count != 1<<limits.NonceRangeBits {
			t.Fatalf("extranonce %08x: count %d", v, count)
		}
		if start+count-1 < start {
			t.Fatalf("extranonce %08x: the range overflows the nonce (start %d)", v, start)
		}
		if start>>limits.NonceRangeBits != uint64(v)&(1<<limits.NonceSessionBits-1) {
			t.Fatalf("extranonce %08x: the prefix is not its low %d bits (start %d)", v, limits.NonceSessionBits, start)
		}
	}
	var a, b [limits.ExtraNonce1Bytes]byte
	binary.BigEndian.PutUint32(a[:], 0x00000001)
	binary.BigEndian.PutUint32(b[:], 0x00000002)
	sa, ca := nonceRangeOf(a)
	sb, _ := nonceRangeOf(b)
	if sa+ca != sb {
		t.Fatalf("two prefixes' ranges are not adjacent and disjoint: %d+%d against %d", sa, ca, sb)
	}
}
