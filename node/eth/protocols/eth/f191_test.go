package eth

import (
	"bytes"
	"encoding/hex"
	"math/big"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/p2p"
	"github.com/ethereum/go-ethereum/rlp"
)

// pushOutcome sends a block through the NewBlock message to a fresh peer
// whose backend names the given carried-list form (nil: no form) and says
// what the receiving side did with it: "delivered" to the backend, "refused"
// with the sender dropped, or "dropped" with the peer kept (the screen's
// miss under the chain's form).
func pushOutcome(t *testing.T, block *types.Block, form *types.ShareListForm) (outcome string, err error) {
	t.Helper()
	backend := &recordingBackend{testBackend: newTestBackend(1), acceptTxs: true, handled: make(chan Packet, 1), form: form}
	defer backend.close()
	peer, errc := newTestPeer("peer", ETH68, backend)
	defer peer.close()

	if err := p2p.Send(peer.app, NewBlockMsg, &NewBlockPacket{Block: block, TD: big.NewInt(1)}); err != nil {
		t.Fatalf("send: %v", err)
	}
	select {
	case <-backend.handled:
		return "delivered", nil
	case err := <-errc:
		return "refused", err
	case <-time.After(400 * time.Millisecond):
	}
	// Nothing delivered and the handler loop still up: the peer must answer
	// a request, or the block was lost with the peer.
	probePeer(t, peer, backend.chain, 7)
	return "dropped", nil
}

// blockWithShares is a block whose body carries the given shares and nothing
// else, its header committing to them.
func blockWithShares(entries []byte) *types.Block {
	root := types.MinedSharesRoot(entries)
	header := &types.Header{
		Number:      big.NewInt(1),
		Difficulty:  big.NewInt(1),
		GasLimit:    1,
		Time:        1,
		TxHash:      types.EmptyTxsHash,
		UncleHash:   types.EmptyUncleHash,
		ReceiptHash: types.EmptyReceiptsHash,
		SharesRoot:  &root,
	}
	return types.NewBlockWithHeader(header).WithMinedShares(entries)
}

var (
	noForm    *types.ShareListForm
	countForm = &types.ShareListForm{}
	stepForm  = &types.ShareListForm{Step: true}
	typedForm = &types.ShareListForm{Typed: true, Step: true}
)

// TestNewBlockTypedSharesFromChain (F-191, 16 October 2026) pushes the
// research chain's block 6867 as its node served it (debug_getRawBlock on
// a builder's node0: eleven hash shares under the typed list form, 781 bytes,
// three payout entries) through the NewBlock message: the receiver takes it
// whole under the chain's typed form, and under no named form; under a
// chain whose form at the height is the count form the block is dropped and
// the peer kept. Before the fix the handler's shares bound knew the count
// and step forms only (69 or 70 bytes an entry) and refused the block as
// "not whole entries", dropping its sender as a protocol error: every whole
// block the chain pushed cost the sender the peer, and no pushed block had
// imported since the typed form; the fetch path, without the check, carried
// the chain.
func TestNewBlockTypedSharesFromChain(t *testing.T) {
	raw, err := os.ReadFile("testdata/vectorwork8-6867.hex")
	if err != nil {
		t.Fatal(err)
	}
	data, err := hex.DecodeString(strings.TrimPrefix(strings.TrimSpace(string(raw)), "0x"))
	if err != nil {
		t.Fatal(err)
	}
	var block types.Block
	if err := rlp.DecodeBytes(data, &block); err != nil {
		t.Fatalf("block decode: %v", err)
	}
	if n := len(block.MinedShares()); n == 0 || n%types.MinedShareBytes == 0 || n%types.MinedShareStepBytes == 0 {
		t.Fatalf("the fixture's shares are %d bytes: not the typed form this test is for", n)
	}
	if n := types.MinedShareCountTyped(block.MinedShares()); n != 11 {
		t.Fatalf("the fixture carries %d typed shares, want 11", n)
	}
	for _, tc := range []struct {
		name string
		form *types.ShareListForm
		want string
	}{
		{"the chain's typed form", typedForm, "delivered"},
		{"no form named", noForm, "delivered"},
		{"a chain under the count form", countForm, "dropped"},
	} {
		outcome, err := pushOutcome(t, &block, tc.form)
		if outcome != tc.want {
			t.Fatalf("%s: %s (%v), want %s", tc.name, outcome, err, tc.want)
		}
	}
	// The delivered block is the chain's, whole.
	backend := &recordingBackend{testBackend: newTestBackend(1), acceptTxs: true, handled: make(chan Packet, 1), form: typedForm}
	defer backend.close()
	peer, _ := newTestPeer("peer", ETH68, backend)
	defer peer.close()
	if err := p2p.Send(peer.app, NewBlockMsg, &NewBlockPacket{Block: &block, TD: big.NewInt(1)}); err != nil {
		t.Fatalf("send: %v", err)
	}
	select {
	case packet := <-backend.handled:
		got, ok := packet.(*NewBlockPacket)
		if !ok {
			t.Fatalf("delivered %T, want *NewBlockPacket", packet)
		}
		if got.Block.Hash() != block.Hash() {
			t.Fatalf("delivered block %x, want %x", got.Block.Hash(), block.Hash())
		}
		if !bytes.Equal(got.Block.MinedShares(), block.MinedShares()) {
			t.Fatalf("delivered shares differ from the chain's")
		}
		if len(got.Block.Withdrawals()) != len(block.Withdrawals()) {
			t.Fatalf("delivered %d payout entries, want %d", len(got.Block.Withdrawals()), len(block.Withdrawals()))
		}
	case <-time.After(3 * time.Second):
		t.Fatalf("the chain's block was not delivered")
	}
}

// TestNewBlockSharesForms pushes carried lists under every form at the
// consensus ceiling against chains naming each form and none: a list whole
// under the chain's form is taken; one that is not is dropped with the peer
// kept; only a list over the byte bound costs the sender the peer.
func TestNewBlockSharesForms(t *testing.T) {
	share := func(i int) types.MinedShare {
		return types.MinedShare{Address: common.Address{byte(i), byte(i >> 8)}, Nonce: uint64(i), Step: 3}
	}
	var countList, stepList, typedHash, typedGraph []byte
	for i := 0; i < 256; i++ {
		s := share(i)
		countList = append(countList, s.Bytes()...)
		stepList = append(stepList, s.BytesOf(true)...)
		typedHash = append(typedHash, s.BytesTyped()...)
		s.Ticket = bytes.Repeat([]byte{byte(i)}, types.GraphTicketBytes)
		typedGraph = append(typedGraph, s.BytesTyped()...)
	}
	if len(typedGraph) != maxBlockMinedShareBytes {
		t.Fatalf("the bound is %d bytes, 256 typed graph entries are %d", maxBlockMinedShareBytes, len(typedGraph))
	}
	typedMixed := append(append([]byte(nil), typedHash[:71*3]...), typedGraph[:239*2]...)
	cutTyped := typedHash[:len(typedHash)-7]
	unknownKind := append([]byte{7}, typedHash[1:]...)
	overBound := append(append([]byte(nil), typedGraph...), typedHash[:71]...)

	type cell struct {
		name    string
		entries []byte
		want    string
	}
	suites := []struct {
		chain string
		form  *types.ShareListForm
		cells []cell
	}{
		{"no form named", noForm, []cell{
			{"count form, 256", countList, "delivered"},
			{"step form, 256", stepList, "delivered"},
			{"typed hash, 256", typedHash, "delivered"},
			{"typed graph, 256", typedGraph, "delivered"},
			{"typed mixed", typedMixed, "delivered"},
			{"a typed list cut short", cutTyped, "dropped"},
			{"an unknown kind", unknownKind, "dropped"},
			{"over the bound", overBound, "refused"},
		}},
		{"the typed form", typedForm, []cell{
			{"typed hash, 256", typedHash, "delivered"},
			{"typed graph, 256", typedGraph, "delivered"},
			{"typed mixed", typedMixed, "delivered"},
			{"count form, 256", countList, "dropped"},
			{"step form, 256", stepList, "dropped"},
			{"a typed list cut short", cutTyped, "dropped"},
			{"an unknown kind", unknownKind, "dropped"},
			{"over the bound", overBound, "refused"},
		}},
		{"the step form", stepForm, []cell{
			{"step form, 256", stepList, "delivered"},
			{"count form, 256", countList, "dropped"},
			{"typed hash, 256", typedHash, "dropped"},
			{"over the bound", overBound, "refused"},
		}},
		{"the count form", countForm, []cell{
			{"count form, 256", countList, "delivered"},
			{"step form, 256", stepList, "dropped"},
			{"typed graph, 256", typedGraph, "dropped"},
		}},
	}
	for _, suite := range suites {
		for _, c := range suite.cells {
			outcome, err := pushOutcome(t, blockWithShares(c.entries), suite.form)
			if outcome != c.want {
				t.Errorf("%s, %s (%d bytes): %s (%v), want %s", suite.chain, c.name, len(c.entries), outcome, err, c.want)
			}
		}
	}
}
