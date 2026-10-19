//go:build controls

package controls

import (
	"math/big"
	"os"
	"strings"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/consensus/igneum/pow"
)

// The verifier's row of record on the minimum node (the outside review's ruling of
// 20:17 UK and the hash team's answer of 20:5x UK, 12 October 2026): the node
// of record verifies every version 8 and later document through the cgo
// adapter to the C reference, so the cost per verified header is
// ignpow_verify through this adapter on the registered twin, not the Go-native
// path. The documents: the share lane's UNKEYED twin (version 8, features 256,
// the blended form with the walk-only constant) and the header seal's KEYED
// document (features 1280, keyed_rounds 2), both at dataset 2,048 MiB, rounds
// 64, cache 32 MiB, parents 256, item_bytes 256, blocks 1, registers 8; the
// version 11 pair of record (f8448 and f9472-k2 with program_ops) from the
// files IGNEUM_TWIN_DOC_V11 and IGNEUM_TWIN_DOC_V11_KEYED when given. Each is
// read under the light context (a node without the dataset: the 64 items
// computed from their parents, the verifier's own case) and with the full
// dataset (the sealer's), a valid proof and an invalid one.
func BenchmarkRefVerifyTwin(b *testing.B) {
	docs := map[string][]byte{
		"v8-f256-unkeyed": []byte(`{"version": 8, "dataset_mib": 2048, "scratch_kib": 0, "rounds": 64, "cache_mib": 32, "parents": 256, "item_bytes": 256, "blocks": 1, "registers": 8, "features": 256, "chain_id": 7310}`),
		"v8-f1280-k2":     []byte(`{"version": 8, "dataset_mib": 2048, "scratch_kib": 0, "rounds": 64, "cache_mib": 32, "parents": 256, "item_bytes": 256, "blocks": 1, "registers": 8, "features": 1280, "keyed_rounds": 2, "chain_id": 7310}`),
	}
	for name, env := range map[string]string{"v11-f8448": "IGNEUM_TWIN_DOC_V11", "v11-f9472-k2": "IGNEUM_TWIN_DOC_V11_KEYED"} {
		if p := os.Getenv(env); p != "" {
			d, err := os.ReadFile(p)
			if err != nil {
				b.Fatalf("%s: %v", env, err)
			}
			docs[name] = d
		}
	}
	// IGNEUM_TWIN_DOCS names any further documents as name=path pairs
	// separated by commas (the version 14 sets at program_ops 128, 512 and
	// 2,048, for one); IGNEUM_TWIN_DOCS_ONLY=1 drops the built-in pair so a
	// run reads the named documents alone.
	if list := os.Getenv("IGNEUM_TWIN_DOCS"); list != "" {
		if os.Getenv("IGNEUM_TWIN_DOCS_ONLY") == "1" {
			docs = map[string][]byte{}
		}
		for _, pair := range strings.Split(list, ",") {
			name, p, ok := strings.Cut(strings.TrimSpace(pair), "=")
			if !ok || name == "" || p == "" {
				b.Fatalf("IGNEUM_TWIN_DOCS: %q is not name=path", pair)
			}
			d, err := os.ReadFile(p)
			if err != nil {
				b.Fatalf("IGNEUM_TWIN_DOCS %s: %v", name, err)
			}
			docs[name] = d
		}
	}
	for name, doc := range docs {
		a, err := newIgneumhashRef(7310, doc)
		if err != nil {
			b.Fatalf("%s: %v", name, err)
		}
		full, _ := a.(interface{ SetFullDataset(bool) })
		h := common.HexToHash("0x1234")
		// difficulty 1: the target is the whole range, so a valid proof's
		// result is never above it (the G1 harness's form); an invalid proof
		// is the wrong nonce for the mix
		one := big.NewInt(1)
		k, keyed := a.(pow.Keyed)
		if keyed && k.KeyedRounds(5300) == 0 {
			keyed = false
		}
		var mix common.Hash
		var shares []byte
		if keyed {
			// the header seal's form: the prover's keyed hash once for the
			// shares, then the verifier's replay from the shares each run,
			// which is the keyed verifier's cost
			var sk [32]byte
			sk[0], sk[31] = 7, 9
			var r common.Hash
			mix, r, shares = k.ComputeKeyed(h, 5300, 7, &sk, nil)
			if mix == (common.Hash{}) && r == (common.Hash{}) {
				b.Fatalf("%s: the keyed entry refused the prover's hash", name)
			}
		} else {
			mix, _ = a.Compute(h, 5300, 7)
			if mix == (common.Hash{}) {
				b.Fatalf("%s: the reference refused the triple", name)
			}
		}
		for _, ctx := range []string{"light", "full"} {
			if full != nil {
				full.SetFullDataset(ctx == "full")
			}
			if keyed {
				flipped := append([]byte(nil), shares...)
				flipped[0] ^= 1
				b.Run(name+"/"+ctx+"/valid", func(b *testing.B) {
					b.ReportAllocs()
					for i := 0; i < b.N; i++ {
						if m, _, _ := k.ComputeKeyed(h, 5300, 7, nil, shares); m != mix {
							b.Fatal("the replay's mix differs from the prover's")
						}
					}
				})
				b.Run(name+"/"+ctx+"/invalid", func(b *testing.B) {
					b.ReportAllocs()
					for i := 0; i < b.N; i++ {
						if m, _, _ := k.ComputeKeyed(h, 5300, 7, nil, flipped); m == mix {
							b.Fatal("a flipped share replayed to the prover's mix")
						}
					}
				})
				continue
			}
			b.Run(name+"/"+ctx+"/valid", func(b *testing.B) {
				b.ReportAllocs()
				for i := 0; i < b.N; i++ {
					if err := a.Verify(h, 5300, 7, mix, one, ctx == "full"); err != nil {
						b.Fatal(err)
					}
				}
			})
			b.Run(name+"/"+ctx+"/invalid", func(b *testing.B) {
				b.ReportAllocs()
				for i := 0; i < b.N; i++ {
					if err := a.Verify(h, 5300, 8, mix, one, ctx == "full"); err == nil {
						b.Fatal("an invalid proof verified")
					}
				}
			})
		}
		if c, ok := a.(interface{ Close() }); ok {
			c.Close()
		}
	}
}
