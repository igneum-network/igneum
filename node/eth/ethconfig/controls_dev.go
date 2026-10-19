// Copyright 2026 The Igneum Authors
// This file is part of the Igneum node.
//
// The Igneum node is free software: you can redistribute it and/or modify
// it under the terms of the GNU Lesser General Public License as published by
// the Free Software Foundation, either version 3 of the License, or
// (at your option) any later version.
//
// The Igneum node is distributed in the hope that it will be useful,
// but WITHOUT ANY WARRANTY; without even the implied warranty of
// MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE. See the
// GNU Lesser General Public License for more details.
//
// You should have received a copy of the GNU Lesser General Public License
// along with the Igneum node. If not, see <http://www.gnu.org/licenses/>.

//go:build controls && cgo

package ethconfig

// The three control algorithms of the verifier comparison register themselves
// in init (development only, --dev.algorithm) when the node is built with the
// "controls" tag against the shared C reference; see
// consensus/igneum/pow/controls.
import (
	_ "github.com/ethereum/go-ethereum/consensus/igneum/pow/controls"
	_ "github.com/ethereum/go-ethereum/consensus/igneum/pow/rxe1"
)
