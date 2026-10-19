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

//go:build linux

package igneum

import (
	"fmt"

	"golang.org/x/sys/unix"
)

// guardSecretProcess is F-55 (the security review, 13:1x UK, 12 October 2026, the
// custody pod): a node holding the bond secret must not write it into a
// core file on the host's disk on a crash (a rented host's landlord reads
// the core_pattern's directory) and must not let the secret's page swap:
// the core limit zero, the process not dumpable (which also refuses
// ptrace and /proc/<pid>/mem to another uid), the secret's bytes locked
// in memory. The first two are the ones that close the core file and are
// errors when refused; the mlock is best effort (RLIMIT_MEMLOCK can be 0
// in a container) and is returned as a warning the caller logs.
func guardSecretProcess(secret []byte) (warn error, err error) {
	if e := unix.Setrlimit(unix.RLIMIT_CORE, &unix.Rlimit{Cur: 0, Max: 0}); e != nil {
		return nil, fmt.Errorf("igneum: the core limit: %w", e)
	}
	if e := unix.Prctl(unix.PR_SET_DUMPABLE, 0, 0, 0, 0); e != nil {
		return nil, fmt.Errorf("igneum: the dumpable bit: %w", e)
	}
	if e := unix.Mlock(secret); e != nil {
		return fmt.Errorf("igneum: mlock of the bond secret refused (%v); the core limit and the dumpable bit hold", e), nil
	}
	return nil, nil
}
