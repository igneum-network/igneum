// Copyright 2026 The Igneum Authors
// This file is part of the go-ethereum library.
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

//go:build !linux

package igneum

import "errors"

// guardSecretProcess on a platform without the Linux calls: the guard is
// not applied and the caller logs it (a sealer of record runs on Linux;
// the lab's other hosts read the warning).
func guardSecretProcess(secret []byte) (warn error, err error) {
	return errors.New("igneum: the secret's process guard (core limit, dumpable bit, mlock) is Linux only; not applied here"), nil
}
