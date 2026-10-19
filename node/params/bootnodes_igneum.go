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

package params

// IgneumBootnodes are the enode URLs of the Igneum bootstrap nodes (PA-02).
// Empty until the reliability engineer runs them; each entry lands as its own
// reviewed change with the node's operator named. No DNS discovery tree at
// launch: an Igneum-signed tree can come later as its own patch.
var IgneumBootnodes = []string{
	// The development network's bootnode is served by the public feed's
	// genesis set (the bootnode line beside the genesis files), never
	// built in: a mesh-only node here was dialled by every fresh node and
	// reached by none (the private-list rule of 12 October 2026).
}
