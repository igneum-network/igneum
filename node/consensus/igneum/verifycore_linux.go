//go:build linux

package igneum

import (
	"runtime"
	"sync"

	"github.com/ethereum/go-ethereum/log"
	"golang.org/x/sys/unix"
)

// The reserved verifier thread (the outside review's form, the project's order of 13
// October 2026 on the 4 GB two-core node's 50.7 ms p99 under a concurrent
// sync against the 50 ms whole-block budget, PASS alone): with
// --igneum.verifier-core N each header-verification worker locks its
// goroutine to an OS thread, pins that thread to core N and raises its
// scheduling priority (nice -10; a refusal without the capability is
// logged once and the pin stands alone), so the sync and the import run
// on the other cores and the verifier's latency is its own. Off (-1) by
// default; Linux only, a no-op elsewhere.
var verifierCoreWarn sync.Once

func reserveVerifierThread(core int) func() {
	if core < 0 {
		return func() {}
	}
	// The core is an index into the thread's ALLOWED set (a container's
	// cpuset is a window of absolute ids, CPUs 90 and 91 on the 8 GB node
	// of the record, where an absolute 1 was refused as invalid): N >= 1
	// the N-th allowed CPU (the last when N is past the end); 0, the
	// shipped default, the last allowed CPU on a host with two or more
	// and no pin on one (the sync and the import keep the rest).
	var allowed unix.CPUSet
	if err := unix.SchedGetaffinity(0, &allowed); err != nil {
		verifierCoreWarn.Do(func() { log.Warn("Verifier core: the allowed set is unreadable; no pin", "err", err) })
		return func() {}
	}
	ids := make([]int, 0, 8)
	for i := 0; i < 1024; i++ {
		if allowed.IsSet(i) {
			ids = append(ids, i)
		}
	}
	// The shipped default (0) pins on the TWO-CPU minimum node alone: on a
	// host with more CPUs every verification worker of every node pinned
	// to the one last CPU serialised the verification of the whole box
	// (vectorwork5 on a builder's 96 cores, 13 October 2026, 11:52Z: the
	// imports from 0.27 s to 2 s at the median and two minutes at the
	// ninetieth, the work path stalled behind them, F-97); a wider host
	// pins only on an explicit core.
	if len(ids) == 0 || (core == 0 && len(ids) != 2) {
		return func() {}
	}
	cpu := ids[len(ids)-1]
	if core >= 1 && core <= len(ids) {
		cpu = ids[core-1]
	}
	runtime.LockOSThread()
	var set unix.CPUSet
	set.Zero()
	set.Set(cpu)
	if err := unix.SchedSetaffinity(0, &set); err != nil {
		verifierCoreWarn.Do(func() { log.Warn("Verifier core: affinity refused", "core", core, "cpu", cpu, "err", err) })
	} else {
		verifierCoreWarn.Do(func() { log.Info("Verifier core: workers pinned", "core", core, "cpu", cpu) })
	}
	if err := unix.Setpriority(unix.PRIO_PROCESS, unix.Gettid(), -10); err != nil {
		verifierCoreWarn.Do(func() { log.Warn("Verifier core: priority refused (no CAP_SYS_NICE); the pin stands", "core", core, "err", err) })
	}
	return runtime.UnlockOSThread
}

// verifierPinned reports whether reserveVerifierThread will pin for the
// configured core on this host (the same rule as the pin itself).
func verifierPinned(core int) bool {
	if core < 0 {
		return false
	}
	var allowed unix.CPUSet
	if err := unix.SchedGetaffinity(0, &allowed); err != nil {
		return false
	}
	n := 0
	for i := 0; i < 1024; i++ {
		if allowed.IsSet(i) {
			n++
		}
	}
	return n > 0 && (core >= 1 || n == 2)
}
