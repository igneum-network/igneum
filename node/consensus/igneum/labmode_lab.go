//go:build ignpi

package igneum

// labBuild is true on the lab build (the tag ignpi, the pi staticlib the lab
// builds carry); the public build (no tags) sets it false in labmode_pub.go.
// The research instruments (--dev.withhold, --dev.lax-endorse) are refused
// unless labBuild is true (the project's rule of 18:0x UK, 11 October 2026: they
// never ship).
const labBuild = true
