//go:build igneumhash && controls && cgo

// The lab build of the validator: importing the node's controls package
// registers the C reference for version-8 documents (its init calls
// igneumhash.RegisterReference), exactly as cmd/geth does under the same
// tag. Built with -tags "igneumhash controls" and CGO against the
// reference libraries in <repo>/build/ref (the protocol team's lab-build recipe).
package validate

import (
	_ "github.com/ethereum/go-ethereum/consensus/igneum/pow/controls"
)
