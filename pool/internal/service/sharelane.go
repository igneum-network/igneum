//go:build igneumhash

package service

// The share lane's pool form (LAUNCH-GUARDS 2.9, the project's order of 11 October
// 2026 for devnet7; the protocol team's node shape of 12:3x UK): the node serves
// one network-wide share target from chain work; a worker builds its own
// share header H_s = KECCAK(RLP(shareParent, shareHeight, address,
// extraNonce)) and submits a share that meets the target with
// igneum_submitShare under its payout address; the chain carries the
// accepted shares into the next blocks' payout lists and pays pro rata. A
// pool on this lane is a relay and an accounting: every authorised member
// gets its own job on H_s under ITS address (the share's identity is the
// member's, never the pool's), and the member's share that meets the
// target goes to the node under the member's address. The pool never
// holds a reward: the chain pays the member by the list. A share is never
// a block (the seal-only rule), so no share-lane job reaches eth_submitWork.

import (
	"context"
	"encoding/hex"
	"fmt"
	"igneum.network/pool/internal/shares"
	"log"
	"math/big"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/rlp"

	"igneum.network/pool/internal/jobsource"
	"igneum.network/pool/internal/stratum"
	"igneum.network/pool/internal/validate"
)

// ShareHeader is H_s for a member's share search.
func ShareHeader(parent [32]byte, height uint64, address string, extraNonce uint64) ([32]byte, error) {
	if !common.IsHexAddress(address) {
		return [32]byte{}, fmt.Errorf("share header: %q is not an address", address)
	}
	enc, err := rlp.EncodeToBytes([]interface{}{common.Hash(parent), height, common.HexToAddress(address), extraNonce})
	if err != nil {
		return [32]byte{}, err
	}
	var h [32]byte
	copy(h[:], crypto.Keccak256(enc))
	return h, nil
}

func shareTarget32(raw []byte) ([32]byte, error) {
	s := strings.Trim(strings.TrimSpace(string(raw)), "\"")
	v := new(big.Int)
	var ok bool
	if strings.HasPrefix(s, "0x") {
		_, ok = v.SetString(s[2:], 16)
	} else {
		_, ok = v.SetString(s, 10)
	}
	if !ok || v.Sign() <= 0 {
		return [32]byte{}, fmt.Errorf("share target: %q", s)
	}
	var t [32]byte
	b := v.Bytes()
	if len(b) > 32 {
		return [32]byte{}, fmt.Errorf("share target over 256 bits")
	}
	copy(t[32-len(b):], b)
	return t, nil
}

// pollShareLane builds one job per authorised member on the current
// template's share fields; a member without a job yet, or a template that
// moved, gets a new one. The share document (the unkeyed twin) is prepared
// once per digest.
func (s *Service) pollShareLane(ctx context.Context) error {
	node := s.templateNode(ctx)
	if err := s.chainStillOfRecord(ctx, node); err != nil {
		return err // the source's chain is not the bound one: no job this poll, named once a minute
	}
	s.readBestHead(ctx)
	if len(s.cfg.Coordinates) == 0 {
		w, err := node.GetWork(ctx)
		s.mu.Lock()
		if err != nil {
			if s.sourceErrAt.IsZero() {
				s.sourceErrAt = s.now()
			}
		} else {
			s.sourceErrAt = time.Time{}
		}
		s.mu.Unlock()
		if err != nil {
			return err
		}
		if behind, best, from := s.behindBestHead(w.ShareHeight); behind {
			return s.sayBehind(fmt.Errorf("the template serves height %d while the chain's best known head is %d (%s): no job issued on it", w.ShareHeight, best, from))
		}
		return s.pollShareLaneWork(ctx, node, w, 0, false)
	}
	// VectorWork (the project's order of 00:5x UK, 13 October 2026): a template per coordinate from the sealer, one job
	// per (address, worker, coord); a coordinate the node does not serve this poll is skipped by name
	var firstErr error
	served := 0
	s.readPayShares(ctx, node)
	s.readRegistryReveals(ctx, node)
	for _, c := range s.laneSet() {
		src := node
		if cs := s.coordSource(c); cs != nil {
			src = cs // the coordinate's own sealer (vectorwork1: one sealer per coordinate)
		} else if paid, known := s.coordPaid(c); known && paid {
			if found := s.findSealer(ctx, c); found != nil {
				src = found // a paid coordinate the configuration names no sealer for: the one found among the configured nodes
			}
		}
		cw, ok := src.(interface {
			GetWorkCoord(context.Context, uint8) (jobsource.Work, error)
		})
		if !ok {
			return fmt.Errorf("the template node serves no per-coordinate work")
		}
		if paid, known := s.coordPaid(c); known && !paid {
			// an unpaid coordinate at the tip (the project's order of 10:3x UK, 15 October 2026): no job offered on it,
			// named once on the log and on /shares; its sessions take the paid coordinates' jobs
			s.mu.Lock()
			if s.payUnpaidSaid == nil {
				s.payUnpaidSaid = map[uint8]bool{}
			}
			say := !s.payUnpaidSaid[c]
			s.payUnpaidSaid[c] = true
			s.mu.Unlock()
			if say {
				log.Printf("share lane: coordinate %d (%s) pays 0 at the tip (height %d): no job offered on it until the chain pays it; its shares would pile at the sealer's bound", c, s.coordName(c), s.payReadHeight)
			}
			served++
			continue
		}
		w, err := cw.GetWorkCoord(ctx, c)
		if err == nil {
			// a reader behind the chain (pool-b2, 10:37 to 10:55 UK, 15 October 2026: coordinate 1's sealer resyncing
			// from genesis served templates at height 297 while the chain stood at 4300, the pool issued them and
			// every share came back refused): a template more than three blocks under the highest template the
			// lanes serve this poll is not issued, named once a minute
			s.mu.Lock()
			if w.ShareHeight > s.laneHead {
				s.laneHead = w.ShareHeight
			}
			ref, refName := s.laneHead, "the lanes"
			if s.payReadHeight > 0 {
				// the source's tip is the reference, never the highest lane (chain 7322's re-serve of 16:2x UTC,
				// 15 October 2026: a sealer left on the abandoned genesis served height 115 against the new
				// chain's 10 and the guard held the right lane back); a lane ahead of the source is on another
				// chain and is not issued either
				ref, refName = s.payReadHeight+1, "the source"
			}
			ahead := w.ShareHeight > ref+3
			// the chain's best known head over EVERY configured node is the reference for "behind" (the project's rule of
			// 16 October 2026): a lane more than three blocks under it is never issued, whatever the source says
			if s.bestHeadVal > ref {
				ref, refName = s.bestHeadVal, "the chain's best known head ("+s.bestHeadFrom+")"
			}
			behind := ref > w.ShareHeight+3
			s.mu.Unlock()
			if behind || ahead {
				err = fmt.Errorf("its reader serves height %d while %s stands at %d: no job issued on it until it agrees", w.ShareHeight, refName, ref)
			} else {
				err = s.pollShareLaneWork(ctx, src, w, c, true)
			}
		}
		if err != nil {
			// a coordinate's failure is named on the log (once a minute per coordinate), never swallowed behind the
			// others' templates (the vectorwork1 soak of 01:52Z, 13 October 2026: coordinate 0 served nothing and
			// the log said nothing)
			s.mu.Lock()
			if s.coordErrAt == nil {
				s.coordErrAt = map[uint8]time.Time{}
			}
			say := s.now().Sub(s.coordErrAt[c]) >= time.Minute
			if say {
				s.coordErrAt[c] = s.now()
			}
			s.mu.Unlock()
			if say {
				log.Printf("share lane: coordinate %d: %v", c, err)
			}
			if firstErr == nil {
				firstErr = fmt.Errorf("coordinate %d: %w", c, err)
			}
			continue
		}
		served++
	}
	if served == 0 {
		return firstErr
	}
	return nil
}

// targetFraction is the share of hashes that meet a target: target over 2^256.
func targetFraction(target [32]byte) float64 {
	t := new(big.Float).SetInt(new(big.Int).SetBytes(target[:]))
	max := new(big.Float).SetInt(new(big.Int).Lsh(big.NewInt(1), 256))
	f, _ := new(big.Float).Quo(t, max).Float64()
	return f
}

// sayBehind names a template held back by the best-head rule once a minute and returns the error for the poll.
func (s *Service) sayBehind(err error) error {
	now := s.now()
	s.mu.Lock()
	say := now.Sub(s.bestHeadSaid) >= time.Minute
	if say {
		s.bestHeadSaid = now
	}
	s.mu.Unlock()
	if say {
		log.Printf("share lane: %v", err)
	}
	return err
}

// laneAlgorithm is the algorithm a coordinate's template names (the node's "algorithm" field: igneumhash, or
// igneum-graph on the graph coordinate), igneumhash when the node names none.
func laneAlgorithm(w jobsource.Work) string {
	if w.Algorithm != "" {
		return w.Algorithm
	}
	return validate.IgneumHashAlgorithm
}

// redialGrace is how long a worker may be away before its count window restarts on its return (the project's order of
// 04:2x UK, 14 October 2026): a same address and worker id back within it resumes the window where it stood.
const redialGrace = 120 * time.Second

// pollShareLaneWork issues the jobs for one template (one coordinate's under VectorWork, the chain's one on a
// one-form chain).
func (s *Service) pollShareLaneWork(ctx context.Context, node Node, w jobsource.Work, coord uint8, hasCoord bool) error {
	if !w.HasShareLane() {
		return fmt.Errorf("work package at height %d carries no shareTarget: the node is not the share lane's", w.Height)
	}
	target, err := shareTarget32(w.ShareTarget)
	if err != nil {
		return err
	}
	params, digest := w.ShareParams, w.ShareParamsDigest
	if len(params) == 0 {
		params, digest = w.Params, w.ParamsDigest
	}
	height := w.ShareHeight
	if height == 0 {
		height = w.Height
	}
	if pr, ok := s.validator.(validate.Preparer); ok && len(params) > 0 {
		if err := pr.Prepare(laneAlgorithm(w), params, digest, height); err != nil {
			return fmt.Errorf("prepare the share document: %w", err)
		}
	}
	// a programmed document (versions 9 to 14): the share is computed under the period's anchor, which the node
	// serves (programAnchor at anchorHeight) or the pool resolves by the chain form where the node serves none; the
	// anchor rides the job and the notify. A served anchor is taken as the node serves it (the review lead's ruling of
	// 14:16 UK, 18 October 2026, F-730: the chain's rule of record is what every node runs, and the interface's rule is
	// that a pool takes the period's anchor as the node names it; from 13,800 on the public test network every node
	// resolved coordinate 7's anchor at the start minus 2 while the registry's revealBlocks said 64, and the pool's
	// re-anchoring by the registry's word had every lane-7 share refused "invalid mix digest" for three hours).
	if w.HasAnchor && w.AnchorHeight > 0 {
		s.mu.Lock()
		if s.anchors == nil {
			s.anchors = map[uint64][32]byte{}
		}
		s.anchors[w.AnchorHeight] = w.ProgramAnchor
		s.mu.Unlock()
	}
	if !w.HasAnchor {
		if av, ok := s.validator.(validate.AnchoredValidator); ok {
			if pb := av.PeriodBlocks(digest); pb > 0 && s.cfg.ProgramRevealBlocks > 0 {
				a, at, err := s.periodAnchor(ctx, node, coord, hasCoord, digest, height, pb)
				if err != nil {
					return fmt.Errorf("resolve the period's anchor for height %d: %w", height, err)
				}
				w.ProgramAnchor, w.HasAnchor, w.AnchorHeight = a, true, at
			}
		}
	}
	graph := validate.ProofCarried(laneAlgorithm(w))
	if graph {
		// a proof-carried relation (the graph coordinate; WHOLEWORK.md section 10, the project's same-day order of
		// 13:0x UK, 14 October 2026): its share is the 168-byte cycle ticket found under the member's share
		// header, verified by the pool with the relation's ticket verifier against the share target, and forwarded
		// to the coordinate's node as igneum_submitGraphShare. Its jobs go to GRAPH sessions alone (a worker name
		// ending ".graph", "-graph" or "_graph": an Ember build without the relation never sees them) and the
		// subscribe document stays the igneumhash coordinates' (F-P2: every session paused on a graph document)
		s.mu.Lock()
		named := s.graphNamed
		s.graphNamed = true
		s.mu.Unlock()
		if !named {
			log.Printf("share lane: coordinate %d (%s): a proof-carried relation; its share jobs go to graph sessions (a worker name ending .graph) and its shares ride as tickets", coord, laneAlgorithm(w))
		}
	}
	// the document on every session's first subscribe, before it has a job (Ember mines only on a document it sees);
	// never a proof-carried coordinate's
	if !graph {
		s.Server.SetLaneParams(params, digest)
	}
	members := s.Server.AuthorisedWorkers()
	declared := s.Server.DeclaredRates()
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.shareJobs == nil {
		s.shareJobs = map[string]shareJob{}
	}
	s.sharesPerBlock = w.SharesPerBlock // the carriage bound's first factor, from the template each poll
	if hasCoord {
		// the coordinate's chain state as this template states it (the accounting pages' inputs)
		if s.coordState == nil {
			s.coordState = map[uint8]coordState{}
		}
		st := coordState{lastHeight: w.LastHeight, height: height, at: s.now()}
		if d, ok := new(big.Int).SetString(w.Difficulty, 10); ok {
			st.difficulty = d
		}
		if r, ok := new(big.Int).SetString(w.RewardWei, 10); ok {
			st.rewardWei = r
		}
		s.coordState[coord] = st
	}
	if height != s.lastHeight {
		s.lastHeight, s.lastHeightAt = height, s.now() // the failover guard's clock
	}
	// 0.1.81: the coordinate's block work as this poll states it, for the block-job fence (blockWorkStale): a new
	// header is recorded with its parent; a header standing while the parent moves keeps the previous record
	if last, seen := s.blockWorks[coord]; !seen || last.header != w.Header {
		if s.blockWorks == nil {
			s.blockWorks = map[uint8]blockWork{}
		}
		s.blockWorks[coord] = blockWork{header: w.Header, parent: w.ShareParent, height: height}
	}
	stepMax := uint8(w.ShareStepMax)
	if s.cfg.ShareStepMaxPool > 0 && s.cfg.ShareStepMaxPool < stepMax {
		stepMax = s.cfg.ShareStepMaxPool
	}
	requested := s.raiseRequests()
	served := map[string]bool{}
	defer s.raiseServed(requested, served)
	if s.workerSteps == nil {
		s.workerSteps = map[string]*workerStep{}
	}
	now := s.now()
	s.feeWindows(now)
	s.sweepRefusalsLocked(now) // under the poll's lock, as feeWindows is
	if hasCoord || len(s.cfg.Coordinates) == 0 {
		s.payoutTick(ctx, w.ShareHeight) // the pooled form's payout run, once a poll (a no-op without a pool key)
	}
	// the five-minute rule per worker, once per poll (the worker's rate across its sessions: the member's and
	// the fee session's shares both count, at their steps)
	// the workers present at this poll; a worker absent since the last poll and back now has its count window
	// restarted at its step (its rate measured from the re-dial, never across the absence)
	presentNow := map[string]bool{}
	for addr, workers := range members {
		for worker := range workers {
			presentNow[stepKeyCoord(addr, worker, coord, hasCoord)] = true
		}
	}
	for wk, ws := range s.workerSteps {
		if ws.present && !presentNow[wk] {
			ws.present = false
			ws.absentAt = now
		}
	}
	for addr, workers := range members {
		// the chain's cap and the sealer's hold are per ADDRESS: an address with several workers shares the quarter
		// among them (the PCs' evening of 13 October 2026: four workers on one address, each at a quarter of 64 a
		// height, the address at the sealer's hold of 256 unpaid and seven of ten shares refused before acceptance)
		quarter := s.cfg.ShareCapQuarter
		if n := len(workers); n > 1 {
			quarter = quarter / float64(n)
			if quarter < 4 {
				quarter = 4
			}
		}
		for worker := range workers {
			wk := stepKeyCoord(addr, worker, coord, hasCoord)
			ws := s.workerSteps[wk]
			if ws == nil {
				ws = &workerStep{countFrom: now, stepAt: now, present: true, presentSince: now}
				// a session that declared its hash rate starts at the step that rate chooses (the share target
				// over 2^step keeping its count a height under the quarter), not at 0: a fresh two-4090 session
				// held back 99 percent of its results for the minutes of the climb (the product team, 17:1x UK, 14 October
				// 2026); the rate is read as step-0 shares a second from the hashes against the node's target
				if rate := declared[addr][worker]; rate > 0 && stepMax > 0 {
					perSec := rate * targetFraction(target)
					if st := ChooseStep(perSec, s.cfg.BlockSeconds, quarter, stepMax); st > 0 {
						ws.ruleStep, ws.firstRead = st, true
						log.Printf("share lane: worker %q declared %.0f hashes a second (%.1f step-0 shares a second against the node's target): starting at step %d", wk, rate, perSec, st)
					}
				}
				s.workerSteps[wk] = ws
				continue
			}
			if !ws.present {
				ws.present = true
				if !ws.absentAt.IsZero() && now.Sub(ws.absentAt) <= redialGrace {
					// a same-worker re-dial within the grace (Ember's hourly dev-fee session and its 30 + 60 s backoff
					// after a roll): the window resumes where it stood, the absence taken out of it
					ws.countFrom = ws.countFrom.Add(now.Sub(ws.absentAt))
					ws.stepAt = ws.stepAt.Add(now.Sub(ws.absentAt))
					continue
				}
				// F-392 (17 October 2026): a session re-dialling after the grace and declaring its hash rate restarts at
				// the step that rate chooses, as a fresh session does (the engine's step re-dial: a raised step is never
				// carried into a session that declares a rate under it)
				restart := restartStep(ws.ruleStep, declared[addr][worker]*targetFraction(target), s.cfg.BlockSeconds, quarter, stepMax)
				if ws.firstRead && !ws.absentAt.IsZero() {
					log.Printf("share lane: worker %q re-dialled after %.0f s: its count window restarts at step %d (was %d; the absence is not read as a rate)", wk, now.Sub(ws.absentAt).Seconds(), restart, ws.ruleStep)
				}
				if restart != ws.ruleStep {
					ws.ruleStep = restart
					s.saveWorkerSteps()
				}
				ws.countFrom, ws.stepAt, ws.accepted = now, now, 0
				continue
			}
			// the silence decay (F-392): before the window's read, so a worker the read never reaches still comes down
			if ws.ruleStep > 0 {
				floor := uint8(0)
				if rate := declared[addr][worker]; rate > 0 && stepMax > 0 {
					floor = ChooseStep(rate*targetFraction(target), s.cfg.BlockSeconds, quarter, stepMax)
				}
				s.silenceDecay(wk, ws, now, floor)
			}
			// the first re-read inside the first height (the block clock), the five-minute clock after; and a FAST
			// re-read every block clock while the worker's count a height at its step runs over the quarter (the
			// E1 study of 20:58 UK, 12 October 2026: a 3060 at step 0 submitted twelve a second and Ember capped
			// the rest, so a five-minute climb left the target too low for an hour; now the step doubles up each
			// height until the count sits under the quarter, then the five-minute clock holds it)
			first := !ws.firstRead && now.Sub(ws.countFrom).Seconds() >= s.cfg.BlockSeconds
			over := false
			if win := now.Sub(ws.countFrom).Seconds(); win >= s.cfg.BlockSeconds && now.Sub(ws.stepAt).Seconds() >= s.cfg.BlockSeconds {
				over = float64(ws.accepted)/float64(uint64(1)<<ws.ruleStep)*s.cfg.BlockSeconds/win > quarter
			}
			if first || over || (now.Sub(ws.stepAt) >= 5*time.Minute && now.Sub(ws.countFrom) >= 30*time.Second) {
				if ws.accepted == 0 {
					// no share in the window from a worker WITH a session (this loop walks the authorised sessions; a
					// worker away is not read at all, so its step holds: economics' 1,748 read of 12 October 2026):
					// the step is above what the card finds in a window (the old public test network (chain 7320) switch of 22:58Z: steps 13 to
					// 21 carried from chain 7319 and held by this rule, the whole fleet shareless for two minutes until
					// the file was reset by hand), so it falls to 0 as a fresh worker's; a fast card is raised
					// mid-height within its first height and the five-minute rule re-reads it; no probe (the 0.1.19
					// soak of 20:17Z: a first read at zero stepped a worker UP against a pace of zero)
					if ws.ruleStep > 0 {
						log.Printf("share lane: worker %q found no share in %.0f s at step %d: step 0 (was %d), the raise and the re-read climb it", wk, now.Sub(ws.countFrom).Seconds(), ws.ruleStep, ws.ruleStep)
						ws.ruleStep = 0
						s.saveWorkerSteps()
					}
					ws.stepAt, ws.countFrom, ws.firstRead = now, now, true
					continue
				}
				rate := float64(ws.accepted) / now.Sub(ws.countFrom).Seconds()
				step := ChooseStep(rate, s.cfg.BlockSeconds, quarter, stepMax)
				// the PROBE (the project's go of 12:2x UK, 12 October 2026, G-34): a card that paces its own submissions
				// shows the same count at any step, so the rule reads its count a height AT the current step: at or
				// above half the quarter it is not target-limited and climbs one step at this re-read; under half it
				// comes down one at the next template; so a paced card climbs until the target bites and holds there,
				// under the cap at every step, and a slow card settles where its hashing settles
				countAtStep := float64(ws.accepted) / float64(uint64(1)<<ws.ruleStep) * s.cfg.BlockSeconds / now.Sub(ws.countFrom).Seconds()
				if countAtStep < 1 {
					// under one share a height at the step: the step is above the worker's hashing (a worker restored
					// at a step it no longer paces: the 0.1.21 soak of 21:10Z, 12 October 2026, a session seeded at
					// step 9 found a share a minute), so the step is what its measured rate chooses (the rate counts
					// step-0-equivalent shares: one share at step 9 is 512 units of evidence) and the fast re-read and
					// the mid-height raise climb it back within block clocks if it is fast; no probe (the 0.1.20 soak
					// of 20:35:51Z: a read of 0.0 a height against a pace of 0 stepped a worker up by 0 >= 0.75 x 0)
					// and no pace measured from it
					if step < ws.ruleStep {
						log.Printf("share lane: worker %q at %.1f step-0 shares a second (%.2f a height at step %d, under one): step %d (was %d) by its rate alone", wk, rate, countAtStep, ws.ruleStep, step, ws.ruleStep)
						ws.ruleStep = step
						s.saveWorkerSteps()
					}
					ws.stepAt, ws.countFrom, ws.accepted, ws.firstRead = now, now, 0, true
					continue
				}
				if countAtStep > ws.pace {
					// the worker's own pace: the highest count a height it has shown at any step (a first read in a
					// reconnect's window measured the Windows card at 2.0 a height after the 0.1.20 roll of 20:47Z,
					// 12 October 2026, and the probe then climbed it one step every read against that pace; a card
					// cannot pace below what it later shows)
					ws.pace = countAtStep
				} else if ws.lastCount > 0 && ws.lastStep != ws.ruleStep && countAtStep >= 0.75*ws.lastCount && countAtStep <= 1.25*ws.lastCount {
					// a STALE pace (the 5090 pod's night of 14 October 2026: 13 a height at every step from 8 down to
					// 2, each read lowering it one step against a pace of 86 recorded under an earlier build, the pay
					// halving every five minutes): the count did not move when the step moved, so the card paces at
					// this count now and the pace is re-based to it, from which the probe climbs again
					old := ws.pace
					ws.pace = countAtStep
					if ws.lastCount > ws.pace {
						ws.pace = ws.lastCount
					}
					log.Printf("share lane: worker %q paces at %.1f a height whatever the step (%.1f at step %d, %.1f at step %d): its pace re-based from %.1f", wk, ws.pace, ws.lastCount, ws.lastStep, countAtStep, ws.ruleStep, old)
				}
				ws.lastCount, ws.lastStep = countAtStep, ws.ruleStep
				// the probe against the worker's OWN pace (economics' form of 19:08 UK, 12 October 2026, the project's
				// default): a raise while the count at the step holds at or above three quarters of the pace (the
				// target has not bitten), a lowering when it falls under half of it; so every paced card climbs to
				// where the target bites whatever its pace, the quarter bounding the count above (ChooseStep)
				probe := ws.ruleStep
				switch {
				case countAtStep >= 0.75*ws.pace && probe < stepMax:
					probe++
				case countAtStep < 0.5*ws.pace && probe > 0:
					probe--
				}
				if step < probe {
					step = probe
				}
				if step != ws.ruleStep {
					log.Printf("share lane: worker %q at %.1f step-0 shares a second (%.1f a height at step %d): step %d (was %d) under the cap's quarter of %.0f a height (%d worker(s) on the address)", wk, rate, countAtStep, ws.ruleStep, step, ws.ruleStep, quarter, len(workers))
				}
				ws.ruleStep, ws.stepAt, ws.accepted, ws.countFrom, ws.firstRead = step, now, 0, now, true
				s.saveWorkerSteps()
			}
		}
	}
	issued := 0
	for addr, workers := range members {
		for worker, extra := range workers {
			if graph != stratum.IsGraphWorker(worker) {
				continue // the graph coordinate's jobs to graph sessions alone, the others' to the rest
			}
			key := jobKeyCoord(addr, worker, coord, hasCoord)
			cur, has := s.shareJobs[key]
			if has && cur.parent == w.ShareParent && cur.height == height && cur.anchor == w.ProgramAnchor && cur.extraNonce == extra {
				// the same template standing: the keepalive (F-164), the same job sent again after KeepaliveSilence
				if s.keepalive(&cur, now) {
					s.shareJobs[key] = cur
				}
				// the same template: the mid-height RAISE when the session's accepted shares at this height reach the
				// quarter (never lowered mid-height; bounded by the node's shareStepMax)
				// ONE raise a height (the window's first minutes, 04:00 UK 12 October 2026: a miner that paces its own
				// submissions keeps the same count at any step, so a raise on every allowance ran the step up by one
				// every half minute and the raised step carried into the next heights); the raised step holds for this
				// height only, the next template returns to the five-minute rule's step
				// or on the operator's request (raise.request beside the disclosure: an install's proof through a
				// mid-height re-issue), one step up, whatever the rule's state
				var raised uint8
				onRequest := requested[strings.ToLower(addr)]
				switch {
				case onRequest && stepMax > 0 && cur.step < stepMax:
					raised = cur.step + 1
				case !s.cfg.ShareStepRaise || stepMax == 0 || cur.sharesAtHeight < cur.raiseAt || cur.step >= stepMax:
					continue
				case !raiseAllowed(cur.raised, now.Sub(cur.heightAt).Seconds(), s.cfg.BlockSeconds):
					// F-271 (16 October 2026): a second raise within the height waits a block clock at the new step, so the
					// rate it reads is the session's at that step and never the backlog of the step before
					continue
				default:
					// the per-session retarget (the project's order of 12:0x UK, 15 October 2026): as many steps at once as the
					// session's rate at this step says, against its own quota a height; again within the height whenever
					// the quota is met at the new step, until it submits at the pool's share rate
					raised = RetargetStep(cur.sharesAtHeight, now.Sub(cur.heightAt).Seconds(), cur.step, stepMax, float64(cur.raiseAt), s.cfg.BlockSeconds)
				}
				if raised <= cur.step {
					continue
				}
				if onRequest {
					served[strings.ToLower(addr)] = true
					log.Printf("share lane: member %s: mid-height re-issue ON REQUEST at height %d to step %d", addr, height, raised)
				}
				h, err := ShareHeader(w.ShareParent, height, addr, extra)
				if err != nil {
					continue
				}
				job := s.shareLaneJob(w, h, addr, worker, height, params, digest, target, raised)
				log.Printf("share lane: member %s worker %q at %d shares at height %d after %.0f s: retarget to step %d (was %d), the quota %d a height", addr, worker, cur.sharesAtHeight, height, now.Sub(cur.heightAt).Seconds(), raised, cur.step, cur.raiseAt)
				cur.step, cur.raised = raised, true
				cur.heightAt, cur.sharesAtHeight = now, 0 // the count starts again at the new step; the raise stands for the height
				cur.jobID, cur.sentAt = job.ID, now
				if ws := s.workerSteps[stepKeyCoord(addr, worker, coord, hasCoord)]; ws != nil && ws.ruleStep < raised {
					ws.ruleStep = raised // the next height starts where the retarget left the session, never lower
				}
				s.shareJobs[key] = cur
				s.Stats.Jobs++
				s.Stats.StepRaises++
				s.Server.SetJob(job)
				continue
			}
			if stratum.IsBlockWorker(worker) {
				// a block session: the template's header at the block target, one job per height per coordinate, on the
				// coordinates block sessions are served on
				if len(s.cfg.BlockCoords) > 0 {
					served := false
					for _, bc := range s.cfg.BlockCoords {
						if hasCoord && bc == coord {
							served = true
						}
					}
					if !served {
						continue
					}
				}
				// 0.1.83 (F-429): a keyed sealer's share-only package carries no block work by design (the block fields
				// zero, the target null): named once a height as what it is, never read as stale work by the fence below
				if w.ShareOnly {
					if s.blockStaleSaid[coord] != height {
						if s.blockStaleSaid == nil {
							s.blockStaleSaid = map[uint8]uint64{}
						}
						s.blockStaleSaid[coord] = height
						log.Printf("share lane: coordinate %d's package at %d is share-only (a keyed sealer under the share lane): no block job on it", coord, height)
					}
					continue
				}
				// 0.1.81 (F-420's pool half): no block job on a sealer's work that is not on the head; the next poll re-reads
				// the sealer, and the fence speaks once a height
				if why := blockWorkStale(w, s.blockWorks[coord], true); why != "" {
					if s.blockStaleSaid[coord] != height {
						if s.blockStaleSaid == nil {
							s.blockStaleSaid = map[uint8]uint64{}
						}
						s.blockStaleSaid[coord] = height
						log.Printf("share lane: the sealer's work on coordinate %d is stale at %d (%s): no block job until it is on the head", coord, height, why)
					}
					continue
				}
				if has && cur.height == height && cur.parent == w.ShareParent && cur.anchor == w.ProgramAnchor && cur.extraNonce == extra {
					if s.keepalive(&cur, now) { // the block session's standing job, the same keepalive
						s.shareJobs[key] = cur
					}
					continue
				}
				if w.ShareOnly || w.Boundary == ([32]byte{}) {
					continue // a share-only package carries no block target
				}
				bj := stratum.Job{
					Height: height, Algorithm: w.Algorithm, AlgorithmVersion: w.AlgorithmVersion, Epoch: w.Epoch,
					Header: w.Header, Boundary: w.Boundary, ShareTarget: w.Boundary, Clean: !w.HasCoord, Parent: w.ShareParent, Member: addr, Worker: worker,
					Params: params, ParamsDigest: digest, Kind: "block",
					Coord: w.Coord, HasCoord: w.HasCoord, Difficulty: w.Difficulty, RewardWei: w.RewardWei, LastHeight: w.LastHeight, CoordName: w.CoordName,
					PaidCoords:    s.paidCoordsLocked(),
					ProgramAnchor: w.ProgramAnchor, HasAnchor: w.HasAnchor, AnchorHeight: w.AnchorHeight,
				}
				if bj.Algorithm == "" {
					bj.Algorithm = laneAlgorithm(w)
				}
				bj.ID = bj.Identity(s.chainID)
				s.shareJobs[key] = shareJob{worker: worker, coord: coord, hasCoord: hasCoord, extraNonce: extra, parent: w.ShareParent, height: height, anchor: w.ProgramAnchor, heightAt: now, jobID: bj.ID, sentAt: now}
				s.Stats.Jobs++
				s.Server.SetJob(bj)
				issued++
				continue
			}
			// the verifiable fee waiver's charge: one member height in a hundred under the pool's own fee address
			// while the member's session is charged (feeacct.go); the job still routes to the member's session
			payee := ""
			if !has || cur.height != height {
				payee = s.feePayee(addr, worker, height, coord, hasCoord)
			}
			headerFor := addr
			if payee != "" {
				headerFor = payee
			}
			if s.cfg.ForwardAs != "" {
				headerFor = s.cfg.ForwardAs // the pooled form: every share under the pool key
			}
			h, err := ShareHeader(w.ShareParent, height, headerFor, extra)
			if err != nil {
				log.Printf("share lane: member %s: %v", addr, err)
				continue
			}
			// the job's step (PA-53's step form; the project's order of 02:36 UK, 12 October 2026): the worker's five-minute
			// rule's step (above), the job's share target the node's over 2^step and an accepted share weighing
			// 2^step; bounded by the node's shareStepMax (0 on a count-form chain)
			if !has {
				cur = shareJob{worker: worker, coord: coord, hasCoord: hasCoord}
			}
			step := s.workerSteps[stepKeyCoord(addr, worker, coord, hasCoord)].ruleStep
			if has && cur.step > step && cur.sharesAtHeight*2 >= cur.raiseAt {
				step = cur.step // a session still at half its quota at the retargeted step keeps it (no drop and flood)
			}
			if stepMax == 0 {
				step = 0
			} else if step > stepMax {
				step = stepMax
			}
			job := s.shareLaneJob(w, h, addr, worker, height, params, digest, target, step)
			job.Payee = payee
			job.ForwardAs = s.cfg.ForwardAs
			cur.extraNonce, cur.parent, cur.height, cur.anchor, cur.step, cur.ruleStep = extra, w.ShareParent, height, w.ProgramAnchor, step, step
			// a new height: the raise's counters start again (the step chosen above stands, never lowered within the height)
			raiseAt := s.cfg.ShareCapQuarter
			if n := len(workers); n > 1 {
				raiseAt = raiseAt / float64(n) // the address's share of the quarter (the cap and the hold are per address)
				if raiseAt < 4 {
					raiseAt = 4
				}
			}
			cur.heightAt, cur.sharesAtHeight, cur.raiseAt, cur.raised = now, 0, uint64(raiseAt), false
			cur.jobID, cur.sentAt = job.ID, now
			s.shareJobs[key] = cur
			s.Stats.Jobs++
			s.Stats.LastJobHeight = height
			s.Server.SetJob(job)
			issued++
		}
	}
	if issued > 0 {
		if hasCoord {
			log.Printf("share lane: %d member job(s) on coordinate %d's template at height %d (parent %x, shares per block %d, difficulty %s)", issued, coord, height, w.ShareParent[:6], w.SharesPerBlock, w.Difficulty)
		} else {
			log.Printf("share lane: %d member job(s) on the template at height %d (parent %x, shares per block %d)", issued, height, w.ShareParent[:6], w.SharesPerBlock)
		}
	}
	return nil
}

// keepalive sends a session's standing job again once KeepaliveSilence has passed since it was last sent (F-164,
// 15 October 2026: on the research chain's lanes 18 to 20 the block gaps ran to 400 s, the pool issued a job only on a
// new template, and Ember 0.2.16's fixed 60 s no-work clock re-dialled at every gap over a minute, twelve re-dials a
// session in forty minutes, each given the standing job again a second later); the SAME job, its identity unchanged,
// so a miner that holds it keeps its search and its clock reads a live pool. True when sent (the caller stores cur).
func (s *Service) keepalive(cur *shareJob, now time.Time) bool {
	if cur.jobID == "" || KeepaliveSilence <= 0 || now.Sub(cur.sentAt) < KeepaliveSilence {
		return false
	}
	cur.sentAt = now
	if n := s.Server.Renotify(cur.jobID); n > 0 {
		s.Stats.Keepalives += uint64(n)
	}
	return true
}

// raiseRequests reads the operator's raise.request file (one member address
// a line) in ControlDir; nil without one. A request stays in the file until
// it is served (the member must hold a job: a request for an address with
// no session waits for its first job rather than being lost, the 04:30Z
// lesson of 12 October 2026); served addresses are removed by raiseServed.
func (s *Service) raiseRequests() map[string]bool {
	if s.cfg.ControlDir == "" {
		return nil
	}
	b, err := os.ReadFile(filepath.Join(s.cfg.ControlDir, "raise.request"))
	if err != nil {
		return nil
	}
	out := map[string]bool{}
	for _, line := range strings.Split(string(b), "\n") {
		if a := strings.ToLower(strings.TrimSpace(line)); common.IsHexAddress(a) {
			out[a] = true
		}
	}
	return out
}

// raiseServed rewrites raise.request without the served addresses (removed
// when none remain).
func (s *Service) raiseServed(requested map[string]bool, served map[string]bool) {
	if len(served) == 0 {
		return
	}
	p := filepath.Join(s.cfg.ControlDir, "raise.request")
	var keep []string
	for a := range requested {
		if !served[a] {
			keep = append(keep, a)
		}
	}
	if len(keep) == 0 {
		_ = os.Remove(p)
		return
	}
	_ = os.WriteFile(p, []byte(strings.Join(keep, "\n")+"\n"), 0o644)
}

// shareLaneJob builds a member's job on its share header at the given step:
// the job's target is the node's share target over 2^step (the step form),
// the step carried on the notify.
func (s *Service) shareLaneJob(w jobsource.Work, h [32]byte, addr, worker string, height uint64, params []byte, digest string, target [32]byte, step uint8) stratum.Job {
	stepped := target
	if step > 0 {
		t := new(big.Int).SetBytes(target[:])
		t.Rsh(t, uint(step))
		stepped = [32]byte{}
		tb := t.Bytes()
		copy(stepped[32-len(tb):], tb)
	}
	// clean is false on a coordinate job (VectorWork, the product team's read of 02:53 UK, 13 October 2026: a session that
	// honoured clean dropped the other coordinates' jobs on every notify and mined the last coordinate alone); a new
	// notify with the same coord replaces that coordinate's job, never another's
	job := stratum.Job{
		Height: height, Algorithm: w.Algorithm, AlgorithmVersion: w.AlgorithmVersion, Epoch: w.Epoch,
		Header: h, Boundary: stepped, ShareTarget: stepped, Clean: !w.HasCoord, Parent: w.ShareParent, Member: addr, Worker: worker, Payouts: w.Payouts,
		Params: params, ParamsDigest: digest, SubsidyWei: s.earnings.SubsidyWei, PriorityFeeMeanWei: s.earnings.PriorityFeeMeanWei,
		ShareStep: step,
		Coord:     w.Coord, HasCoord: w.HasCoord, Difficulty: w.Difficulty, RewardWei: w.RewardWei, LastHeight: w.LastHeight, CoordName: w.CoordName,
		PaidCoords:    s.paidCoordsLocked(),
		LaneWorth:     s.laneWorthLocked(w.Coord, w.HasCoord),
		LaneWorkers:   s.laneWorkersLocked(w.Coord, w.HasCoord),
		ProgramAnchor: w.ProgramAnchor, HasAnchor: w.HasAnchor, AnchorHeight: w.AnchorHeight,
	}
	if job.Algorithm == "" {
		job.Algorithm = laneAlgorithm(w)
	}
	job.ID = job.Identity(s.chainID)
	return job
}

// submitShare forwards a member's share that met the network-wide target
// to the node under the member's address: igneum_submitShare(address,
// extraNonce, nonce, mixHash, height), the node's answer recorded.
func (s *Service) submitShare(ctx context.Context, job stratum.Job, nonce uint64, mix [32]byte) error {
	return s.submitShareTicket(ctx, job, nonce, mix, nil)
}

// submitShareTicket forwards a share, or a proof-carried share with its ticket (the graph coordinate:
// igneum_submitGraphShare [address, extraNonce, nonce, ticket, height, parent, step], the protocol team's form of
// 13:2x UK, 14 October 2026, the ticket hash standing where the mix stands on the list entry).
func (s *Service) submitShareTicket(ctx context.Context, job stratum.Job, nonce uint64, mix [32]byte, ticket []byte) error {
	s.mu.Lock()
	sj, has := s.shareJobs[jobKeyCoord(job.Member, job.Worker, job.Coord, job.HasCoord)]
	s.mu.Unlock()
	if !has {
		return fmt.Errorf("share lane: no share job for member %s worker %q", job.Member, job.Worker)
	}
	// the sixth parameter, the parent the share was mined on (share gossip, main 39d13865): a share whose parent is
	// not the node's canonical block at its height is refused as "stale parent" before any work check
	method := "igneum_submitShare"
	third := "0x" + hex.EncodeToString(mix[:])
	if len(ticket) > 0 {
		method, third = "igneum_submitGraphShare", "0x"+hex.EncodeToString(ticket)
	}
	params := []interface{}{common.HexToAddress(jobAccount(job)).Hex(), fmt.Sprintf("0x%x", sj.extraNonce), fmt.Sprintf("0x%x", nonce), third, fmt.Sprintf("0x%x", job.Height), "0x" + hex.EncodeToString(job.Parent[:])}
	if job.ShareStep > 0 {
		// PA-53's step form: the seventh parameter declares the step the share was mined at (the share target
		// over 2^step), weighed 2^step by the node; absent on a step-0 share so a count-form node reads it as before
		params = append(params, fmt.Sprintf("0x%x", job.ShareStep))
	}
	// VectorWork: the coordinate is NOT a parameter of igneum_submitShare (vectorwork1's sealers take at most seven,
	// the box soak of 02:07Z, 13 October 2026: 662 shares refused "too many arguments"); a coordinate's share goes to
	// the sealers that seal that coordinate (coordTargets), which know their own
	// the fan-out (the finding of 11 October 2026: a share pays only in the blocks of the sealer whose pool holds it):
	// the share goes to every sealer named, the answers counted per node, the share accepted when any accepts it
	s.mu.Lock()
	targets := append([]shareNode(nil), s.shareNodes...)
	s.mu.Unlock()
	if job.HasCoord {
		// VectorWork: a coordinate's share goes to the sealers that seal that coordinate (vectorwork1: one each,
		// which refuses another coordinate's share by name)
		if ct := s.coordTargets(job.Coord); len(ct) > 0 {
			targets = ct
		}
	}
	if len(targets) == 0 {
		url := s.cfg.RPCURL
		if url == "" {
			url = "the pool's node"
		}
		targets = []shareNode{{url: url, node: s.node}}
	}
	acceptedBy, refusedBy := 0, 0
	var lastReason string
	// the sealers asked AT ONCE, not one after another (F-50's forward lag of 12 October 2026: under the security review's
	// flood the serial asks behind sixteen workers put the forward three to six minutes after the receipt, median
	// 199 s, and 5,509 shares reached the sealers with a stale parent); each sealer's answer then goes on the
	// journal and the counters in the sealers' order
	type answer struct {
		Accepted bool   `json:"accepted"`
		Reason   string `json:"reason"`
	}
	answers := make([]answer, len(targets))
	errs := make([]error, len(targets))
	asked := make([]bool, len(targets))
	if s.cfg.ShareForwardOne {
		// one sealer at a time, in order, stopping at the first acceptance (a chain with share gossip carries the
		// share to the others; the second and third copies would read "carried twice")
		for i := range targets {
			asked[i] = true
			errs[i] = targets[i].node.Call(ctx, method, params, &answers[i])
			if errs[i] == nil && answers[i].Accepted {
				break
			}
			if errs[i] == nil && strings.Contains(answers[i].Reason, "carried twice") {
				// the gossip delivered it already (the protocol team's word of 14:08 UK, 13 October 2026): held and paid
				// once, not a failure to fall through further
				answers[i].Accepted = true
				answers[i].Reason = "held by the sealer already (gossip): " + answers[i].Reason
				break
			}
		}
	} else {
		var wg sync.WaitGroup
		for i := range targets {
			asked[i] = true
			wg.Add(1)
			go func(i int) {
				defer wg.Done()
				errs[i] = targets[i].node.Call(ctx, method, params, &answers[i])
			}(i)
		}
		wg.Wait()
	}
	s.mu.Lock()
	seq := s.seqOf[strings.ToLower(jobAccount(job))+"/"+job.ID+"/"+fmt.Sprint(nonce)]
	s.mu.Unlock()
	for i := range targets {
		if !asked[i] {
			continue
		}
		ans, err := answers[i], errs[i]
		s.mu.Lock()
		if i < len(s.shareNodes) {
			switch {
			case err != nil:
				s.shareNodes[i].refused++
				s.shareNodes[i].lastErr = err.Error()
			case !ans.Accepted:
				s.shareNodes[i].refused++
				s.shareNodes[i].lastErr = ans.Reason
			default:
				s.shareNodes[i].accepted++
				s.noteLaneAccepted(job.Coord, job.HasCoord, job.Member, job.Worker)
			}
		}
		s.mu.Unlock()
		// the node's answer on the journal beside the pool's acceptance (the settlement team's ask): the sealer, the
		// share's chain identity and the answer by name, one line per node asked
		if s.journal != nil {
			fl := shares.Line{Seq: seq, Account: strings.ToLower(jobAccount(job)), JobID: job.ID, Height: job.Height, Nonce: nonce, Mix: hex.EncodeToString(mix[:]),
				Sealer: targets[i].url, ExtraNonce: sj.extraNonce, Parent: hex.EncodeToString(job.Parent[:]), NodeOK: err == nil && ans.Accepted, Step: job.ShareStep, Coord: job.Coord, HasCoord: job.HasCoord}
			switch {
			case err != nil:
				fl.NodeReason = "rpc: " + err.Error()
			case !ans.Accepted:
				fl.NodeReason = ans.Reason
			}
			if jerr := s.journal.Forwarded(fl); jerr != nil {
				log.Printf("share lane: journal: %v", jerr)
			}
		}
		if err != nil {
			refusedBy++
			lastReason = err.Error()
			continue
		}
		if !ans.Accepted {
			refusedBy++
			lastReason = ans.Reason
			continue
		}
		acceptedBy++
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if acceptedBy > 0 {
		// the session's measured rate for the step rule: an accepted share at step s counts 2^s step-0 shares
		if ws := s.workerSteps[stepKeyCoord(job.Member, job.Worker, job.Coord, job.HasCoord)]; ws != nil {
			ws.accepted += uint64(1) << job.ShareStep
			ws.lastAcceptedAt = s.now() // the silence decay's clock (F-392): a verified share, accepted
		}
		jk := jobKeyCoord(job.Member, job.Worker, job.Coord, job.HasCoord)
		if cur, ok := s.shareJobs[jk]; ok {
			if countsForRaise(job.ShareStep, cur.step, job.Height, cur.height) {
				cur.sharesAtHeight++ // by count, as the node's per-address cap counts; at the current step alone (0.1.74)
			}
			s.shareJobs[jk] = cur
		}
	}
	s.Stats.SharesForwarded++
	if acceptedBy == 0 {
		s.Stats.SharesRefused++
		log.Printf("share lane: member %s's share at height %d refused by every sealer (%d): %s", job.Member, job.Height, refusedBy, lastReason)
		return fmt.Errorf("refused: %s", lastReason)
	}
	s.Stats.SharesAccepted++
	asked_ := 0
	for _, a := range asked {
		if a {
			asked_++
		}
	}
	log.Printf("share lane: member %s's share at height %d ACCEPTED by %d of %d sealer(s) asked (nonce %d)", job.Member, job.Height, acceptedBy, asked_, nonce)
	return nil
}

var _ = jobsource.Work{}
