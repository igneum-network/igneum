//go:build !devhold

package igneum

import "github.com/ethereum/go-ethereum/core/types"

// DevBuildTags is empty: a served build carries no development knob.
var DevBuildTags []string

// devHoldRefuses is false on every build without the devhold tag.
func (e *Engine) devHoldRefuses(header *types.Header) bool { return false }

// DevBuildTags is igneum_devBuildTags: empty on a served build, the box
// check's refusal rule for a release label (go version -m <binary> names
// the tags too).
func (api *IgneumAPI) DevBuildTags() []string { return DevBuildTags }
