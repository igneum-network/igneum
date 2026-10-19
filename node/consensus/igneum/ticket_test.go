package igneum

import (
	"encoding/binary"
	"encoding/json"
	"errors"
	"math/big"
	"strings"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/consensus/igneum/pow"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/rlp"
)

// ticketStub is a witness relation for the public build (the Graph
// coordinate's shape without the reference): a four-byte ticket, LE32 of
// the nonce, found on every third nonce; its hash the keccak of the
// triple and the ticket.
type ticketStub struct{}

func (ticketStub) Name() string                       { return "test-ticket" }
func (ticketStub) Epoch(height uint64) uint64         { return 0 }
func (ticketStub) SeedHash(height uint64) common.Hash { return common.Hash{} }
func (ticketStub) Vectors() []pow.Vector              { return nil }
func (ticketStub) Compute(headerHash common.Hash, height uint64, nonce uint64) (common.Hash, common.Hash) {
	return common.Hash{}, common.Hash{}
}
func (ticketStub) Verify(headerHash common.Hash, height uint64, nonce uint64, mixDigest common.Hash, difficulty *big.Int, fullDataset bool) error {
	return errors.New("test-ticket: the hash form")
}
func (ticketStub) TicketSize() int { return 4 }
func (ticketStub) VerifyTicket(headerHash common.Hash, height uint64, nonce uint64, ticket []byte) (common.Hash, error) {
	if len(ticket) != 4 || binary.LittleEndian.Uint32(ticket) != uint32(nonce) {
		return common.Hash{}, errors.New("test-ticket: false ticket")
	}
	var b [16]byte
	binary.LittleEndian.PutUint64(b[:], height)
	binary.LittleEndian.PutUint64(b[8:], nonce)
	return crypto.Keccak256Hash(headerHash[:], b[:], ticket), nil
}
func (ticketStub) SolveTicket(headerHash common.Hash, height uint64, nonce uint64) ([][]byte, error) {
	if nonce%3 != 0 {
		return nil, nil
	}
	t := make([]byte, 4)
	binary.LittleEndian.PutUint32(t, uint32(nonce))
	return [][]byte{t}, nil
}

func ticketEngine(t *testing.T) *Engine {
	t.Helper()
	e, err := New(Config{Algorithm: ticketStub{}, Parts: testParts()})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { e.Close() })
	return e
}

// TestTicketedSeal: the ticket search, the seal's verification through the
// ticket, the refusals (a false ticket, no ticket, a wrong mix, a ticket on
// a hash relation's block), the ticket outside the seal hash, the codecs.
func TestTicketedSeal(t *testing.T) {
	e := ticketEngine(t)
	g := genesisHeader()
	h := childOf(g)
	h.Difficulty = big.NewInt(1)
	height := h.Number.Uint64()
	sealHash := e.SealHash(h)
	nonce, mix, ticket, ok := pow.SolveTicketTick(ticketStub{}, sealHash, height, h.Difficulty, 1, nil, nil)
	if !ok || nonce != 3 || len(ticket) != 4 {
		t.Fatalf("the ticket search: ok %v nonce %d ticket %x", ok, nonce, ticket)
	}
	h.Nonce = types.EncodeNonce(nonce)
	h.MixDigest = mix
	h.Ticket = ticket
	if e.SealHash(h) != sealHash {
		t.Fatal("the ticket is inside the seal hash")
	}
	if err := e.verifySeal(nil, h, height, false); err != nil {
		t.Fatalf("a right ticket refused: %v", err)
	}
	// The refusals.
	bad := types.CopyHeader(h)
	bad.Ticket[0] ^= 1
	if err := e.verifySeal(nil, bad, height, false); err == nil || !errors.Is(err, errInvalidPoW) {
		t.Fatalf("a false ticket: %v", err)
	}
	if bad.Ticket[0] == h.Ticket[0] {
		t.Fatal("CopyHeader shares the ticket")
	}
	none := types.CopyHeader(h)
	none.Ticket = nil
	if err := e.verifySeal(nil, none, height, false); err == nil {
		t.Fatal("no ticket accepted")
	}
	wrongMix := types.CopyHeader(h)
	wrongMix.MixDigest[0] ^= 1
	if err := e.verifySeal(nil, wrongMix, height, false); err == nil || !errors.Is(err, errInvalidPoW) {
		t.Fatalf("a wrong mix: %v", err)
	}
	hashEngine := newTestEngine(t)
	if err := hashEngine.verifySeal(nil, h, height, false); err == nil || !strings.Contains(err.Error(), "ticket on a hash") {
		t.Fatalf("a ticket on a hash relation's block: %v", err)
	}
	// The codecs carry the ticket.
	enc, err := rlp.EncodeToBytes(h)
	if err != nil {
		t.Fatal(err)
	}
	var dec types.Header
	if err := rlp.DecodeBytes(enc, &dec); err != nil {
		t.Fatal(err)
	}
	if string(dec.Ticket) != string(ticket) || dec.Hash() != h.Hash() {
		t.Fatalf("RLP: ticket %x hash %v, want %x %v", dec.Ticket, dec.Hash(), ticket, h.Hash())
	}
	js, err := json.Marshal(h)
	if err != nil {
		t.Fatal(err)
	}
	var fromJSON types.Header
	if err := json.Unmarshal(js, &fromJSON); err != nil {
		t.Fatal(err)
	}
	if string(fromJSON.Ticket) != string(ticket) || !strings.Contains(string(js), `"ticket":"0x`) {
		t.Fatalf("JSON: %s", js)
	}
	// A hash relation's header without a ticket keeps its wire form: no
	// ticket field, and the tag alone as the last field.
	plain := childOf(g)
	plain.SetCoordTag(0)
	encPlain, _ := rlp.EncodeToBytes(plain)
	var decPlain types.Header
	if err := rlp.DecodeBytes(encPlain, &decPlain); err != nil {
		t.Fatal(err)
	}
	if c, ok := decPlain.CoordTag(); !ok || c != 0 || len(decPlain.Ticket) != 0 {
		t.Fatalf("the tag alone: %v %d ticket %x", ok, c, decPlain.Ticket)
	}
}
