//go:build igneumhash

package main

import (
	"encoding/json"
	"testing"
)

// The member's rule before working (GUARD-C-CLOSURE 3.2 "change the payout list": the member refuses before signing): a
// template whose payout list does not name the member's account is refused; a template without a list is refused too.
func TestListNames(t *testing.T) {
	list := json.RawMessage(`[{"address":"0x00000000000000000000000000000000000c3a01","weight":"0x3"},{"address":"0x00000000000000000000000000000000000C3B02","weight":"0x2"}]`)
	if !listNames(list, "0x00000000000000000000000000000000000c3a01") {
		t.Fatal("the listed member must be named")
	}
	if !listNames(list, "0x00000000000000000000000000000000000c3b02") {
		t.Fatal("the address comparison is case-insensitive")
	}
	if listNames(list, "0x00000000000000000000000000000000000c3c03") {
		t.Fatal("an address not on the list is not named")
	}
	if listNames(nil, "0x00000000000000000000000000000000000c3a01") || listNames(json.RawMessage(`[]`), "0x00000000000000000000000000000000000c3a01") {
		t.Fatal("a template without a list names nobody")
	}
	if listNames(json.RawMessage(`not json`), "0x00000000000000000000000000000000000c3a01") {
		t.Fatal("a malformed list names nobody")
	}
}
