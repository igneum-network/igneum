package types

import "testing"

// TestMinedSharesTyped: the typed list (section 10 (b)) round-trips hash
// and graph entries and refuses a bad kind or a short ticket.
func TestMinedSharesTyped(t *testing.T) {
	a := MinedShare{Nonce: 1, Step: 2}
	g := MinedShare{Nonce: 2, Step: 1, Ticket: make([]byte, GraphTicketBytes)}
	g.Ticket[0], g.Ticket[167] = 7, 9
	entries := append(a.BytesTyped(), g.BytesTyped()...)
	if MinedShareCountTyped(entries) != 2 {
		t.Fatalf("count %d", MinedShareCountTyped(entries))
	}
	list, ok := ParseMinedSharesTyped(entries)
	if !ok || len(list) != 2 || list[0].Ticket != nil || len(list[1].Ticket) != GraphTicketBytes || list[1].Ticket[167] != 9 || list[0].Step != 2 {
		t.Fatalf("round trip: %v %+v", ok, list)
	}
	if _, ok := ParseMinedSharesTyped(append([]byte{5}, a.BytesOf(true)...)); ok {
		t.Fatal("a bad kind parsed")
	}
	if _, ok := ParseMinedSharesTyped(g.BytesTyped()[:100]); ok {
		t.Fatal("a short ticket parsed")
	}
	if _, ok := ParseMinedSharesForm(entries, ShareListForm{Typed: true}); !ok {
		t.Fatal("the form parse")
	}
}
