//go:build devhold

package igneum

import (
	"sync"

	"github.com/ethereum/go-ethereum/core/types"
)

// The development hold (the project's order, 13 October 2026, closure 20 row 5:
// "delayed coordinate delivery"): a node built with the devhold tag can
// HOLD one coordinate's tagged blocks at import (refused as future blocks:
// the peer is kept, nothing propagates, the chain stalls at the first held
// block) and RELEASE them on command, after which the downloader catches
// up. Never in a served chain's binary: the tag is off in every release
// build and the box check refuses a release label built with it.

// DevBuildTags names the development build tags the binary carries.
var DevBuildTags = []string{"devhold"}

type devHoldState struct {
	mu   sync.Mutex
	held map[uint8]bool
}

var devHold devHoldState

// devHoldRefuses reports whether a header's coordinate is held.
func (e *Engine) devHoldRefuses(header *types.Header) bool {
	c, ok := header.CoordTag()
	if !ok {
		return false
	}
	devHold.mu.Lock()
	defer devHold.mu.Unlock()
	return devHold.held[c]
}

// DevHoldCoordinate holds (on) or releases (off) a coordinate's blocks.
func (e *Engine) DevHoldCoordinate(c uint8, on bool) {
	devHold.mu.Lock()
	defer devHold.mu.Unlock()
	if devHold.held == nil {
		devHold.held = map[uint8]bool{}
	}
	devHold.held[c] = on
	e.log.Warn("DEVELOPMENT HOLD on a coordinate's blocks (never a served chain's form)", "coordinate", c, "held", on)
}

// DevHoldCoordinate is igneum_devHoldCoordinate(c, on): the knob.
func (api *IgneumAPI) DevHoldCoordinate(c uint8, on bool) ([]string, error) {
	api.engine.DevHoldCoordinate(c, on)
	return DevBuildTags, nil
}

// DevBuildTags is igneum_devBuildTags: the development tags this binary
// carries (empty on a served build).
func (api *IgneumAPI) DevBuildTags() []string { return DevBuildTags }
