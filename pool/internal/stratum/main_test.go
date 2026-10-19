package stratum

import (
	"os"
	"testing"
)

// TestMain: the subscribe's wait for the first document (SubscribeWait, 0.1.62) is off across this package's tests,
// which subscribe before any job or document as a matter of course; the wait's own tests set it by hand.
func TestMain(m *testing.M) {
	SubscribeWait = 0
	os.Exit(m.Run())
}
