//go:build !ignpi

package igneum

// labBuild is false in the public build (no tags): the research instruments
// (--dev.withhold, --dev.lax-endorse) are refused. The lab build (the tag
// ignpi) sets it true in labmode_lab.go.
const labBuild = false
