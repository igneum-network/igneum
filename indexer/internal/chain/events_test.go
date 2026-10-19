package chain

import "testing"

// The ERC-20 Transfer topic is the frozen vector that holds the hash to
// the node's definition.
func TestTopicTransfer(t *testing.T) {
	want := "0xddf252ad1be2c89b69c2b068fc378daa952ba7f163c4a11628f55a4df523b3ef"
	if got := Topic("Transfer(address,address,uint256)"); got != want {
		t.Fatalf("Transfer topic %s, want %s", got, want)
	}
	if EventName(want) != "Transfer" || EventName("0x00") != "0x00" {
		t.Fatalf("event names: %s %s", EventName(want), EventName("0x00"))
	}
	if EventName(Topic("Paid(bytes32,address,address,address,uint256,bytes32)")) != "Paid" {
		t.Fatal("Paid not named")
	}
}
