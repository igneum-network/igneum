package igneum

import (
	"math/big"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/consensus/igneum/pow"
	"github.com/ethereum/go-ethereum/core/types"
)

// TestWithholdSealJob (F-420, 17 October 2026): a work package whose
// template is at or behind the head, or whose parent is not the head,
// loses its block job (HeaderHash zero, Target nil as the share-only
// package serves it, StaleTemplate set); a package on the head keeps it.
func TestWithholdSealJob(t *testing.T) {
	head := &types.Header{Number: big.NewInt(3872), Difficulty: big.NewInt(1000)}
	live := func() pow.WorkPackage {
		return pow.WorkPackage{HeaderHash: common.Hash{0xaa}, Height: 3873, ParentHash: head.Hash(), Target: pow.Target(big.NewInt(22469))}
	}
	w := live()
	if withholdSealJob(&w, head) || w.HeaderHash == (common.Hash{}) || w.StaleTemplate != "" || w.Target == nil || w.Target.Sign() == 0 {
		t.Fatalf("a template on the head lost its block job: %+v", w)
	}
	behind := live()
	behind.Height, behind.ParentHash = 3871, common.Hash{0x70}
	if !withholdSealJob(&behind, head) || behind.HeaderHash != (common.Hash{}) || behind.Target != nil || behind.StaleTemplate == "" {
		t.Fatalf("a template behind the head kept its block job: %+v", behind)
	}
	twin := live()
	twin.ParentHash = common.Hash{0x71} // the head's height, another parent
	if !withholdSealJob(&twin, head) || twin.HeaderHash != (common.Hash{}) || twin.Target != nil || twin.StaleTemplate == "" {
		t.Fatalf("a template off the head kept its block job: %+v", twin)
	}
	// A package without a parent (an older node's form): the height rule alone.
	noParent := live()
	noParent.ParentHash = common.Hash{}
	if withholdSealJob(&noParent, head) {
		t.Fatalf("a package without a parent at head+1 lost its block job: %+v", noParent)
	}
}
