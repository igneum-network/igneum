package igneum

import (
	"encoding/binary"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"sync"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/consensus"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/log"
)

// The share journal (DESIGN.md PA-44): a sealer's accepted, not yet
// carried shares on disk under the data dir, so a restart between a share's
// acceptance and the block that carries it still pays the share (a miner's
// accepted-but-unpaid shares never vanish on a node restart; devnet7's
// swap of 11 October 2026 dropped 4,119 from a pool held in memory).
//
// Append-only, and since the ledger change of 11 October 2026 (the project's
// order of 19:2x UK, the 180 unaccounted shares) never rewritten: an
// acceptance record for each share as the pool accepts it (the pool's own
// fields: the height, the parent, the address, the extra nonce, the nonce,
// the mix), a carriage record for each share a canonical block carries
// (MarkCarried, every head), an expiry record for each pooled share that
// left the pool uncarried (the window's end or a parent that lost a
// reorg). The file is the node's share LEDGER: at start it rebuilds the
// per-address accounts (shareaccount.go), so a miner's accepted = carried +
// pooled + expired holds across restarts, and it replays the pool: every
// acceptance with neither a carriage nor an expiry record is re-verified
// as a submission would be (the work at the chain's difficulty, the
// recency against the current head, no duplicate); one the window passed
// while the node was down is recorded expired then, by the window or by a
// reorg, never counted as a refusal. The credits of settled shares live in
// state and survive by construction. A truncated tail (a crash mid-write)
// is cut at the last whole record before the appends go on.
const (
	shareJournalAccepted       = 1 // height LE64 || parent (32) || address (20) || extraNonce LE64 || nonce LE64 || mix (32)
	shareJournalCarried        = 2 // address (20) || extraNonce LE64 || nonce LE64 (the first form, without the height: READ as carried at any height, never written again)
	shareJournalCarriedAt      = 3 // address (20) || extraNonce LE64 || nonce LE64 || height LE64 (the node team's finding of 17:2x UK, 12 October 2026: a share's identity includes its height)
	shareJournalExpired        = 4 // why (1: the window's end, 2: the parent lost a reorg) || height LE64 || address (20) || extraNonce LE64 || nonce LE64
	shareJournalAcceptedStep   = 5 // the accepted record (type 1's fields) || step (1), PA-53's declared target step; written for every acceptance from PA-53, read beside type 1
	shareJournalUncarried      = 6 // type 3's fields: the carriage undone, the carrying block lost its height (UnmarkCarried, identity 4 of 12 October 2026); the replay reads it as the carried key withdrawn
	shareJournalCarriedP       = 7 // type 3's fields || parent (32): the carriage keyed WITH the share's parent (the ledger-20 class of 12 October 2026: a sibling parent's share of the same address, nonces and height is another share); written for every carriage from this form, type 3 read beside it for older journals
	shareJournalUncarriedP     = 8 // type 7's fields: the carriage undone, with the parent
	shareJournalAcceptedGraph  = 9 // type 5's fields || ticket (168): a graph share's accepted record (WHOLEWORK.md section 10 (b))
	shareJournalCarriedPBytes  = 20 + 8 + 8 + 8 + 32
	shareJournalAcceptedBytes  = 8 + 32 + 20 + 8 + 8 + 32
	shareJournalCarriedBytes   = 20 + 8 + 8
	shareJournalCarriedAtBytes = 20 + 8 + 8 + 8
	shareJournalExpiredBytes   = 1 + 8 + 20 + 8 + 8
)

type shareJournal struct {
	mu       sync.Mutex
	path     string
	f        *os.File
	replayed uint64 // shares the last replay put back into the pool
	dropped  uint64 // shares the last replay refused (carried, stale, duplicate, or failing the work)
}

// shareKey is the pool's carriage key of a share: the address, the extra
// nonce and the nonce (MarkCarried's reading of a carried block).
func shareKey(address common.Address, extraNonce, nonce uint64) string {
	return string(address[:]) + fmt.Sprint(extraNonce, nonce)
}

func acceptedRecord(ps pooledShare) []byte {
	if len(ps.share.Ticket) == types.GraphTicketBytes {
		// type 9: type 5's fields then the graph share's ticket (section
		// 10 (b)), so the replay re-verifies it as submitted.
		b := make([]byte, 1+shareJournalAcceptedBytes+1, 1+shareJournalAcceptedBytes+1+types.GraphTicketBytes)
		b[0] = shareJournalAcceptedGraph
		binary.LittleEndian.PutUint64(b[1:], ps.height)
		copy(b[9:], ps.parent[:])
		copy(b[41:], ps.share.Address[:])
		binary.LittleEndian.PutUint64(b[61:], ps.share.ExtraNonce)
		binary.LittleEndian.PutUint64(b[69:], ps.share.Nonce)
		copy(b[77:], ps.share.MixDigest[:])
		b[109] = ps.share.Step
		return append(b, ps.share.Ticket...)
	}
	b := make([]byte, 1+shareJournalAcceptedBytes+1)
	b[0] = shareJournalAcceptedStep
	binary.LittleEndian.PutUint64(b[1:], ps.height)
	copy(b[9:], ps.parent[:])
	copy(b[41:], ps.share.Address[:])
	binary.LittleEndian.PutUint64(b[61:], ps.share.ExtraNonce)
	binary.LittleEndian.PutUint64(b[69:], ps.share.Nonce)
	copy(b[77:], ps.share.MixDigest[:])
	b[109] = ps.share.Step
	return b
}

// carriedRecord is the carriage record WITH the share's height (type 3):
// a share's identity is (address, extraNonce, nonce, height), so a pooled
// share at another height with the same three fields is not marked carried
// (the node team's finding of 17:2x UK, 12 October 2026, the same class as
// MarkCarried's key).
func carriedRecord(address common.Address, extraNonce, nonce, height uint64) []byte {
	b := make([]byte, 1+shareJournalCarriedAtBytes)
	b[0] = shareJournalCarriedAt
	copy(b[1:], address[:])
	binary.LittleEndian.PutUint64(b[21:], extraNonce)
	binary.LittleEndian.PutUint64(b[29:], nonce)
	binary.LittleEndian.PutUint64(b[37:], height)
	return b
}

// uncarriedRecord is the carriage undone (type 6): the block that carried
// the share lost its height and the share is pooled again.
func uncarriedRecord(address common.Address, extraNonce, nonce, height uint64) []byte {
	b := carriedRecord(address, extraNonce, nonce, height)
	b[0] = shareJournalUncarried
	return b
}

// carriedRecordP is the carriage record with the share's parent (type 7).
func carriedRecordP(address common.Address, extraNonce, nonce, height uint64, parent common.Hash) []byte {
	b := make([]byte, 1+shareJournalCarriedPBytes)
	b[0] = shareJournalCarriedP
	copy(b[1:], address[:])
	binary.LittleEndian.PutUint64(b[21:], extraNonce)
	binary.LittleEndian.PutUint64(b[29:], nonce)
	binary.LittleEndian.PutUint64(b[37:], height)
	copy(b[45:], parent[:])
	return b
}

// uncarriedRecordP is the carriage undone, with the parent (type 8).
func uncarriedRecordP(address common.Address, extraNonce, nonce, height uint64, parent common.Hash) []byte {
	b := carriedRecordP(address, extraNonce, nonce, height, parent)
	b[0] = shareJournalUncarriedP
	return b
}

// carriedRecordAt is the carriage record in the form the chain runs at the
// share's height: type 7 (with the parent) from PolicyV1, type 3 below it.
func (e *Engine) carriedRecordAt(address common.Address, extraNonce, nonce, height uint64, parent common.Hash) []byte {
	if e.policyOn(height) {
		return carriedRecordP(address, extraNonce, nonce, height, parent)
	}
	return carriedRecord(address, extraNonce, nonce, height)
}

// uncarriedRecordAt is the carriage undone in the form of the height.
func (e *Engine) uncarriedRecordAt(address common.Address, extraNonce, nonce, height uint64, parent common.Hash) []byte {
	if e.policyOn(height) {
		return uncarriedRecordP(address, extraNonce, nonce, height, parent)
	}
	return uncarriedRecord(address, extraNonce, nonce, height)
}

// carriedPKey is the journal's carried key with the height and the parent
// (mineshares.go carriedKey's form).
func carriedPKey(address common.Address, extraNonce, nonce, height uint64, parent common.Hash) string {
	return shareKey(address, extraNonce, nonce) + "@" + fmt.Sprint(height) + "/" + parent.Hex()[2:18]
}

// carriedAtKey is the journal's carried key with the height.
func carriedAtKey(address common.Address, extraNonce, nonce, height uint64) string {
	return shareKey(address, extraNonce, nonce) + "@" + fmt.Sprint(height)
}

// expiredRecord is the expiry record (type 4): the share left the pool
// uncarried, by the window's end or by its parent losing a reorg.
func expiredRecord(ps pooledShare, why shareExpiry) []byte {
	b := make([]byte, 1+shareJournalExpiredBytes)
	b[0] = shareJournalExpired
	b[1] = byte(why)
	binary.LittleEndian.PutUint64(b[2:], ps.height)
	copy(b[10:], ps.share.Address[:])
	binary.LittleEndian.PutUint64(b[30:], ps.share.ExtraNonce)
	binary.LittleEndian.PutUint64(b[38:], ps.share.Nonce)
	return b
}

// shareLedger is a journal's parsed records: the acceptances in file
// order, the carried keys, the expired keys with their reason, and the
// bytes the whole records took (a truncated tail is cut there).
type shareLedger struct {
	accepted []pooledShare
	carried  map[string]bool
	expired  map[string]shareExpiry
	consumed int64
}

// parseShareJournal reads the records of a journal's bytes.
func parseShareJournal(data []byte) (l shareLedger) {
	l = shareLedger{carried: make(map[string]bool), expired: make(map[string]shareExpiry)}
	accepted := &l.accepted
	carried := l.carried
	total := len(data)
	// the named result: the deferred write lands after every return below
	defer func() { l.consumed = int64(total - len(data)) }()
	for len(data) > 0 {
		switch data[0] {
		case shareJournalExpired:
			if len(data) < 1+shareJournalExpiredBytes {
				return l
			}
			r := data[1 : 1+shareJournalExpiredBytes]
			var a common.Address
			copy(a[:], r[9:29])
			l.expired[carriedAtKey(a, binary.LittleEndian.Uint64(r[29:]), binary.LittleEndian.Uint64(r[37:]), binary.LittleEndian.Uint64(r[1:]))] = shareExpiry(r[0])
			data = data[1+shareJournalExpiredBytes:]
		case shareJournalAccepted, shareJournalAcceptedStep, shareJournalAcceptedGraph:
			// type 5 is type 1 with PA-53's step last; type 1 reads step 0;
			// type 9 is type 5 with the graph share's ticket last
			n := 1 + shareJournalAcceptedBytes
			if data[0] == shareJournalAcceptedStep || data[0] == shareJournalAcceptedGraph {
				n++
			}
			if data[0] == shareJournalAcceptedGraph {
				n += types.GraphTicketBytes
			}
			if len(data) < n {
				return l
			}
			r := data[1:n]
			var ps pooledShare
			ps.height = binary.LittleEndian.Uint64(r[0:])
			copy(ps.parent[:], r[8:40])
			copy(ps.share.Address[:], r[40:60])
			ps.share.ExtraNonce = binary.LittleEndian.Uint64(r[60:])
			ps.share.Nonce = binary.LittleEndian.Uint64(r[68:])
			copy(ps.share.MixDigest[:], r[76:108])
			if data[0] == shareJournalAcceptedStep || data[0] == shareJournalAcceptedGraph {
				ps.share.Step = r[108]
			}
			if data[0] == shareJournalAcceptedGraph {
				ps.share.Ticket = append([]byte(nil), r[109:109+types.GraphTicketBytes]...)
			}
			*accepted = append(*accepted, ps)
			data = data[n:]
		case shareJournalCarried:
			// the first form, without the height: carried at any height
			// (read for one swap of the journals written before type 3)
			if len(data) < 1+shareJournalCarriedBytes {
				return l
			}
			r := data[1 : 1+shareJournalCarriedBytes]
			var a common.Address
			copy(a[:], r[0:20])
			carried[shareKey(a, binary.LittleEndian.Uint64(r[20:]), binary.LittleEndian.Uint64(r[28:]))] = true
			data = data[1+shareJournalCarriedBytes:]
		case shareJournalCarriedAt:
			if len(data) < 1+shareJournalCarriedAtBytes {
				return l
			}
			r := data[1 : 1+shareJournalCarriedAtBytes]
			var a common.Address
			copy(a[:], r[0:20])
			carried[carriedAtKey(a, binary.LittleEndian.Uint64(r[20:]), binary.LittleEndian.Uint64(r[28:]), binary.LittleEndian.Uint64(r[36:]))] = true
			data = data[1+shareJournalCarriedAtBytes:]
		case shareJournalUncarried:
			// the carriage undone (the carrying block lost its height):
			// the carried key withdrawn, so the replay pools the share
			// again when its window still holds
			if len(data) < 1+shareJournalCarriedAtBytes {
				return l
			}
			r := data[1 : 1+shareJournalCarriedAtBytes]
			var a common.Address
			copy(a[:], r[0:20])
			// F-532 (17 October 2026): an uncarry withdraws the carriage
			// under EVERY key form the share may have been carried under
			// (the height form and the older height-less form beside the
			// one this record names), else an older record of another form
			// resurrects the carriage at the replay and a paid height's
			// class moves across a restart (the pool's first close under
			// 0.1.92 re-paid 8,639 old lines on the nine's rebuilt accounts).
			delete(carried, carriedAtKey(a, binary.LittleEndian.Uint64(r[20:]), binary.LittleEndian.Uint64(r[28:]), binary.LittleEndian.Uint64(r[36:])))
			delete(carried, shareKey(a, binary.LittleEndian.Uint64(r[20:]), binary.LittleEndian.Uint64(r[28:])))
			data = data[1+shareJournalCarriedAtBytes:]
		case shareJournalCarriedP, shareJournalUncarriedP:
			// the carriage with the share's parent (type 7) and its undoing
			// (type 8): the key carries the parent, so a sibling parent's
			// share of the same fields stays pooled at the replay
			if len(data) < 1+shareJournalCarriedPBytes {
				return l
			}
			r := data[1 : 1+shareJournalCarriedPBytes]
			var a common.Address
			var parent common.Hash
			copy(a[:], r[0:20])
			copy(parent[:], r[44:76])
			key := carriedPKey(a, binary.LittleEndian.Uint64(r[20:]), binary.LittleEndian.Uint64(r[28:]), binary.LittleEndian.Uint64(r[36:]), parent)
			if data[0] == shareJournalCarriedP {
				carried[key] = true
			} else {
				// F-532: the uncarry withdraws every key form (see type 6)
				delete(carried, key)
				delete(carried, carriedAtKey(a, binary.LittleEndian.Uint64(r[20:]), binary.LittleEndian.Uint64(r[28:]), binary.LittleEndian.Uint64(r[36:])))
				delete(carried, shareKey(a, binary.LittleEndian.Uint64(r[20:]), binary.LittleEndian.Uint64(r[28:])))
			}
			data = data[1+shareJournalCarriedPBytes:]
		default:
			// an unknown record type: the rest is unreadable
			return l
		}
	}
	return l
}

// write appends a record; a failure is logged and the pool goes on (the
// journal is a convenience of the sealer, never a rule).
func (j *shareJournal) write(rec []byte) {
	j.mu.Lock()
	defer j.mu.Unlock()
	if j.f == nil {
		return
	}
	if _, err := j.f.Write(rec); err != nil {
		log.Warn("Share journal write failed", "path", j.path, "err", err)
	}
}

// SetShareJournal opens the journal at path, replays it into the pool at
// the chain's current head and rewrites it with the live pool; the file is
// created when absent. An empty path leaves the pool in memory.
func (e *Engine) SetShareJournal(path string, chain consensus.ChainHeaderReader) error {
	if path == "" {
		return nil
	}
	data, err := os.ReadFile(path)
	if err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("igneum shares: journal %s: %w", path, err)
	}
	ledger := parseShareJournal(data)
	// The canonical chain's carriages within the window (F-42): a replayed
	// share any block up to the head carried, by any sealer, is dropped,
	// not re-admitted (security's pool read replayed 192, carried 0, and
	// every template re-carried them).
	chainCarried := e.carriedAtHead(chain)
	// The file is cut at its last whole record and opened for the appends
	// BEFORE the replay, so the records the replay writes (a carriage the
	// chain shows, an expiry the window passed) go to the ledger.
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("igneum shares: journal %s: %w", path, err)
	}
	if ledger.consumed < int64(len(data)) {
		if err := os.Truncate(path, ledger.consumed); err != nil {
			return fmt.Errorf("igneum shares: journal %s: %w", path, err)
		}
	}
	af, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return fmt.Errorf("igneum shares: journal %s: %w", path, err)
	}
	j := &e.shareJournal
	j.mu.Lock()
	if j.f != nil {
		j.f.Close()
	}
	j.path, j.f = path, af
	j.mu.Unlock()
	p := &e.sharePool
	p.mu.Lock()
	p.journal = j
	p.mu.Unlock()
	var replayed, dropped uint64
	var late []byte
	for _, ps := range ledger.accepted {
		key := carriedAtKey(ps.share.Address, ps.share.ExtraNonce, ps.share.Nonce, ps.height)
		p.mu.Lock()
		acc := p.account(ps.share.Address)
		acc.accepted[ps.height]++
		why, expired := ledger.expired[key]
		// the parent-keyed record first (type 7), then the older forms
		journaled := ledger.carried[carriedPKey(ps.share.Address, ps.share.ExtraNonce, ps.share.Nonce, ps.height, ps.parent)] || ledger.carried[key] || ledger.carried[shareKey(ps.share.Address, ps.share.ExtraNonce, ps.share.Nonce)]
		onChain := chainCarried[e.carriedKeyAt(ps.share, ps.height, ps.parent)]
		switch {
		case journaled || onChain:
			acc.carried[ps.height]++
			if !journaled {
				// carried while the node was down: the ledger gets the
				// record the running node would have written
				late = append(late, e.carriedRecordAt(ps.share.Address, ps.share.ExtraNonce, ps.share.Nonce, ps.height, ps.parent)...)
			}
		case expired:
			// The record's class, re-read against the chain: a parent
			// that is not canonical at its height is a reorg whatever the
			// record says (the records of 11 October 2026 before the
			// canonicity rule filed 531's and 537's under the window).
			if why == expiryReorg || expiryClass(chain, ps) == expiryReorg {
				acc.expiredReorg[ps.height]++
			} else {
				acc.expiredWindow[ps.height]++
			}
		}
		p.mu.Unlock()
		if journaled || onChain || expired {
			dropped++
			continue
		}
		parent := ps.parent
		if ok, _ := e.acceptShare(chain, ps.share.Address, ps.share.ExtraNonce, ps.share.Nonce, ps.share.MixDigest, ps.height, &parent, ps.share.Step, false, false, ps.share.Ticket); ok {
			replayed++
			continue
		}
		// Neither carried nor pooled again: the window passed, or the
		// parent lost a reorg, while the node was down. Recorded as that,
		// not as a refusal (the replay counts none).
		dropped++
		// The class by the parent's canonicity at its height (expiryClass),
		// never by the window's position at the replay.
		why = expiryClass(chain, ps)
		p.mu.Lock()
		if why == expiryReorg {
			acc.expiredReorg[ps.height]++
		} else {
			acc.expiredWindow[ps.height]++
		}
		p.mu.Unlock()
		late = append(late, expiredRecord(ps, why)...)
	}
	if len(late) > 0 {
		j.write(late)
	}
	j.mu.Lock()
	j.replayed, j.dropped = replayed, dropped
	j.mu.Unlock()
	log.Info("Share journal replayed", "path", path, "replayed", replayed, "dropped", dropped, "records", len(ledger.accepted), "accounts", e.shareAccountCount())
	return nil
}

// shareAccountCount is the number of addresses with a ledger.
func (e *Engine) shareAccountCount() int {
	p := &e.sharePool
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.accounts)
}

// pooledShares is the pool's live shares by height, then arrival order.
func (e *Engine) pooledShares() []pooledShare {
	p := &e.sharePool
	p.mu.Lock()
	defer p.mu.Unlock()
	var out []pooledShare
	for _, list := range p.byParent {
		out = append(out, list...)
	}
	sort.SliceStable(out, func(i, k int) bool { return out[i].height < out[k].height })
	return out
}

// closeShareJournal closes the file (Close).
func (e *Engine) closeShareJournal() {
	j := &e.shareJournal
	j.mu.Lock()
	defer j.mu.Unlock()
	if j.f != nil {
		j.f.Close()
		j.f = nil
	}
}

// journalCarried records a carried block's shares (MarkCarried) with each
// share's own height: the carrying block's height less the entry's parent
// index (k = 0 is the parent, so the share's height is the block's).
func (e *Engine) journalCarried(list []types.MinedShare, parents []common.Hash, blockHeight uint64) {
	j := &e.shareJournal
	j.mu.Lock()
	open := j.f != nil
	j.mu.Unlock()
	if !open {
		return
	}
	var rec []byte
	for i, sh := range list {
		if parents[i] == (common.Hash{}) {
			// the entry's parent is not held: no carriage recorded (the
			// pool marked nothing for it either)
			continue
		}
		rec = append(rec, e.carriedRecordAt(sh.Address, sh.ExtraNonce, sh.Nonce, blockHeight-uint64(sh.ParentIdx), parents[i])...)
	}
	j.write(rec)
}
