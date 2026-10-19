//go:build !igneumhash

package service

import (
	"context"
	"time"
)

// The payout run needs the node's packages (the key, the transaction's signing); the plain build serves /payouts
// as absent.
type payoutState struct{}

func (s *Service) payoutInit() error                           { return nil }
func (s *Service) payoutLoadRecord()                           {}
func (s *Service) PayoutStart() error                          { return nil }
func (s *Service) PayoutWait(time.Duration) bool               { return true }
func (s *Service) laneWorthAt(coord uint8) string              { return "" }
func (s *Service) payoutTick(ctx context.Context, head uint64) {}
func (s *Service) Payouts() map[string]interface{}             { return nil }
