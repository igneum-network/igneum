package igneum

import "testing"

// TestDevHoldOffOnServedBuild: a build without the devhold tag carries no
// development tag and refuses nothing (the box check's rule for a release
// label reads the same list through igneum_devBuildTags and go version -m).
func TestDevHoldOffOnServedBuild(t *testing.T) {
	e := vectorEngine(t, "")
	for _, tag := range DevBuildTags {
		if tag == "devhold" {
			t.Skip("built with the devhold tag: the served-build rule does not apply to this binary")
		}
	}
	if len(DevBuildTags) != 0 {
		t.Fatalf("a served build carries development tags: %v", DevBuildTags)
	}
	c := newForkChain()
	g := genesisHeader()
	c.setCanonical(g)
	h := taggedChild(t, e, c, g, 1, 20)
	if e.devHoldRefuses(h) {
		t.Fatal("a served build holds a coordinate's block")
	}
}
