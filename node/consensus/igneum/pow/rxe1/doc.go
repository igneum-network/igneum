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

// Package rxe1 is the node's development-only adapter for the RXE1
// experimental family (the engineering specification's sections 6 to 8;
// the hash team's scalar reference, pow/ref/include/rxe1.h), the adapter
// boundary of its section 19 on the second development chain only: its own
// algorithm id "rxe1" and namespace, the chain's epoch-seed rule in place of
// the harness's, and the chain's target encoding and range rule in place of
// the harness's little-endian convention, reconciled explicitly. Built only
// with the "controls" tag against the built reference (see package
// controls); the public build never carries it, and the registry marks it
// development-only (--dev.algorithm rxe1 --dev.algorithm.params '<document>').
package rxe1
