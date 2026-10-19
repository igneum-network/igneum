package chain

import (
	"encoding/hex"
	"strings"
)

// The event signatures the dashboard names: the product team's IgneumPay
// (contracts/src/interfaces/IIgneumPay.sol) and the ownership standards
// the ownership contracts emit (ERC-20, ERC-721, ERC-1155, ERC-5192).
// Anything else is reported under its signature hash.
var eventSignatures = []string{
	"InvoiceCreated(bytes32,address,address,uint256,uint64,bytes32,address)",
	"InvoiceCancelled(bytes32)",
	"Paid(bytes32,address,address,address,uint256,bytes32)",
	"Credited(address,uint256)",
	"Claimed(address,uint256)",
	"Transfer(address,address,uint256)",
	"Approval(address,address,uint256)",
	"TransferSingle(address,address,address,uint256,uint256)",
	"TransferBatch(address,address,address,uint256[],uint256[])",
	"Locked(uint256)",
	"Unlocked(uint256)",
}

var eventNames = func() map[string]string {
	m := map[string]string{}
	for _, sig := range eventSignatures {
		m[Topic(sig)] = sig[:strings.IndexByte(sig, '(')]
	}
	return m
}()

// Topic is the event signature hash as the node reports it: 0x and 64
// lower-case hex digits of Keccak-256 over the canonical signature.
func Topic(signature string) string {
	h := Keccak256([]byte(signature))
	return "0x" + hex.EncodeToString(h[:])
}

// EventName returns the event's name for a known signature hash, or the
// hash itself.
func EventName(topic0 string) string {
	if n, ok := eventNames[strings.ToLower(topic0)]; ok {
		return n
	}
	return topic0
}
