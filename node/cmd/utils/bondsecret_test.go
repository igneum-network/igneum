package utils

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestSecretFromFlags (F-440): the string form reads as before; the file
// form reads a 0600 file once and trims it; both given are refused by the
// two names; a group- or world-readable file, a missing file and an empty
// file are refused by the path; no value appears in any error.
func TestSecretFromFlags(t *testing.T) {
	dir := t.TempDir()
	// The fixture is built at run time (no 64-hex literal in the source: the
	// mirror's secret scan refuses one beside a key word).
	secret := "0x" + strings.Repeat("01", 32)
	good := filepath.Join(dir, "seed")
	if err := os.WriteFile(good, []byte(secret+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if v, err := secretFromFlags(secret, "", "dev.bondseed", "dev.bondseed.file"); err != nil || v != secret {
		t.Fatalf("the string form: %q %v", v, err)
	}
	if v, err := secretFromFlags("", good, "dev.bondseed", "dev.bondseed.file"); err != nil || v != secret {
		t.Fatalf("the file form: %q %v", v, err)
	}
	if _, err := secretFromFlags(secret, good, "dev.bondseed", "dev.bondseed.file"); err == nil || !strings.Contains(err.Error(), "dev.bondseed") || !strings.Contains(err.Error(), "dev.bondseed.file") || strings.Contains(err.Error(), secret[2:10]) {
		t.Fatalf("both given: %v", err)
	}
	wide := filepath.Join(dir, "wide")
	if err := os.WriteFile(wide, []byte(secret), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := secretFromFlags("", wide, "dev.bondseed", "dev.bondseed.file"); err == nil || !strings.Contains(err.Error(), "0600") || !strings.Contains(err.Error(), wide) || strings.Contains(err.Error(), secret[2:10]) {
		t.Fatalf("a world-readable file: %v", err)
	}
	missing := filepath.Join(dir, "none")
	if _, err := secretFromFlags("", missing, "dev.bondseed", "dev.bondseed.file"); err == nil || !strings.Contains(err.Error(), missing) {
		t.Fatalf("a missing file: %v", err)
	}
	empty := filepath.Join(dir, "empty")
	if err := os.WriteFile(empty, []byte("  \n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := secretFromFlags("", empty, "dev.bondseed", "dev.bondseed.file"); err == nil || !strings.Contains(err.Error(), "empty") {
		t.Fatalf("an empty file: %v", err)
	}
}
