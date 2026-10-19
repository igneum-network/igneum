package eth

import (
	"math/big"
	"testing"

	"github.com/ethereum/go-ethereum/consensus/igneum"
	"github.com/ethereum/go-ethereum/consensus/igneum/pow/igneumhash"
	"github.com/ethereum/go-ethereum/eth/ethconfig"
	"github.com/ethereum/go-ethereum/node"
	"github.com/ethereum/go-ethereum/p2p"
	"github.com/ethereum/go-ethereum/params"
	"github.com/ethereum/go-ethereum/params/types/ctypes"
)

// TestIgneumIssuanceOverrideReachesEngine (F-480, 17 October 2026): the
// chain id's issuance default, given on every start as an override, reaches
// a persisted datadir's stored configuration and the engine; a datadir
// whose stored configuration predates the default's next form (the
// 12-second gate) runs the new form from the restart, and a fresh datadir
// the same. The public test network and the research chain missed their
// first gates for want of this: their nodes' stored configurations were
// written before the landing check existed and nothing refreshed them.
func TestIgneumIssuanceOverrideReachesEngine(t *testing.T) {
	dir := t.TempDir()
	genesis := params.IgneumDevGenesisBlock(0)
	ic := genesis.Config.GetIgneumConfig()
	ic.Algorithm = igneumhash.Name
	ic.AlgorithmParams = []byte(`{"version": 1, "cache_mib": 1}`)
	old := &ctypes.IgneumIssuance{Epoch: 100, M: new(big.Int).Mul(big.NewInt(1000), big.NewInt(1e18)), K: 1, Tail: big.NewInt(0)}
	ic.Issuance = old
	if err := genesis.Config.SetIgneumConfig(ic); err != nil {
		t.Fatal(err)
	}
	open := func(override *ctypes.IgneumIssuance) (*node.Node, *Ethereum) {
		stack, err := node.New(&node.Config{DataDir: dir, P2P: p2p.Config{NoDiscovery: true, MaxPeers: 0, ListenAddr: ""}})
		if err != nil {
			t.Fatal(err)
		}
		cfg := ethconfig.Defaults
		cfg.Genesis = genesis
		cfg.DevMode = true
		cfg.NetworkId = 1
		cfg.OverrideIgneumIssuance = override
		e, err := New(stack, &cfg)
		if err != nil {
			stack.Close()
			t.Fatal(err)
		}
		return stack, e
	}
	issuanceOf := func(e *Ethereum) ctypes.IgneumIssuance {
		return *e.BlockChain().Config().GetIgneumConfig().Issuance
	}
	// First start: the stored configuration carries the old form (no gate).
	stack, e := open(nil)
	if got := issuanceOf(e); got.NextFrom != 0 || got.Next != nil {
		t.Fatalf("the first start stored a next form: %+v", got)
	}
	if p := e.Engine().(*igneum.Engine).Parts().Issuance.Params(); p.NextFrom != 0 {
		t.Fatalf("the engine's first issuance carries a gate: %+v", p)
	}
	stack.Close()
	// Second start on the persisted datadir with the default carrying the
	// gate: the stored configuration and the engine take it.
	next := &ctypes.IgneumIssuance{Epoch: 50, M: old.M, K: 1, Tail: big.NewInt(0)}
	refreshed := &ctypes.IgneumIssuance{Epoch: 100, M: old.M, K: 1, Tail: big.NewInt(0), NextFrom: 200, Next: next}
	stack, e = open(refreshed)
	if got := issuanceOf(e); got.NextFrom != 200 || got.Next == nil || got.Next.Epoch != 50 {
		t.Fatalf("the stored configuration did not take the refreshed issuance: %+v", got)
	}
	p := e.Engine().(*igneum.Engine).Parts().Issuance.Params()
	if p.NextFrom != 200 || p.Next == nil || p.Next.Epoch != 50 {
		t.Fatalf("the engine did not take the refreshed issuance: %+v", p)
	}
	stack.Close()
	// Third start without the override: the stored configuration keeps it.
	stack, e = open(nil)
	defer stack.Close()
	if got := issuanceOf(e); got.NextFrom != 200 {
		t.Fatalf("the stored configuration lost the refreshed issuance: %+v", got)
	}
}
