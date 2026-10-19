// Copyright 2026 The Igneum Authors
// This file is part of the go-ethereum library (Igneum fork).
//
// The go-ethereum library is free software: you can redistribute it and/or modify
// it under the terms of the GNU Lesser General Public License as published by
// the Free Software Foundation, either version 3 of the License, or
// (at your option) any later version.
//
// The go-ethereum library is distributed in the hope that it will be useful,
// but WITHOUT ANY WARRANTY; without even the implied warranty of
// MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE. See the
// GNU Lesser General Public License for more details.
//
// You should have received a copy of the GNU Lesser General Public License
// along with the go-ethereum library. If not, see <http://www.gnu.org/licenses/>.

package params

import (
	"math/big"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/params/types/coregeth"
	"github.com/ethereum/go-ethereum/params/types/ctypes"
	"github.com/ethereum/go-ethereum/params/vars"
)

// Igneum chain identity (docs/protocol-a/DESIGN.md section 6; PA-01).
//
// IgneumDevChainConfig is the development chain: the Igneum EVM-1 rule set,
// every supported rule active from block 0, the "null" stand-in algorithm
// (D-A10). It is a different genesis from the launch genesis by construction:
// nothing mined on it converts (the plan, page 19). The launch configuration is
// added when the founder has decided D-A1 to D-A6 and ratified the Phase Zero
// selection; it names the ratified algorithm and nothing else.
var (
	// IgneumDevChainID is the chain and network id of the development chain
	// (D-A1 default 7310, verified free in the public registry on 9 October 2026).
	IgneumDevChainID = big.NewInt(7310)

	// IgneumChainConfig is the launch chain's configuration placeholder: the
	// same rules, chain id and parameters as the development chain, with no
	// algorithm selected (Phase Zero) and the launch values still the founder's
	// (D-A1 to D-A6, P-I03, P-I04). A node refuses to run on it until then.
	IgneumChainConfig = func() *coregeth.CoreGethChainConfig {
		c := *IgneumDevChainConfig
		engine := *IgneumDevChainConfig.Igneum
		engine.Algorithm = ""
		// The launch genesis carries the one-billion form from block 0
		// (IgneumIssuance1B; the founder's order of 18 October 2026); the
		// fund's address comes with the launch genesis.
		engine.Issuance = withFund(IgneumIssuance1B, common.Address{})
		c.Igneum = &engine
		return &c
	}()

	// IgneumLaunchIssuance is the re-cut schedule (the project's ruling of 14:0x UK,
	// 11 October 2026; the economics team's constants): M 100,000,000 IGN,
	// epochs of 6,144 blocks, each issuing (M minus the supply) / 2,048
	// floor-divided over the epoch with the remainder in its last block
	// (7.947286 IGN a block at the opening), the tail the calendar quantity
	// 525,600 IGN a year at 20 s in integer form: 2,048 IGN an epoch (one
	// third of an IGN a block on average, the remainder 2,048 wei in the
	// epoch's last block; the project's correction of 14:2x UK), the fund one
	// percent. A chain carries it as its CONFIG DEFAULT keyed by chain id
	// (IgneumIssuanceForChainID), never as a flag: a launch-equivalent
	// chain carries its rules in its config.
	IgneumLaunchIssuance = &ctypes.IgneumIssuance{
		Epoch:        6144,
		EpochDiv:     2048,
		M:            new(big.Int).Mul(big.NewInt(100_000_000), big.NewInt(1e18)),
		Tail:         new(big.Int),
		TailPerEpoch: new(big.Int).Mul(big.NewInt(2048), big.NewInt(1e18)),
		FundBps:      100,
	}

	// IgneumIssuance12s is the launch schedule re-cut for the 12-second
	// target (the founder's "re cut" through main, 16 October 2026; the
	// economics seat's ISSUANCE-12S-2026-10-16.md, tools/issuance/recut12.py):
	// the same calendar epoch of 122,880 s as 10,240 blocks at 12 s in place
	// of 6,144 at 20, the same epoch rule (M minus the supply over 2,048),
	// the same tail an epoch (2,048 IGN: 525,600 IGN a year, a fifth of an
	// IGN a block with no remainder) and the same fund share; the subsidy
	// a block 12/20 of the 20-second one (4.768372 IGN at the opening, no
	// remainder: 48,828.125 IGN divides by 10,240). A chain takes it from a
	// height gate as the Next form of its schedule, the gross issued at
	// the gate carried whole (IgneumIssuance.NextFrom; issuance.Check).
	IgneumIssuance12s = &ctypes.IgneumIssuance{
		Epoch:        10240,
		EpochDiv:     2048,
		M:            new(big.Int).Mul(big.NewInt(100_000_000), big.NewInt(1e18)),
		Tail:         new(big.Int),
		TailPerEpoch: new(big.Int).Mul(big.NewInt(2048), big.NewInt(1e18)),
		FundBps:      100,
	}

	// IgneumIssuance1B is the 12-second schedule at the ONE BILLION supply
	// (the founder's order of 18 October 2026, 14:3x UK, "execute now";
	// docs/tokenomics/SUPPLY-2026-10-18.md): the SHAPE of IgneumIssuance12s
	// unchanged (the epoch 10,240 blocks of 122,880 s, the epoch's issuance
	// (M minus the supply) over 2,048 with the remainder in the epoch's
	// last block, the fund one percent, genesis issuing nothing, every
	// ratio, height and percentage as before) and every AMOUNT ten times:
	// M 1,000,000,000 IGN in place of 100,000,000; the tail 20,480 IGN an
	// epoch in place of 2,048 (5,256,000 IGN a year in place of 525,600;
	// two IGN a block, no remainder); the opening subsidy 47.683716 IGN a
	// block in place of 4.768372 (10^27 / 20,971,520 wei, no remainder:
	// 488,281.25 IGN divides by 10,240), the fund's part of it 0.476837
	// IGN. The launch (mainnet) genesis carries it from block 0
	// (IgneumChainConfig's engine block; the fund's address comes with the
	// launch genesis). The public test network and the research chain take
	// it as the LAST form of their schedules from the gates
	// IgneumPublic1BFrom and IgneumResearch1BFrom (withSupply), the gross
	// issued at the landing check carried whole, and serve the old figures until
	// then.
	IgneumIssuance1B = &ctypes.IgneumIssuance{
		Epoch:        10240,
		EpochDiv:     2048,
		M:            new(big.Int).Mul(big.NewInt(1_000_000_000), big.NewInt(1e18)),
		Tail:         new(big.Int),
		TailPerEpoch: new(big.Int).Mul(big.NewInt(20480), big.NewInt(1e18)),
		FundBps:      100,
	}

	// IgneumDevIssuance is the development schedule every chain id not
	// listed in IgneumChainIssuance carries (IgneumDevChainConfig's block:
	// the shift form, epochs of 8,192 blocks, (M minus the supply) / 2^24
	// a block, the tail a quarter of an IGN a block, the fund one percent
	// to the development chain's visible stand-in).
	IgneumDevIssuance = &ctypes.IgneumIssuance{
		Epoch:       8192,
		M:           new(big.Int).Mul(big.NewInt(100_000_000), big.NewInt(1e18)),
		K:           24,
		Tail:        big.NewInt(250_000_000_000_000_000),
		FundBps:     100,
		FundAddress: common.HexToAddress("0x0000000000000000000000000000000000007310"),
	}

	// IgneumResearch12sFrom is the research chain's (7358) gate onto the
	// 12-second re-cut: the last height under its development schedule
	// (the shift form, so any height is a whole gate); the block above it
	// is the first under IgneumIssuance12s, the gross issued at the landing check
	// carried whole. the project's rule for the gate (12:2x UK, 16 October 2026):
	// set at cut time as the first multiple of 2,048 at least three hours
	// past the minute every node of record is expected on the version,
	// from the chain's measured block time of the last hour (12 to 16 s on
	// the 36-second lanes), the arithmetic named in the cut ask; by the
	// day's numbers 12,288 (about 02:00 to 04:00 UK on 17 October), 14,336
	// if the ladder slips. F-480 (17 October 2026): 12,288 passed under the
	// shift form on every node of record, since a persisted datadir's
	// stored configuration never took the next form until the issuance
	// rode the start's overrides; the gate moved to 16,384 (the epoch
	// boundary at least 1,200 blocks ahead of the head at the landing;
	// 24,576 the next), the review lead's ruling of 08:5x UK.
	IgneumResearch12sFrom uint64 = 16384

	// IgneumThrowaway12sFrom is the bonded box chain's (7398, "the
	// throwaway") gate onto the 12-second re-cut: the proof chain crosses
	// it inside its first hour (the two-replay comparison against
	// igneum_blockIssuance across the gate and the fresh walk from genesis).
	IgneumThrowaway12sFrom uint64 = 200

	// IgneumPublic12sFrom is the public test network's (7322) gate onto the
	// 12-second re-cut of its launch schedule: the first multiple of 2,048
	// at least three hours past the minute every node of record is on the
	// version, from the 36-second succession's activation 2,860 (16 October
	// 2026, the landings 2,761 and 2,774) at 12 s a block: 4,096 falls at
	// about 23:30 UK that day, before the nodes can be on the version;
	// 6,144 at about 06:20 UK on 17 October (the review lead's figure of record
	// in the cut ask); the fund's address and share unchanged across it.
	// F-480: 6,144 passed under the launch form on every node of record
	// (the stored configuration's issuance, never refreshed); the gate
	// moved to 12,288, the second 6,144-block boundary (economics: 12,289
	// pays 4,763,716,106,026,549,823 wei, the fund 47,637,161,060,265,498),
	// the review lead's ruling of 08:5x UK; the throwaway chain's 200 stands
	// (every box chain runs from a fresh genesis).
	IgneumPublic12sFrom uint64 = 12288

	// IgneumResearch1BFrom and IgneumPublic1BFrom are the research chain's
	// (7358) and the public test network's (7322) gates onto the
	// one-billion form (IgneumIssuance1B; the founder's order of 18 October
	// 2026): the last height under the 12-second form at the old figures,
	// the block above it the first at the new, the gross issued at the landing check
	// carried whole. ZERO is UNSET: no form is appended (withSupply) and the
	// chain serves the old figures for ever. the review lead's heights (the first
	// node cut after the F-730 fix, 18 October 2026): each the first epoch
	// boundary of the 12-second form counted from the chain's own gate
	// (12,288 + 10,240 on the public test network; 16,384 + 10,240 on the
	// research chain), since issuance.Check refuses a gate inside an epoch
	// of the re-cut form (its remainder would be lost). Block 22,529 on the
	// public test network and 26,625 on the research chain are the first at
	// the new figures, 47,676,734,311,052,904,047 wei each (the gross
	// carried 146,412.86 IGN; supply1b_test.go). The throwaway chain (7398)
	// keeps the old figures: every box chain runs from a fresh genesis.
	IgneumResearch1BFrom uint64 = 26624
	IgneumPublic1BFrom   uint64 = 22528

	// IgneumLab1BFrom is the one-billion lab chain's (7397) gate onto the
	// one-billion form (docs/node/SUPPLY-1B-LAB-2026-10-18.md): the lab
	// chain runs the 12-second form from genesis and the one-billion form
	// from the landing check, both at IgneumLabEpoch blocks an epoch (the smallest
	// epoch that keeps the forms' arithmetic whole, 1e26 / 2,048 / 32 wei a
	// block with no remainder: the opening subsidy 1,525.87890625 IGN at the
	// old figures, 15,258.7890625 at the new), so the gate is the second
	// epoch boundary and the row reads the issuance to the wei at 63, 64,
	// 65 and across the first new epoch's last block, 96. A throwaway chain
	// from a fresh genesis; nothing of record runs on it.
	IgneumLab1BFrom uint64 = 64
	IgneumLabEpoch  uint64 = 32

	// IgneumPublicClockFrom is the public test network's (7322) gate onto
	// the re-pinned round clock (ClockDelta 13 s, 0.10.49): set at the cut
	// as the first height at least three hours past the minute every node
	// of record is expected on the version (the three rolls 0.10.47,
	// 0.10.48, 0.10.49 ending about 23:00 UK on 16 October 2026) from the
	// chain's measured pace (about 17 s a block after the succession's
	// transient): 4,400 (about 02:50 UK on 17 October at 17 s; later if
	// slower); the arithmetic in the cut ask.
	IgneumPublicClockFrom uint64 = 4400

	// IgneumChainIssuance is the issuance each chain id carries as its
	// config default; a chain id not listed keeps the development
	// schedule. The seventh development chain (7317) runs the re-cut
	// schedule with the development fund stand-in; the launch id is added
	// with the launch genesis.
	// IgneumLaunchRPCLimits are the public-RPC limits of a launch-equivalent
	// chain (SECURITY-GAPS.md 8c6289fd; the project's order of 21:4x UK, 11
	// October 2026): the per-source token bucket at the edge's 20 calls a
	// second with a burst of 40, and the share lane's per-address cap of 64
	// shares a block (a quarter of the 256 ceiling: four addresses at the
	// cap fill a block, one cannot; the project's "expected rate times 8" has no
	// per-address expected rate to anchor on, so the ceiling's quarter is
	// the anchor, stated). the protocol team's node code reads them through
	// IgneumRPCLimitsForChainID; a chain id without an entry runs with the
	// limits off.
	// ShareCapPerAddress is the lane's ceiling, 256 (the project's rule of 17:0x
	// UK, 12 October 2026: at 64, forty percent of the 160 target, the cap
	// refused an honest single address above its share while a splitter
	// escaped it; the 20 a second token bucket stays the flood guard).
	IgneumLaunchRPCLimits = &ctypes.IgneumRPCLimits{RPCRate: 20, RPCBurst: 40, ShareCapPerAddress: 256}

	// IgneumChainRPCLimits is the limits each chain id carries as its config
	// default; the seventh development chain runs the launch limits.
	// The eighth development chain (7318; the project's order of 19:3x UK, 11
	// October 2026: the genesis of 23:30 UK on three independent builds,
	// E 1, T_min 12, shareRecency 64, the share gossip, PA-47, PA-48,
	// PA-50) runs the same limits and the re-cut schedule with its own
	// fund stand-in.
	// The ninth (7319; the outside review's third reading of 20:15 UK, 11 October
	// 2026, through main: C14 and C17 at the launch clock close only at
	// the launch constants with GPU proving, so devnet9 runs tMin 12,
	// tPiMs 250 and the launch dMin on a GPU-proving pair, with PA-53 and
	// the rank-0 fallback from genesis) runs the same.
	IgneumChainRPCLimits = map[uint64]*ctypes.IgneumRPCLimits{
		7317: IgneumLaunchRPCLimits,
		7318: IgneumLaunchRPCLimits,
		7319: IgneumLaunchRPCLimits,
		7320: IgneumLaunchRPCLimits,
		7321: IgneumLaunchRPCLimits,
		7322: IgneumLaunchRPCLimits,
	}

	IgneumChainIssuance = map[uint64]*ctypes.IgneumIssuance{
		7317: withFund(IgneumLaunchIssuance, common.HexToAddress("0x0000000000000000000000000000000000007317")),
		7318: withFund(IgneumLaunchIssuance, common.HexToAddress("0x0000000000000000000000000000000000007318")),
		7319: withFund(IgneumLaunchIssuance, common.HexToAddress("0x0000000000000000000000000000000000007319")),
		// The tenth (7320; the founder's order of 17:3x UK, 12 October 2026,
		// through main: the earliest frozen form, genesis tonight): the
		// launch constants with every registered rule on from genesis.
		7320: withFund(IgneumLaunchIssuance, common.HexToAddress("0x0000000000000000000000000000000000007320")),
		// The eleventh (7321; the founder's go of 15:2x UK, 13 October 2026):
		// VectorWork revision 15 on vectorwork6's form, version 15 as the
		// bandwidth coordinate with the governing share.
		7321: withFund(IgneumLaunchIssuance, common.HexToAddress("0x0000000000000000000000000000000000007321")),
		// The twelfth (7322; the project's path (a) of 15:0x UK, 15 October 2026): the
		// public test network on the vector form from genesis, the chain-held
		// registration under the 64-block notice, the bounded tail from genesis.
		// The one-billion form last, from IgneumPublic1BFrom (zero: unset,
		// the old figures for ever; the founder's order of 18 October 2026).
		7322: withSupply(withNext(withFund(IgneumLaunchIssuance, common.HexToAddress("0x0000000000000000000000000000000000007322")), IgneumPublic12sFrom, IgneumIssuance12s), IgneumPublic1BFrom, IgneumIssuance1B),
		// The research chain (7358) and the bonded box chain (7398): the
		// development schedule they ran from genesis, then the 12-second
		// re-cut from their gates (the founder's "re cut", 16 October
		// 2026): the fund's address and share unchanged across the gate.
		// The research chain's one-billion form last, from
		// IgneumResearch1BFrom (zero: unset, the old figures for ever).
		7358: withSupply(withNext(IgneumDevIssuance, IgneumResearch12sFrom, IgneumIssuance12s), IgneumResearch1BFrom, IgneumIssuance1B),
		// The one-billion lab chain (7397; docs/node/SUPPLY-1B-LAB-2026-10-18.md):
		// the 12-second form from genesis and the one-billion form from
		// IgneumLab1BFrom, both at the lab's short epoch, the development
		// chain's fund stand-in.
		7397: withSupply(withEpoch(withFund(IgneumIssuance12s, common.HexToAddress("0x0000000000000000000000000000000000007310")), IgneumLabEpoch), IgneumLab1BFrom, withEpoch(IgneumIssuance1B, IgneumLabEpoch)),
		7398: withNext(IgneumDevIssuance, IgneumThrowaway12sFrom, IgneumIssuance12s),
	}

	// IgneumDevChainConfig is the chain parameters of the Igneum development chain.
	IgneumDevChainConfig = &coregeth.CoreGethChainConfig{
		NetworkID:                 IgneumDevChainID.Uint64(),
		ChainID:                   IgneumDevChainID,
		SupportedProtocolVersions: vars.DefaultProtocolVersions,
		Igneum: &ctypes.IgneumConfig{
			Algorithm: "null",
			// Protocol engineer B's candidates (docs/protocol-b/DESIGN.md) with the
			// development floor; the launch values are the founder's (P-I03, P-I04).
			Difficulty: &ctypes.IgneumDifficulty{Target: 15, HalfLife: 3600, DtMax: 3600, Min: big.NewInt(1 << 16)},
			Issuance: &ctypes.IgneumIssuance{
				Epoch:       8192,
				M:           new(big.Int).Mul(big.NewInt(100_000_000), big.NewInt(1e18)),
				K:           24,
				Tail:        big.NewInt(250_000_000_000_000_000),
				FundBps:     100,
				FundAddress: common.HexToAddress("0x0000000000000000000000000000000000007310"), // the dev chain's visible stand-in
			},
			Fees: &ctypes.IgneumFees{ElasticityMultiplier: 2, BaseFeeChangeDenominator: 8, InitialBaseFee: big.NewInt(1_000_000_000), MinBaseFee: big.NewInt(1_000_000_000)},
		},

		// Igneum EVM-1 (DESIGN.md 6.3): one fork, genesis.

		// Homestead
		EIP2FBlock: big.NewInt(0),
		EIP7FBlock: big.NewInt(0),

		// Tangerine Whistle
		EIP150Block: big.NewInt(0),

		// Spurious Dragon
		EIP155Block:  big.NewInt(0),
		EIP160FBlock: big.NewInt(0),
		EIP161FBlock: big.NewInt(0),
		EIP170FBlock: big.NewInt(0),

		// Byzantium (EIP-100 excluded: the difficulty rule is the engine's, PB-01)
		EIP140FBlock: big.NewInt(0),
		EIP198FBlock: big.NewInt(0),
		EIP211FBlock: big.NewInt(0),
		EIP212FBlock: big.NewInt(0),
		EIP213FBlock: big.NewInt(0),
		EIP214FBlock: big.NewInt(0),
		EIP658FBlock: big.NewInt(0),

		// Constantinople
		EIP145FBlock:  big.NewInt(0),
		EIP1014FBlock: big.NewInt(0),
		EIP1052FBlock: big.NewInt(0),

		// Istanbul (EIP-2200 is the net SSTORE rule; EIP-1283 never active)
		EIP152FBlock:  big.NewInt(0),
		EIP1108FBlock: big.NewInt(0),
		EIP1344FBlock: big.NewInt(0),
		EIP1884FBlock: big.NewInt(0),
		EIP2028FBlock: big.NewInt(0),
		EIP2200FBlock: big.NewInt(0),

		// Berlin
		EIP2565FBlock: big.NewInt(0),
		EIP2718FBlock: big.NewInt(0),
		EIP2929FBlock: big.NewInt(0),
		EIP2930FBlock: big.NewInt(0),

		// London (EIP-1559 fee market from block 0; parameters are PB-03)
		EIP1559FBlock: big.NewInt(0),
		EIP3198FBlock: big.NewInt(0),
		EIP3529FBlock: big.NewInt(0),
		EIP3541FBlock: big.NewInt(0),

		// Shanghai opcode EIPs (no withdrawals, EIP-4895 absent)
		EIP3651FBlock: big.NewInt(0),
		EIP3855FBlock: big.NewInt(0),
		EIP3860FBlock: big.NewInt(0),

		// Cancun opcode EIPs (no blobs 4844/7516, no beacon root 4788)
		EIP1153FBlock: big.NewInt(0),
		EIP5656FBlock: big.NewInt(0),
		EIP6780FBlock: big.NewInt(0),

		// Not supported, absent: EIP-4399, 4788, 4844, 4895, 7516, 2537, 2315,
		// the DAO fork, the difficulty bomb and its delays, ECIP-1010/1017/1099,
		// Verkle, the merge (terminal total difficulty).
		DisposalBlock:      big.NewInt(0), // the ethash bomb never applies; disposed for any code that asks
		RequireBlockHashes: map[uint64]common.Hash{},
	}
)

// withNext is a copy of an issuance (its fund address kept) with the next
// form from the gate height: the next form's own fund fields are not read
// (the fund is one across the gate, consensus/igneum.issuanceParams).
func withNext(i *ctypes.IgneumIssuance, from uint64, next *ctypes.IgneumIssuance) *ctypes.IgneumIssuance {
	c := withFund(i, i.FundAddress)
	c.NextFrom = from
	c.Next = withFund(next, common.Address{})
	return c
}

// withEpoch is a copy of an issuance with its epoch length set (the lab
// chain's short epochs; the fund address kept).
func withEpoch(i *ctypes.IgneumIssuance, epoch uint64) *ctypes.IgneumIssuance {
	c := withFund(i, i.FundAddress)
	c.Epoch = epoch
	return c
}

// withFund is a copy of an issuance with its fund address set.
func withFund(i *ctypes.IgneumIssuance, fund common.Address) *ctypes.IgneumIssuance {
	c := *i
	c.M = new(big.Int).Set(i.M)
	c.Tail = new(big.Int).Set(i.Tail)
	if i.TailPerEpoch != nil {
		c.TailPerEpoch = new(big.Int).Set(i.TailPerEpoch)
	}
	c.FundAddress = fund
	return &c
}

// IgneumChainRuleForks are the heights at which a rule's form changed on a
// running chain (ctypes.IgneumRuleForks; the project's standing rule of 20:1x UK,
// 11 October 2026). The seventh development chain: PA-48's seal
// measurement swapped in place at 17:36 UK at height 351 (devnet7's
// record), so a fresh node refused block 13 (a seal step over a round-2
// bonded parent under the first form) until this gate; the eighth runs
// the second form from genesis and needs no entry.
// PA-48a's rank slot was swapped in place at 20:18 UK at height 681
// (the first block whose Finalize wrote the clock's fifth slot on the
// live nodes, read off the archive peer's storage: 680 reads 0, 681
// reads 2), and a fresh node under it refused block 10 on the state
// root; the second gate.
var IgneumChainRuleForks = map[uint64]*ctypes.IgneumRuleForks{
	// The third gate is a PLANNED height (the standing rule's better form):
	// the sealer's payout term over the summed weight (the node team's
	// dd652a75) is swapped onto devnet7 after devnet8's genesis, near
	// height 1,200, and every node runs the count-form term to 1,299 and
	// the fix from 1,300 whenever it restarted; a fresh node reproduces
	// both. If the swap slips past 1,300 the entry moves before it, never
	// after.
	// The fourth, ClockDarkV1, the same planned height: PA-48b's dark count
	// (cadce178) writes a sixth clock slot at every Finalize and landed
	// without a gate; the pool cell's members on 39dd2dad sealed 821 and
	// the sealers on a790fec5 refused it on the state root (21:45 UK).
	7317: {SealMeasureV1: 351, ClockRankV1: 681, SealerSharesV1: 1300, ClockDarkV1: 1300},
	// The fifth field, ForkLengthV1 (F-44 part 3, the fork choice's length
	// order), is OFF at zero. The ninth chain (genesis 02:00 UK, 12 October
	// 2026, 20 s blocks) names 700: the release team's arithmetic, the
	// landing by 04:00 near height 360, the rollout from about 400, so 700
	// is 300 heights' notice at the latest rollout start and activates
	// about 05:53 UK; the entry lands in the same landing as the swap (the
	// standing rule of 12 October 2026). Parts 1 and 2a of the same
	// release carry no state write and take effect at each node's restart.
	// A chain started on the rule names height 1.
	// PolicyV1 900 on the ninth chain (the project's order of 05:3x UK, 12 October
	// 2026, the second fault release node/0.9.3): the table-share carry
	// quorum, policy parts 4 and 5 and the parent-keyed carried mark from
	// 900, so the activation record at 700 reads part 3 alone; the roll
	// between 760 and 880, the rollback decision at 950.
	// ClockV1 on the ninth chain (the outside review's condition 4, C17 over 1,000
	// heights at a frozen resident-path tPiMs and a live-meter dMin; the project's
	// order of 09:0x UK, 12 October 2026): the re-pin's height, ClockTPiMs
	// 300 (the resident proof's p99 under a sealer's own search, 223 ms on
	// the slowest admitted card, under the design seat's margin rule) and
	// ClockDMin (the slowest sealer's live keyed-search rate by
	// igneum_searchRate over the target, 1,925 ms at tMin 12 and Delta 20)
	// are set by the figures' commit, the last before the release's build;
	// zero here is the genesis document's figures throughout.
	7319: {ForkLengthV1: 700, PolicyV1: 900},
	// The tenth chain (7320): every by-height rule on from genesis (the
	// length order, the policy parts, the clock amendment with its two
	// frozen constants, the Pay rule's second form). ClockTPiMs and
	// ClockDMin are the figures' commit of 12 October 2026: the resident
	// proof's p99 under a 32-thread keyed search on the C17 box chain with
	// the design seat's margin, and the live meter's dMin at the ninth
	// chain's 2,870, the one clock-bound input of the freeze.
	// The two clock constants are the 21:20 UK reading of the condition-4
	// box chain T (c17-load.sh over its first 185 keyed heights, the
	// research cut 86164ffa, a 32-thread keyed search with the resident
	// prover): the proof's p99 3,479.7 ms x 300/223 rounded up to the ten
	// (p50 305.7, p90 344.1; the p99 is two proofs of 3.5 and 5.0 s among
	// 167), and the live meter 60,706.5 H/s x 1.925 s. the project's rule of
	// 18:5x UK, 12 October 2026.
	// DiffMinV1 1200 (the security review's F-86, 13 October 2026, 01:0x UK): the
	// chain difficulty's floor raised from the genesis set's 8,192 to
	// 6,000,000 from height 1,200, the three sealers' measured search rate
	// (about 360,000 H/s at their thread counts) times the 20 s target
	// less a margin, so the open seat's seal takes the interval rather than
	// a fraction of a second and the controller binds above the floor; the
	// gate three hours of blocks past the cut at the 12 s floor.
	// FundV2 (the fund contract of record IgneumFund at
	// 0x142d218ccf2d20e57cc2fdf56f2616d25c20ad60, the product team) is NOT on the
	// public test network's rule set: 6400, 7200 and 8000 passed without the
	// fork on the nodes of record (15 October 2026); it takes a future height
	// in its own release, by F-114, on the project's word.
	// TailV1 at 12,400 (the founder's word of 15:1x UK, 15 October 2026, the bounded
	// tail: the rounds past round 0 every 10 s, the clock floor 6 s), about an hour
	// after the release's roll on every node of record.
	7320: {ForkLengthV1: 1, PolicyV1: 1, ClockV1: 1, ClockTPiMs: 4690, ClockDMin: 116860, PayV2: 1, DiffMinV1: 1200, DiffMinV1Min: 6_000_000, WithdrawalIndexV1: 5000, TailV1: 12400, TailDelta: 10, TailTMin: 6},
	// The eleventh chain (7321), VectorWork: the length order, the policy
	// parts, the Pay rule's second form over the coordinate rule's accrued
	// share, the withdrawal index; no clock amendment and no single-chain
	// difficulty floor, since under the vector form each coordinate's
	// controller carries its own registered floor and reference
	// (the eleventh chain's registry document, chain 7321) and the clock amendment is the
	// single chain's.
	7321: {ForkLengthV1: 1, PolicyV1: 1, PayV2: 1, WithdrawalIndexV1: 5000},
	// The twelfth chain (7322): every rule's latest form from genesis.
	// The public test network's clock re-pin (0.10.49, the project's order of
	// 21:3x UK, 16 October 2026, on the security review's reading that the round
	// clock sets the pace: round 1 opens at Delta = 20 s whatever the
	// registry's tailDelta, so the mean cannot reach 12 s on the genesis
	// clock): from IgneumPublicClockFrom the round clock's Delta is 13 s
	// (tMin 3 plus the resident proof's 4.69 s plus a seal window of
	// about 5 s; the registry's tMin 3 and tailDelta 5 from 3,184 stay
	// under it) and tPiMs is 1,200 ms (0.10.50, the review lead's ruling of
	// 22:40 UK on the reconciliation of record: the public test network's
	// three lane documents are unkeyed, so no block there carries the
	// keyed seal's proof and the genesis 4,690 ms was paid on every
	// height for a proof no block carries; 1,200 is 4.4 times the only
	// measured resident proof, the 12 October cells' p95 of 271.8 ms on
	// an RTX 4090 under a 32-thread CPU search, the margin for a keyed
	// document ever landing; the seal target (13,000 minus 3,000 minus
	// 1,200) / 4 = 2,200 ms; no keyed-seal path goes live on the chain
	// until the resident proof on the sealers' own cards is measured
	// against it); the gate past the roll's end by the three-hour rule,
	// named with the arithmetic in the cut ask.
	// The sponsored lane's gate is NOT in this table (F-703): a chain
	// listed here with the other rules at zero loses the forms a nil entry
	// gives it (clock.go policyOn, shares_engine.go payV2On), so the gate
	// has its own table, IgneumChainSponsoredFrom, as the pay bound and
	// the share-lane gate do.
	7322: {ForkLengthV1: 1, PolicyV1: 1, PayV2: 1, WithdrawalIndexV1: 1, ClockV1: IgneumPublicClockFrom, ClockDelta: 13, ClockTPiMs: 1200},
}

// IgneumRuleForksForChainID is the rule forks a chain id carries (a copy),
// or nil for a chain id with none listed (every rule's latest form from
// genesis).
func IgneumRuleForksForChainID(id uint64) *ctypes.IgneumRuleForks {
	f := IgneumChainRuleForks[id]
	if f == nil {
		return nil
	}
	c := *f
	return &c
}

// IgneumChainPayBoundUnscaledUntil is, by chain id, the last height whose
// minimum pay rule derives with the unscaled y bound (y at most A256_c), the
// scaled bound (A256_c x window / 256, node 0.10.18 of 17:32 UK, 15 October
// 2026) standing above it (F-192, consensus/igneum/paybound.go). It is kept
// apart from IgneumChainRuleForks because a chain listed there with the
// other rules at zero would lose the forms a nil entry gives it (the policy
// parts on from genesis). The research chain (7358) rolled onto 0.10.18
// between 17:40 and 17:46 UK on 15 October 2026, its eight nodes between
// heights 5920 and 5938, and no block of the roll's window derived
// differently under the two bounds (a split would have shown); 5939 is the
// last height under the unscaled bound. The public test network (7322)
// synced clean from genesis under the scaled bound (the node team, 00:4x UK,
// 16 October 2026) and carries no entry; a chain without an entry derives
// scaled from genesis.
var IgneumChainPayBoundUnscaledUntil = map[uint64]uint64{
	7358: 5939,
}

// IgneumPayBoundUnscaledUntilForChainID is the chain id's gate (0: none).
func IgneumPayBoundUnscaledUntilForChainID(id uint64) uint64 {
	return IgneumChainPayBoundUnscaledUntil[id]
}

// IgneumChainRegistryNoticeFrom is, by chain id, the first landing height at
// which the notice floor binds (a chain-held registration without the
// research mark needs noticeBlocks of at least 64; cut B's public-chain
// residual of 11:45 UK, 15 October 2026, consensus/igneum ReregistrationFrom).
// F-196 (16 October 2026): the research chain's landings 4115, 4206, 4218,
// 4228, 4984 and 5379 were admitted by its nodes before the rule and carry
// no research mark, so a fresh walk under the rule refused them all and
// stopped at 4211 (the 4206 landing's pay entry never taken). The gate is
// the height the rule reached the chain's nodes: the eight rolled onto node
// 0.10.18 between heights 5920 and 5938 (the same roll as the y bound's,
// IgneumChainPayBoundUnscaledUntil), and the rule's first work was at
// 5947, an unmarked document refused by every node ("needs noticeBlocks of
// at least 64 unless marked research"), re-sent marked and admitted at
// 5949; a fresh walk must refuse 5947 as the chain did (a gate of 5949 let
// it in, and the chain's 5949 document then differed from it in
// balancedMinFrom and was refused: the walk stopped at 5956). A chain
// without an entry has the floor at every landing, as the code stood (the
// public test network's landings carry a notice of 64).
var IgneumChainRegistryNoticeFrom = map[uint64]uint64{
	7358: 5939,
}

// IgneumRegistryNoticeFromForChainID is the chain id's gate (0: none).
func IgneumRegistryNoticeFromForChainID(id uint64) uint64 {
	return IgneumChainRegistryNoticeFrom[id]
}

// IgneumChainFirstSeenFrom is, by chain id, the first height at which the
// fork choice's same-height rule is in force (consensus/igneum/firstseen.go;
// the review lead's ruling ed600ddc8 of 16 October 2026): at one height the block
// a node imported first stands and a same-height competitor replaces it
// only as part of a branch of greater height; below the gate the normalised
// weight decided the race at one height too (the heavy lane paid for
// arriving late). Each gate is set at the cut with every node of record on
// the version at least three hours before it, from the chain's measured
// block time (the research chain 12 to 16 s on the 36-second lanes, the
// public test network 27 s), and named in the cut ask with the arithmetic;
// a chain without an entry runs the measure at every height as before.
var IgneumChainFirstSeenFrom = map[uint64]uint64{
	7358: 14336,
	// The public test network (7322): re-set from 4,096 at the cut of
	// 16 October 2026, 19:3x UK (the review lead's ruling on the security review's
	// cell): under the 36-second succession of block 2,860 the chain runs
	// at about 12 s, so 4,096 falls at about 23:32 UK that night, and the
	// nine nodes of record stay on 0.10.40 to A + 300 (about 20:45 UK)
	// with a nine-node roll of about 25 minutes: the whole set on the
	// rule's version at about 21:15 UK at the earliest, under the three
	// hours. 5,120 is the roll's expected end plus three hours at the
	// measured pace plus the margin for a slip (about 02:30 UK on 17
	// October at 12 s); every 0.10.41-based version before this one
	// carries 4,096 and is never rolled onto the public test network.
	7322: 5120,
	// The short-epoch box chain (7398, the proof): the gate inside its
	// run, the races below it on the measure and above it on the rule.
	7398: 150,
}

// IgneumFirstSeenFromForChainID is the chain id's gate, or 0 for none.
func IgneumFirstSeenFromForChainID(id uint64) uint64 {
	return IgneumChainFirstSeenFrom[id]
}

// IgneumChainForkWeightFrom is, by chain id, the first height at which the
// fork choice's served measure weighs a lane's blocks by the lane's RUNNING
// difficulty at the race's common ancestor in place of its registered
// reference dRef (consensus/igneum/vector.go laneWeightsBy; the review lead's
// ruling 7f95a4dc1 of 18 October 2026 on F-569: at 8,342 two raster blocks
// at 1.84 x their reference displaced five card-lane blocks at 0.26 to
// 0.29 x theirs, since a block's weight was D / dRef and the card lanes'
// references were cut from a rate no sealer on the chain reaches; under
// the running difficulty every lane's block weighs about one unit of its
// own lane whatever its distance from dRef, and the breadth key does the
// rest). The three-hour rule as for the first-seen gate: every node of
// record on the version three hours before the landing check (the research chain
// by 18,600, the public test network by 12,412 at 12 s); below the gate the
// measure runs on dRef as before; a chain without an entry stays on dRef.
var IgneumChainForkWeightFrom = map[uint64]uint64{
	7358: 21504, // the research chain: 21 x 1,024, about six hours past 19,600 (02:01Z, 18 October 2026)
	7322: 13312, // the public test network: 13 x 1,024, clear of the 12,288 read, about six and a half hours past 11,356
	7398: 150,   // the short-epoch box chain (the proof): beside its first-seen gate
}

// IgneumForkWeightFromForChainID is the chain id's gate, or 0 for none.
func IgneumForkWeightFromForChainID(id uint64) uint64 {
	return IgneumChainForkWeightFrom[id]
}

// IgneumChainShareLaneFrom is, by chain id, the first height at which the
// share target's difficulty is the LANE's difficulty of the block (the
// coordinate controller's figure under the vector form) in place of the
// round clock's chain difficulty D(h) (F-690, the review lead's ruling bd0784c6d
// of 18 October 2026): the clock's D(h) is a chain-level ASERT at the
// chain's part target (20 s on the public test network) against the lanes'
// combined cadence (12.6 s under three 36-second lanes), so it climbs
// 2^((20 - 12.6) / 3600) a block without bound, a doubling every 1.7
// hours, and the share target with it (728 million a share on lane 5 at
// 06:17Z against the lane's 59,000 / 160); on the template (the served
// shareTarget) and on the verifier (a carried share, a submitted share)
// alike, so a chain's nodes agree on every share from G; below G, and on a
// chain without an entry, the clock's D(h) as before. The three-hour rule
// as for the fork-weight gate: every node of record on the version before
// the gate.
var IgneumChainShareLaneFrom = map[uint64]uint64{
	7358: 22400, // the research chain: a window boundary past the fork-weight gate 21,504
	7322: 14400, // the public test network: 600 past the registry entry (c)'s A = 13,800, a window and period boundary
	7398: 150,   // the short-epoch box chain (the proof): beside its first-seen gate
}

// IgneumShareLaneFromForChainID is the chain id's gate, or 0 for none.
func IgneumShareLaneFromForChainID(id uint64) uint64 {
	return IgneumChainShareLaneFrom[id]
}

// IgneumChainSponsoredFrom is, by chain id, the first height at which the
// sponsored lane's rule is in force (SPONSORED-LANE-NODE-FORM.md; the
// chair's rulings e881c07ca and 1c08db88c, 18 October 2026): the registry
// admits a coordinate marked sponsored at a landing at or past it, and
// Finalize reads the locks and roots sent to a sponsored lane's escrow,
// marks the chain's paid keys for the lane's windows, settles each window
// and refunds each span (the state writes of consensus/igneum/sponsored.go,
// listed in tools/ci/finalize-writes.txt). A chain without an entry admits
// no sponsored lane and nothing of the rule writes state (ZERO IS OFF), so
// a binary swapped onto a running chain changes no state root before the
// named height. It is its own table, not a field of IgneumChainRuleForks
// (F-703, 18 October 2026): a chain listed in the forks table with the
// other rules at zero loses the forms a nil entry gives it (the policy
// parts on from genesis), as IgneumChainPayBoundUnscaledUntil records. The
// public test network at 15,360 (960 past the share-lane gate 14,400, a
// window and period boundary), the research chain at 23,552 (1,152 past
// its share-lane gate 22,400), the box chain at 150, the sponsored lab
// chains from 1; every node of record on the version before each.
var IgneumChainSponsoredFrom = map[uint64]uint64{
	7322: 15360,
	7358: 23552,
	7398: 150,
	7396: 1,
	7399: 1,
}

// IgneumChainPoolFrom is, by chain id, the first height at which the
// shielded pool's rule is in force (Project Money stage one, cut two;
// consensus/igneum/pool and pool_settle.go): from it the pool address
// exists and Finalize admits the shields and spends sent to it (the
// commitment tree, the nullifier set and the turnstile written under the
// pool address's storage). A chain without an entry, or at zero, runs no
// pool and nothing of the rule writes state (ZERO IS OFF): every chain of
// record is at zero until the review's read of the measured stage; the
// Money lab chain 7402 (cut four: the real verifier, Poseidon2, the proven
// shield) from block 1, and the stage-two coinbase lab chain 7405 (the
// sealer's share minted as a note; docs/money/STAGE2-CUT2-COINBASE-2026-10-18.md)
// from block 1; cut two's 7401 ran the earlier forms and is retired with
// its data. Its own table, not a field of
// IgneumChainRuleForks (F-703's rule).
var IgneumChainPoolFrom = map[uint64]uint64{
	7402: 1,
	7409: 1, // the 128-bit cut's lab chain (docs/money/STAGE1-128BIT-2026-10-18.md), from block 1
	7403: 1, // the pruned-sync lab (docs/money/STAGE1-PRUNED-SYNC-2026-10-18.md)
	7405: 1, // the shielded coinbase lab (stage two, cut two)
	7404: 1, // the vault lab (stage two, cut one; docs/money/STAGE2-CUT1-VAULT-2026-10-18.md)
	7410: 1, // the vault's re-run lab (the cured withdraw at 128 bits; docs/money/STAGE2-CUT1-VAULT-RERUN-2026-10-19.md)
	7421: 1, // the vault findings' lab (F-799, F-800, F-801; docs/money/STAGE2-CUT3-VAULT-FINDINGS-2026-10-19.md)
}

// IgneumChainDAGFrom is, by chain id, the first height at which the block
// DAG ordering is in force (Kaspa's GHOSTDAG under the lanes,
// docs/money/ORDERING-DESIGN-2026-10-18.md; the ladder's step 0,
// docs/money/ORDERING-STEP1-2026-10-19.md; consensus/igneum/dagform.go): from
// it a block names its merged parents beside its selected parent, the head
// is chosen by blue work, the state is the selected parent's after the
// ordered mergeset, and a blue block's subsidy is paid at its order by the
// block that merges it. A chain without an entry, or at zero, runs the
// single chain as before and REFUSES a header carrying merged parents
// (ZERO IS OFF): every chain of record is at zero; the throwaway lab chains
// 7411 (step 0), 7412 and 7413 (step 1), 7422 and 7423 (step 2) from block 1,
// the only chains the steps run on. Its own table, not a field of
// IgneumChainRuleForks (F-703's rule).
//
// The entry's second field is the LANE CADENCE (the ladder's step 2,
// docs/money/ORDERING-STEP3-2026-10-19.md): the seconds a lane's controller
// holds between the lane's blocks from the gate height, in place of the
// registration's target (consensus/igneum/vector.go reads it through the
// engine's dagLaneTarget); zero keeps the registration's. The merged block
// rate is the lane count over it (three lanes at 12 s: a block every 4 s;
// at 6 s every 2 s; at 3 s every second) and the ordering's k and slack
// follow it (consensus/igneum/dagform.go DAGKFor). Read only through a
// non-zero gate (IgneumDAGLaneSecondsForChainID).
var IgneumChainDAGFrom = map[uint64]DAGGate{
	7411: {From: 1},                 // the ordering ladder's step 0 lab (docs/money/ORDERING-STEP1-2026-10-19.md)
	7412: {From: 1},                 // the ordering ladder's step 1 lab, the lanes at one rate (docs/money/ORDERING-STEP2-2026-10-19.md)
	7413: {From: 1},                 // the ordering ladder's step 1 lab, one lane at ten times the rate from the start (the same page)
	7422: {From: 1, LaneSeconds: 6}, // the ordering ladder's step 2 lab, each lane at 6 s, a merged block every 2 s (docs/money/ORDERING-STEP3-2026-10-19.md)
	7423: {From: 1, LaneSeconds: 3}, // the ordering ladder's step 2 lab, each lane at 3 s, a merged block every second (the same page)
}

// DAGGate is one entry of IgneumChainDAGFrom: the first height under the
// ordering (zero off) and the lane cadence in seconds (zero: the
// registration's target).
type DAGGate struct {
	From        uint64
	LaneSeconds uint64
}

// IgneumDAGFromForChainID is the chain id's DAG gate, or 0 for none.
func IgneumDAGFromForChainID(id uint64) uint64 {
	return IgneumChainDAGFrom[id].From
}

// IgneumDAGLaneSecondsForChainID is the chain id's lane cadence under the
// ordering, or 0 where the gate is zero or the entry names none.
func IgneumDAGLaneSecondsForChainID(id uint64) uint64 {
	g := IgneumChainDAGFrom[id]
	if g.From == 0 {
		return 0
	}
	return g.LaneSeconds
}

// IgneumPoolFromForChainID is the chain id's pool gate, or 0 for none.
func IgneumPoolFromForChainID(id uint64) uint64 {
	return IgneumChainPoolFrom[id]
}

// IgneumSponsoredFromForChainID is the chain id's sponsored gate, or 0 for
// none: the one source the registry's admission (SponsoredGateRefusal) and
// the signer's check read; the backend hands the same figure to the engine
// (SetSponsoredFrom).
func IgneumSponsoredFromForChainID(id uint64) uint64 {
	return IgneumChainSponsoredFrom[id]
}

// IgneumChainDocumentRevealFrom is, by chain id, the first height at which
// a programme's reveal depth is resolved from the coordinate's
// registration (revealBlocks) for EVERY instance of the lane's document
// (consensus/igneum/program.go programRevealOf, vector.Menu.DocumentRevealOf;
// F-730, the review lead's reading of 14:15 UK, 18 October 2026): before it the
// match is the base instance alone, so a lane whose document was changed
// in place resolved the relation's default of two from the change's height
// (the public test network from the registry entry at 13,800: the sealers
// built with two, the pool's anvil shares built with the document's 64
// were refused as an invalid mix digest, 3,657 in an hour; no split). A
// validity rule gated by height so every block sealed under the old
// resolution stays valid as it was sealed: a chain without an entry, or at
// zero, keeps the form before (ZERO IS THE FORM BEFORE), and the gate sits
// above the head of every node of record when it is set. Its own table,
// not a field of IgneumChainRuleForks (F-703's rule). The public test
// network and the research chain at 0 for the review lead to set (a period
// boundary above the roll, each node of record on the version before it);
// the F-730 lab chain 7395 at 192 (docs/node/F-730-REVEAL-LAB-2026-10-18.md).
var IgneumChainDocumentRevealFrom = map[uint64]uint64{
	7322: 15616, // the review lead's height of 15:2x UK, 18 October 2026: a period boundary (244 x 64) above every node of record's head at the roll's close (the nine on 0.10.66 and this version by ~16:50 UK, the head then ~15,400); about 17:40 UK at 12.4 s a block
	7358: 23808, // the review lead's height: a period boundary (372 x 64) above the roll's close on the research chain (the head then ~23,500); about 17:55 UK at 12.9 s a block
	7395: 192,
}

// IgneumDocumentRevealFromForChainID is the chain id's document-reveal
// gate, or 0 for none (the form before F-730): the one source the backend
// hands to the engine (SetDocumentRevealFrom).
func IgneumDocumentRevealFromForChainID(id uint64) uint64 {
	return IgneumChainDocumentRevealFrom[id]
}

// IgneumRPCLimitsForChainID is the public-RPC limits a chain id carries as
// its config default (a copy), or nil for a chain id with none listed
// (the limits off).
func IgneumRPCLimitsForChainID(id uint64) *ctypes.IgneumRPCLimits {
	l := IgneumChainRPCLimits[id]
	if l == nil {
		return nil
	}
	c := *l
	return &c
}

// withSupply is a copy of an issuance with the form `next` appended as the
// LAST form of its chain from the gate height `from` (the founder's supply
// order of 18 October 2026: the one-billion form after the 12-second one);
// every form on the way is copied, so the table's entries are never written
// through, and the appended form's fund fields are not read (the fund is one
// across every gate). A gate of ZERO is unset: the issuance is returned as it
// is and the chain keeps its figures for ever.
func withSupply(i *ctypes.IgneumIssuance, from uint64, next *ctypes.IgneumIssuance) *ctypes.IgneumIssuance {
	if from == 0 {
		return i
	}
	c := withFund(i, i.FundAddress)
	q := c
	for q.Next != nil {
		q.Next = withFund(q.Next, common.Address{})
		q = q.Next
	}
	q.NextFrom = from
	q.Next = withFund(next, common.Address{})
	return c
}

// IgneumIssuanceForChainID is the issuance a chain id carries as its config
// default (a copy), or nil for a chain id with none listed.
func IgneumIssuanceForChainID(id uint64) *ctypes.IgneumIssuance {
	i := IgneumChainIssuance[id]
	if i == nil {
		return nil
	}
	return withFund(i, i.FundAddress)
}
