package igneum

import (
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/params/types/ctypes"
)

// AdmitRegistryDocument runs a registry document through the admission a
// node applies at a landing (admitLanding: the sender against the key in
// force, the document's shape, the re-registration rules against the
// registration in force at the landing height, the notice), and returns
// the document it would hold or the refusal by name. The signer's check
// before a transaction (the project's rule of 20:19 UK, 15 October 2026, after
// the public test network's 364 refusal: a document without this line is
// not signed); the same code path as the import's, nothing beside it.
func AdmitRegistryDocument(current *ctypes.IgneumVector, doc []byte, sender common.Address, landing uint64) (*ctypes.IgneumVector, error) {
	return admitLanding(current, doc, sender, landing)
}
