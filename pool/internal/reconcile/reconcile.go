// Package reconcile keeps the ledger agreeing with the canonical chain
// (docs/ecosystem/DESIGN.md, section 2.11): every found block is
// rechecked against the chain until it matures; a mismatch before
// maturity reverses its credits; a mismatch after maturity is recorded as
// an incident and never netted silently. On restart the same loop runs
// from the stored list of found blocks; nothing is trusted from memory.
package reconcile

import (
	"context"
	"fmt"
	"time"

	"igneum.network/pool/internal/ledger"
)

// Chain is the node view the reconciler needs.
type Chain interface {
	Head(ctx context.Context) (uint64, error)
	HashAt(ctx context.Context, height uint64) (string, error)
}

// Book is the ledger surface the reconciler drives.
type Book interface {
	MatureBlock(ctx context.Context, blockHash string, at time.Time) error
	OrphanBlock(ctx context.Context, blockHash string, at time.Time) error
	BlockState(blockHash string) string
}

// Found is a block the pool submitted and saw accepted.
type Found struct {
	Hash   string
	Height uint64
}

// Reconciler tracks found blocks until they mature or are orphaned.
type Reconciler struct {
	chain     Chain
	book      Book
	maturity  uint64
	pending   []Found
	Incidents []string
}

// New creates a reconciler with the published maturity depth.
func New(chain Chain, book Book, maturity uint64) *Reconciler {
	return &Reconciler{chain: chain, book: book, maturity: maturity}
}

// Track adds a found block to the pending set.
func (r *Reconciler) Track(f Found) { r.pending = append(r.pending, f) }

// Pending is the number of blocks not yet settled.
func (r *Reconciler) Pending() int { return len(r.pending) }

// Run checks every pending block once against the chain.
func (r *Reconciler) Run(ctx context.Context, now time.Time) error {
	head, err := r.chain.Head(ctx)
	if err != nil {
		return err
	}
	keep := make([]Found, 0, len(r.pending))
	for _, f := range r.pending {
		canonical, err := r.chain.HashAt(ctx, f.Height)
		if err != nil {
			return err
		}
		if canonical != f.Hash {
			switch r.book.BlockState(f.Hash) {
			case ledger.StateImmature:
				if err := r.book.OrphanBlock(ctx, f.Hash, now); err != nil {
					return err
				}
			case ledger.StateMature:
				r.Incidents = append(r.Incidents, fmt.Sprintf("block %s at height %d is not canonical after maturity; operator decision required", f.Hash, f.Height))
			}
			continue
		}
		if head >= f.Height && head-f.Height >= r.maturity {
			if r.book.BlockState(f.Hash) == ledger.StateImmature {
				if err := r.book.MatureBlock(ctx, f.Hash, now); err != nil {
					return err
				}
			}
			continue
		}
		keep = append(keep, f)
	}
	r.pending = keep
	return nil
}
