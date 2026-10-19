package igneum

import (
	"errors"
	"os"
	"sync"

	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/log"
)

// The research instruments (DEVNET6-RULES.md; the project's rule of 18:0x UK, 11
// October 2026): two sealer-side modes that exist only to drive the guard's
// rejection rows, refused outside the lab build and never shipped.
//
//	--dev.withhold N      the sealer holds each block it seals, broadcasting
//	                      nothing, until it holds N; then it releases them at
//	                      once, in order. Two withholding sealers race at a
//	                      chosen depth on command (C13's forced branch races,
//	                      the outside review's condition 4). The blocks are the
//	                      node's own, written to its own chain as always;
//	                      only the broadcast is delayed.
//	--dev.lax-endorse F   the sealer attaches the endorsement list in file F
//	                      to the blocks it seals WITHOUT verifying the
//	                      entries, so the other nodes' import refusals are
//	                      exercised by class (C11's entry-level classes: a
//	                      unit not in the table, a lot that did not hit, a
//	                      failing signature, a unit twice). The file is the
//	                      entries' raw bytes (100 per entry), from security.
var errLabModeOffBuild = errors.New("igneum: the research instruments (--dev.withhold, --dev.lax-endorse) are refused outside the lab build")

type labMode struct {
	mu sync.Mutex
	// withhold: N and the blocks held so far, by the order they were sealed.
	withholdN uint64
	held      []*types.Block
	// laxEndorsePath: the file of endorsement entries attached unverified,
	// re-read on every seal so the harness can rewrite it against the live
	// parent between blocks (security's C11 generator; 14:0x UK).
	laxEndorsePath string
	// laxLast is the last whole list read from the file: attached when a
	// read lands between the harness's rewrite steps and sees an empty or
	// torn file (security's sealer b sealed 1500 and 1514 with ZERO
	// entries and no instrument line, 11 October 2026: an empty read is a
	// whole number of entries, so the armed sealer fell through to the
	// compliant attach with nothing to attach).
	laxLast []byte
	// broadcast reveals released blocks that no fresh seal will carry (a
	// disarm by RPC); set by the backend to the mined-block post. nil in a
	// test, where the released blocks are returned to the caller instead.
	broadcast func([]*types.Block)
}

// SetLabBroadcast gives the withholding instrument a way to reveal blocks it
// released outside the worker's own seal path (an RPC disarm); the backend
// sets it to the mined-block event post.
func (e *Engine) SetLabBroadcast(fn func([]*types.Block)) {
	e.lab.mu.Lock()
	defer e.lab.mu.Unlock()
	e.lab.broadcast = fn
}

// SetWithhold arms the withholding sealer (N > 0) or disarms it (N == 0),
// releasing any held blocks to the caller so the worker can broadcast them.
// Refused off the lab build.
func (e *Engine) SetWithhold(n uint64) ([]*types.Block, error) {
	if !labBuild {
		return nil, errLabModeOffBuild
	}
	e.lab.mu.Lock()
	e.lab.withholdN = n
	released := e.lab.held
	e.lab.held = nil
	bc := e.lab.broadcast
	e.lab.mu.Unlock()
	if n > 0 {
		log.Warn("Research instrument armed: the sealer withholds its blocks", "rounds", n)
	} else if len(released) > 0 {
		log.Warn("Research instrument disarmed: releasing the withheld blocks", "count", len(released))
	}
	// A disarm reveals the held blocks through the backend's broadcaster
	// when one is set (the worker's own seal path handles the auto-release
	// at N); a test reads them from the return instead.
	if len(released) > 0 && bc != nil {
		bc(released)
	}
	return released, nil
}

// Withhold is the worker's hook for a block it has sealed and written to its
// own chain: it returns the blocks to broadcast now and whether THIS block is
// being held back. With the instrument off, the block is returned for an
// immediate broadcast and hold is false. With it on, the block joins the
// held set; when the set reaches N the whole set is returned and the buffer
// cleared (released in seal order), otherwise nothing is returned and hold is
// true. Off the lab build the instrument is never armed, so this always
// broadcasts.
func (e *Engine) Withhold(block *types.Block) (release []*types.Block, hold bool) {
	e.lab.mu.Lock()
	defer e.lab.mu.Unlock()
	if e.lab.withholdN == 0 {
		return []*types.Block{block}, false
	}
	e.lab.held = append(e.lab.held, block)
	if uint64(len(e.lab.held)) < e.lab.withholdN {
		log.Warn("Research instrument: the sealer withholds a block", "number", block.NumberU64(), "held", len(e.lab.held), "of", e.lab.withholdN)
		return nil, true
	}
	release = e.lab.held
	e.lab.held = nil
	log.Warn("Research instrument: the sealer releases its withheld blocks", "count", len(release))
	return release, true
}

// SetLaxEndorse arms the lax-endorsement sealer with the entries in path (the
// raw bytes, a multiple of the 100-byte entry), or disarms it with an empty
// path. Refused off the lab build.
func (e *Engine) SetLaxEndorse(path string) error {
	if !labBuild {
		return errLabModeOffBuild
	}
	if path == "" {
		e.lab.mu.Lock()
		e.lab.laxEndorsePath = ""
		e.lab.mu.Unlock()
		return nil
	}
	// Validate the file once at arm time; it is re-read on every seal.
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	if len(data)%types.EndorsementBytes != 0 {
		return errors.New("igneum: --dev.lax-endorse: the file is not a whole number of 100-byte entries")
	}
	e.lab.mu.Lock()
	e.lab.laxEndorsePath = path
	e.lab.mu.Unlock()
	log.Warn("Research instrument armed: the sealer attaches an unverified endorsement list, re-read per seal", "entries", len(data)/types.EndorsementBytes, "file", path)
	return nil
}

// laxEndorseEntries reads the armed file fresh (so the harness can rewrite it
// against the live parent between seals) and returns its entries, or nil when
// disarmed or the file is unreadable or not a whole number of entries (logged,
// the block then carries the normal endorsements).
func (e *Engine) laxEndorseEntries() []byte {
	e.lab.mu.Lock()
	path := e.lab.laxEndorsePath
	e.lab.mu.Unlock()
	if path == "" {
		return nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		log.Warn("Research instrument: the lax endorsement file is unreadable this seal, the last list attached", "file", path, "err", err)
		return e.laxLastList()
	}
	if len(data) == 0 || len(data)%types.EndorsementBytes != 0 {
		// A read between the harness's rewrite steps (an empty file, or a
		// torn one): the last whole list, never a fall-through to the
		// compliant attach with nothing (sealer b's 1500 and 1514).
		log.Warn("Research instrument: the lax endorsement file is empty or torn this seal, the last list attached", "file", path, "bytes", len(data))
		return e.laxLastList()
	}
	e.lab.mu.Lock()
	e.lab.laxLast = data
	e.lab.mu.Unlock()
	return data
}

// laxLastList is the last whole list the armed sealer read (nil before the
// first; the attach then logs the empty read and seals nothing lax).
func (e *Engine) laxLastList() []byte {
	e.lab.mu.Lock()
	defer e.lab.mu.Unlock()
	return e.lab.laxLast
}
