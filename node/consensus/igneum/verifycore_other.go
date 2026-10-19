//go:build !linux

package igneum

// reserveVerifierThread is a no-op off Linux (verifycore_linux.go).
func reserveVerifierThread(core int) func() { return func() {} }

// verifierPinned is false off Linux.
func verifierPinned(core int) bool { return false }
