package jobsource

import (
	"encoding/json"
	"testing"
)

// A keyed sealer's getWork under the share lane (devnet7's member b at a790fec5, 11 October 2026): the block fields
// zero, the target null, the share fields the whole work. The parser must return a share-only package, not ErrShape.
const shareOnlyPackage = `{"algorithm":"igneumhash","headerHash":"0x0000000000000000000000000000000000000000000000000000000000000000","height":716,"epoch":0,"seedHash":"0x0000000000000000000000000000000000000000000000000000000000000000","target":null,"paramsDigest":"0x0000000000000000000000000000000000000000000000000000000000000000","nextParamsDigest":"0x0000000000000000000000000000000000000000000000000000000000000000","coinbase":"0x0000000000000000000000000000000000000000","shareTarget":2270433122300317557324921274680155055946470287561579687048187921723786855685,"sharesPerBlock":160,"shareHeight":716,"shareParent":"0xf5569fb8cafd93938b4c20d70a027569db51c5c43812efac258e8e6550d3426b","shareParams":{"blocks":2,"cache_mib":32,"chain_id":7317,"dataset_mib":2048,"features":4128,"item_bytes":128,"parents":256,"registers":16,"rounds":64,"scratch_kib":0,"version":8},"shareParamsDigest":"0xf3ff71d531aca97bd98fcbacb0dd8282837c434795c99d83c3e150b922075fee","shareOnly":true}`

func TestShareOnlyPackage(t *testing.T) {
	var obj map[string]json.RawMessage
	if err := json.Unmarshal([]byte(shareOnlyPackage), &obj); err != nil {
		t.Fatal(err)
	}
	w, err := workFromObject(obj)
	if err != nil {
		t.Fatalf("share-only package refused: %v", err)
	}
	if !w.ShareOnly || !w.HasShareLane() || w.ShareHeight != 716 || w.SharesPerBlock != 160 || w.Height != 716 {
		t.Fatalf("share-only package misread: %+v", w)
	}
	if len(w.ShareParams) == 0 || w.ShareParamsDigest == "" {
		t.Fatalf("share document missing: %+v", w)
	}
}
