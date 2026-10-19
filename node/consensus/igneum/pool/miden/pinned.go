// Copyright 2026 The Igneum Authors
// This file is part of the go-ethereum library (Igneum fork).

package miden

import "github.com/ethereum/go-ethereum/consensus/igneum/pool"

// The pinned program hashes of the two statements (the Miden assembly in
// tools/money/stage1-prover/prover/masm, assembled by Miden VM 0.36.1;
// the values of tools/money/vectors/money-stage1-vectors.json). New
// checks the linked library's programs against them, so a library built
// from other assembly is refused at start, never trusted.
var (
	pinnedShield = [4]uint64{0x2b0fbb2a6e680117, 0x80432333586e5a67, 0xae7df2a89c730e58, 0x92b428f462b47ae5}
	pinnedSpend  = [4]uint64{0xf2fd077c4662c585, 0x3802bed244dd8d49, 0x38508212eb7ceeb5, 0xcb369241d28932ef}
)

// The pinned program hashes of the vault's seven statements (stage two, cut
// one; tools/money/stage1-prover/prover/masm/vault-*.masm, each the prelude
// and its body; the values of tools/money/vectors/money-vault-vectors.json),
// by tag from VaultCreateTag.
var pinnedVault = [7][4]uint64{
	{0x33f55dfa8c2da823, 0xc4d415030cd5c69f, 0x368cfbc9c58306f6, 0x0002ca070ee0632c}, // money-vault-create-v1
	{0x366b94f954e3623a, 0xef0564c7285605c0, 0x981ff8b28514d3cc, 0xd3503f3ba1b03f6e}, // money-vault-withdraw-v1 (with the period-index cure of 19 October; the lab chain 7404 verified 0xa311dd7aba17c5d1... without it)
	{0x785f979fe4bc3d7b, 0x73641066f294fbd9, 0xf12547c7622a5bd9, 0x204706d6fec77904}, // money-vault-cancel-v1
	{0xb895507916e6196d, 0xb391b1e5b75dae15, 0x4c8b6d889ebffaf7, 0xa343493b8ed47436}, // money-vault-complete-v1
	{0x807a179fa18e0bc9, 0x495870249b32c01c, 0x5859190ca8699ca3, 0x143184eaf86bb07c}, // money-vault-announce-v1
	{0x6f18b069347b7ca6, 0x1db54d4bcd0909be, 0x9e6b546c8dc296c8, 0xa9c3b3daa277bf23}, // money-vault-policy-complete-v1
	{0xe815e83f30a33e41, 0x3349fe357ef12539, 0x6ccf6d1344b4d1ef, 0x153e56885d1fd965}, // money-vault-rec-cancel-v1
}

// PinnedShield and PinnedSpend are the pinned hashes (for the page and
// the tests).
func PinnedShield() [4]uint64 { return pinnedShield }
func PinnedSpend() [4]uint64  { return pinnedSpend }

// PinnedVault is the pinned hash of the vault statement of a tag.
func PinnedVault(tag byte) [4]uint64 { return pinnedVault[tag-pool.VaultCreateTag] }
