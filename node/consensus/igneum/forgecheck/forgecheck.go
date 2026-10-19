// Package forgecheck is protocol B's independent implementation of the Forge
// F0 relation (docs/igneumhash/forge/F0-RELATION.md, relation version 1),
// written from the protocol team's text alone (sections 1 to 5), never from its
// implementation in pow/forge, for the encoding check of FORGE.md section 9.
// Research only: nothing here is deployable.
package forgecheck

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"

	"lukechampine.com/blake3"
)

// The domains of section 2.1.
const (
	DomainX      = "Igneum/Forge/x/v1"
	DomainSeed   = "Igneum/Forge/seed/v1"
	DomainTicket = "Igneum/Forge/ticket/v1"
	DomainPublic = "Igneum/Forge/public/v1"
)

// Version is the relation version of this text.
const Version uint32 = 1

// XLen is the length of a version 1 x: 1 + 17 + 8 + 4 + 32 + 1 + 20 + 32 + 32 + 32 + 8.
const XLen = 187

var ErrEncoding = errors.New("forge f0: encoding rejected")

// Hd is H_d(m): BLAKE3-256 over one byte holding the domain's length, the
// domain's ASCII bytes, then m (section 1).
func Hd(domain string, m ...[]byte) [32]byte {
	in := append([]byte{byte(len(domain))}, domain...)
	return blake3.Sum256(append(in, bytes.Join(m, nil)...))
}

// Fields is the tuple of section 2.2.
type Fields struct {
	ChainID  uint64
	Version  uint32
	ParentID [32]byte
	Reward   [20]byte
	Contents [32]byte
	Target   [32]byte
	ParamsID [32]byte
	Nonce    uint64
}

// Encode is x: the fields in order, big-endian fixed-width integers, the two
// variable-length fields with their length byte (section 2.2).
func (f Fields) Encode() []byte {
	x := make([]byte, 0, XLen)
	x = append(x, byte(len(DomainX)))
	x = append(x, DomainX...)
	x = binary.BigEndian.AppendUint64(x, f.ChainID)
	x = binary.BigEndian.AppendUint32(x, f.Version)
	x = append(x, f.ParentID[:]...)
	x = append(x, byte(len(f.Reward)))
	x = append(x, f.Reward[:]...)
	x = append(x, f.Contents[:]...)
	x = append(x, f.Target[:]...)
	x = append(x, f.ParamsID[:]...)
	x = binary.BigEndian.AppendUint64(x, f.Nonce)
	return x
}

// Decode is the strict decoder of section 2.4: one byte string decodes to
// one tuple or to nothing. It rejects any length other than 187, a domain
// length other than 17 or domain bytes other than the x domain, a reward
// length other than 20, a relation version other than 1.
func Decode(x []byte) (Fields, error) {
	var f Fields
	if len(x) != XLen {
		return f, fmt.Errorf("%w: x of %d bytes, not %d", ErrEncoding, len(x), XLen)
	}
	if x[0] != byte(len(DomainX)) {
		return f, fmt.Errorf("%w: domain length %d, not %d", ErrEncoding, x[0], len(DomainX))
	}
	if string(x[1:18]) != DomainX {
		return f, fmt.Errorf("%w: domain bytes are not the x domain", ErrEncoding)
	}
	f.ChainID = binary.BigEndian.Uint64(x[18:26])
	f.Version = binary.BigEndian.Uint32(x[26:30])
	if f.Version != Version {
		return f, fmt.Errorf("%w: relation version %d, not %d", ErrEncoding, f.Version, Version)
	}
	copy(f.ParentID[:], x[30:62])
	if x[62] != 20 {
		return f, fmt.Errorf("%w: reward length %d, not 20", ErrEncoding, x[62])
	}
	copy(f.Reward[:], x[63:83])
	copy(f.Contents[:], x[83:115])
	copy(f.Target[:], x[115:147])
	copy(f.ParamsID[:], x[147:179])
	f.Nonce = binary.BigEndian.Uint64(x[179:187])
	return f, nil
}

// Seed is H_seed(x) (section 3).
func Seed(x []byte) [32]byte { return Hd(DomainSeed, x) }

// Ticket is H_ticket(x || y) (section 4); y must be exactly 32 bytes.
func Ticket(x []byte, y [32]byte) [32]byte { return Hd(DomainTicket, x, y[:]) }

// Qualifies: the ticket as a 256-bit big-endian integer is at most the target (inclusive).
func Qualifies(ticket, target [32]byte) bool { return bytes.Compare(ticket[:], target[:]) <= 0 }

// Public is H_public(program id || x || y) (section 5).
func Public(programID [32]byte, x []byte, y [32]byte) [32]byte {
	return Hd(DomainPublic, programID[:], x, y[:])
}

// CheckY enforces section 2.4's rule that a y of any length other than 32
// bytes is rejected before it enters a ticket.
func CheckY(y []byte) ([32]byte, error) {
	var out [32]byte
	if len(y) != 32 {
		return out, fmt.Errorf("%w: y of %d bytes, not 32", ErrEncoding, len(y))
	}
	copy(out[:], y)
	return out, nil
}
