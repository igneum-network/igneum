//go:build igneumhash

package service

import (
	"math/big"
	"os"
	"path/filepath"
	"testing"
)

// the payout run's key: made on first start, 0600, never printed; the address becomes the forwarding address
func TestPayoutKeyFile(t *testing.T) {
	dir := t.TempDir()
	s := &Service{cfg: Config{PoolKeyFile: filepath.Join(dir, "pool.key"), MinPayoutWei: big.NewInt(1)}}
	if err := s.payoutInit(); err != nil {
		t.Fatal(err)
	}
	st, err := os.Stat(s.cfg.PoolKeyFile)
	if err != nil || st.Mode().Perm() != 0o600 {
		t.Fatalf("key file: %v %v", err, st.Mode())
	}
	if s.cfg.ForwardAs == "" || len(s.cfg.ForwardAs) != 42 {
		t.Fatalf("forward-as from the key: %q", s.cfg.ForwardAs)
	}
	first := s.cfg.ForwardAs
	s2 := &Service{cfg: Config{PoolKeyFile: s.cfg.PoolKeyFile}}
	if err := s2.payoutInit(); err != nil || s2.cfg.ForwardAs != first {
		t.Fatalf("the key is not read back the same: %v %q %q", err, first, s2.cfg.ForwardAs)
	}
	if p := s2.Payouts(); p["pool_key"] != first || p["pending"].(int) != 0 {
		t.Fatalf("payouts: %v", p)
	}
}
