package eth

import (
	"math/big"
	"testing"

	"github.com/ethereum/go-ethereum/common"
)

// TestFetcherLeadHoldsOnlyAHeldHead (F-199, 16 October 2026): a peer's lead
// is the fetcher's to close only when it is at most the peer's head block's
// difficulty AND this node holds the peer's head block (the true one-block
// lead); a head this node lacks, a sibling of its own head after a restart
// or a longer gap, goes to the downloader however small the lead. Two of
// the research chain's eight sat on a sibling head for two and a half
// minutes after a roll under the rule without the second condition, the
// peers' seven small-difficulty blocks under one head's difficulty.
func TestFetcherLeadHoldsOnlyAHeldHead(t *testing.T) {
	held := common.HexToHash("0x01")
	sibling := common.HexToHash("0x02")
	has := func(h common.Hash) bool { return h == held }
	big_ := func(n int64) *big.Int { return big.NewInt(n) }
	for _, tc := range []struct {
		name            string
		head            common.Hash
		headDiff        *big.Int
		peerTD, ourTD   *big.Int
		wantFetcherLead bool
	}{
		{"a held head one block ahead", held, big_(100), big_(1100), big_(1000), true},
		{"a held head ahead by exactly its difficulty", held, big_(100), big_(1100), big_(1000), true},
		{"a held head ahead by more than one block", held, big_(100), big_(1300), big_(1000), false},
		{"a sibling head one block ahead", sibling, big_(100), big_(1100), big_(1000), false},
		{"a sibling head ahead by seven small blocks under one head", sibling, big_(562_265_136), big_(1_000_200_000), big_(1_000_000_000), false},
		{"a held head, the peer behind", held, big_(100), big_(900), big_(1000), false},
		{"a held head, the peer level", held, big_(100), big_(1000), big_(1000), false},
		{"no head difficulty known", held, nil, big_(1100), big_(1000), false},
		{"a zero head difficulty", held, big_(0), big_(1100), big_(1000), false},
	} {
		if got := fetcherLead(has, tc.head, tc.headDiff, tc.peerTD, tc.ourTD); got != tc.wantFetcherLead {
			t.Errorf("%s: fetcher's lead %v, want %v", tc.name, got, tc.wantFetcherLead)
		}
	}
}
