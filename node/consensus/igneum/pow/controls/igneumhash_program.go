//go:build controls && cgo

package controls

/*
#include <stdint.h>
#include <stdlib.h>
#include "ignpow.h"
#include "igneumhash.h"
*/
import "C"

import (
	"math/big"
	"unsafe"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/consensus/igneum/pow"
)

// The per-period programme through the reference (pow.Programmed; the
// chain-side form is docs/igneumhash/schedule/DEVNET11-R0E-CHAIN-FORM.md
// and the engine's program.go): the document's PERIOD_BLOCKS is the
// period, and the view under an anchor runs the reference's anchored
// entries (the hash team's ignh_hash_anchored, ignh_hash_keyed_anchored and
// ignh_hash_keyed_m_anchored), which draw the period's programme from
// (chain id, height / PERIOD_BLOCKS, the anchor) and cache it inside the
// context by that pair, so one context serves two branches and two periods
// without a rebuild. The plain entries keep the document's anchor (the
// vector sets and the tools).

// PeriodBlocks implements pow.Programmed: the document's period_blocks,
// zero on a document without a programme (versions below 9).
func (a *ihRef) PeriodBlocks(height uint64) uint64 {
	v, ok := a.fields["period_blocks"]
	if !ok {
		return 0
	}
	f, ok := v.(float64)
	if !ok || f <= 0 {
		return 0
	}
	return uint64(f)
}

// Under implements pow.Programmed.
func (a *ihRef) Under(anchor common.Hash) pow.Algorithm {
	return &ihUnder{ihRef: a, anchor: anchor}
}

// ihUnder is the adapter bound to one period anchor: the three hash
// entries under it, everything else the adapter's own.
type ihUnder struct {
	*ihRef
	anchor common.Hash
}

func (u *ihUnder) Compute(headerHash common.Hash, height uint64, nonce uint64) (common.Hash, common.Hash) {
	var mix, out common.Hash
	st := C.int(C.IGNPOW_OK)
	if err := u.withContext(height, func(ctx *C.ignpow_ctx) {
		st = C.ignh_hash_anchored(ctx, C.uint64_t(height), (*C.uint8_t)(unsafe.Pointer(&headerHash[0])), C.uint64_t(nonce),
			(*C.uint8_t)(unsafe.Pointer(&u.anchor[0])), (*C.uint8_t)(unsafe.Pointer(&mix[0])), (*C.uint8_t)(unsafe.Pointer(&out[0])))
	}); err != nil || st != C.IGNPOW_OK {
		return common.Hash{}, common.Hash{}
	}
	return mix, out
}

func (u *ihUnder) ComputeKeyed(headerHash common.Hash, height uint64, nonce uint64, sk *[32]byte, shares []byte) (common.Hash, common.Hash, []byte) {
	s := u.KeyedRounds(height)
	if s == 0 {
		return common.Hash{}, common.Hash{}, nil
	}
	buf := make([]byte, s*pow.ShareBytes)
	var skp *C.uint8_t
	if sk != nil {
		skp = (*C.uint8_t)(unsafe.Pointer(&sk[0]))
	} else {
		if uint64(len(shares)) != s*pow.ShareBytes {
			return common.Hash{}, common.Hash{}, nil
		}
		copy(buf, shares)
	}
	var mix, out common.Hash
	st := C.int(C.IGNPOW_OK)
	if err := u.withContext(height, func(ctx *C.ignpow_ctx) {
		st = C.ignh_hash_keyed_anchored(ctx, C.uint64_t(height), (*C.uint8_t)(unsafe.Pointer(&headerHash[0])), C.uint64_t(nonce),
			skp, (*C.uint8_t)(unsafe.Pointer(&u.anchor[0])), (*C.uint8_t)(unsafe.Pointer(&buf[0])), (*C.uint8_t)(unsafe.Pointer(&mix[0])), (*C.uint8_t)(unsafe.Pointer(&out[0])))
	}); err != nil || st != C.IGNPOW_OK {
		return common.Hash{}, common.Hash{}, nil
	}
	return mix, out, buf
}

func (u *ihUnder) ComputeKeyedM(headerHash common.Hash, height uint64, nonce uint64, sk *[32]byte, shares []byte) (common.Hash, common.Hash, []byte, []byte) {
	s := u.KeyedRounds(height)
	if s == 0 {
		return common.Hash{}, common.Hash{}, nil, nil
	}
	buf := make([]byte, s*pow.ShareBytes)
	m := make([]byte, s*32)
	var skp *C.uint8_t
	if sk != nil {
		skp = (*C.uint8_t)(unsafe.Pointer(&sk[0]))
	} else {
		if uint64(len(shares)) != s*pow.ShareBytes {
			return common.Hash{}, common.Hash{}, nil, nil
		}
		copy(buf, shares)
	}
	var mix, out common.Hash
	st := C.int(C.IGNPOW_OK)
	if err := u.withContext(height, func(ctx *C.ignpow_ctx) {
		st = C.ignh_hash_keyed_m_anchored(ctx, C.uint64_t(height), (*C.uint8_t)(unsafe.Pointer(&headerHash[0])), C.uint64_t(nonce),
			skp, (*C.uint8_t)(unsafe.Pointer(&u.anchor[0])), (*C.uint8_t)(unsafe.Pointer(&buf[0])), (*C.uint8_t)(unsafe.Pointer(&mix[0])), (*C.uint8_t)(unsafe.Pointer(&out[0])), (*C.uint8_t)(unsafe.Pointer(&m[0])))
	}); err != nil || st != C.IGNPOW_OK {
		return common.Hash{}, common.Hash{}, nil, nil
	}
	return mix, out, buf, m
}

// Verify is the shared body over the view, so the mix and the result are
// the anchored hash's.
func (u *ihUnder) Verify(headerHash common.Hash, height uint64, nonce uint64, mixDigest common.Hash, difficulty *big.Int, fullDataset bool) error {
	if pow.Target(difficulty) == nil {
		return pow.ErrInvalidDifficulty
	}
	return pow.Verify(u, headerHash, height, nonce, mixDigest, difficulty)
}

// Under on a view rebinds to the new anchor.
func (u *ihUnder) Under(anchor common.Hash) pow.Algorithm { return &ihUnder{ihRef: u.ihRef, anchor: anchor} }
