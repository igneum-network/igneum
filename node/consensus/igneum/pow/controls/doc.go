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

// Package controls is the node's development-only adapters for the three
// control algorithms of the Phase Zero comparison (KAWPOW 0.9.4, FishHash 1,
// Autolykos 2), verifying through the shared C reference's light path
// (pow/ref, ignpow.h) over cgo. They exist so that the verifier of the
// IgneumHash candidate is measured beside the controls on the same node and
// CPU (the founder's review, 10 October 2026): the controls are reference
// implementations, never Igneum consensus candidates, registered development
// only (pow.RegisterDevOnly, reached by --dev.algorithm alone) and built only
// with the build tag "controls" against a built reference:
//
//	cmake -S pow/ref -B build/ref -DCMAKE_BUILD_TYPE=Release \
//	  -DIGNPOW_FISHHASH_SRC=$PWD/pow/third_party/fish-hash-823531c/src
//	cmake --build build/ref -j
//	cd node && CGO_ENABLED=1 go build -tags controls ./... && go test -tags controls ./consensus/igneum/pow/controls/
//
// Without the tag this package is an empty package and the node knows no
// control; the public build never carries them.
package controls
