package eth

import (
	"bytes"
	"testing"

	"github.com/ethereum/go-ethereum/consensus/igneum"
	"github.com/ethereum/go-ethereum/consensus/igneum/pow/igneumhash"
	"github.com/ethereum/go-ethereum/consensus/igneum/pow/schedule"
	"github.com/ethereum/go-ethereum/eth/ethconfig"
	"github.com/ethereum/go-ethereum/node"
	"github.com/ethereum/go-ethereum/p2p"
	"github.com/ethereum/go-ethereum/params"
	"github.com/ethereum/go-ethereum/params/types/ctypes"
)

func TestMakeExtraDataDefault(t *testing.T) {
	if !bytes.Contains(makeExtraData(nil), []byte("CoreGeth")) {
		t.Error("missing extra data default client identifier")
	}
}

// TestIgneumScheduleOverrideReachesEngine: a schedule given on the start of a
// persisted datadir (whose stored configuration predates it) reaches the
// consensus engine, not only the chain's configuration. The second
// development chain missed its height 1000 transition for want of this
// (the engine was built from the stored configuration alone).
func TestIgneumScheduleOverrideReachesEngine(t *testing.T) {
	dir := t.TempDir()
	genesis := params.IgneumDevGenesisBlock(0)
	ic := genesis.Config.GetIgneumConfig()
	ic.Algorithm = igneumhash.Name
	ic.AlgorithmParams = []byte(`{"version": 1, "cache_mib": 1}`)
	if err := genesis.Config.SetIgneumConfig(ic); err != nil {
		t.Fatal(err)
	}
	open := func(override *ctypes.IgneumSchedule) (*node.Node, *Ethereum) {
		stack, err := node.New(&node.Config{DataDir: dir, P2P: p2p.Config{NoDiscovery: true, MaxPeers: 0, ListenAddr: ""}})
		if err != nil {
			t.Fatal(err)
		}
		cfg := ethconfig.Defaults
		cfg.Genesis = genesis
		cfg.DevMode = true
		cfg.NetworkId = 1
		cfg.OverrideIgneumSchedule = override
		e, err := New(stack, &cfg)
		if err != nil {
			stack.Close()
			t.Fatal(err)
		}
		return stack, e
	}
	// First start: the genesis is written without a schedule.
	stack, e := open(nil)
	if _, ok := e.Engine().(*igneum.Engine).Algorithm().(*schedule.Algorithm); ok {
		t.Fatal("no schedule was given, yet the engine dispatches over one")
	}
	stack.Close()
	// Second start on the persisted datadir with the schedule: the engine
	// serves the transition's document from its height.
	s := &ctypes.IgneumSchedule{
		Entries:  []ctypes.IgneumScheduleEntry{{Name: "more-rounds", Height: 1000, AlgorithmParams: []byte(`{"version": 1, "rounds": 128, "cache_mib": 1}`)}},
		Terminal: ctypes.IgneumTerminal{Hold: true},
	}
	stack, e = open(s)
	m, ok := e.Engine().(*igneum.Engine).Algorithm().(*schedule.Algorithm)
	if !ok {
		t.Fatalf("the schedule override did not reach the engine: %T", e.Engine().(*igneum.Engine).Algorithm())
	}
	if got := m.At(999).Name; got != "base" {
		t.Fatalf("height 999 is on the base document, got entry %q", got)
	}
	if got := m.At(1000).Name; got != "more-rounds" {
		t.Fatalf("height 1000 is on the transition's document, got entry %q", got)
	}
	if m.ParamsDigestAt(1000) == m.ParamsDigestAt(999) {
		t.Fatal("the transition's document digest equals the base document's")
	}
	stack.Close()
	// Third start: a schedule that fails the ratification rule (a window of
	// 100 with a new row 10 ahead of the head) is refused; the engine runs
	// the held schedule and the stored chain config keeps it too, so a
	// fourth start reads the held one, not the refused one.
	refused := &ctypes.IgneumSchedule{
		Entries:          []ctypes.IgneumScheduleEntry{{Name: "more-rounds", Height: 1000, AlgorithmParams: []byte(`{"version": 1, "rounds": 128, "cache_mib": 1}`)}, {Name: "too-near", Height: 10, AlgorithmParams: []byte(`{"version": 1, "rounds": 32, "cache_mib": 1}`)}},
		Terminal:         ctypes.IgneumTerminal{Hold: true},
		ActivationWindow: 100,
	}
	// The held schedule gains the window first (an admitted change: no row moves).
	held := &ctypes.IgneumSchedule{Entries: s.Entries, Terminal: s.Terminal, ActivationWindow: 100}
	stack, e = open(held)
	stack.Close()
	stack, e = open(refused)
	m, ok = e.Engine().(*igneum.Engine).Algorithm().(*schedule.Algorithm)
	if !ok || len(m.Entries()) != 2 || m.Entries()[1].Name != "more-rounds" {
		t.Fatalf("the refused schedule reached the engine: %+v", m.Entries())
	}
	stack.Close()
	stack, e = open(nil)
	defer stack.Close()
	m, ok = e.Engine().(*igneum.Engine).Algorithm().(*schedule.Algorithm)
	if !ok || len(m.Entries()) != 2 || m.Entries()[1].Name != "more-rounds" {
		t.Fatalf("the stored schedule after a refusal is not the held one: %+v", m.Entries())
	}
}
