// Copyright 2022 The go-ethereum Authors
// This file is part of go-ethereum.
//
// go-ethereum is free software: you can redistribute it and/or modify
// it under the terms of the GNU General Public License as published by
// the Free Software Foundation, either version 3 of the License, or
// (at your option) any later version.
//
// go-ethereum is distributed in the hope that it will be useful,
// but WITHOUT ANY WARRANTY; without even the implied warranty of
// MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE. See the
// GNU General Public License for more details.
//
// You should have received a copy of the GNU General Public License
// along with go-ethereum. If not, see <http://www.gnu.org/licenses/>.

package main

import (
	"bytes"
	"fmt"
	"os"
	"testing"

	"github.com/ethereum/go-ethereum/common"
)

// TestExport does a basic test of "geth export", exporting the Igneum development genesis.
func TestExport(t *testing.T) {
	t.Parallel()
	outfile := fmt.Sprintf("%v/testExport.out", os.TempDir())
	defer os.Remove(outfile)
	geth := runGeth(t, "--datadir", initGeth(t), "export", outfile)
	geth.WaitExit()
	if have, want := geth.ExitStatus(), 0; have != want {
		t.Errorf("exit error, have %d want %d", have, want)
	}
	have, err := os.ReadFile(outfile)
	if err != nil {
		t.Fatal(err)
	}
	want := common.FromHex("0xf9020cf90207a00000000000000000000000000000000000000000000000000000000000000000a01dcc4de8dec75d7aab85b567b6ccd41ad312451b948a7413f0a142fd40d49347940000000000000000000000000000000000000000a0b2fb62828f898d73909c50f307bc2c02b6b405e56e776aeee6c025f759a4ba71a056e81f171bcc55a6ff8345e692c0f86e5b48e01b996cadc001622fb5e363b421a056e81f171bcc55a6ff8345e692c0f86e5b48e01b996cadc001622fb5e363b421b9010000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000830100008083f4240080808f69676e65756d2d6465762f6e756c6ca00000000000000000000000000000000000000000000000000000000000000000880000000000000000843b9aca00c0c0") // the Igneum development genesis (PA-09b)
	if !bytes.Equal(have, want) {
		t.Fatalf("wrong content exported")
	}
}
