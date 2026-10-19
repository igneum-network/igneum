// Copyright 2026 The core-geth Authors
// This file is part of the core-geth library.
//
// The core-geth library is free software: you can redistribute it and/or modify
// it under the terms of the GNU Lesser General Public License as published by
// the Free Software Foundation, either version 3 of the License, or
// (at your option) any later version.
//
// The core-geth library is distributed in the hope that it will be useful,
// but WITHOUT ANY WARRANTY; without even the implied warranty of
// MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE. See the
// GNU Lesser General Public License for more details.
//
// You should have received a copy of the GNU Lesser General Public License
// along with the core-geth library. If not, see <http://www.gnu.org/licenses/>.

package pow

import (
	"math/big"
	"testing"
)

// TestTarget pins the target to the text: floor((2^256 - 1) / difficulty).
func TestTarget(t *testing.T) {
	max := new(big.Int).Sub(new(big.Int).Lsh(big.NewInt(1), 256), big.NewInt(1))
	cases := []struct {
		difficulty *big.Int
		want       *big.Int
	}{
		{big.NewInt(1), max},
		{big.NewInt(2), new(big.Int).Rsh(max, 1)},
		{big.NewInt(3), new(big.Int).Div(max, big.NewInt(3))},
		{new(big.Int).Lsh(big.NewInt(1), 255), big.NewInt(1)},
		{max, big.NewInt(1)},
	}
	for _, c := range cases {
		if got := Target(c.difficulty); got == nil || got.Cmp(c.want) != 0 {
			t.Fatalf("difficulty %v: target %v, want %v", c.difficulty, got, c.want)
		}
	}
	for _, d := range []*big.Int{nil, big.NewInt(0), big.NewInt(-1), new(big.Int).Lsh(big.NewInt(1), 256)} {
		if Target(d) != nil {
			t.Fatalf("difficulty %v: want nil", d)
		}
	}
	// The spec's example: at difficulty 2 the boundary is 2^255 - 1, not 2^255.
	if got := Target(big.NewInt(2)); got.Bit(255) != 0 {
		t.Fatalf("difficulty 2: target %x has bit 255 set", got)
	}
}
