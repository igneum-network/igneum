//go:build !igneumhash

package service

// The share lane needs the node's packages (the share header's RLP and
// Keccak, the version-8 document); the default (vendored) build carries
// Null alone and refuses the lane by name.

import (
	"context"
	"errors"

	"igneum.network/pool/internal/stratum"
)

var errShareLaneBuild = errors.New("service: the share lane needs the igneumhash build (-tags igneumhash against ../node)")

func (s *Service) pollShareLane(ctx context.Context) error { return errShareLaneBuild }

func (s *Service) submitShareTicket(ctx context.Context, job stratum.Job, nonce uint64, mix [32]byte, ticket []byte) error {
	return errShareLaneBuild
}

func (s *Service) submitShare(ctx context.Context, job stratum.Job, nonce uint64, mix [32]byte) error {
	return errShareLaneBuild
}
