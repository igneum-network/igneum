package core

import (
	"sync"
	"testing"

	"github.com/ethereum/go-ethereum/consensus"
	"github.com/ethereum/go-ethereum/consensus/igneum"
	"github.com/ethereum/go-ethereum/core/rawdb"
	"github.com/ethereum/go-ethereum/core/types"
)

// landingEngine wraps an engine and names one block number as a registry
// landing block, recording the header batches it is asked to verify and
// the landings it is asked to apply, in order (F-197).
type landingEngine struct {
	consensus.Engine
	landing uint64

	mu     sync.Mutex
	events []string // "verify a-b" and "land n", in order
}

func (l *landingEngine) VerifyHeaders(chain consensus.ChainHeaderReader, headers []*types.Header, seals []bool) (chan<- struct{}, <-chan error) {
	if len(headers) > 0 {
		l.mu.Lock()
		l.events = append(l.events, "verify "+headers[0].Number.String()+"-"+headers[len(headers)-1].Number.String())
		l.mu.Unlock()
	}
	return l.Engine.VerifyHeaders(chain, headers, seals)
}

func (l *landingEngine) RegistryLandingBlock(block *types.Block) bool {
	return block.NumberU64() == l.landing
}

func (l *landingEngine) LandRegistry(chain consensus.ChainHeaderReader, block *types.Block) {
	l.mu.Lock()
	l.events = append(l.events, "land "+block.Number().String())
	l.mu.Unlock()
}

// TestInsertChainSplitsAtRegistryLanding (F-197, 16 October 2026): a batch
// import is cut after a registry landing block, the landing applied once
// the block is canonical, and the blocks above it verified only after:
// the batch 1 to 10 with the landing at 4 verifies 1 to 4, lands 4, then
// verifies 5 to 10. A batch without a landing verifies whole; a landing at
// the batch's last block lands after it with nothing left to split.
func TestInsertChainSplitsAtRegistryLanding(t *testing.T) {
	for _, tc := range []struct {
		name    string
		landing uint64
		want    []string
	}{
		{"landing inside the batch", 4, []string{"verify 1-4", "land 4", "verify 5-10"}},
		{"no landing", 0, []string{"verify 1-10"}},
		{"landing at the batch's end", 10, []string{"verify 1-10", "land 10"}},
		{"landing at the batch's first block", 1, []string{"verify 1-1", "land 1", "verify 2-10"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			engine := &landingEngine{Engine: igneum.NewFaker(igneum.TestParts()), landing: tc.landing}
			genDb, _, chain, err := newCanonical(engine, 0, true, rawdb.HashScheme)
			if err != nil {
				t.Fatal(err)
			}
			defer chain.Stop()
			// The blocks are made with the inner engine: the wrapper only
			// watches the import.
			blocks := makeBlockChain(chain.chainConfig, chain.GetBlockByHash(chain.CurrentBlock().Hash()), 10, engine.Engine, genDb, 0)
			n, err := chain.InsertChain(blocks)
			if err != nil {
				t.Fatalf("insert: %v", err)
			}
			if n != len(blocks) {
				t.Fatalf("inserted %d of %d", n, len(blocks))
			}
			if head := chain.CurrentBlock().Number.Uint64(); head != 10 {
				t.Fatalf("head %d, want 10", head)
			}
			engine.mu.Lock()
			got := append([]string(nil), engine.events...)
			engine.mu.Unlock()
			if len(got) != len(tc.want) {
				t.Fatalf("events %v, want %v", got, tc.want)
			}
			for i := range got {
				if got[i] != tc.want[i] {
					t.Fatalf("events %v, want %v", got, tc.want)
				}
			}
		})
	}
}
