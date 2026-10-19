// Copyright 2026 The Igneum Authors
// This file is part of the go-ethereum library (Igneum fork).
//
// The go-ethereum library is free software: you can redistribute it and/or modify
// it under the terms of the GNU Lesser General Public License as published by
// the Free Software Foundation, either version 3 of the License, or
// (at your option) any later version.
//
// The go-ethereum library is distributed in the hope that it will be useful,
// but WITHOUT ANY WARRANTY; without even the implied warranty of
// MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE. See the
// GNU Lesser General Public License for more details.
//
// You should have received a copy of the GNU Lesser General Public License
// along with the go-ethereum library. If not, see <http://www.gnu.org/licenses/>.

package core

import (
	"errors"
	"fmt"

	"github.com/ethereum/go-ethereum/consensus"
	"github.com/ethereum/go-ethereum/core/types"
)

// ShareFormAt is the chain's carried-list form at a height as the engine
// names it: PA-53's step form (one byte more an entry) where the engine's
// ShareStepMax is set, the typed form where the engine's ShareFormTypedAt
// says so (the registration's graphSharesFrom, WHOLEWORK.md section 10 (b)),
// else the count form. known is false for an engine that names no form at
// all. The import's check (validateSharesBody) and the NewBlock screen
// (eth/protocols/eth) read one form through it (F-191's residual, 16
// October 2026: a screen that was a union of the forms it knew dropped the
// sender of every list under a form it did not).
func ShareFormAt(engine consensus.Engine, height uint64) (form types.ShareListForm, known bool) {
	if sm, ok := engine.(interface{ ShareStepMax() uint64 }); ok {
		form.Step, known = sm.ShareStepMax() > 0, true
	}
	if ty, ok := engine.(interface{ ShareFormTypedAt(uint64) bool }); ok {
		known = true
		if ty.ShareFormTypedAt(height) {
			form.Typed, form.Step = true, true
		}
	}
	return form, known
}

// validateSharesBody is the body check of the share lane's carried shares
// (LAUNCH-GUARDS.md 2.9): the body carries exactly the entries the header
// commits to, none without one, counted in the chain's form. The count
// form alone refused every block that carried a share on the ninth
// development chain (its own sealers' first such block, 111, at 02:05 UK
// on 12 October 2026; the chain stood at 110 until the fix).
func validateSharesBody(engine consensus.Engine, header *types.Header, entries []byte) error {
	if header.SharesRoot != nil {
		var height uint64
		if header.Number != nil {
			height = header.Number.Uint64()
		}
		form, _ := ShareFormAt(engine, height)
		if n := types.MinedShareCountForm(entries, form); n <= 0 {
			return errors.New("missing or malformed shares in block body")
		}
		if hash := types.MinedSharesRoot(entries); hash != *header.SharesRoot {
			return fmt.Errorf("shares commitment mismatch (header value %x, calculated %x)", *header.SharesRoot, hash)
		}
	} else if len(entries) > 0 {
		return errors.New("shares present without a commitment")
	}
	return nil
}
