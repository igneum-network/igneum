//go:build igneumhash

// The IgneumHash validator: the same code the node runs, imported from
// this repository's node module (protocol engineer A, 10 October 2026:
// build the algorithm from the job's params document once per digest,
// anchor it per template height, verify each share by recomputation).
// Built with -tags igneumhash and a go.mod replace to ../node; the default
// (vendored) build carries Null alone.
//
// The document goes through the node's own registry (pow.NewDev, the
// development chain's path): a version-4 document builds the Go
// algorithm on every build; a version-8 document (the blended relation,
// the keyed rows of the fifth development chain) builds the C
// reference under the lab build (-tags "igneumhash controls" against the
// built reference libraries) and is refused by name on a build without
// it (igneumhash.ErrReferenceBuild), which closed the gap the C3 row
// named on 11 October 2026 (the validator parsed the document itself and
// refused version 8's fields on every build).
package validate

import (
	"errors"
	"sync"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/consensus/igneum/pow"
	"github.com/ethereum/go-ethereum/consensus/igneum/pow/igneumhash"
)

// IgneumHashAlgorithm is the id the validator answers to.
const IgneumHashAlgorithm = "igneumhash"

// IgneumHash holds one built algorithm per params digest.
type IgneumHash struct {
	mu      sync.Mutex
	current string
	algos   map[string]pow.Algorithm
}

var errNoParams = errors.New("validate: no params document prepared")

func init() {
	Register(IgneumHashAlgorithm, func() Validator { return &IgneumHash{algos: map[string]pow.Algorithm{}} })
}

// Prepare builds the algorithm for a digest once and anchors it at height.
// knownAlgorithm: the names this validator prepares and verifies: igneumhash and, on VectorWork, the graph
// coordinate's relation ("igneum-graph", Cuckaroo29 under Igneum's rules, registered dev-only by the node's
// controls package the lab build imports; the project's fault of 11:5x UK, 14 October 2026: coordinate 3 served no
// template because the lane prepared every coordinate as igneumhash).
func knownAlgorithm(algorithm string) bool {
	return algorithm == IgneumHashAlgorithm || algorithm == GraphAlgorithm
}


func (v *IgneumHash) Prepare(algorithm string, params []byte, digest string, height uint64) error {
	if !knownAlgorithm(algorithm) {
		return nil
	}
	v.mu.Lock()
	defer v.mu.Unlock()
	a, ok := v.algos[digest]
	if !ok {
		if len(params) == 0 {
			return errNoParams
		}
		name := igneumhash.Name
		if algorithm != IgneumHashAlgorithm {
			name = algorithm
		}
		built, err := pow.NewDev(name, params)
		if err != nil {
			return err
		}
		a = built
		v.algos[digest] = a
	}
	// the Go algorithm anchors its dataset per template height; the C reference keeps a context per epoch and has no
	// anchor to take
	if an, ok := a.(interface{ Anchor(uint64) }); ok {
		an.Anchor(height)
	}
	v.current = digest
	return nil
}

// Verify recomputes mix and result for the share on the current document.
func (v *IgneumHash) Verify(algorithm string, header [32]byte, height uint64, nonce uint64) ([32]byte, [32]byte, error) {
	if !knownAlgorithm(algorithm) {
		return [32]byte{}, [32]byte{}, ErrAlgorithm
	}
	v.mu.Lock()
	a := v.algos[v.current]
	v.mu.Unlock()
	if a == nil {
		return [32]byte{}, [32]byte{}, errNoParams
	}
	mix, result := a.Compute(common.Hash(header), height, nonce)
	return [32]byte(mix), [32]byte(result), nil
}

// VerifyOn recomputes mix and result for the share on the document named by digest (VectorWork, 13 October
// 2026: a template is one coordinate's and each coordinate has its own document, so the share is checked on its
// job's document, not the last one prepared); a digest never prepared is refused.
func (v *IgneumHash) VerifyOn(algorithm, digest string, header [32]byte, height uint64, nonce uint64) ([32]byte, [32]byte, error) {
	if !knownAlgorithm(algorithm) {
		return [32]byte{}, [32]byte{}, ErrAlgorithm
	}
	v.mu.Lock()
	a := v.algos[digest]
	v.mu.Unlock()
	if a == nil {
		return [32]byte{}, [32]byte{}, errNoParams
	}
	mix, result := a.Compute(common.Hash(header), height, nonce)
	return [32]byte(mix), [32]byte(result), nil
}

// programmed is the binding's per-period form (pow.Programmed in the node module): the document's period_blocks
// and the algorithm bound to one period anchor.
type programmed interface {
	PeriodBlocks(height uint64) uint64
	Under(anchor common.Hash) pow.Algorithm
}

// PeriodBlocks is the document's period length (0 on a document without a programme, versions below 9).
func (v *IgneumHash) PeriodBlocks(digest string) uint64 {
	v.mu.Lock()
	a := v.algos[digest]
	v.mu.Unlock()
	if pr, ok := a.(programmed); ok {
		return pr.PeriodBlocks(0)
	}
	return 0
}

// VerifyUnder recomputes mix and result for the share on the document named by digest UNDER the period's anchor
// (the VectorWork box soak of 02:27Z, 13 October 2026: the sealers verify a keyed document's share with the
// period's anchor as the chain resolves it, the pool computed on the document's zeros and 76 percent of its
// forwards were refused); a document without a programme ignores the anchor.
func (v *IgneumHash) VerifyUnder(algorithm, digest string, anchor [32]byte, header [32]byte, height uint64, nonce uint64) ([32]byte, [32]byte, error) {
	if !knownAlgorithm(algorithm) {
		return [32]byte{}, [32]byte{}, ErrAlgorithm
	}
	v.mu.Lock()
	a := v.algos[digest]
	v.mu.Unlock()
	if a == nil {
		return [32]byte{}, [32]byte{}, errNoParams
	}
	if pr, ok := a.(programmed); ok && pr.PeriodBlocks(height) > 0 {
		a = pr.Under(common.Hash(anchor))
	}
	mix, result := a.Compute(common.Hash(header), height, nonce)
	return [32]byte(mix), [32]byte(result), nil
}

// ReferenceAvailable reports whether this build carries the C reference
// (the lab build), so a listener can say which document versions it
// verifies.
func ReferenceAvailable() bool { return igneumhash.ReferenceAvailable() }

// VerifyTicket runs the graph relation's ticket verifier on the prepared document (the node's own, through the
// controls package the lab build imports).
func (v *IgneumHash) VerifyTicket(digest string, header [32]byte, height uint64, nonce uint64, ticket []byte) ([32]byte, error) {
	v.mu.Lock()
	a := v.algos[digest]
	v.mu.Unlock()
	if a == nil {
		return [32]byte{}, errNoParams
	}
	tv, ok := a.(interface {
		VerifyTicket(common.Hash, uint64, uint64, []byte) (common.Hash, error)
	})
	if !ok {
		return [32]byte{}, ErrAlgorithm
	}
	h, err := tv.VerifyTicket(common.Hash(header), height, nonce, ticket)
	return [32]byte(h), err
}
