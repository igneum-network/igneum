//go:build linux

package igneum

import (
	"os"
	"strings"
	"testing"

	"golang.org/x/sys/unix"
)

// F-55: after the bond secret enters the engine, the process's core limit
// is zero and the dumpable bit off; the secret's page is locked when the
// host allows it (VmLck in the status file).
func TestSecretProcessGuard(t *testing.T) {
	e := newTestEngine(t)
	seed := make([]byte, 32)
	seed[0] = 7
	if err := e.SetSigner(seed); err != nil {
		t.Fatal(err)
	}
	var lim unix.Rlimit
	if err := unix.Getrlimit(unix.RLIMIT_CORE, &lim); err != nil {
		t.Fatal(err)
	}
	if lim.Cur != 0 || lim.Max != 0 {
		t.Fatalf("the core limit reads %d/%d, want 0/0", lim.Cur, lim.Max)
	}
	d, err := unix.PrctlRetInt(unix.PR_GET_DUMPABLE, 0, 0, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	if d != 0 {
		t.Fatalf("dumpable reads %d, want 0", d)
	}
	st, _ := os.ReadFile("/proc/self/status")
	for _, line := range strings.Split(string(st), "\n") {
		if strings.HasPrefix(line, "VmLck:") {
			t.Logf("%s (the secret's page, when the host allows mlock)", strings.TrimSpace(line))
		}
	}
}
