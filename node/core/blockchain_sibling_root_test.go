package core

import (
	"math/big"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/consensus/igneum"
	"github.com/ethereum/go-ethereum/core/rawdb"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/core/vm"
	"github.com/ethereum/go-ethereum/params"
	"github.com/ethereum/go-ethereum/params/types/genesisT"
	"github.com/ethereum/go-ethereum/params/vars"
)

// the security review's F-84 (13 October 2026): on the Igneum chain two empty
// siblings of one parent under one sealer produce the same post-state, so a
// sidechain of empty blocks from a pruned ancestor shares every state root
// with the canonical chain at the same heights. Upstream's insertSideChain
// read that as a "sidechain ghost-state attack", refused the import and
// dropped the peer for good, so a node restarted on the losing side of a
// sibling race (the old public test network (chain 7320) carried an eight-height fork in its first 260
// heights) could never rejoin without a datadir wipe. The fix writes the
// shared-root sidechain block without state like any other, and executes
// a known block this node never executed (no receipts of its own) rather
// than trusting the root another block wrote. The cases:
//
//  1. a sidechain of empty blocks from a pruned ancestor, every root equal
//     to canonical's at its height, longer than canonical: imported, the
//     chain reorganised onto it, every adopted block executed (receipts);
//  2. the same sidechain one block SHORTER than canonical: written without
//     state, no reorg, the canonical head unchanged, no refusal;
//  3. the losing-side restart: the node's own canonical blocks re-imported
//     after the reorg (the fork's siblings at the same heights with the same
//     roots) are accepted as a sidechain and not refused;
//  4. a sidechain block that LIES about its root (names the canonical root
//     with a different coinbase, so execution yields another root) is
//     refused at adoption by the state validation, never trusted on the
//     shared root;
//  5. the depth-8 case the old public test network (chain 7320) showed: the fork eight heights deep from
//     the common ancestor reorganises cleanly.
func TestSidechainSharedRootIsNotAGhost(t *testing.T) {
	// The hash scheme is the nodes of record's (the default); under the
	// path scheme the adoption's walk asks the state history whether a
	// pruned root is recoverable, which a memory database without an
	// ancient store cannot answer (pathdb's reader panics on the absent
	// freezer), so that scheme's row is the box chain's, not this test's.
	testSidechainSharedRoot(t, rawdb.HashScheme)
}

func testSidechainSharedRoot(t *testing.T, scheme string) {
	engine := igneum.NewFaker(igneum.TestParts())
	genesis := &genesisT.Genesis{
		Config:  params.IgneumTestChainConfig,
		BaseFee: big.NewInt(vars.InitialBaseFee),
	}
	genDb, blocks, _ := GenerateChainWithGenesis(genesis, engine, 2*TriesInMemory, nil)
	chain, err := NewBlockChain(rawdb.NewMemoryDatabase(), DefaultCacheConfigWithScheme(scheme), genesis, nil, engine, vm.Config{}, nil, nil)
	if err != nil {
		t.Fatalf("failed to create tester chain: %v", err)
	}
	defer chain.Stop()
	if n, err := chain.InsertChain(blocks); err != nil {
		t.Fatalf("block %d: failed to insert into chain: %v", n, err)
	}
	// The fork point: a pruned ancestor (its state gone from memory).
	forkAt := 5
	if chain.HasBlockAndState(blocks[forkAt].Hash(), blocks[forkAt].NumberU64()) {
		t.Fatalf("block %d not pruned; the test needs a pruned fork point", blocks[forkAt].NumberU64())
	}
	// Case 2: a shorter empty sidechain sharing every root: written, no
	// reorg, no refusal.
	short, _ := GenerateChain(genesis.Config, blocks[forkAt], engine, genDb, len(blocks)-forkAt-2, func(i int, gen *BlockGen) {
		gen.SetExtra([]byte("side")) // a different hash, the same state
	})
	for i, b := range short {
		if canon := blocks[forkAt+1+i]; b.Root() != canon.Root() {
			t.Fatalf("side block %d root %x differs from canonical %x (the test needs shared roots)", b.NumberU64(), b.Root(), canon.Root())
		} else if b.Hash() == canon.Hash() {
			t.Fatalf("side block %d is the canonical block", b.NumberU64())
		}
	}
	head := chain.CurrentBlock().Hash()
	n, err := chain.InsertChain(short)
	if err != nil {
		t.Fatalf("case 2: the shorter shared-root sidechain refused at %d: %v", n, err)
	}
	if chain.CurrentBlock().Hash() != head {
		t.Fatal("case 2: a shorter sidechain reorganised the chain")
	}
	first, last := short[0], short[len(short)-1]
	if !chain.HasBlock(last.Hash(), last.NumberU64()) {
		t.Fatalf("case 2: the sidechain block was not written (n %d; first header %v body %v; last header %v)", n,
			chain.HasHeader(first.Hash(), first.NumberU64()), chain.HasBlock(first.Hash(), first.NumberU64()), chain.HasHeader(last.Hash(), last.NumberU64()))
	}
	// Cases 1 and 5: a longer empty sidechain from the same pruned ancestor
	// (eight heights deeper than the fork the old public test network (chain 7320) showed is well inside):
	// imported, adopted, every adopted block executed.
	long, _ := GenerateChain(genesis.Config, blocks[forkAt], engine, genDb, len(blocks)-forkAt+2, func(i int, gen *BlockGen) {
		gen.SetExtra([]byte("longer side"))
	})
	if n, err := chain.InsertChain(long); err != nil {
		t.Fatalf("case 1: the longer shared-root sidechain refused at %d: %v", n, err)
	}
	if chain.CurrentBlock().Hash() != long[len(long)-1].Hash() {
		t.Fatalf("case 1: the chain did not reorganise onto the longer sidechain (head %x)", chain.CurrentBlock().Hash())
	}
	for _, b := range long {
		if !rawdb.HasReceipts(chain.db, b.Hash(), b.NumberU64()) {
			t.Fatalf("case 1: adopted block %d was not executed (no receipts)", b.NumberU64())
		}
	}
	// Case 3: the losing side's blocks (the old canonical chain above the
	// fork) re-imported after the reorg: a sidechain sharing roots, not a
	// ghost; accepted, no reorg (it is shorter now).
	if n, err := chain.InsertChain(blocks[forkAt+1:]); err != nil {
		t.Fatalf("case 3: the losing side re-imported refused at %d: %v", n, err)
	}
	if chain.CurrentBlock().Hash() != long[len(long)-1].Hash() {
		t.Fatal("case 3: the shorter losing side reorganised the chain")
	}
	// Case 4: the ghost itself. A sidechain from the same pruned ancestor
	// under ANOTHER coinbase (so its true post-states differ from the
	// canonical ones) whose headers copy the canonical roots at their
	// heights, one block longer than the head: every root exists in the
	// database, the headers verify under the faker's seal, and upstream's
	// worry was that adoption would trust those roots without executing.
	// The blocks are written without state (a sidechain), the adoption
	// executes them because this node never did, the first block's true
	// root differs from its header's, and the state validation refuses
	// the chain; the head stays.
	liars, _ := GenerateChain(genesis.Config, blocks[forkAt], engine, genDb, len(long)+1, func(i int, gen *BlockGen) {
		gen.SetCoinbase(common.HexToAddress("0x00000000000000000000000000000000000000ee"))
	})
	forged := make([]*types.Block, 0, len(liars))
	for i, b := range liars {
		h := b.Header()
		if i < len(long) {
			h.Root = long[i].Root() // the lie: the canonical root at this height
		}
		if i > 0 {
			h.ParentHash = forged[i-1].Hash()
		}
		forged = append(forged, types.NewBlockWithHeader(h).WithBody(b.Transactions(), b.Uncles()))
	}
	if _, err := chain.InsertChain(forged); err == nil {
		t.Fatal("case 4: a sidechain lying about its roots was adopted")
	}
	if chain.CurrentBlock().Hash() != long[len(long)-1].Hash() {
		t.Fatalf("case 4: the head moved off the honest chain (%x)", chain.CurrentBlock().Hash())
	}
}
