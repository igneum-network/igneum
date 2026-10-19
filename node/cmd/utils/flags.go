// Copyright 2015 The go-ethereum Authors
// This file is part of go-ethereum.
//
// go-ethereum is free software: you can redistribute it and/or modify
// it under the terms of the GNU General Public License as published by
// the Free Software Foundation, either version 3 of the License, or
// (at your option) any later version.
//
// go-ethereum is distributed in the hope that it will be useful,
// but WITHOUT ANY WARRANTY; without even the implied warranty of
// MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE. See the
// GNU General Public License for more details.
//
// You should have received a copy of the GNU General Public License
// along with go-ethereum. If not, see <http://www.gnu.org/licenses/>.

// Package utils contains internal helper functions for go-ethereum commands.
package utils

import (
	"context"
	"crypto/ecdsa"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"math/big"
	"net"
	"net/http"
	"os"
	"os/user"
	"path/filepath"
	godebug "runtime/debug"
	"strconv"
	"strings"
	"time"

	"github.com/ethereum/go-ethereum/accounts"
	"github.com/ethereum/go-ethereum/accounts/keystore"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/common/fdlimit"
	"github.com/ethereum/go-ethereum/common/hexutil"
	"github.com/ethereum/go-ethereum/consensus/igneum"
	"github.com/ethereum/go-ethereum/consensus/igneum/pow"
	"github.com/ethereum/go-ethereum/consensus/igneum/pow/igneumhash"
	"github.com/ethereum/go-ethereum/consensus/igneum/pow/schedule"
	"github.com/ethereum/go-ethereum/consensus/igneum/pow/vector"
	"github.com/ethereum/go-ethereum/core/vm/igneumreg"

	"github.com/ethereum/go-ethereum/core"
	"github.com/ethereum/go-ethereum/core/rawdb"
	"github.com/ethereum/go-ethereum/core/txpool/legacypool"
	"github.com/ethereum/go-ethereum/core/vm"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/eth"
	"github.com/ethereum/go-ethereum/eth/downloader"
	"github.com/ethereum/go-ethereum/eth/ethconfig"
	"github.com/ethereum/go-ethereum/eth/filters"
	"github.com/ethereum/go-ethereum/eth/gasprice"
	"github.com/ethereum/go-ethereum/eth/tracers"
	"github.com/ethereum/go-ethereum/ethdb"
	"github.com/ethereum/go-ethereum/ethdb/remotedb"
	"github.com/ethereum/go-ethereum/internal/ethapi"
	"github.com/ethereum/go-ethereum/internal/flags"
	"github.com/ethereum/go-ethereum/log"
	"github.com/ethereum/go-ethereum/metrics"
	"github.com/ethereum/go-ethereum/metrics/exp"
	"github.com/ethereum/go-ethereum/metrics/influxdb"
	"github.com/ethereum/go-ethereum/miner"
	"github.com/ethereum/go-ethereum/node"
	"github.com/ethereum/go-ethereum/p2p"
	"github.com/ethereum/go-ethereum/p2p/enode"
	"github.com/ethereum/go-ethereum/p2p/nat"
	"github.com/ethereum/go-ethereum/p2p/netutil"
	"github.com/ethereum/go-ethereum/params"
	"github.com/ethereum/go-ethereum/params/types/ctypes"
	"github.com/ethereum/go-ethereum/params/types/genesisT"
	"github.com/ethereum/go-ethereum/params/vars"
	"github.com/ethereum/go-ethereum/rpc"
	"github.com/ethereum/go-ethereum/triedb"
	"github.com/ethereum/go-ethereum/triedb/hashdb"
	"github.com/ethereum/go-ethereum/triedb/pathdb"
	pcsclite "github.com/gballet/go-libpcsclite"
	gopsutil "github.com/shirou/gopsutil/mem"
	"github.com/urfave/cli/v2"
)

const (
	gcModeArchive = "archive"
	gcModeFull    = "full"
)

// These are all the command line flags we support.
// If you add to this list, please remember to include the
// flag in the appropriate command definition.
//
// The flags are defined here so their names and help texts
// are the same for all commands.

var (
	// General settings
	DataDirFlag = &flags.DirectoryFlag{
		Name:     "datadir",
		Usage:    "Data directory for the databases and keystore",
		Value:    flags.DirectoryString(vars.DefaultDataDir()),
		Category: flags.EthCategory,
	}
	RemoteDBFlag = &cli.StringFlag{
		Name:     "remotedb",
		Usage:    "URL for remote database",
		Category: flags.LoggingCategory,
	}
	DBEngineFlag = &cli.StringFlag{
		Name:     "db.engine",
		Usage:    "Backing database implementation to use ('pebble' or 'leveldb')",
		Value:    node.DefaultConfig.DBEngine,
		Category: flags.EthCategory,
	}
	AncientFlag = &flags.DirectoryFlag{
		Name:     "datadir.ancient",
		Usage:    "Root directory for ancient data (default = inside chaindata)",
		Category: flags.EthCategory,
	}
	MinFreeDiskSpaceFlag = &flags.DirectoryFlag{
		Name:     "datadir.minfreedisk",
		Usage:    "Minimum free disk space in MB, once reached triggers auto shut down (default = --cache.gc converted to MB, 0 = disabled)",
		Category: flags.EthCategory,
	}
	KeyStoreDirFlag = &flags.DirectoryFlag{
		Name:     "keystore",
		Usage:    "Directory for the keystore (default = inside the datadir)",
		Category: flags.AccountCategory,
	}
	USBFlag = &cli.BoolFlag{
		Name:     "usb",
		Usage:    "Enable monitoring and management of USB hardware wallets",
		Category: flags.AccountCategory,
	}
	USBPathIDFlag = &cli.Uint64Flag{
		Name:  "usb.pathid",
		Usage: "Specify USB path ID per SLIP-0044 (1=all testnets, 60=ETH mainnet, 61=ETC mainnet)",
	}
	SmartCardDaemonPathFlag = &cli.StringFlag{
		Name:     "pcscdpath",
		Usage:    "Path to the smartcard daemon (pcscd) socket file",
		Value:    pcsclite.PCSCDSockName,
		Category: flags.AccountCategory,
	}
	NetworkIdFlag = &cli.Uint64Flag{
		Name:     "networkid",
		Usage:    "Explicitly set network id (integer)(For testnets: use --sepolia, --holesky instead)",
		Value:    ethconfig.Defaults.NetworkId,
		Category: flags.EthCategory,
	}
	EthProtocolsFlag = &cli.StringFlag{
		Name:     "eth.protocols",
		Usage:    "Sets the Ethereum Protocol versions (first is primary)",
		Category: flags.EthCategory,
		Value: strings.Join(func() (strings []string) {
			for _, s := range ethconfig.Defaults.ProtocolVersions {
				strings = append(strings, strconv.Itoa(int(s)))
			}
			return
		}(), ","),
	}
	// Dev mode
	DeveloperFlag = &cli.BoolFlag{
		Name:     "dev",
		Usage:    "Ephemeral proof-of-authority network with a pre-funded developer account, mining enabled",
		Category: flags.DevCategory,
	}
	DeveloperPeriodFlag = &cli.Uint64Flag{
		Name:     "dev.period",
		Usage:    "Least seconds between blocks in developer mode (0 = as fast as the wall clock allows, one block a second)",
		Category: flags.DevCategory,
	}
	// DeveloperChainIDFlag gives a research chain its own chain id in the
	// research range (7311 to 7499), so a signed transaction replays on no
	// other development chain (the product team's finding, 11 October 2026: every
	// dev chain shared 7310 with different genesis hashes). Zero keeps the
	// built-in development chain id. It changes the genesis hash.
	DeveloperChainIDFlag = &cli.Uint64Flag{
		Name:     "dev.chainid",
		Usage:    "Chain id of a research chain in developer mode (7311 to 7499; 0 = the built-in development chain, 7310)",
		Category: flags.DevCategory,
	}
	DeveloperPoWFlag = &cli.BoolFlag{
		Name:     "dev.pow",
		Usage:    "Ephemeral proof-of-work network with a pre-funded developer account, mining enabled",
		Category: flags.DevCategory,
	}
	// DeveloperAlgorithmFlag selects the development chain's proof of work
	// among the development-only algorithms: "null" (the default, the
	// devnet's) or "igneumhash" (the R0A v0 research candidate, PHASE-ZERO.md).
	// The genesis block is the same either way; the chain configuration's
	// algorithm row differs, so a datadir is one or the other.
	DeveloperAlgorithmFlag = &cli.StringFlag{
		Name:     "dev.algorithm",
		Usage:    "Development chain proof of work: null (default) or igneumhash (research candidate, dev-only)",
		Value:    "null",
		Category: flags.DevCategory,
	}
	// DeveloperCPUSealFlag turns the development chain's own CPU sealer off:
	// the node then seals only through remote workers (igneum_getWork and
	// submitWork), so a worker's accepted blocks are the chain's only blocks.
	// DeveloperAlgorithmParamsFlag is the params document for --dev.algorithm
	// igneumhash (SPEC-R0A-v0.md section 0.1, revision 0.5): "version" is
	// mandatory in it; {"version": 1} is the version 1 baseline, {"version":
	// 0} the version 0 one, any accepted variant otherwise. The chain id is
	// the development chain's whatever the document says.
	DeveloperAlgorithmParamsFlag = &cli.StringFlag{
		Name:     "dev.algorithm.params",
		Usage:    "IgneumHash params document (JSON, \"version\" mandatory) for --dev.algorithm igneumhash",
		Value:    `{"version": 1}`,
		Category: flags.DevCategory,
	}
	// DeveloperAlgorithmWorkFlag selects which of the node's two WORK
	// implementations the IgneumHash adapter runs: "spec" (the protocol team's, the
	// default) or "candidate" (protocol B's), for the differential. Both agree
	// on every vector; the switch changes no consensus result.
	DeveloperAlgorithmWorkFlag = &cli.StringFlag{
		Name:     "dev.algorithm.work",
		Usage:    "IgneumHash WORK implementation: spec (default) or candidate",
		Value:    "spec",
		Category: flags.DevCategory,
	}
	// DeveloperAlgorithmScheduleFlag is the bounded menu's schedule for the
	// development chain (the research test plan's section 6): a JSON object
	// {"entries": [{"name", "height", "algorithmParams"}, ...], "terminal":
	// {"hold": true} | {"endHeight": N}}. On a fresh datadir it enters the
	// genesis configuration; on a persisted one it is applied as a
	// development-only override at every start (give it on every start).
	DeveloperAlgorithmScheduleFlag = &cli.StringFlag{
		Name:     "dev.algorithm.schedule",
		Usage:    "Bounded menu for the development chain: JSON schedule of parameter documents by height with a terminal entry",
		Category: flags.DevCategory,
	}
	// DeveloperAuthorityFlag is the miner-bound work authority prototype's
	// rule for the development chain (consensus/igneum/authority.go; a
	// research PROTOTYPE, nothing adopted): a JSON object {"height": N,
	// "registry": "0x..", "cap": "<base units>"}. On a fresh datadir the
	// registry contract enters the genesis allocation with the cap in slot
	// 0 and the rule enters the engine block; on a persisted one it is a
	// development-only override at every start (give it on every start).
	DeveloperAuthorityFlag = &cli.StringFlag{
		Name:     "dev.authority",
		Usage:    "Miner-bound work authority prototype: JSON {height, registry, cap} (research prototype)",
		Category: flags.DevCategory,
	}
	// DeveloperBondKeyFlag is the 32-byte key this development node seals
	// under when the rule is in force (hex).
	DeveloperBondKeyFlag = &cli.StringFlag{
		Name:     "dev.bondkey",
		Usage:    "Bond key (32-byte hex) this node seals under, for --dev.authority",
		Category: flags.DevCategory,
	}
	// DeveloperBondKeyFileFlag is --dev.bondkey's value read from a file the
	// owner alone can read (F-440, 17 October 2026: the string form puts a
	// node's identity on its command line, readable through /proc by every
	// user of the box); the two are mutually exclusive.
	DeveloperBondKeyFileFlag = &cli.StringFlag{
		Name:     "dev.bondkey.file",
		Usage:    "File holding --dev.bondkey's value (the 32-byte hex key or the word keyed), mode 0600; exclusive with --dev.bondkey",
		Category: flags.DevCategory,
	}
	// DeveloperAuthorityLaxFlag makes this node the test harness's
	// non-compliant node: it carries the header field but skips the stateful
	// rule, so it seals rule-breaking blocks for compliant nodes to refuse.
	DeveloperAuthorityLaxFlag = &cli.BoolFlag{
		Name:     "dev.authority.lax",
		Usage:    "TEST HARNESS: skip the authority rule's state check (a non-compliant node for rejection tests)",
		Category: flags.DevCategory,
	}
	// DeveloperWithholdFlag arms the withholding sealer (the lab build's
	// research instrument, consensus/igneum/labmode.go): the sealer holds
	// each block it seals, broadcasting nothing, until it holds N, then
	// releases them at once, so two sealers race at a chosen depth for the
	// guard's branch races. Refused off the lab build.
	DeveloperWithholdFlag = &cli.Uint64Flag{
		Name:     "dev.withhold",
		Usage:    "TEST HARNESS (lab build only): the sealer withholds each block until it holds N, then releases them at once (branch races)",
		Category: flags.DevCategory,
	}
	// MinerSealStaggerFlag is PA-52's sealer policy (consensus/igneum/
	// stagger.go): at a round where several keys are eligible, each rank
	// after the least-lot key waits this much more before its search; a
	// node's value, no consensus change.
	MinerSealStaggerFlag = &cli.Uint64Flag{
		Name:     "miner.seal-stagger",
		Usage:    "Stagger in milliseconds between eligible keys' search starts by lot rank (0 off)",
		Value:    igneum.DefaultSealStaggerMs,
		Category: flags.MinerCategory,
	}
	// MinerOpenSpreadFlag is F-114's open-seat spread (consensus/igneum/
	// stagger.go): an open-seat sealer's search start moves by slot x
	// this width, slot = keccak(parent hash, key) mod 4; a node's value,
	// no consensus change.
	MinerOpenSpreadFlag = &cli.Uint64Flag{
		Name:     "miner.open-spread",
		Usage:    "Open-seat spread slot width in milliseconds (slot = keccak(parent, key) mod 4; 0 off)",
		Value:    igneum.DefaultOpenSpreadMs,
		Category: flags.MinerCategory,
	}
	// DeveloperLaxEndorseFlag arms the lax-endorsement sealer (the lab
	// build's research instrument): the sealer attaches the file's
	// endorsement entries WITHOUT verifying them, so the other nodes' import
	// refusals are exercised by class. Refused off the lab build.
	DeveloperLaxEndorseFlag = &cli.StringFlag{
		Name:     "dev.lax-endorse",
		Usage:    "TEST HARNESS (lab build only): attach the file's endorsement entries unverified (import-refusal classes)",
		Category: flags.DevCategory,
	}
	// DeveloperSortitionFlag is the sortition-gated rule for the development
	// chain (consensus/igneum/sortition.go; the same PROTOTYPE, labelled
	// hybrid proof of work and bonded capital): a JSON object {"height",
	// "epochBlocks", "seedBlocks", "expected", "delta", "maxRound",
	// "unbondedRound", "maxPayouts"}. Needs --dev.authority; the registry
	// then carries the signer operation (version 2 of its code).
	DeveloperSortitionFlag = &cli.StringFlag{
		Name:     "dev.sortition",
		Usage:    "Sortition-gated rule prototype: JSON {height, epochBlocks, seedBlocks, expected, delta, maxRound, unbondedRound, maxPayouts}",
		Category: flags.DevCategory,
	}
	// DeveloperDifficultyFlag sets the development chain's difficulty rule
	// constants in its genesis engine block (consensus/igneum/difficulty,
	// PB-01): a JSON object {"target", "halfLife", "dtMax", "min"}, the
	// block-time target in seconds first. A research chain's value, never a
	// launch value (the project's order of 12:2x UK, 11 October 2026: the fifth
	// development chain at a 5 s target so the E rows complete in hours);
	// the genesis hash moves with it.
	DeveloperDifficultyFlag = &cli.StringFlag{
		Name:     "dev.difficulty",
		Usage:    "Difficulty rule constants of the development chain: JSON {target, halfLife, dtMax, min} (research values; moves the genesis hash)",
		Category: flags.DevCategory,
	}
	// DeveloperBondSeedFlag is the 32-byte Ed25519 seed this node signs
	// templates with under the sortition rule; its public key is the bond
	// key unless --dev.bondkey names another bond whose signer it is.
	DeveloperBondSeedFlag = &cli.StringFlag{
		Name:     "dev.bondseed",
		Usage:    "Ed25519 seed (32-byte hex) this node signs under, for --dev.sortition",
		Category: flags.DevCategory,
	}
	// DeveloperBondSeedFileFlag is --dev.bondseed's value read from a file
	// the owner alone can read (F-440): the seed is the bond's secret under
	// the keyed form, and the string form put it on the command line of
	// every sealer of record; the two are mutually exclusive.
	DeveloperBondSeedFileFlag = &cli.StringFlag{
		Name:     "dev.bondseed.file",
		Usage:    "File holding --dev.bondseed's value (the 32-byte hex seed), mode 0600; exclusive with --dev.bondseed",
		Category: flags.DevCategory,
	}
	// DeveloperPoolUnsafeVerifierFlag installs the shielded pool's LAB
	// TEST DOUBLE as the spend proof verifier (consensus/igneum/pool
	// UnsafeDigestDouble: a proof is accepted when its first 32 bytes hash
	// with the statement; it proves nothing). Honoured only under --dev on
	// a research-range chain id; refused anywhere else.
	DeveloperPoolUnsafeVerifierFlag = &cli.BoolFlag{
		Name:     "dev.pool.unsafe-verifier",
		Usage:    "UNSAFE, LAB ONLY: accept shielded pool spends under the digest test double in place of a proof verifier (--dev on a research-range chain id only)",
		Category: flags.DevCategory,
	}
	// PoolPruneFlag turns on the shielded pool's proof pruning (the
	// pruned-sync cut, docs/money/PRUNED-SYNC-DESIGN-2026-10-18.md): a
	// proof is verified once at import and its bytes dropped from the
	// stored body PoolPruneKeepFlag blocks later; the retained records
	// are kept and served; a fresh node under the flag joins by the
	// pruned sync (snap sync, no bodies before the pivot, the retained
	// sets backfilled from a peer and checked against the state). Off by
	// default: every node keeps every body whole.
	PoolPruneFlag = &cli.BoolFlag{
		Name:     "pool.prune",
		Usage:    "Drop shielded pool proof bodies after import (keep the retained records); a fresh node joins by the pruned sync",
		Category: flags.EthCategory,
	}
	PoolPruneKeepFlag = &cli.Uint64Flag{
		Name:     "pool.prune.keep",
		Usage:    "Blocks a proof body is kept whole after import before pruning (at least 256)",
		Value:    256,
		Category: flags.EthCategory,
	}
	DeveloperCPUSealFlag = &cli.BoolFlag{
		Name:     "dev.cpuseal",
		Usage:    "Seal the open seat's development blocks on the node's CPU (false: remote workers only for the open seat; a keyed template under a bond key always searches on the node itself)",
		Value:    true,
		Category: flags.DevCategory,
	}
	// The keyed seal's proof pi (consensus/igneum/pi.go): verification behind
	// a flag (off for devnet5, on for devnet6) and the prover CLI this node's
	// own seals use.
	IgneumPiVerifyFlag = &cli.BoolFlag{
		Name:     "igneum.pi-verify",
		Usage:    "Verify the keyed seal's proof pi on every block (needs a node built with the verifier, tag ignpi)",
		Category: flags.DevCategory,
	}
	IgneumPiProverFlag = &cli.StringFlag{
		Name:     "igneum.pi-prover",
		Usage:    "Path of the prover CLI (pi-prove) this node's own keyed seals use under --igneum.pi-verify",
		Category: flags.DevCategory,
	}
	IgneumVerifierCoreFlag = &cli.IntFlag{
		Name:     "igneum.verifier-core",
		Usage:    "The reserved verifier thread: 0 (the default) pins the header-verification worker to the last CPU of the allowed set at elevated priority on a two-CPU host (the minimum node) and nowhere else; N the N-th allowed CPU on any host; -1 off",
		Value:    0,
		Category: flags.DevCategory,
	}
	IgneumPiProverFallbackFlag = &cli.StringFlag{
		Name:     "igneum.pi-prover-fallback",
		Usage:    "Path of a second prover CLI (the CPU build) a seal falls back to when the prover cannot run on this host (F-89); empty falls back to --igneum.pi-prover itself",
		Category: flags.DevCategory,
	}
	// The resident prover (consensus/igneum/piserve.go): pi-prove serve as
	// one process for the sealer's life instead of one process per seal.
	// The verifier's global CPU budget (consensus/igneum/verifybudget.go):
	// milliseconds of verifier CPU a second across every peer; a header
	// defers while the budget is in debt, a share is refused by name.
	// VectorWork (consensus/igneum/vector.go): the coordinate this node
	// seals under, 0 to 3, on a chain with the vector form.
	IgneumCoordinateFlag = &cli.UintFlag{
		Name:     "igneum.coord",
		Usage:    "VectorWork: the work coordinate this node seals under (0 to 3)",
		Category: flags.DevCategory,
	}
	// DeveloperVectorFlag gives the development chain the vector form: a
	// JSON IgneumVector {coordinates: [{name, algorithm, algorithmParams,
	// target, dRef}], halfLife, dtMax, rule}.
	DeveloperVectorFlag = &cli.StringFlag{
		Name:     "dev.vector",
		Usage:    "VectorWork on the development chain: JSON {coordinates: [{name, algorithm, algorithmParams, target, dRef}], halfLife, dtMax, rule}",
		Category: flags.DevCategory,
	}
	IgneumVerifyBudgetFlag = &cli.Uint64Flag{
		Name:     "igneum.verify-budget-ms",
		Usage:    "Verifier CPU budget in milliseconds a second across all peers (headers defer, shares are refused at the limit; 0 = off)",
		Category: flags.DevCategory,
	}
	// The resident dataset (consensus/igneum/resident.go): a verifying node
	// holds the full-dataset contexts instead of the light ones.
	IgneumResidentDatasetFlag = &cli.BoolFlag{
		Name:     "igneum.resident-dataset",
		Usage:    "Verify on the full-dataset contexts (the resident dataset, the verifier of record's path; on by default; =false keeps the light contexts)",
		Value:    true,
		Category: flags.DevCategory,
	}
	IgneumPiResidentFlag = &cli.BoolFlag{
		Name:     "igneum.pi-resident",
		Usage:    "Run the prover as one resident process (pi-prove serve) holding its context warm; this node's own receipts are trusted at its own import (on by default from the clock re-pin, the project's order of 09:3x UK, 12 October 2026: every sealer proves on the resident path; --igneum.pi-resident=false keeps the per-seal process)",
		Value:    true,
		Category: flags.DevCategory,
	}
	// The keyed seal worker (consensus/igneum/sealworker.go): the keyed
	// search on a GPU worker child, one job per template, the first result
	// replayed through the reference; no CPU search on a node with one.
	IgneumSealWorkerFlag = &cli.StringFlag{
		Name:     "igneum.seal-worker",
		Usage:    "Path of the GPU worker executable this node's keyed seals search on (the worker protocol's seal verb); the CPU sealer is then off",
		Category: flags.DevCategory,
	}
	IgneumSealWorkerDeviceFlag = &cli.StringFlag{
		Name:     "igneum.seal-worker-device",
		Usage:    "The card the seal worker opens, by a substring of its name (empty: the first)",
		Category: flags.DevCategory,
	}
	// The settlement rule's endorser (consensus/igneum/endorse.go): this
	// node endorses the heads it holds under its bonded units and pushes
	// the entries to the named sealers (the lab transport).
	IgneumEndorseFlag = &cli.BoolFlag{
		Name:     "igneum.endorse",
		Usage:    "Endorse the heads this node holds under its bonded units (the settlement rule; needs endorseExpected in the chain's sortition parameters)",
		Category: flags.DevCategory,
	}
	IgneumPropagateAllFlag = &cli.BoolFlag{
		Name:     "igneum.propagate-all",
		Usage:    "Push a new block whole to every peer instead of the square-root subset (a sealer with few direct peers; the peers skip the header and body fetches)",
		Category: flags.DevCategory,
	}
	IgneumEndorseToFlag = &cli.StringFlag{
		Name:     "igneum.endorse-to",
		Usage:    "Comma-separated sealer RPC URLs this node pushes its endorsements to (igneum_submitEndorsements)",
		Category: flags.DevCategory,
	}
	DeveloperGasLimitFlag = &cli.Uint64Flag{
		Name:     "dev.gaslimit",
		Usage:    "Initial block gas limit (0 = the development genesis's 16,000,000; any other value changes the genesis hash)",
		Value:    0,
		Category: flags.DevCategory,
	}
	// The multi-node devnet (docs/reliability/DEVNET.md): --dev stays the only
	// source of dev mode; these two make several --dev nodes one chain.
	DeveloperP2PFlag = &cli.BoolFlag{
		Name:     "dev.p2p",
		Usage:    "Keep the p2p listener and dialling on under --dev (discovery stays off: peers come from --bootnodes and the Igneum bootnode list only)",
		Category: flags.DevCategory,
	}
	DeveloperFaucetFlag = &cli.StringFlag{
		Name:     "dev.faucet",
		Usage:    "The development chain's faucet address (fixed in code; accepted for older devnet scripts, any other address is refused)",
		Category: flags.DevCategory,
	}

	IdentityFlag = &cli.StringFlag{
		Name:     "identity",
		Usage:    "Custom node name",
		Category: flags.NetworkingCategory,
	}
	DocRootFlag = &flags.DirectoryFlag{
		Name:     "docroot",
		Usage:    "Document Root for HTTPClient file scheme",
		Value:    flags.DirectoryString(flags.HomeDir()),
		Category: flags.APICategory,
	}
	ExitWhenSyncedFlag = &cli.BoolFlag{
		Name:     "exitwhensynced",
		Usage:    "Exits after block synchronisation completes",
		Category: flags.EthCategory,
	}

	// Dump command options.
	IterativeOutputFlag = &cli.BoolFlag{
		Name:  "iterative",
		Usage: "Print streaming JSON iteratively, delimited by newlines",
		Value: true,
	}
	ExcludeStorageFlag = &cli.BoolFlag{
		Name:  "nostorage",
		Usage: "Exclude storage entries (save db lookups)",
	}
	IncludeIncompletesFlag = &cli.BoolFlag{
		Name:  "incompletes",
		Usage: "Include accounts for which we don't have the address (missing preimage)",
	}
	ExcludeCodeFlag = &cli.BoolFlag{
		Name:  "nocode",
		Usage: "Exclude contract code (save db lookups)",
	}
	StartKeyFlag = &cli.StringFlag{
		Name:  "start",
		Usage: "Start position. Either a hash or address",
		Value: "0x0000000000000000000000000000000000000000000000000000000000000000",
	}
	DumpLimitFlag = &cli.Uint64Flag{
		Name:  "limit",
		Usage: "Max number of elements (0 = no limit)",
		Value: 0,
	}

	defaultSyncMode = ethconfig.Defaults.SyncMode
	SnapshotFlag    = &cli.BoolFlag{
		Name:     "snapshot",
		Usage:    `Enables snapshot-database mode (default = enable)`,
		Value:    true,
		Category: flags.EthCategory,
	}
	LightKDFFlag = &cli.BoolFlag{
		Name:     "lightkdf",
		Usage:    "Reduce key-derivation RAM & CPU usage at some expense of KDF strength",
		Category: flags.AccountCategory,
	}
	EthRequiredBlocksFlag = &cli.StringFlag{
		Name:     "eth.requiredblocks",
		Usage:    "Comma separated block number-to-hash mappings to require for peering (<number>=<hash>)",
		Category: flags.EthCategory,
	}
	BloomFilterSizeFlag = &cli.Uint64Flag{
		Name:     "bloomfilter.size",
		Usage:    "Megabytes of memory allocated to bloom-filter for pruning",
		Value:    2048,
		Category: flags.EthCategory,
	}
	OverrideShanghai = &cli.Uint64Flag{
		Name:     "override.shanghai",
		Usage:    "Manually specify the Shanghai fork timestamp, overriding the bundled setting",
		Category: flags.EthCategory,
	}
	OverrideCancun = &cli.Uint64Flag{
		Name:     "override.cancun",
		Usage:    "Manually specify the Cancun fork timestamp, overriding the bundled setting",
		Category: flags.EthCategory,
	}
	OverrideVerkle = &cli.Uint64Flag{
		Name:     "override.verkle",
		Usage:    "Manually specify the Verkle fork timestamp, overriding the bundled setting",
		Category: flags.EthCategory,
	}
	SyncModeFlag = &flags.TextMarshalerFlag{
		Name:     "syncmode",
		Usage:    `Blockchain sync mode ("snap" or "full")`,
		Value:    &defaultSyncMode,
		Category: flags.StateCategory,
	}
	GCModeFlag = &cli.StringFlag{
		Name:     "gcmode",
		Usage:    `Blockchain garbage collection mode, only relevant in state.scheme=hash ("full", "archive")`,
		Value:    "full",
		Category: flags.StateCategory,
	}
	StateSchemeFlag = &cli.StringFlag{
		Name:     "state.scheme",
		Usage:    "Scheme to use for storing ethereum state ('hash' or 'path')",
		Category: flags.StateCategory,
	}
	StateHistoryFlag = &cli.Uint64Flag{
		Name:     "history.state",
		Usage:    "Number of recent blocks to retain state history for (default = 90,000 blocks, 0 = entire chain)",
		Value:    ethconfig.Defaults.StateHistory,
		Category: flags.StateCategory,
	}
	TransactionHistoryFlag = &cli.Uint64Flag{
		Name:     "history.transactions",
		Usage:    "Number of recent blocks to maintain transactions index for (default = about one year, 0 = entire chain)",
		Value:    ethconfig.Defaults.TransactionHistory,
		Category: flags.StateCategory,
	}
	// Light server and client settings
	LightServeFlag = &cli.IntFlag{
		Name:     "light.serve",
		Usage:    "Maximum percentage of time allowed for serving LES requests (multi-threaded processing allows values over 100)",
		Value:    ethconfig.Defaults.LightServ,
		Category: flags.LightCategory,
	}
	LightIngressFlag = &cli.IntFlag{
		Name:     "light.ingress",
		Usage:    "Incoming bandwidth limit for serving light clients (kilobytes/sec, 0 = unlimited)",
		Value:    ethconfig.Defaults.LightIngress,
		Category: flags.LightCategory,
	}
	LightEgressFlag = &cli.IntFlag{
		Name:     "light.egress",
		Usage:    "Outgoing bandwidth limit for serving light clients (kilobytes/sec, 0 = unlimited)",
		Value:    ethconfig.Defaults.LightEgress,
		Category: flags.LightCategory,
	}
	LightMaxPeersFlag = &cli.IntFlag{
		Name:     "light.maxpeers",
		Usage:    "Maximum number of light clients to serve, or light servers to attach to",
		Value:    ethconfig.Defaults.LightPeers,
		Category: flags.LightCategory,
	}
	UltraLightServersFlag = &cli.StringFlag{
		Name:     "ulc.servers",
		Usage:    "List of trusted ultra-light servers",
		Value:    strings.Join(ethconfig.Defaults.UltraLightServers, ","),
		Category: flags.LightCategory,
	}
	UltraLightFractionFlag = &cli.IntFlag{
		Name:     "ulc.fraction",
		Usage:    "Minimum % of trusted ultra-light servers required to announce a new head",
		Value:    ethconfig.Defaults.UltraLightFraction,
		Category: flags.LightCategory,
	}
	UltraLightOnlyAnnounceFlag = &cli.BoolFlag{
		Name:     "ulc.onlyannounce",
		Usage:    "Ultra light server sends announcements only",
		Category: flags.LightCategory,
	}
	LightNoPruneFlag = &cli.BoolFlag{
		Name:     "light.nopruning",
		Usage:    "Disable ancient light chain data pruning",
		Category: flags.LightCategory,
	}
	LightNoSyncServeFlag = &cli.BoolFlag{
		Name:     "light.nosyncserve",
		Usage:    "Enables serving light clients before syncing",
		Category: flags.LightCategory,
	}

	// Transaction pool settings
	TxPoolLocalsFlag = &cli.StringFlag{
		Name:     "txpool.locals",
		Usage:    "Comma separated accounts to treat as locals (no flush, priority inclusion)",
		Category: flags.TxPoolCategory,
	}
	TxPoolNoLocalsFlag = &cli.BoolFlag{
		Name:     "txpool.nolocals",
		Usage:    "Disables price exemptions for locally submitted transactions",
		Category: flags.TxPoolCategory,
	}
	TxPoolJournalFlag = &cli.StringFlag{
		Name:     "txpool.journal",
		Usage:    "Disk journal for local transaction to survive node restarts",
		Value:    ethconfig.Defaults.TxPool.Journal,
		Category: flags.TxPoolCategory,
	}
	TxPoolRejournalFlag = &cli.DurationFlag{
		Name:     "txpool.rejournal",
		Usage:    "Time interval to regenerate the local transaction journal",
		Value:    ethconfig.Defaults.TxPool.Rejournal,
		Category: flags.TxPoolCategory,
	}
	TxPoolPriceLimitFlag = &cli.Uint64Flag{
		Name:     "txpool.pricelimit",
		Usage:    "Minimum gas price tip to enforce for acceptance into the pool",
		Value:    ethconfig.Defaults.TxPool.PriceLimit,
		Category: flags.TxPoolCategory,
	}
	TxPoolPriceBumpFlag = &cli.Uint64Flag{
		Name:     "txpool.pricebump",
		Usage:    "Price bump percentage to replace an already existing transaction",
		Value:    ethconfig.Defaults.TxPool.PriceBump,
		Category: flags.TxPoolCategory,
	}
	TxPoolAccountSlotsFlag = &cli.Uint64Flag{
		Name:     "txpool.accountslots",
		Usage:    "Minimum number of executable transaction slots guaranteed per account",
		Value:    ethconfig.Defaults.TxPool.AccountSlots,
		Category: flags.TxPoolCategory,
	}
	TxPoolGlobalSlotsFlag = &cli.Uint64Flag{
		Name:     "txpool.globalslots",
		Usage:    "Maximum number of executable transaction slots for all accounts",
		Value:    ethconfig.Defaults.TxPool.GlobalSlots,
		Category: flags.TxPoolCategory,
	}
	TxPoolAccountQueueFlag = &cli.Uint64Flag{
		Name:     "txpool.accountqueue",
		Usage:    "Maximum number of non-executable transaction slots permitted per account",
		Value:    ethconfig.Defaults.TxPool.AccountQueue,
		Category: flags.TxPoolCategory,
	}
	TxPoolGlobalQueueFlag = &cli.Uint64Flag{
		Name:     "txpool.globalqueue",
		Usage:    "Maximum number of non-executable transaction slots for all accounts",
		Value:    ethconfig.Defaults.TxPool.GlobalQueue,
		Category: flags.TxPoolCategory,
	}
	TxPoolLifetimeFlag = &cli.DurationFlag{
		Name:     "txpool.lifetime",
		Usage:    "Maximum amount of time non-executable transaction are queued",
		Value:    ethconfig.Defaults.TxPool.Lifetime,
		Category: flags.TxPoolCategory,
	}
	// Performance tuning settings
	CacheFlag = &cli.IntFlag{
		Name:     "cache",
		Usage:    "Megabytes of memory allocated to internal caching (default = 4096 mainnet full node, 128 light mode)",
		Value:    1024,
		Category: flags.PerfCategory,
	}
	CacheDatabaseFlag = &cli.IntFlag{
		Name:     "cache.database",
		Usage:    "Percentage of cache memory allowance to use for database io",
		Value:    50,
		Category: flags.PerfCategory,
	}
	CacheTrieFlag = &cli.IntFlag{
		Name:     "cache.trie",
		Usage:    "Percentage of cache memory allowance to use for trie caching (default = 15% full mode, 30% archive mode)",
		Value:    15,
		Category: flags.PerfCategory,
	}
	CacheGCFlag = &cli.IntFlag{
		Name:     "cache.gc",
		Usage:    "Percentage of cache memory allowance to use for trie pruning (default = 25% full mode, 0% archive mode)",
		Value:    25,
		Category: flags.PerfCategory,
	}
	CacheSnapshotFlag = &cli.IntFlag{
		Name:     "cache.snapshot",
		Usage:    "Percentage of cache memory allowance to use for snapshot caching (default = 10% full mode, 20% archive mode)",
		Value:    10,
		Category: flags.PerfCategory,
	}
	CacheNoPrefetchFlag = &cli.BoolFlag{
		Name:     "cache.noprefetch",
		Usage:    "Disable heuristic state prefetch during block import (less CPU and disk IO, more time waiting for data)",
		Category: flags.PerfCategory,
	}
	CachePreimagesFlag = &cli.BoolFlag{
		Name:     "cache.preimages",
		Usage:    "Enable recording the SHA3/keccak preimages of trie keys",
		Category: flags.PerfCategory,
	}
	CacheLogSizeFlag = &cli.IntFlag{
		Name:     "cache.blocklogs",
		Usage:    "Size (in number of blocks) of the log cache for filtering",
		Category: flags.PerfCategory,
		Value:    ethconfig.Defaults.FilterLogCacheSize,
	}
	FDLimitFlag = &cli.IntFlag{
		Name:     "fdlimit",
		Usage:    "Raise the open file descriptor resource limit (default = system fd limit)",
		Category: flags.PerfCategory,
	}

	// Miner settings
	MiningEnabledFlag = &cli.BoolFlag{
		Name:     "mine",
		Usage:    "Enable mining",
		Category: flags.MinerCategory,
	}
	MinerThreadsFlag = &cli.IntFlag{
		Name:     "miner.threads",
		Usage:    "Number of CPU threads to use for mining",
		Value:    0,
		Category: flags.MinerCategory,
	}
	MinerNotifyFlag = &cli.StringFlag{
		Name:     "miner.notify",
		Usage:    "Comma separated HTTP URL list to notify of new work packages",
		Category: flags.MinerCategory,
	}
	MinerNotifyFullFlag = &cli.BoolFlag{
		Name:     "miner.notify.full",
		Usage:    "Notify with pending block headers instead of work packages",
		Category: flags.MinerCategory,
	}
	MinerGasLimitFlag = &cli.Uint64Flag{
		Name:     "miner.gaslimit",
		Usage:    "Target gas ceiling for mined blocks",
		Value:    ethconfig.Defaults.Miner.GasCeil,
		Category: flags.MinerCategory,
	}
	MinerGasPriceFlag = &flags.BigFlag{
		Name:     "miner.gasprice",
		Usage:    "Minimum gas price for mining a transaction",
		Value:    ethconfig.Defaults.Miner.GasPrice,
		Category: flags.MinerCategory,
	}
	MinerEtherbaseFlag = &cli.StringFlag{
		Name:     "miner.etherbase",
		Usage:    "0x prefixed public address for block mining rewards",
		Category: flags.MinerCategory,
	}
	MinerExtraDataFlag = &cli.StringFlag{
		Name:     "miner.extradata",
		Usage:    "Block extra data set by the miner (default = client version)",
		Category: flags.MinerCategory,
	}
	MinerRecommitIntervalFlag = &cli.DurationFlag{
		Name:     "miner.recommit",
		Usage:    "Time interval to recreate the block being mined",
		Value:    ethconfig.Defaults.Miner.Recommit,
		Category: flags.MinerCategory,
	}
	MinerNoVerifyFlag = &cli.BoolFlag{
		Name:     "miner.noverify",
		Usage:    "Disable remote sealing verification",
		Category: flags.MinerCategory,
	}

	// Account settings
	UnlockedAccountFlag = &cli.StringFlag{
		Name:     "unlock",
		Usage:    "Comma separated list of accounts to unlock",
		Value:    "",
		Category: flags.AccountCategory,
	}
	PasswordFileFlag = &cli.PathFlag{
		Name:      "password",
		Usage:     "Password file to use for non-interactive password input",
		TakesFile: true,
		Category:  flags.AccountCategory,
	}
	ExternalSignerFlag = &cli.StringFlag{
		Name:     "signer",
		Usage:    "External signer (url or path to ipc file)",
		Value:    "",
		Category: flags.AccountCategory,
	}
	InsecureUnlockAllowedFlag = &cli.BoolFlag{
		Name:     "allow-insecure-unlock",
		Usage:    "Allow insecure account unlocking when account-related RPCs are exposed by http",
		Category: flags.AccountCategory,
	}

	// EVM settings
	VMEnableDebugFlag = &cli.BoolFlag{
		Name:     "vmdebug",
		Usage:    "Record information useful for VM and contract debugging",
		Category: flags.VMCategory,
	}

	// API options.
	RPCGlobalGasCapFlag = &cli.Uint64Flag{
		Name:     "rpc.gascap",
		Usage:    "Sets a cap on gas that can be used in eth_call/estimateGas (0=infinite)",
		Value:    ethconfig.Defaults.RPCGasCap,
		Category: flags.APICategory,
	}
	RPCGlobalEVMTimeoutFlag = &cli.DurationFlag{
		Name:     "rpc.evmtimeout",
		Usage:    "Sets a timeout used for eth_call (0=infinite)",
		Value:    ethconfig.Defaults.RPCEVMTimeout,
		Category: flags.APICategory,
	}
	RPCGlobalTxFeeCapFlag = &cli.Float64Flag{
		Name:     "rpc.txfeecap",
		Usage:    "Sets a cap on transaction fee (in ether) that can be sent via the RPC APIs (0 = no cap)",
		Value:    ethconfig.Defaults.RPCTxFeeCap,
		Category: flags.APICategory,
	}
	// Authenticated RPC HTTP settings
	AuthListenFlag = &cli.StringFlag{
		Name:     "authrpc.addr",
		Usage:    "Listening address for authenticated APIs",
		Value:    node.DefaultConfig.AuthAddr,
		Category: flags.APICategory,
	}
	AuthPortFlag = &cli.IntFlag{
		Name:     "authrpc.port",
		Usage:    "Listening port for authenticated APIs",
		Value:    node.DefaultConfig.AuthPort,
		Category: flags.APICategory,
	}
	AuthVirtualHostsFlag = &cli.StringFlag{
		Name:     "authrpc.vhosts",
		Usage:    "Comma separated list of virtual hostnames from which to accept requests (server enforced). Accepts '*' wildcard.",
		Value:    strings.Join(node.DefaultConfig.AuthVirtualHosts, ","),
		Category: flags.APICategory,
	}
	JWTSecretFlag = &flags.DirectoryFlag{
		Name:     "authrpc.jwtsecret",
		Usage:    "Path to a JWT secret to use for authenticated RPC endpoints",
		Category: flags.APICategory,
	}

	// Logging and debug settings
	FakePoWFlag = &cli.BoolFlag{
		Name:     "fakepow",
		Usage:    "Disables proof-of-work verification",
		Category: flags.LoggingCategory, // FIXME(meowsbits) Logging, really? Not Developer?
	}
	NoCompactionFlag = &cli.BoolFlag{
		Name:     "nocompaction",
		Usage:    "Disables db compaction after import",
		Category: flags.LoggingCategory,
	}

	// MISC settings

	// RPC settings
	IPCDisabledFlag = &cli.BoolFlag{
		Name:     "ipcdisable",
		Usage:    "Disable the IPC-RPC server",
		Category: flags.APICategory,
	}
	IPCPathFlag = &flags.DirectoryFlag{
		Name:     "ipcpath",
		Usage:    "Filename for IPC socket/pipe within the datadir (explicit paths escape it)",
		Category: flags.APICategory,
	}
	HTTPEnabledFlag = &cli.BoolFlag{
		Name:     "http",
		Usage:    "Enable the HTTP-RPC server",
		Category: flags.APICategory,
	}
	HTTPListenAddrFlag = &cli.StringFlag{
		Name:     "http.addr",
		Usage:    "HTTP-RPC server listening interface",
		Value:    node.DefaultHTTPHost,
		Category: flags.APICategory,
	}
	HTTPPortFlag = &cli.IntFlag{
		Name:     "http.port",
		Usage:    "HTTP-RPC server listening port",
		Value:    node.DefaultHTTPPort,
		Category: flags.APICategory,
	}
	HTTPCORSDomainFlag = &cli.StringFlag{
		Name:     "http.corsdomain",
		Usage:    "Comma separated list of domains from which to accept cross origin requests (browser enforced)",
		Value:    "",
		Category: flags.APICategory,
	}
	HTTPVirtualHostsFlag = &cli.StringFlag{
		Name:     "http.vhosts",
		Usage:    "Comma separated list of virtual hostnames from which to accept requests (server enforced). Accepts '*' wildcard.",
		Value:    strings.Join(node.DefaultConfig.HTTPVirtualHosts, ","),
		Category: flags.APICategory,
	}
	HTTPApiFlag = &cli.StringFlag{
		Name:     "http.api",
		Usage:    "API's offered over the HTTP-RPC interface",
		Value:    "",
		Category: flags.APICategory,
	}
	HTTPPathPrefixFlag = &cli.StringFlag{
		Name:     "http.rpcprefix",
		Usage:    "HTTP path path prefix on which JSON-RPC is served. Use '/' to serve on all paths.",
		Value:    "",
		Category: flags.APICategory,
	}
	WSEnabledFlag = &cli.BoolFlag{
		Name:     "ws",
		Usage:    "Enable the WS-RPC server",
		Category: flags.APICategory,
	}
	WSListenAddrFlag = &cli.StringFlag{
		Name:     "ws.addr",
		Usage:    "WS-RPC server listening interface",
		Value:    node.DefaultWSHost,
		Category: flags.APICategory,
	}
	WSPortFlag = &cli.IntFlag{
		Name:     "ws.port",
		Usage:    "WS-RPC server listening port",
		Value:    node.DefaultWSPort,
		Category: flags.APICategory,
	}
	WSApiFlag = &cli.StringFlag{
		Name:     "ws.api",
		Usage:    "API's offered over the WS-RPC interface",
		Value:    "",
		Category: flags.APICategory,
	}
	WSAllowedOriginsFlag = &cli.StringFlag{
		Name:     "ws.origins",
		Usage:    "Origins from which to accept websockets requests",
		Value:    "",
		Category: flags.APICategory,
	}
	WSPathPrefixFlag = &cli.StringFlag{
		Name:     "ws.rpcprefix",
		Usage:    "HTTP path prefix on which JSON-RPC is served. Use '/' to serve on all paths.",
		Value:    "",
		Category: flags.APICategory,
	}
	ExecFlag = &cli.StringFlag{
		Name:     "exec",
		Usage:    "Execute JavaScript statement",
		Category: flags.APICategory,
	}
	PreloadJSFlag = &cli.StringFlag{
		Name:     "preload",
		Usage:    "Comma separated list of JavaScript files to preload into the console",
		Category: flags.APICategory,
	}
	AllowUnprotectedTxs = &cli.BoolFlag{
		Name:     "rpc.allow-unprotected-txs",
		Usage:    "Allow for unprotected (non EIP155 signed) transactions to be submitted via RPC",
		Category: flags.APICategory,
	}
	// RPCPublicFlag marks the HTTP and WebSocket listeners as the PUBLIC ones
	// the edge fronts (node.Config.RPCPublic): a per-source token bucket at
	// the chain config's rate and burst, and igneum_verifyBlock, the research
	// instrument, the sealer-to-sealer push and the admin, miner, personal
	// and debug namespaces refused (served on the mesh and the local IPC
	// only). SECURITY-GAPS.md; the project's order of 21:4x UK, 11 October 2026.
	RPCPublicFlag = &cli.BoolFlag{
		Name:     "rpc.public",
		Usage:    "Treat the HTTP and WS listeners as public: per-source rate limit at the chain's rate and burst; the dry run and the admin and write methods refused",
		Category: flags.APICategory,
	}
	// RPCPublicTrustedFlag names the edge hosts whose forwarded client
	// address is the public guard's source (node.Config.RPCPublicTrusted):
	// behind an edge that keys its own bucket per client, every client
	// draws its own bucket here too, instead of the edge's one.
	RPCPublicTrustedFlag = &cli.StringFlag{
		Name:     "rpc.public.trusted",
		Usage:    "Comma-separated edge hosts whose X-Forwarded-For client address the public rate limit keys on (the header is honoured from these peers alone)",
		Category: flags.APICategory,
	}
	// RPCPublicExemptFlag names the sources the public guard never charges
	// (node.Config.RPCPublicExempt; F-209): the mesh's own hosts or
	// prefixes, whose calls are not the public allowance's.
	RPCPublicExemptFlag = &cli.StringFlag{
		Name:     "rpc.public.exempt",
		Usage:    "Comma-separated hosts or CIDR prefixes the public rate limit never charges (the mesh's own callers; the method deny set still holds)",
		Category: flags.APICategory,
	}
	BatchRequestLimit = &cli.IntFlag{
		Name:     "rpc.batch-request-limit",
		Usage:    "Maximum number of requests in a batch",
		Value:    node.DefaultConfig.BatchRequestLimit,
		Category: flags.APICategory,
	}
	BatchResponseMaxSize = &cli.IntFlag{
		Name:     "rpc.batch-response-max-size",
		Usage:    "Maximum number of bytes returned from a batched call",
		Value:    node.DefaultConfig.BatchResponseMaxSize,
		Category: flags.APICategory,
	}
	EnablePersonal = &cli.BoolFlag{
		Name:     "rpc.enabledeprecatedpersonal",
		Usage:    "Enables the (deprecated) personal namespace",
		Category: flags.APICategory,
	}

	// Network Settings
	MaxPeersFlag = &cli.IntFlag{
		Name:     "maxpeers",
		Usage:    "Maximum number of network peers (network disabled if set to 0)",
		Value:    node.DefaultConfig.P2P.MaxPeers,
		Category: flags.NetworkingCategory,
	}
	MaxPendingPeersFlag = &cli.IntFlag{
		Name:     "maxpendpeers",
		Usage:    "Maximum number of pending connection attempts (defaults used if set to 0)",
		Value:    node.DefaultConfig.P2P.MaxPendingPeers,
		Category: flags.NetworkingCategory,
	}
	ListenPortFlag = &cli.IntFlag{
		Name:     "port",
		Usage:    "Network listening port",
		Value:    30303,
		Category: flags.NetworkingCategory,
	}
	BootnodesFlag = &cli.StringFlag{
		Name:     "bootnodes",
		Usage:    "Comma separated enode URLs for P2P discovery bootstrap",
		Value:    "",
		Category: flags.NetworkingCategory,
	}
	NodeKeyFileFlag = &cli.StringFlag{
		Name:     "nodekey",
		Usage:    "P2P node key file",
		Category: flags.NetworkingCategory,
	}
	NodeKeyHexFlag = &cli.StringFlag{
		Name:     "nodekeyhex",
		Usage:    "P2P node key as hex (for testing)",
		Category: flags.NetworkingCategory,
	}
	NATFlag = &cli.StringFlag{
		Name:     "nat",
		Usage:    "NAT port mapping mechanism (any|none|upnp|pmp|pmp:<IP>|extip:<IP>)",
		Value:    "any",
		Category: flags.NetworkingCategory,
	}
	NoDiscoverFlag = &cli.BoolFlag{
		Name:     "nodiscover",
		Usage:    "Disables the peer discovery mechanism (manual peer addition)",
		Category: flags.NetworkingCategory,
	}
	DiscoveryV4Flag = &cli.BoolFlag{
		Name:     "discovery.v4",
		Aliases:  []string{"discv4"},
		Usage:    "Enables the V4 discovery mechanism",
		Category: flags.NetworkingCategory,
		Value:    true,
	}
	DiscoveryV5Flag = &cli.BoolFlag{
		Name:     "discovery.v5",
		Aliases:  []string{"discv5"},
		Usage:    "Enables the experimental RLPx V5 (Topic Discovery) mechanism",
		Category: flags.NetworkingCategory,
	}
	NetrestrictFlag = &cli.StringFlag{
		Name:     "netrestrict",
		Usage:    "Restricts network communication to the given IP networks (CIDR masks)",
		Category: flags.NetworkingCategory,
	}
	DNSDiscoveryFlag = &cli.StringFlag{
		Name:     "discovery.dns",
		Usage:    "Sets DNS discovery entry points (use \"\" to disable DNS)",
		Category: flags.NetworkingCategory,
	}
	DiscoveryPortFlag = &cli.IntFlag{
		Name:     "discovery.port",
		Usage:    "Use a custom UDP port for P2P discovery",
		Value:    30303,
		Category: flags.NetworkingCategory,
	}

	// Console
	JSpathFlag = &flags.DirectoryFlag{
		Name:     "jspath",
		Usage:    "JavaScript root path for `loadScript`",
		Value:    flags.DirectoryString("."),
		Category: flags.APICategory,
	}
	HttpHeaderFlag = &cli.StringSliceFlag{
		Name:     "header",
		Aliases:  []string{"H"},
		Usage:    "Pass custom headers to the RPC server when using --" + RemoteDBFlag.Name + " or the geth attach console. This flag can be given multiple times.",
		Category: flags.APICategory,
	}

	// Gas price oracle settings
	GpoBlocksFlag = &cli.IntFlag{
		Name:     "gpo.blocks",
		Usage:    "Number of recent blocks to check for gas prices",
		Value:    ethconfig.Defaults.GPO.Blocks,
		Category: flags.GasPriceCategory,
	}
	GpoPercentileFlag = &cli.IntFlag{
		Name:     "gpo.percentile",
		Usage:    "Suggested gas price is the given percentile of a set of recent transaction gas prices",
		Value:    ethconfig.Defaults.GPO.Percentile,
		Category: flags.GasPriceCategory,
	}
	GpoMaxGasPriceFlag = &cli.Int64Flag{
		Name:     "gpo.maxprice",
		Usage:    "Maximum transaction priority fee (or gasprice before London fork) to be recommended by gpo",
		Value:    ethconfig.Defaults.GPO.MaxPrice.Int64(),
		Category: flags.GasPriceCategory,
	}
	GpoIgnoreGasPriceFlag = &cli.Int64Flag{
		Name:     "gpo.ignoreprice",
		Usage:    "Gas price below which gpo will ignore transactions",
		Value:    ethconfig.Defaults.GPO.IgnorePrice.Int64(),
		Category: flags.GasPriceCategory,
	}

	// Metrics flags
	MetricsEnabledFlag = &cli.BoolFlag{
		Name:     "metrics",
		Usage:    "Enable metrics collection and reporting",
		Category: flags.MetricsCategory,
	}
	MetricsEnabledExpensiveFlag = &cli.BoolFlag{
		Name:     "metrics.expensive",
		Usage:    "Enable expensive metrics collection and reporting",
		Category: flags.MetricsCategory,
	}

	// MetricsHTTPFlag defines the endpoint for a stand-alone metrics HTTP endpoint.
	// Since the pprof service enables sensitive/vulnerable behavior, this allows a user
	// to enable a public-OK metrics endpoint without having to worry about ALSO exposing
	// other profiling behavior or information.
	MetricsHTTPFlag = &cli.StringFlag{
		Name:     "metrics.addr",
		Usage:    `Enable stand-alone metrics HTTP server listening interface.`,
		Category: flags.MetricsCategory,
	}
	MetricsPortFlag = &cli.IntFlag{
		Name: "metrics.port",
		Usage: `Metrics HTTP server listening port.
Please note that --` + MetricsHTTPFlag.Name + ` must be set to start the server.`,
		Value:    metrics.DefaultConfig.Port,
		Category: flags.MetricsCategory,
	}
	MetricsEnableInfluxDBFlag = &cli.BoolFlag{
		Name:     "metrics.influxdb",
		Usage:    "Enable metrics export/push to an external InfluxDB database",
		Category: flags.MetricsCategory,
	}
	MetricsInfluxDBEndpointFlag = &cli.StringFlag{
		Name:     "metrics.influxdb.endpoint",
		Usage:    "InfluxDB API endpoint to report metrics to",
		Value:    metrics.DefaultConfig.InfluxDBEndpoint,
		Category: flags.MetricsCategory,
	}
	MetricsInfluxDBDatabaseFlag = &cli.StringFlag{
		Name:     "metrics.influxdb.database",
		Usage:    "InfluxDB database name to push reported metrics to",
		Value:    metrics.DefaultConfig.InfluxDBDatabase,
		Category: flags.MetricsCategory,
	}
	MetricsInfluxDBUsernameFlag = &cli.StringFlag{
		Name:     "metrics.influxdb.username",
		Usage:    "Username to authorize access to the database",
		Value:    metrics.DefaultConfig.InfluxDBUsername,
		Category: flags.MetricsCategory,
	}
	MetricsInfluxDBPasswordFlag = &cli.StringFlag{
		Name:     "metrics.influxdb.password",
		Usage:    "Password to authorize access to the database",
		Value:    metrics.DefaultConfig.InfluxDBPassword,
		Category: flags.MetricsCategory,
	}
	// Tags are part of every measurement sent to InfluxDB. Queries on tags are faster in InfluxDB.
	// For example `host` tag could be used so that we can group all nodes and average a measurement
	// across all of them, but also so that we can select a specific node and inspect its measurements.
	// https://docs.influxdata.com/influxdb/v1.4/concepts/key_concepts/#tag-key
	MetricsInfluxDBTagsFlag = &cli.StringFlag{
		Name:     "metrics.influxdb.tags",
		Usage:    "Comma-separated InfluxDB tags (key/values) attached to all measurements",
		Value:    metrics.DefaultConfig.InfluxDBTags,
		Category: flags.MetricsCategory,
	}
	EWASMInterpreterFlag = &cli.StringFlag{
		Name:  "vm.ewasm",
		Usage: "External ewasm configuration (default = built-in interpreter)",
		Value: "",
	}
	EVMInterpreterFlag = &cli.StringFlag{
		Name:  "vm.evm",
		Usage: "External EVM configuration (default = built-in interpreter)",
		Value: "",
	}

	MetricsEnableInfluxDBV2Flag = &cli.BoolFlag{
		Name:     "metrics.influxdbv2",
		Usage:    "Enable metrics export/push to an external InfluxDB v2 database",
		Category: flags.MetricsCategory,
	}

	MetricsInfluxDBTokenFlag = &cli.StringFlag{
		Name:     "metrics.influxdb.token",
		Usage:    "Token to authorize access to the database (v2 only)",
		Value:    metrics.DefaultConfig.InfluxDBToken,
		Category: flags.MetricsCategory,
	}

	MetricsInfluxDBBucketFlag = &cli.StringFlag{
		Name:     "metrics.influxdb.bucket",
		Usage:    "InfluxDB bucket name to push reported metrics to (v2 only)",
		Value:    metrics.DefaultConfig.InfluxDBBucket,
		Category: flags.MetricsCategory,
	}

	MetricsInfluxDBOrganizationFlag = &cli.StringFlag{
		Name:     "metrics.influxdb.organization",
		Usage:    "InfluxDB organization name (v2 only)",
		Value:    metrics.DefaultConfig.InfluxDBOrganization,
		Category: flags.MetricsCategory,
	}
)

var (
	// TestnetFlags is the flag group of all built-in supported testnets.
	TestnetFlags = []cli.Flag{}
	// NetworkFlags is the flag group of all built-in supported networks: Igneum
	// is one network (PA-03), so there is no network flag; --dev is the only
	// other chain and it is ephemeral.
	NetworkFlags = []cli.Flag{}

	// DatabaseFlags is the flag group of all database flags.
	DatabaseFlags = []cli.Flag{
		DataDirFlag,
		AncientFlag,
		RemoteDBFlag,
		DBEngineFlag,
		StateSchemeFlag,
		HttpHeaderFlag,
	}
)

// MakeDataDir retrieves the currently requested data directory, terminating
// if none (or the empty string) is specified. If the node is starting a testnet,
// then a subdirectory of the specified datadir will be used.
func MakeDataDir(ctx *cli.Context) string {
	if path := ctx.String(DataDirFlag.Name); path != "" {
		return dataDirPathForCtxChainConfig(ctx, path)
	}
	Fatalf("Cannot determine default data directory, please set manually (--%s)", DataDirFlag.Name)
	return ""
}

// setNodeKey creates a node key from set command line flags, either loading it
// from a file or as a specified hex value. If neither flags were provided, this
// method returns nil and an ephemeral key is to be generated.
func setNodeKey(ctx *cli.Context, cfg *p2p.Config) {
	var (
		hex  = ctx.String(NodeKeyHexFlag.Name)
		file = ctx.String(NodeKeyFileFlag.Name)
		key  *ecdsa.PrivateKey
		err  error
	)
	switch {
	case file != "" && hex != "":
		Fatalf("Options %q and %q are mutually exclusive", NodeKeyFileFlag.Name, NodeKeyHexFlag.Name)
	case file != "":
		if key, err = crypto.LoadECDSA(file); err != nil {
			Fatalf("Option %q: %v", NodeKeyFileFlag.Name, err)
		}
		cfg.PrivateKey = key
	case hex != "":
		if key, err = crypto.HexToECDSA(hex); err != nil {
			Fatalf("Option %q: %v", NodeKeyHexFlag.Name, err)
		}
		cfg.PrivateKey = key
	}
}

// setNodeUserIdent creates the user identifier from CLI flags.
func setNodeUserIdent(ctx *cli.Context, cfg *node.Config) {
	if identity := ctx.String(IdentityFlag.Name); len(identity) > 0 {
		cfg.UserIdent = identity
	}
}

// setBootstrapNodes creates a list of bootstrap nodes from the command line
// flags, reverting to pre-configured ones if none have been specified.
// Priority order for bootnodes configuration:
//
// 1. --bootnodes flag
// 2. Config file
// 3. Network preset flags (e.g. --classic)
// 4. default to mainnet nodes
func setBootstrapNodes(ctx *cli.Context, cfg *p2p.Config) {
	urls := params.IgneumBootnodes
	if ctx.IsSet(BootnodesFlag.Name) {
		urls = SplitAndTrim(ctx.String(BootnodesFlag.Name))
	} else if cfg.BootstrapNodes != nil {
		return // Already set by config file, don't apply defaults.
	}
	cfg.BootstrapNodes = mustParseBootnodes(urls)
}

func mustParseBootnodes(urls []string) []*enode.Node {
	nodes := make([]*enode.Node, 0, len(urls))
	for _, url := range urls {
		if url != "" {
			node, err := enode.Parse(enode.ValidSchemes, url)
			if err != nil {
				log.Crit("Bootstrap URL invalid", "enode", url, "err", err)
				return nil
			}
			nodes = append(nodes, node)
		}
	}
	return nodes
}

// setBootstrapNodesV5 creates a list of bootstrap nodes from the command line
// flags, reverting to pre-configured ones if none have been specified.
func setBootstrapNodesV5(ctx *cli.Context, cfg *p2p.Config) {
	urls := params.IgneumBootnodes
	switch {
	case ctx.IsSet(BootnodesFlag.Name):
		urls = SplitAndTrim(ctx.String(BootnodesFlag.Name))
	case cfg.BootstrapNodesV5 != nil:
		return // already set, don't apply defaults.
	}

	cfg.BootstrapNodesV5 = make([]*enode.Node, 0, len(urls))
	for _, url := range urls {
		if url != "" {
			node, err := enode.Parse(enode.ValidSchemes, url)
			if err != nil {
				log.Error("Bootstrap URL invalid", "enode", url, "err", err)
				continue
			}
			cfg.BootstrapNodesV5 = append(cfg.BootstrapNodesV5, node)
		}
	}
}

// setListenAddress creates TCP/UDP listening address strings from set command
// line flags
func setListenAddress(ctx *cli.Context, cfg *p2p.Config) {
	if ctx.IsSet(ListenPortFlag.Name) {
		cfg.ListenAddr = fmt.Sprintf(":%d", ctx.Int(ListenPortFlag.Name))
	}
	if ctx.IsSet(DiscoveryPortFlag.Name) {
		cfg.DiscAddr = fmt.Sprintf(":%d", ctx.Int(DiscoveryPortFlag.Name))
	}
}

// setNAT creates a port mapper from command line flags.
func setNAT(ctx *cli.Context, cfg *p2p.Config) {
	if ctx.IsSet(NATFlag.Name) {
		natif, err := nat.Parse(ctx.String(NATFlag.Name))
		if err != nil {
			Fatalf("Option %s: %v", NATFlag.Name, err)
		}
		cfg.NAT = natif
	}
}

// SplitAndTrim splits input separated by a comma
// and trims excessive white space from the substrings.
func SplitAndTrim(input string) (ret []string) {
	l := strings.Split(input, ",")
	for _, r := range l {
		if r = strings.TrimSpace(r); r != "" {
			ret = append(ret, r)
		}
	}
	return ret
}

// setHTTP creates the HTTP RPC listener interface string from the set
// command line flags, returning empty if the HTTP endpoint is disabled.
func setHTTP(ctx *cli.Context, cfg *node.Config) {
	if ctx.Bool(HTTPEnabledFlag.Name) {
		if cfg.HTTPHost == "" {
			cfg.HTTPHost = "127.0.0.1"
		}
		if ctx.IsSet(HTTPListenAddrFlag.Name) {
			cfg.HTTPHost = ctx.String(HTTPListenAddrFlag.Name)
		}
	}

	if ctx.IsSet(HTTPPortFlag.Name) {
		cfg.HTTPPort = ctx.Int(HTTPPortFlag.Name)
	}

	if ctx.IsSet(AuthListenFlag.Name) {
		cfg.AuthAddr = ctx.String(AuthListenFlag.Name)
	}

	if ctx.IsSet(AuthPortFlag.Name) {
		cfg.AuthPort = ctx.Int(AuthPortFlag.Name)
	}

	if ctx.IsSet(AuthVirtualHostsFlag.Name) {
		cfg.AuthVirtualHosts = SplitAndTrim(ctx.String(AuthVirtualHostsFlag.Name))
	}

	if ctx.IsSet(HTTPCORSDomainFlag.Name) {
		cfg.HTTPCors = SplitAndTrim(ctx.String(HTTPCORSDomainFlag.Name))
	}

	if ctx.IsSet(HTTPApiFlag.Name) {
		cfg.HTTPModules = SplitAndTrim(ctx.String(HTTPApiFlag.Name))
	}

	if ctx.IsSet(HTTPVirtualHostsFlag.Name) {
		cfg.HTTPVirtualHosts = SplitAndTrim(ctx.String(HTTPVirtualHostsFlag.Name))
	}

	if ctx.IsSet(HTTPPathPrefixFlag.Name) {
		cfg.HTTPPathPrefix = ctx.String(HTTPPathPrefixFlag.Name)
	}
	if ctx.IsSet(AllowUnprotectedTxs.Name) {
		cfg.AllowUnprotectedTxs = ctx.Bool(AllowUnprotectedTxs.Name)
	}

	if ctx.IsSet(RPCPublicFlag.Name) {
		cfg.RPCPublic = ctx.Bool(RPCPublicFlag.Name)
	}
	if v := strings.TrimSpace(ctx.String(RPCPublicTrustedFlag.Name)); v != "" {
		cfg.RPCPublicTrusted = nil
		for _, h := range strings.Split(v, ",") {
			if h = strings.TrimSpace(h); h != "" {
				cfg.RPCPublicTrusted = append(cfg.RPCPublicTrusted, h)
			}
		}
	}
	if v := strings.TrimSpace(ctx.String(RPCPublicExemptFlag.Name)); v != "" {
		cfg.RPCPublicExempt = nil
		for _, h := range strings.Split(v, ",") {
			if h = strings.TrimSpace(h); h != "" {
				cfg.RPCPublicExempt = append(cfg.RPCPublicExempt, h)
			}
		}
	}

	if ctx.IsSet(BatchRequestLimit.Name) {
		cfg.BatchRequestLimit = ctx.Int(BatchRequestLimit.Name)
	}

	if ctx.IsSet(BatchResponseMaxSize.Name) {
		cfg.BatchResponseMaxSize = ctx.Int(BatchResponseMaxSize.Name)
	}
}

// setWS creates the WebSocket RPC listener interface string from the set
// command line flags, returning empty if the HTTP endpoint is disabled.
func setWS(ctx *cli.Context, cfg *node.Config) {
	if ctx.Bool(WSEnabledFlag.Name) {
		if cfg.WSHost == "" {
			cfg.WSHost = "127.0.0.1"
		}
		if ctx.IsSet(WSListenAddrFlag.Name) {
			cfg.WSHost = ctx.String(WSListenAddrFlag.Name)
		}
	}
	if ctx.IsSet(WSPortFlag.Name) {
		cfg.WSPort = ctx.Int(WSPortFlag.Name)
	}

	if ctx.IsSet(WSAllowedOriginsFlag.Name) {
		cfg.WSOrigins = SplitAndTrim(ctx.String(WSAllowedOriginsFlag.Name))
	}

	if ctx.IsSet(WSApiFlag.Name) {
		cfg.WSModules = SplitAndTrim(ctx.String(WSApiFlag.Name))
	}

	if ctx.IsSet(WSPathPrefixFlag.Name) {
		cfg.WSPathPrefix = ctx.String(WSPathPrefixFlag.Name)
	}
}

// setIPC creates an IPC path configuration from the set command line flags,
// returning an empty string if IPC was explicitly disabled, or the set path.
func setIPC(ctx *cli.Context, cfg *node.Config) {
	CheckExclusive(ctx, IPCDisabledFlag, IPCPathFlag)
	switch {
	case ctx.Bool(IPCDisabledFlag.Name):
		cfg.IPCPath = ""
	case ctx.IsSet(IPCPathFlag.Name):
		cfg.IPCPath = ctx.String(IPCPathFlag.Name)
	}
}

// setLes shows the deprecation warnings for LES flags.
func setLes(ctx *cli.Context, cfg *ethconfig.Config) {
	if ctx.IsSet(LightServeFlag.Name) {
		log.Warn("The light server has been deprecated, please remove this flag", "flag", LightServeFlag.Name)
	}
	if ctx.IsSet(LightIngressFlag.Name) {
		log.Warn("The light server has been deprecated, please remove this flag", "flag", LightIngressFlag.Name)
	}
	if ctx.IsSet(LightEgressFlag.Name) {
		log.Warn("The light server has been deprecated, please remove this flag", "flag", LightEgressFlag.Name)
	}
	if ctx.IsSet(LightMaxPeersFlag.Name) {
		log.Warn("The light server has been deprecated, please remove this flag", "flag", LightMaxPeersFlag.Name)
	}
	if ctx.IsSet(UltraLightServersFlag.Name) {
		cfg.UltraLightServers = strings.Split(ctx.String(UltraLightServersFlag.Name), ",")
	}
	if ctx.IsSet(UltraLightFractionFlag.Name) {
		cfg.UltraLightFraction = ctx.Int(UltraLightFractionFlag.Name)
	}
	if cfg.UltraLightFraction <= 0 && cfg.UltraLightFraction > 100 {
		log.Error("Ultra light fraction is invalid", "had", cfg.UltraLightFraction, "updated", ethconfig.Defaults.UltraLightFraction)
		cfg.UltraLightFraction = ethconfig.Defaults.UltraLightFraction
	}
	if ctx.IsSet(UltraLightOnlyAnnounceFlag.Name) {
		cfg.UltraLightOnlyAnnounce = ctx.Bool(UltraLightOnlyAnnounceFlag.Name)
	}
	if ctx.IsSet(LightNoPruneFlag.Name) {
		log.Warn("The light server has been deprecated, please remove this flag", "flag", LightNoPruneFlag.Name)
	}
	if ctx.IsSet(LightNoSyncServeFlag.Name) {
		log.Warn("The light server has been deprecated, please remove this flag", "flag", LightNoSyncServeFlag.Name)
	}
}

// MakeDatabaseHandles raises out the number of allowed file handles per process
// for Geth and returns half of the allowance to assign to the database.
func MakeDatabaseHandles(max int) int {
	limit, err := fdlimit.Maximum()
	if err != nil {
		Fatalf("Failed to retrieve file descriptor allowance: %v", err)
	}
	switch {
	case max == 0:
		// User didn't specify a meaningful value, use system limits
	case max < 128:
		// User specified something unhealthy, just use system defaults
		log.Error("File descriptor limit invalid (<128)", "had", max, "updated", limit)
	case max > limit:
		// User requested more than the OS allows, notify that we can't allocate it
		log.Warn("Requested file descriptors denied by OS", "req", max, "limit", limit)
	default:
		// User limit is meaningful and within allowed range, use that
		limit = max
	}
	raised, err := fdlimit.Raise(uint64(limit))
	if err != nil {
		Fatalf("Failed to raise file descriptor allowance: %v", err)
	}
	return int(raised / 2) // Leave half for networking and other stuff
}

// MakeAddress converts an account specified directly as a hex encoded string or
// a key index in the key store to an internal account representation.
func MakeAddress(ks *keystore.KeyStore, account string) (accounts.Account, error) {
	// If the specified account is a valid address, return it
	if common.IsHexAddress(account) {
		return accounts.Account{Address: common.HexToAddress(account)}, nil
	}
	// Otherwise try to interpret the account as a keystore index
	index, err := strconv.Atoi(account)
	if err != nil || index < 0 {
		return accounts.Account{}, fmt.Errorf("invalid account address or index %q", account)
	}
	log.Warn("-------------------------------------------------------------------")
	log.Warn("Referring to accounts by order in the keystore folder is dangerous!")
	log.Warn("This functionality is deprecated and will be removed in the future!")
	log.Warn("Please use explicit addresses! (can search via `geth account list`)")
	log.Warn("-------------------------------------------------------------------")

	accs := ks.Accounts()
	if len(accs) <= index {
		return accounts.Account{}, fmt.Errorf("index %d higher than number of accounts %d", index, len(accs))
	}
	return accs[index], nil
}

// setEtherbase retrieves the etherbase from the directly specified command line flags.
// devAlgorithmBlock is the engine block's algorithm name and parameter
// document as the development flags give them (nil when the flags name
// none): the repair source for a stored development configuration whose
// engine block lost its algorithm.
func devAlgorithmBlock(ctx *cli.Context) *ctypes.IgneumConfig {
	switch algo := ctx.String(DeveloperAlgorithmFlag.Name); algo {
	case "":
		return nil
	case igneumhash.Name:
		_, p, err := igneumhash.DecodeChainParams([]byte(ctx.String(DeveloperAlgorithmParamsFlag.Name)))
		if err != nil {
			return nil
		}
		return &ctypes.IgneumConfig{Algorithm: igneumhash.Name, AlgorithmParams: igneumhash.EncodeChainParams(params.IgneumDevChainID.Uint64(), p)}
	default:
		return &ctypes.IgneumConfig{Algorithm: algo}
	}
}

func setEtherbase(ctx *cli.Context, cfg *ethconfig.Config) {
	if !ctx.IsSet(MinerEtherbaseFlag.Name) {
		return
	}
	addr := ctx.String(MinerEtherbaseFlag.Name)
	if strings.HasPrefix(addr, "0x") || strings.HasPrefix(addr, "0X") {
		addr = addr[2:]
	}
	b, err := hex.DecodeString(addr)
	if err != nil || len(b) != common.AddressLength {
		Fatalf("-%s: invalid etherbase address %q", MinerEtherbaseFlag.Name, addr)
		return
	}
	cfg.Miner.Etherbase = common.BytesToAddress(b)
}

// MakePasswordList reads password lines from the file specified by the global --password flag.
func MakePasswordList(ctx *cli.Context) []string {
	path := ctx.Path(PasswordFileFlag.Name)
	if path == "" {
		return nil
	}
	text, err := os.ReadFile(path)
	if err != nil {
		Fatalf("Failed to read password file: %v", err)
	}
	lines := strings.Split(string(text), "\n")
	// Sanitise DOS line endings.
	for i := range lines {
		lines[i] = strings.TrimRight(lines[i], "\r")
	}
	return lines
}

func SetP2PConfig(ctx *cli.Context, cfg *p2p.Config) {
	setNodeKey(ctx, cfg)
	setNAT(ctx, cfg)
	setListenAddress(ctx, cfg)
	setBootstrapNodes(ctx, cfg)
	setBootstrapNodesV5(ctx, cfg)

	if ctx.IsSet(MaxPeersFlag.Name) {
		cfg.MaxPeers = ctx.Int(MaxPeersFlag.Name)
	}
	ethPeers := cfg.MaxPeers
	log.Info("Maximum peer count", "ETH", ethPeers, "total", cfg.MaxPeers)

	if ctx.IsSet(MaxPendingPeersFlag.Name) {
		cfg.MaxPendingPeers = ctx.Int(MaxPendingPeersFlag.Name)
	}
	if ctx.IsSet(NoDiscoverFlag.Name) {
		cfg.NoDiscovery = true
	}

	CheckExclusive(ctx, DiscoveryV4Flag, NoDiscoverFlag)
	CheckExclusive(ctx, DiscoveryV5Flag, NoDiscoverFlag)
	cfg.DiscoveryV4 = ctx.Bool(DiscoveryV4Flag.Name)
	cfg.DiscoveryV5 = ctx.Bool(DiscoveryV5Flag.Name)

	if netrestrict := ctx.String(NetrestrictFlag.Name); netrestrict != "" {
		list, err := netutil.ParseNetlist(netrestrict)
		if err != nil {
			Fatalf("Option %q: %v", NetrestrictFlag.Name, err)
		}
		cfg.NetRestrict = list
	}

	if ctx.Bool(DeveloperFlag.Name) || ctx.Bool(DeveloperPoWFlag.Name) {
		// --dev mode never discovers peers. Without --dev.p2p it has no p2p at
		// all; with it (the multi-node devnet) the listener and dialling stay on
		// and the peers are the static lists only.
		cfg.NoDiscovery = true
		cfg.DiscoveryV5 = false
		if !ctx.Bool(DeveloperP2PFlag.Name) {
			cfg.MaxPeers = 0
			cfg.ListenAddr = ""
			cfg.NoDial = true
		} else {
			// With discovery off, the bootnode lists would never be dialled; on
			// the devnet they are the static peers.
			cfg.StaticNodes = append(cfg.StaticNodes, cfg.BootstrapNodes...)
		}
	}
}

// SetNodeConfig applies node-related command line flags to the config.
func SetNodeConfig(ctx *cli.Context, cfg *node.Config) {
	SetP2PConfig(ctx, &cfg.P2P)
	setIPC(ctx, cfg)
	setHTTP(ctx, cfg)
	setWS(ctx, cfg)
	setNodeUserIdent(ctx, cfg)
	SetDataDir(ctx, cfg)
	setSmartCard(ctx, cfg)

	if ctx.IsSet(JWTSecretFlag.Name) {
		cfg.JWTSecret = ctx.String(JWTSecretFlag.Name)
	}

	if ctx.IsSet(EnablePersonal.Name) {
		cfg.EnablePersonal = true
	}

	if ctx.IsSet(ExternalSignerFlag.Name) {
		cfg.ExternalSigner = ctx.String(ExternalSignerFlag.Name)
	}

	if ctx.IsSet(KeyStoreDirFlag.Name) {
		cfg.KeyStoreDir = ctx.String(KeyStoreDirFlag.Name)
	}
	if ctx.IsSet(DeveloperFlag.Name) {
		cfg.UseLightweightKDF = true
	}
	if ctx.IsSet(LightKDFFlag.Name) {
		cfg.UseLightweightKDF = ctx.Bool(LightKDFFlag.Name)
	}
	if ctx.IsSet(NoUSBFlag.Name) || cfg.NoUSB {
		log.Warn("Option nousb is deprecated and USB is deactivated by default. Use --usb to enable")
	}
	if ctx.IsSet(USBFlag.Name) {
		cfg.USB = ctx.Bool(USBFlag.Name)

		// We handle configuration of HD paths only if --usb is set to a truthy value.
		if cfg.USB {
			// Flag --usb.pathid allows configuration for arbitrary SLIP-0044 values.
			if ctx.IsSet(USBPathIDFlag.Name) {
				pathID := ctx.Uint64(USBPathIDFlag.Name)
				if pathID > math.MaxUint32 {
					Fatalf("Invalid USB path ID (exceeds uint32): %d", pathID)
				}
				accounts.SetCoinTypeConfiguration(uint32(pathID))
				log.Info("Using custom HD derivation path", "pathid", uint32(pathID), "basepath", accounts.DefaultBaseDerivationPath)
			} else {
				// Set default hd path based on --chain configuration flags, if any.
				// Set default derivation path to a testnet value if we're configuring for a testnet.
				for _, f := range TestnetFlags {
					name := f.Names()[0] // This won't overflow because the flags always have a Name, because they are defined as --name.
					if ctx.IsSet(name) && ctx.Bool(name) {
						accounts.SetCoinTypeConfiguration(accounts.BIP0044CoinTypeTestnet)
						log.Info("Using testnet HD derivation path", "basepath", accounts.DefaultBaseDerivationPath)
						break
					}
				}

			}
		}
	}
	if ctx.IsSet(InsecureUnlockAllowedFlag.Name) {
		cfg.InsecureUnlockAllowed = ctx.Bool(InsecureUnlockAllowedFlag.Name)
	}
	if ctx.IsSet(DBEngineFlag.Name) {
		dbEngine := ctx.String(DBEngineFlag.Name)
		if dbEngine != "leveldb" && dbEngine != "pebble" {
			Fatalf("Invalid choice for db.engine '%s', allowed 'leveldb' or 'pebble'", dbEngine)
		}
		log.Info(fmt.Sprintf("Using %s as db engine", dbEngine))
		cfg.DBEngine = dbEngine
	}
	// deprecation notice for log debug flags (TODO: find a more appropriate place to put these?)
	if ctx.IsSet(LogBacktraceAtFlag.Name) {
		log.Warn("log.backtrace flag is deprecated")
	}
	if ctx.IsSet(LogDebugFlag.Name) {
		log.Warn("log.debug flag is deprecated")
	}
}

func setSmartCard(ctx *cli.Context, cfg *node.Config) {
	// Skip enabling smartcards if no path is set
	path := ctx.String(SmartCardDaemonPathFlag.Name)
	if path == "" {
		return
	}
	// Sanity check that the smartcard path is valid
	fi, err := os.Stat(path)
	if err != nil {
		log.Info("Smartcard socket not found, disabling", "err", err)
		return
	}
	if fi.Mode()&os.ModeType != os.ModeSocket {
		log.Error("Invalid smartcard daemon path", "path", path, "type", fi.Mode().String())
		return
	}
	// Smartcard daemon path exists and is a socket, enable it
	cfg.SmartCardDaemonPath = path
}

func dataDirPathForCtxChainConfig(ctx *cli.Context, baseDataDirPath string) string {
	return baseDataDirPath // one network (PA-03)
}

func SetDataDir(ctx *cli.Context, cfg *node.Config) {
	switch {
	case ctx.IsSet(DataDirFlag.Name):
		cfg.DataDir = ctx.String(DataDirFlag.Name)

	case ctx.Bool(DeveloperFlag.Name) || ctx.Bool(DeveloperPoWFlag.Name):
		cfg.DataDir = "" // unless explicitly requested, use memory databases

	case cfg.DataDir == vars.DefaultDataDir():
		cfg.DataDir = dataDirPathForCtxChainConfig(ctx, vars.DefaultDataDir())
	}
}

func setGPO(ctx *cli.Context, cfg *gasprice.Config) {
	if ctx.IsSet(GpoBlocksFlag.Name) {
		cfg.Blocks = ctx.Int(GpoBlocksFlag.Name)
	}
	if ctx.IsSet(GpoPercentileFlag.Name) {
		cfg.Percentile = ctx.Int(GpoPercentileFlag.Name)
	}
	if ctx.IsSet(GpoMaxGasPriceFlag.Name) {
		cfg.MaxPrice = big.NewInt(ctx.Int64(GpoMaxGasPriceFlag.Name))
	}
	if ctx.IsSet(GpoIgnoreGasPriceFlag.Name) {
		cfg.IgnorePrice = big.NewInt(ctx.Int64(GpoIgnoreGasPriceFlag.Name))
	}
}

func setTxPool(ctx *cli.Context, cfg *legacypool.Config) {
	if ctx.IsSet(TxPoolLocalsFlag.Name) {
		locals := strings.Split(ctx.String(TxPoolLocalsFlag.Name), ",")
		for _, account := range locals {
			if trimmed := strings.TrimSpace(account); !common.IsHexAddress(trimmed) {
				Fatalf("Invalid account in --txpool.locals: %s", trimmed)
			} else {
				cfg.Locals = append(cfg.Locals, common.HexToAddress(account))
			}
		}
	}
	if ctx.IsSet(TxPoolNoLocalsFlag.Name) {
		cfg.NoLocals = ctx.Bool(TxPoolNoLocalsFlag.Name)
	}
	if ctx.IsSet(TxPoolJournalFlag.Name) {
		cfg.Journal = ctx.String(TxPoolJournalFlag.Name)
	}
	if ctx.IsSet(TxPoolRejournalFlag.Name) {
		cfg.Rejournal = ctx.Duration(TxPoolRejournalFlag.Name)
	}
	if ctx.IsSet(TxPoolPriceLimitFlag.Name) {
		cfg.PriceLimit = ctx.Uint64(TxPoolPriceLimitFlag.Name)
	}
	if ctx.IsSet(TxPoolPriceBumpFlag.Name) {
		cfg.PriceBump = ctx.Uint64(TxPoolPriceBumpFlag.Name)
	}
	if ctx.IsSet(TxPoolAccountSlotsFlag.Name) {
		cfg.AccountSlots = ctx.Uint64(TxPoolAccountSlotsFlag.Name)
	}
	if ctx.IsSet(TxPoolGlobalSlotsFlag.Name) {
		cfg.GlobalSlots = ctx.Uint64(TxPoolGlobalSlotsFlag.Name)
	}
	if ctx.IsSet(TxPoolAccountQueueFlag.Name) {
		cfg.AccountQueue = ctx.Uint64(TxPoolAccountQueueFlag.Name)
	}
	if ctx.IsSet(TxPoolGlobalQueueFlag.Name) {
		cfg.GlobalQueue = ctx.Uint64(TxPoolGlobalQueueFlag.Name)
	}
	if ctx.IsSet(TxPoolLifetimeFlag.Name) {
		cfg.Lifetime = ctx.Duration(TxPoolLifetimeFlag.Name)
	}
}

func homeDir() string {
	if home := os.Getenv("HOME"); home != "" {
		return home
	}
	if usr, err := user.Current(); err == nil {
		return usr.HomeDir
	}
	return ""
}

func setMiner(ctx *cli.Context, cfg *miner.Config) {
	if ctx.IsSet(MinerNotifyFlag.Name) {
		cfg.Notify = strings.Split(ctx.String(MinerNotifyFlag.Name), ",")
	}
	cfg.NotifyFull = ctx.Bool(MinerNotifyFullFlag.Name)
	if ctx.IsSet(MinerExtraDataFlag.Name) {
		cfg.ExtraData = []byte(ctx.String(MinerExtraDataFlag.Name))
	}
	if ctx.IsSet(MinerGasLimitFlag.Name) {
		cfg.GasCeil = ctx.Uint64(MinerGasLimitFlag.Name)
	}
	if ctx.IsSet(MinerGasPriceFlag.Name) {
		cfg.GasPrice = flags.GlobalBig(ctx, MinerGasPriceFlag.Name)
	}
	if ctx.IsSet(MinerRecommitIntervalFlag.Name) {
		cfg.Recommit = ctx.Duration(MinerRecommitIntervalFlag.Name)
	}
	if ctx.IsSet(MinerNoVerifyFlag.Name) {
		cfg.Noverify = ctx.Bool(MinerNoVerifyFlag.Name)
	}
}

func setRequiredBlocks(ctx *cli.Context, cfg *ethconfig.Config) {
	requiredBlocks := ctx.String(EthRequiredBlocksFlag.Name)
	if requiredBlocks == "" {
		if ctx.IsSet(LegacyWhitelistFlag.Name) {
			log.Warn("The flag --whitelist is deprecated and will be removed, please use --eth.requiredblocks")
			requiredBlocks = ctx.String(LegacyWhitelistFlag.Name)
		} else {
			return
		}
	}
	cfg.RequiredBlocks = make(map[uint64]common.Hash)
	for _, entry := range strings.Split(requiredBlocks, ",") {
		parts := strings.Split(entry, "=")
		if len(parts) != 2 {
			Fatalf("Invalid required block entry: %s", entry)
		}
		number, err := strconv.ParseUint(parts[0], 0, 64)
		if err != nil {
			Fatalf("Invalid required block number %s: %v", parts[0], err)
		}
		var hash common.Hash
		if err = hash.UnmarshalText([]byte(parts[1])); err != nil {
			Fatalf("Invalid required block hash %s: %v", parts[1], err)
		}
		cfg.RequiredBlocks[number] = hash
	}
}

// CheckExclusive verifies that only a single instance of the provided flags was
// set by the user. Each flag might optionally be followed by a string type to
// specialize it further.
func CheckExclusive(ctx *cli.Context, args ...interface{}) {
	set := make([]string, 0, 1)
	for i := 0; i < len(args); i++ {
		// Make sure the next argument is a flag and skip if not set
		flag, ok := args[i].(cli.Flag)
		if !ok {
			panic(fmt.Sprintf("invalid argument, not cli.Flag type: %T", args[i]))
		}
		// Check if next arg extends current and expand its name if so
		name := flag.Names()[0]

		if i+1 < len(args) {
			switch option := args[i+1].(type) {
			case string:
				// Extended flag check, make sure value set doesn't conflict with passed in option
				if ctx.String(flag.Names()[0]) == option {
					name += "=" + option
					set = append(set, "--"+name)
				}
				// shift arguments and continue
				i++
				continue

			case cli.Flag:
			default:
				panic(fmt.Sprintf("invalid argument, not cli.Flag or string extension: %T", args[i+1]))
			}
		}
		// Mark the flag if it's set
		if ctx.IsSet(flag.Names()[0]) {
			set = append(set, "--"+name)
		}
	}
	if len(set) > 1 {
		Fatalf("Flags %v can't be used at the same time", strings.Join(set, ", "))
	}
}

// SetEthConfig applies eth-related command line flags to the config.
func SetEthConfig(ctx *cli.Context, stack *node.Node, cfg *ethconfig.Config) {
	// Avoid conflicting network flags
	CheckExclusive(ctx, DeveloperFlag, DeveloperPoWFlag)
	CheckExclusive(ctx, LightServeFlag, SyncModeFlag, "light")
	CheckExclusive(ctx, DeveloperFlag, DeveloperPoWFlag, ExternalSignerFlag) // Can't use both ephemeral unlocked and external signer

	setEtherbase(ctx, cfg)
	setGPO(ctx, &cfg.GPO)
	setTxPool(ctx, &cfg.TxPool)
	cfg.FakePoW = ctx.Bool(FakePoWFlag.Name)
	cfg.PiVerify = ctx.Bool(IgneumPiVerifyFlag.Name)
	cfg.PoolUnsafeVerifier = ctx.Bool(DeveloperPoolUnsafeVerifierFlag.Name)
	cfg.PoolPrune = ctx.Bool(PoolPruneFlag.Name)
	cfg.PoolPruneKeep = ctx.Uint64(PoolPruneKeepFlag.Name)
	if cfg.PoolPruneKeep < 256 {
		cfg.PoolPruneKeep = 256
	}
	cfg.PiProver = ctx.String(IgneumPiProverFlag.Name)
	cfg.PiProverFallback = ctx.String(IgneumPiProverFallbackFlag.Name)
	cfg.VerifierCore = ctx.Int(IgneumVerifierCoreFlag.Name)
	cfg.PiResident = ctx.Bool(IgneumPiResidentFlag.Name)
	cfg.VerifyBudgetMs = ctx.Uint64(IgneumVerifyBudgetFlag.Name)
	if ctx.IsSet(IgneumCoordinateFlag.Name) {
		c := ctx.Uint(IgneumCoordinateFlag.Name)
		if c >= uint(vector.MaxCoordinates) {
			Fatalf("Option %q: the coordinate is 0 to %d, got %d", IgneumCoordinateFlag.Name, vector.MaxCoordinates-1, c)
		}
		cc := uint8(c)
		cfg.Coordinate = &cc
	}
	cfg.ResidentDataset = ctx.Bool(IgneumResidentDatasetFlag.Name)
	cfg.SealWorker = ctx.String(IgneumSealWorkerFlag.Name)
	cfg.SealWorkerDevice = ctx.String(IgneumSealWorkerDeviceFlag.Name)
	cfg.Endorse = ctx.Bool(IgneumEndorseFlag.Name)
	cfg.PropagateAll = ctx.Bool(IgneumPropagateAllFlag.Name)
	if v := strings.TrimSpace(ctx.String(IgneumEndorseToFlag.Name)); v != "" {
		for _, u := range strings.Split(v, ",") {
			if u = strings.TrimSpace(u); u != "" {
				cfg.EndorseTo = append(cfg.EndorseTo, u)
			}
		}
	}
	setMiner(ctx, &cfg.Miner)
	setRequiredBlocks(ctx, cfg)
	setLes(ctx, cfg)

	// Cap the cache allowance and tune the garbage collector
	mem, err := gopsutil.VirtualMemory()
	if err == nil {
		if 32<<(^uintptr(0)>>63) == 32 && mem.Total > 2*1024*1024*1024 {
			log.Warn("Lowering memory allowance on 32bit arch", "available", mem.Total/1024/1024, "addressable", 2*1024)
			mem.Total = 2 * 1024 * 1024 * 1024
		}
		allowance := int(mem.Total / 1024 / 1024 / 3)
		if cache := ctx.Int(CacheFlag.Name); cache > allowance {
			log.Warn("Sanitizing cache to Go's GC limits", "provided", cache, "updated", allowance)
			ctx.Set(CacheFlag.Name, strconv.Itoa(allowance))
		}
	}
	// Ensure Go's GC ignores the database cache for trigger percentage
	cache := ctx.Int(CacheFlag.Name)
	gogc := math.Max(20, math.Min(100, 100/(float64(cache)/1024)))

	log.Debug("Sanitizing Go's GC trigger", "percent", int(gogc))
	godebug.SetGCPercent(int(gogc))

	if ctx.IsSet(SyncModeFlag.Name) {
		cfg.SyncMode = *flags.GlobalTextMarshaler(ctx, SyncModeFlag.Name).(*downloader.SyncMode)
	}

	if ctx.IsSet(CacheFlag.Name) || ctx.IsSet(CacheDatabaseFlag.Name) {
		cfg.DatabaseCache = ctx.Int(CacheFlag.Name) * ctx.Int(CacheDatabaseFlag.Name) / 100
	}
	cfg.DatabaseHandles = MakeDatabaseHandles(ctx.Int(FDLimitFlag.Name))
	if ctx.IsSet(AncientFlag.Name) {
		cfg.DatabaseFreezer = ctx.String(AncientFlag.Name)
	}

	if gcmode := ctx.String(GCModeFlag.Name); gcmode != "full" && gcmode != gcModeArchive {
		Fatalf("--%s must be either 'full' or 'archive'", GCModeFlag.Name)
	}
	if ctx.IsSet(GCModeFlag.Name) {
		cfg.NoPruning = ctx.String(GCModeFlag.Name) == gcModeArchive
	}
	if ctx.IsSet(CacheNoPrefetchFlag.Name) {
		cfg.NoPrefetch = ctx.Bool(CacheNoPrefetchFlag.Name)
	}
	// Read the value from the flag no matter if it's set or not.
	cfg.Preimages = ctx.Bool(CachePreimagesFlag.Name)
	if cfg.NoPruning && !cfg.Preimages {
		cfg.Preimages = true
		log.Info("Enabling recording of key preimages since archive mode is used")
	}
	if ctx.IsSet(StateHistoryFlag.Name) {
		cfg.StateHistory = ctx.Uint64(StateHistoryFlag.Name)
	}
	if ctx.IsSet(StateSchemeFlag.Name) {
		cfg.StateScheme = ctx.String(StateSchemeFlag.Name)
	}
	// Parse transaction history flag, if user is still using legacy config
	// file with 'TxLookupLimit' configured, copy the value to 'TransactionHistory'.
	if cfg.TransactionHistory == ethconfig.Defaults.TransactionHistory && cfg.TxLookupLimit != ethconfig.Defaults.TxLookupLimit {
		log.Warn("The config option 'TxLookupLimit' is deprecated and will be removed, please use 'TransactionHistory'")
		cfg.TransactionHistory = cfg.TxLookupLimit
	}
	if ctx.IsSet(TransactionHistoryFlag.Name) {
		cfg.TransactionHistory = ctx.Uint64(TransactionHistoryFlag.Name)
	} else if ctx.IsSet(TxLookupLimitFlag.Name) {
		log.Warn("The flag --txlookuplimit is deprecated and will be removed, please use --history.transactions")
		cfg.TransactionHistory = ctx.Uint64(TxLookupLimitFlag.Name)
	}
	if ctx.String(GCModeFlag.Name) == gcModeArchive && cfg.TransactionHistory != 0 {
		cfg.TransactionHistory = 0
		log.Warn("Disabled transaction unindexing for archive node")
	}
	if ctx.IsSet(CacheFlag.Name) || ctx.IsSet(CacheTrieFlag.Name) {
		cfg.TrieCleanCache = ctx.Int(CacheFlag.Name) * ctx.Int(CacheTrieFlag.Name) / 100
	}
	if ctx.IsSet(CacheFlag.Name) || ctx.IsSet(CacheGCFlag.Name) {
		cfg.TrieDirtyCache = ctx.Int(CacheFlag.Name) * ctx.Int(CacheGCFlag.Name) / 100
	}
	if ctx.IsSet(CacheFlag.Name) || ctx.IsSet(CacheSnapshotFlag.Name) {
		cfg.SnapshotCache = ctx.Int(CacheFlag.Name) * ctx.Int(CacheSnapshotFlag.Name) / 100
	}
	if ctx.IsSet(CacheLogSizeFlag.Name) {
		cfg.FilterLogCacheSize = ctx.Int(CacheLogSizeFlag.Name)
	}
	if !ctx.Bool(SnapshotFlag.Name) || cfg.SnapshotCache == 0 {
		// If snap-sync is requested, this flag is also required
		if cfg.SyncMode == downloader.SnapSync {
			if !ctx.Bool(SnapshotFlag.Name) {
				log.Warn("Snap sync requested, enabling --snapshot")
			}
			if cfg.SnapshotCache == 0 {
				log.Warn("Snap sync requested, resetting --cache.snapshot")
				cfg.SnapshotCache = ctx.Int(CacheFlag.Name) * CacheSnapshotFlag.Value / 100
			}
		} else {
			cfg.TrieCleanCache += cfg.SnapshotCache
			cfg.SnapshotCache = 0 // Disabled
		}
	}
	if ctx.IsSet(DocRootFlag.Name) {
		cfg.DocRoot = ctx.String(DocRootFlag.Name)
	}
	if ctx.IsSet(VMEnableDebugFlag.Name) {
		// TODO(fjl): force-enable this in --dev mode
		cfg.EnablePreimageRecording = ctx.Bool(VMEnableDebugFlag.Name)
	}

	if ctx.IsSet(RPCGlobalGasCapFlag.Name) {
		cfg.RPCGasCap = ctx.Uint64(RPCGlobalGasCapFlag.Name)
	}

	if ctx.IsSet(EWASMInterpreterFlag.Name) {
		cfg.EWASMInterpreter = ctx.String(EWASMInterpreterFlag.Name)
		vm.InitEVMCEwasm(cfg.EWASMInterpreter)
	}

	if ctx.IsSet(EVMInterpreterFlag.Name) {
		cfg.EVMInterpreter = ctx.String(EVMInterpreterFlag.Name)
		vm.InitEVMCEVM(cfg.EVMInterpreter)
	}
	if ctx.IsSet(RPCGlobalGasCapFlag.Name) {
		cfg.RPCGasCap = ctx.Uint64(RPCGlobalGasCapFlag.Name)
	}
	if cfg.RPCGasCap != 0 {
		log.Info("Set global gas cap", "cap", cfg.RPCGasCap)
	} else {
		log.Info("Global gas cap disabled")
	}
	if ctx.IsSet(RPCGlobalEVMTimeoutFlag.Name) {
		cfg.RPCEVMTimeout = ctx.Duration(RPCGlobalEVMTimeoutFlag.Name)
	}
	if ctx.IsSet(RPCGlobalTxFeeCapFlag.Name) {
		cfg.RPCTxFeeCap = ctx.Float64(RPCGlobalTxFeeCapFlag.Name)
	}
	if ctx.IsSet(NoDiscoverFlag.Name) {
		cfg.EthDiscoveryURLs, cfg.SnapDiscoveryURLs = []string{}, []string{}
	} else if ctx.IsSet(DNSDiscoveryFlag.Name) {
		urls := ctx.String(DNSDiscoveryFlag.Name)
		if urls == "" {
			cfg.EthDiscoveryURLs = []string{}
		} else {
			cfg.EthDiscoveryURLs = SplitAndTrim(urls)
		}
	}

	// Override any default configs for hard coded networks.

	// Override genesis configuration if a --<chain> flag.
	if !ctx.Bool(DeveloperFlag.Name) && !ctx.Bool(DeveloperPoWFlag.Name) {
		if gen := genesisForCtxChainConfig(ctx); gen != nil {
			cfg.Genesis = gen
		}
	}

	// Establish NetworkID.
	// If dev-mode is used, then NetworkID will be overridden.
	if ctx.IsSet(NetworkIdFlag.Name) {
		cfg.NetworkId = ctx.Uint64(NetworkIdFlag.Name)
	} else if cfg.Genesis != nil {
		cfg.NetworkId = *cfg.Genesis.GetNetworkID()
	}

	// Set the supported ETH Protocol Versions
	supportedProtocolVersions := ethconfig.Defaults.ProtocolVersions
	if cfg.Genesis != nil {
		supportedProtocolVersions = cfg.Genesis.GetSupportedProtocolVersions()
	}

	configuredProtocolVersions := SplitAndTrim(ctx.String(EthProtocolsFlag.Name))
	if len(configuredProtocolVersions) == 0 {
		Fatalf("--%s must be comma separated list of %s", EthProtocolsFlag.Name, strings.Join(strings.Fields(fmt.Sprint(supportedProtocolVersions)), ","))
	}

	// Since EthProtocolsFlag defines a default value that is the ethconfig.Defaults slice,
	// we can always parse and act on this value whether or not the user sets the flag.
	// Since our logic here will append to the parameterized 'cfg' value ProtocolVersions field,
	// we need to make sure that value starts empty.
	cfg.ProtocolVersions = []uint{}

	seenVersions := map[uint]interface{}{}
	for _, versionString := range configuredProtocolVersions {
		version, err := strconv.ParseUint(versionString, 10, 0)
		if err != nil {
			Fatalf("--%s has invalid value \"%v\" with error: %v", EthProtocolsFlag.Name, versionString, err)
		}

		if _, duplicate := seenVersions[uint(version)]; duplicate {
			Fatalf("--%s has duplicate version of %v", EthProtocolsFlag.Name, versionString)
		}

		isValid := false
		for _, proto := range supportedProtocolVersions {
			if proto == uint(version) {
				isValid = true
				seenVersions[uint(version)] = nil
				break
			}
		}

		if !isValid {
			Fatalf("--%s invalid version value: %d, must be one of %s", EthProtocolsFlag.Name, version, strings.Join(strings.Fields(fmt.Sprint(supportedProtocolVersions)), ","))
		}
		cfg.ProtocolVersions = append(cfg.ProtocolVersions, uint(version))
	}
	log.Info("Configured Ethereum protocol versions", "capabilities", cfg.ProtocolVersions)

	// Set DNS discovery defaults for hard coded networks with DNS defaults.
	// core-geth: NetworkID is configured several stanzas above, based on the returned genesis GetNetworkID() value.
	// GetNetworkID is a configurator method which will either return a custom json:- field NetworkID if set for
	// the config, or it'll return the ChainID if the NetworkID is not explicitly set.
	// This behavior matches practical expectations for how network id and chain id are normally defined.
	// No DNS discovery tree at launch (PA-02): peers come from the Igneum
	// bootnodes, --bootnodes and the config file.

	if ctx.Bool(DeveloperFlag.Name) || ctx.Bool(DeveloperPoWFlag.Name) {
		cfg.DevMode = true // the only source of dev mode (F-02): never the genesis
		cfg.Miner.MinInterval = time.Duration(ctx.Uint64(DeveloperPeriodFlag.Name)) * time.Second
		if !ctx.IsSet(NetworkIdFlag.Name) {
			cfg.NetworkId = params.IgneumDevChainID.Uint64()
		}
		cfg.SyncMode = downloader.FullSync
		if cfg.PoolPrune {
			// The pruned sync: a fresh node under --pool.prune joins by snap
			// sync (the handler keeps full sync once the node has state).
			cfg.SyncMode = downloader.SnapSync
		}
		// Create new developer account or reuse existing one
		var (
			developer  accounts.Account
			passphrase string
			err        error
		)
		if list := MakePasswordList(ctx); len(list) > 0 {
			// Just take the first value. Although the function returns a possible multiple values and
			// some usages iterate through them as attempts, that doesn't make sense in this setting,
			// when we're definitely concerned with only one account.
			passphrase = list[0]
		}

		// Unlock the developer account by local keystore.
		var ks *keystore.KeyStore
		if keystores := stack.AccountManager().Backends(keystore.KeyStoreType); len(keystores) > 0 {
			ks = keystores[0].(*keystore.KeyStore)
		}
		if ks == nil {
			Fatalf("Keystore is not available")
		}

		// The developer account is the funded, unlocked faucet of the dev chain:
		// the keystore's first account, or a new one. A --miner.etherbase given
		// on the command line is the payout address and needs no key (the plan,
		// page 11: a miner needs a payout address, not the private key); without
		// one the rewards go to params.IgneumDevEtherbase, never to the faucet
		// (F-11: rewards on top of the faucet's allocation wrapped its balance).
		// A fresh keystore gets the development chain's fixed faucet key (public
		// material, params.IgneumDevFaucetKey), so the funded account is the
		// unlocked developer account on every dev node and every dev node has the
		// same genesis; an existing keystore keeps its first account.
		if accs := ks.Accounts(); len(accs) > 0 {
			developer = accs[0]
		} else {
			developer, err = ks.ImportECDSA(params.IgneumDevFaucetKey(), passphrase)
			if err != nil {
				Fatalf("Failed to import the development faucet key: %v", err)
			}
		}
		if cfg.Miner.Etherbase == (common.Address{}) {
			cfg.Miner.Etherbase = params.IgneumDevEtherbase
		}

		// The faucet is a convenience: the dev chain runs without it unlocked
		// (an existing keystore account with another passphrase, for instance).
		if err := ks.Unlock(developer, passphrase); err != nil {
			log.Warn("Developer account not unlocked; the dev chain runs without a faucet", "address", developer.Address, "err", err)
		} else {
			log.Info("Using developer account", "address", developer.Address)
		}

		// Create a new developer genesis block or reuse existing one
		// The Igneum development chain: EVM-1, the null algorithm, the developer
		// account funded (PA-03). --dev.pow is the same chain (there is no other).
		// One development genesis, in code: the faucet is params.IgneumDevFaucet
		// and nothing on the command line changes the allocation. --dev.faucet is
		// accepted only as that address (older devnet scripts) and refused otherwise.
		if v := ctx.String(DeveloperFaucetFlag.Name); v != "" {
			if !common.IsHexAddress(v) || common.HexToAddress(v) != params.IgneumDevFaucet {
				Fatalf("Option %q: the development chain's faucet is fixed at %s (one genesis, in code); %q is not it", DeveloperFaucetFlag.Name, params.IgneumDevFaucet.Hex(), v)
			}
		}
		cfg.Genesis = params.IgneumDevGenesisBlock(ctx.Uint64(DeveloperGasLimitFlag.Name))
		if id := ctx.Uint64(DeveloperChainIDFlag.Name); id != 0 {
			if !params.IsIgneumResearchChainID(id) {
				Fatalf("Option %q: %d is outside the research range (%d to %d)", DeveloperChainIDFlag.Name, id, params.IgneumResearchChainIDFirst, params.IgneumResearchChainIDLast)
			}
			if err := cfg.Genesis.Config.SetChainID(new(big.Int).SetUint64(id)); err != nil {
				Fatalf("Option %q: %v", DeveloperChainIDFlag.Name, err)
			}
			if !ctx.IsSet(NetworkIdFlag.Name) {
				cfg.NetworkId = id
			}
			if iss := params.IgneumIssuanceForChainID(id); iss != nil {
				// The chain id's issuance is its config default (the
				// seventh development chain runs the re-cut launch
				// schedule; params.IgneumChainIssuance), never a flag.
				ic := cfg.Genesis.Config.GetIgneumConfig()
				ic.Issuance = iss
				if err := cfg.Genesis.Config.SetIgneumConfig(ic); err != nil {
					Fatalf("Failed to set the chain id's issuance: %v", err)
				}
				// The default reaches a persisted datadir's stored config and
				// the engine through the override, as the vector does: the
				// genesis above is read only on an empty database.
				cfg.OverrideIgneumIssuance = iss
				log.Warn("Issuance from the chain id's config default", "chainId", id, "epoch", iss.Epoch, "epochDiv", iss.EpochDiv, "tail", iss.Tail, "fund", iss.FundAddress)
			}
			log.Warn("Research chain with its own chain id (a transaction signed for it replays on no other development chain)", "chainId", id)
		}
		switch algo := ctx.String(DeveloperAlgorithmFlag.Name); algo {
		case "", "null":
		case igneumhash.Name:
			_, p, err := igneumhash.DecodeChainParams([]byte(ctx.String(DeveloperAlgorithmParamsFlag.Name)))
			if err != nil {
				Fatalf("Option %q: %v", DeveloperAlgorithmParamsFlag.Name, err)
			}
			if err := igneumhash.SelectWork(ctx.String(DeveloperAlgorithmWorkFlag.Name)); err != nil {
				Fatalf("Option %q: %v", DeveloperAlgorithmWorkFlag.Name, err)
			}
			ic := cfg.Genesis.Config.GetIgneumConfig()
			ic.Algorithm = igneumhash.Name
			ic.AlgorithmParams = igneumhash.EncodeChainParams(cfg.Genesis.Config.GetChainID().Uint64(), p)
			if err := cfg.Genesis.Config.SetIgneumConfig(ic); err != nil {
				Fatalf("Failed to select %s for the development chain: %v", algo, err)
			}
			log.Warn("Development chain on the IgneumHash R0A research candidate (not ratified)", "version", p.Version, "params", string(ic.AlgorithmParams))
		default:
			// Any other development-only algorithm the build registered: the
			// three controls (KAWPOW, FishHash, Autolykos) of the verifier
			// comparison, present with the "controls" build tag only, taking
			// no parameter document. A launch genesis can never name them
			// (pow.New refuses development-only names).
			if !pow.IsDevOnly(algo) {
				Fatalf("Option %q: unknown development algorithm %q (null, igneumhash, or a control of a build with the controls tag: %v)", DeveloperAlgorithmFlag.Name, algo, pow.Names())
			}
			ic := cfg.Genesis.Config.GetIgneumConfig()
			ic.Algorithm = algo
			ic.AlgorithmParams = nil
			if ctx.IsSet(DeveloperAlgorithmParamsFlag.Name) {
				// The document as given (rxe1 takes one; the controls refuse
				// one); the registry validates it at engine construction.
				ic.AlgorithmParams = json.RawMessage(ctx.String(DeveloperAlgorithmParamsFlag.Name))
			}
			if err := cfg.Genesis.Config.SetIgneumConfig(ic); err != nil {
				Fatalf("Failed to select %s for the development chain: %v", algo, err)
			}
			log.Warn("Development chain on a development-only algorithm of the research programme (not an Igneum candidate)", "algorithm", algo, "params", string(ic.AlgorithmParams))
		}
		if v := ctx.String(DeveloperAlgorithmScheduleFlag.Name); v != "" {
			var s ctypes.IgneumSchedule
			if err := json.Unmarshal([]byte(v), &s); err != nil {
				Fatalf("Option %q: %v", DeveloperAlgorithmScheduleFlag.Name, err)
			}
			if err := schedule.Validate(ctx.String(DeveloperAlgorithmFlag.Name), &s); err != nil {
				Fatalf("Option %q: %v", DeveloperAlgorithmScheduleFlag.Name, err)
			}
			ic := cfg.Genesis.Config.GetIgneumConfig()
			ic.Schedule = &s
			if err := cfg.Genesis.Config.SetIgneumConfig(ic); err != nil {
				Fatalf("Failed to set the schedule on the development chain: %v", err)
			}
			cfg.OverrideIgneumSchedule = &s
			log.Warn("Development chain on a bounded parameter menu", "entries", len(s.Entries), "hold", s.Terminal.Hold, "endHeight", s.Terminal.EndHeight)
		}
		if v := ctx.String(DeveloperDifficultyFlag.Name); v != "" {
			var d ctypes.IgneumDifficulty
			if err := json.Unmarshal([]byte(v), &d); err != nil {
				Fatalf("Option %q: %v", DeveloperDifficultyFlag.Name, err)
			}
			if d.Target == 0 || d.HalfLife == 0 || d.DtMax == 0 || d.Min == nil || d.Min.Sign() <= 0 {
				Fatalf("Option %q: target, halfLife, dtMax and min must all be positive", DeveloperDifficultyFlag.Name)
			}
			ic := cfg.Genesis.Config.GetIgneumConfig()
			ic.Difficulty = &d
			if err := cfg.Genesis.Config.SetIgneumConfig(ic); err != nil {
				Fatalf("Failed to set the difficulty rule on the development chain: %v", err)
			}
			cfg.Genesis.Difficulty = new(big.Int).Set(d.Min)
			log.Warn("Development chain with its own difficulty constants (research values; the genesis hash moves)", "target", d.Target, "halfLife", d.HalfLife, "dtMax", d.DtMax, "min", d.Min)
		}
		if v := ctx.String(DeveloperVectorFlag.Name); v != "" {
			// VectorWork on the development chain (consensus/igneum/vector.go):
			// the four coordinates' relations, targets and reference
			// difficulties, validated by shape here and built by the engine.
			var vec ctypes.IgneumVector
			if err := json.Unmarshal([]byte(v), &vec); err != nil {
				Fatalf("Option %q: %v", DeveloperVectorFlag.Name, err)
			}
			if err := vector.Validate(&vec); err != nil {
				Fatalf("Option %q: %v", DeveloperVectorFlag.Name, err)
			}
			ic := cfg.Genesis.Config.GetIgneumConfig()
			// The registration check (VECTORWORK.md revision 7, section
			// 4): every coordinate's reference at or above its floor (its
			// own dMin, else the chain's minimum), refused at the genesis.
			// The check is the REGISTRATION's: a fresh data directory (no
			// chain yet) refuses a failing set; a chain already sealed
			// restarts on its registration whatever the rule (vectorwork2's
			// four nodes refused their restart on it a second time at
			// 02:47Z, 13 October 2026, when the check ran at every start).
			fresh := true
			if dir := ctx.String(DataDirFlag.Name); dir != "" {
				if _, err := os.Stat(filepath.Join(dir, "geth", "chaindata")); err == nil {
					fresh = false
				}
			}
			if fresh && ic.Difficulty != nil && ic.Difficulty.Min != nil {
				for i, c := range vec.Coordinates {
					floor := ic.Difficulty.Min.Uint64()
					if c.DMin > 0 {
						floor = c.DMin
					}
					if c.DRef < floor {
						Fatalf("Option %q: coordinate %d (%s) registers dRef %d under its floor %d", DeveloperVectorFlag.Name, i, c.Name, c.DRef, floor)
					}
					// The lag cap is derived, not registered (section 4 at
					// a3b8803c9): a field that differs from the derivation
					// is refused; a chain already sealed keeps its own.
					if want := vector.DerivedLagCap(vec.HalfLife, c.DRef, floor); c.LagCap > 0 && c.LagCap != want {
						Fatalf("Option %q: coordinate %d (%s) registers lagCap %d; the derived cap floor(%d x log2(%d / %d)) is %d", DeveloperVectorFlag.Name, i, c.Name, c.LagCap, vec.HalfLife, c.DRef, floor, want)
					}
					// Resolution (the claims gate's rule, 03:37 UK): the
					// controller's fixed point gives a reference under 2^16
					// no resolution (its output 1 or a power of two above),
					// so a witness relation's work registers in a scaled
					// unit that lands its reference there.
					if c.DRef < 1<<16 {
						Fatalf("Option %q: coordinate %d (%s) registers dRef %d under the controller's resolution 65536", DeveloperVectorFlag.Name, i, c.Name, c.DRef)
					}
					// The relation document's chain_id is the chain's own
					// (the Graph key's domain constant; vectorwork5's set
					// carried vectorwork4's 7354 by a copy, C-28 row 65,
					// 13 October 2026): a fresh registration naming
					// another chain's id is refused at the genesis.
					if len(c.AlgorithmParams) > 0 {
						var doc struct {
							ChainID *uint64 `json:"chain_id"`
						}
						if json.Unmarshal(c.AlgorithmParams, &doc) == nil && doc.ChainID != nil && *doc.ChainID != cfg.Genesis.Config.GetChainID().Uint64() {
							Fatalf("Option %q: coordinate %d (%s) registers a relation document with chain_id %d, not this chain's %d", DeveloperVectorFlag.Name, i, c.Name, *doc.ChainID, cfg.Genesis.Config.GetChainID().Uint64())
						}
					}
				}
			}
			ic.Vector = &vec
			cfg.OverrideIgneumVector = &vec
			if err := cfg.Genesis.Config.SetIgneumConfig(ic); err != nil {
				Fatalf("Failed to set the vector form on the development chain: %v", err)
			}
			cfg.Genesis.Difficulty = new(big.Int).SetUint64(vec.Coordinates[0].DRef)
			log.Warn("Development chain under VectorWork (research; nothing adopted)", "coordinates", len(vec.Coordinates), "halfLife", vec.HalfLife, "rule", vec.Rule)
		}
		var sortition *ctypes.IgneumSortition
		if v := ctx.String(DeveloperSortitionFlag.Name); v != "" {
			var so ctypes.IgneumSortition
			if err := json.Unmarshal([]byte(v), &so); err != nil {
				Fatalf("Option %q: %v", DeveloperSortitionFlag.Name, err)
			}
			sortition = &so
		}
		if v := ctx.String(DeveloperAuthorityFlag.Name); v != "" {
			var a ctypes.IgneumAuthority
			if err := json.Unmarshal([]byte(v), &a); err != nil {
				Fatalf("Option %q: %v", DeveloperAuthorityFlag.Name, err)
			}
			if _, err := igneum.AuthorityFor(&ctypes.IgneumConfig{Authority: &a}); err != nil {
				Fatalf("Option %q: %v", DeveloperAuthorityFlag.Name, err)
			}
			if _, err := igneum.SortitionFor(&ctypes.IgneumConfig{Authority: &a, Sortition: sortition}); err != nil {
				Fatalf("Option %q: %v", DeveloperSortitionFlag.Name, err)
			}
			ic := cfg.Genesis.Config.GetIgneumConfig()
			ic.Authority = &a
			ic.Sortition = sortition
			if err := cfg.Genesis.Config.SetIgneumConfig(ic); err != nil {
				Fatalf("Failed to set the authority rule on the development chain: %v", err)
			}
			if cfg.Genesis.Alloc == nil {
				cfg.Genesis.Alloc = make(genesisT.GenesisAlloc)
			}
			if igneumreg.Native(a.RegistryVersion) {
				// The native registry (version 3 or 4): a placeholder code so the
				// account exists and a node without the native form reverts,
				// the static cap, the fund address for the hostile claim's
				// burn (the issuance's fund address), the floor at zero
				// until Finalize writes it.
				storage := map[common.Hash]common.Hash{igneumreg.SlotCap: common.BigToHash(a.Cap)}
				if ic := cfg.Genesis.Config.GetIgneumConfig(); ic != nil && ic.Issuance != nil {
					storage[igneumreg.SlotFund] = common.BytesToHash(ic.Issuance.FundAddress.Bytes())
				}
				cfg.Genesis.Alloc[a.Registry] = genesisT.GenesisAccount{Code: common.CopyBytes(igneumreg.Placeholder), Storage: storage, Balance: new(big.Int)}
			} else {
				reg := igneum.RegistryAlloc(a.Cap, sortition != nil)
				cfg.Genesis.Alloc[a.Registry] = genesisT.GenesisAccount{Code: reg.Code, Storage: reg.Storage, Balance: reg.Balance}
			}
			cfg.OverrideIgneumAuthority = &a
			cfg.OverrideIgneumSortition = sortition
			log.Warn("Development chain on the miner-bound work authority PROTOTYPE (research; nothing adopted)", "height", a.Height, "registry", a.Registry, "cap", a.Cap)
			if sortition != nil {
				log.Warn("Development chain on the sortition-gated rule PROTOTYPE (hybrid proof of work and bonded capital; research; nothing adopted)", "height", sortition.Height, "epochBlocks", sortition.EpochBlocks, "expected", sortition.Expected, "delta", sortition.Delta, "unbondedRound", sortition.UnbondedRound)
			}
		} else if sortition != nil {
			Fatalf("Option %q needs %q", DeveloperSortitionFlag.Name, DeveloperAuthorityFlag.Name)
		}
		seedValue, err := secretFromFlags(ctx.String(DeveloperBondSeedFlag.Name), ctx.String(DeveloperBondSeedFileFlag.Name), DeveloperBondSeedFlag.Name, DeveloperBondSeedFileFlag.Name)
		if err != nil {
			Fatalf("%v", err)
		}
		if v := seedValue; v != "" {
			b, err := hexutil.Decode(v)
			if err != nil || len(b) != 32 {
				Fatalf("Option %q: a 32-byte hex seed is needed", DeveloperBondSeedFlag.Name)
			}
			cfg.IgneumBondSeed = b
		}
		keyValue, err := secretFromFlags(ctx.String(DeveloperBondKeyFlag.Name), ctx.String(DeveloperBondKeyFileFlag.Name), DeveloperBondKeyFlag.Name, DeveloperBondKeyFileFlag.Name)
		if err != nil {
			Fatalf("%v", err)
		}
		if v := keyValue; v == "keyed" {
			// The keyed form: the bond key is pk = G(seed), derived from the
			// seed (core/vm/igneumreg.BondKeyOf); the seed is also the
			// secret the attempts consume.
			if len(cfg.IgneumBondSeed) != 32 {
				Fatalf("Option %q: 'keyed' needs %q", DeveloperBondKeyFlag.Name, DeveloperBondSeedFlag.Name)
			}
			var sk [32]byte
			copy(sk[:], cfg.IgneumBondSeed)
			k := igneumreg.BondKeyOf(sk)
			cfg.IgneumBondKey = &k
			log.Warn("Bond key derived from the seed (the keyed form, pk = G(sk))", "bondKey", k.Hex())
		} else if v != "" {
			b, err := hexutil.Decode(v)
			if err != nil || len(b) != 32 {
				Fatalf("Option %q: a 32-byte hex key is needed", DeveloperBondKeyFlag.Name)
			}
			k := common.BytesToHash(b)
			cfg.IgneumBondKey = &k
		}
		seedValue, keyValue = "", ""
		cfg.IgneumAuthorityLax = ctx.Bool(DeveloperAuthorityLaxFlag.Name)
		cfg.IgneumWithhold = ctx.Uint64(DeveloperWithholdFlag.Name)
		cfg.IgneumLaxEndorse = ctx.String(DeveloperLaxEndorseFlag.Name)
		cfg.IgneumSealStaggerMs = ctx.Uint64(MinerSealStaggerFlag.Name)
		cfg.IgneumOpenSpreadMs = ctx.Uint64(MinerOpenSpreadFlag.Name)
		if ctx.IsSet(DataDirFlag.Name) {
			chaindb := tryMakeReadOnlyDatabase(ctx, stack)
			if rawdb.ReadCanonicalHash(chaindb, 0) != (common.Hash{}) {
				cfg.Genesis = nil // fallback to db content

				// --dev refuses any stored genesis that is not an Igneum development
				// genesis (F-02): the engine block must name the null algorithm and the
				// chain id must be the development chain's.
				genesis, err := core.ReadGenesis(chaindb)
				if err != nil {
					Fatalf("Could not read genesis from database: %v", err)
				}
				// A stored engine block that lost its algorithm name (the
				// placeholder overwrite, core.SetupGenesisBlockWithOverride)
				// takes it again from the development flags, which every
				// development start gives; the stored configuration is
				// repaired on this start.
				if sic := genesis.Config.GetIgneumConfig(); sic != nil && sic.Algorithm == "" && genesis.Config.GetConsensusEngineType() == ctypes.ConsensusEngineT_Igneum {
					if dic := devAlgorithmBlock(ctx); dic != nil {
						log.Warn("Stored development engine block names no algorithm; restoring it from the flags", "algorithm", dic.Algorithm)
						cfg.OverrideIgneumAlgorithm = dic.Algorithm
						cfg.OverrideIgneumAlgorithmParams = dic.AlgorithmParams
						sic.Algorithm = dic.Algorithm
						sic.AlgorithmParams = dic.AlgorithmParams
						genesis.Config.SetIgneumConfig(sic)
					}
				}
				if !params.IsIgneumDevGenesis(genesis) {
					Fatalf("Bad developer-mode genesis: the datadir holds a genesis that is not the Igneum development chain (engine %v, chain id %v); --dev runs only the built-in development chain", genesis.Config.GetConsensusEngineType(), genesis.Config.GetChainID())
				}
			}
			chaindb.Close()
		}
		if !ctx.IsSet(MinerGasPriceFlag.Name) {
			cfg.Miner.GasPrice = big.NewInt(1)
		}
	}
	// Set any dangling config values
}

// RegisterEthService adds an Ethereum client to the stack.
// The second return value is the full node instance.
func RegisterEthService(stack *node.Node, cfg *ethconfig.Config) (ethapi.Backend, *eth.Ethereum) {
	backend, err := eth.New(stack, cfg)
	if err != nil {
		Fatalf("Failed to register the Ethereum service: %v", err)
	}
	stack.RegisterAPIs(tracers.APIs(backend.APIBackend))
	return backend.APIBackend, backend
}

// RegisterFilterAPI adds the eth log filtering RPC API to the node.
func RegisterFilterAPI(stack *node.Node, backend ethapi.Backend, ethcfg *ethconfig.Config) *filters.FilterSystem {
	filterSystem := filters.NewFilterSystem(backend, filters.Config{
		LogCacheSize: ethcfg.FilterLogCacheSize,
	})
	stack.RegisterAPIs([]rpc.API{{
		Namespace: "eth",
		Service:   filters.NewFilterAPI(filterSystem, false),
	}})
	return filterSystem
}

func SetupMetrics(ctx *cli.Context) {
	if metrics.Enabled {
		log.Info("Enabling metrics collection")

		var (
			enableExport   = ctx.Bool(MetricsEnableInfluxDBFlag.Name)
			enableExportV2 = ctx.Bool(MetricsEnableInfluxDBV2Flag.Name)
		)

		if enableExport || enableExportV2 {
			CheckExclusive(ctx, MetricsEnableInfluxDBFlag, MetricsEnableInfluxDBV2Flag)

			v1FlagIsSet := ctx.IsSet(MetricsInfluxDBUsernameFlag.Name) ||
				ctx.IsSet(MetricsInfluxDBPasswordFlag.Name)

			v2FlagIsSet := ctx.IsSet(MetricsInfluxDBTokenFlag.Name) ||
				ctx.IsSet(MetricsInfluxDBOrganizationFlag.Name) ||
				ctx.IsSet(MetricsInfluxDBBucketFlag.Name)

			if enableExport && v2FlagIsSet {
				Fatalf("Flags --influxdb.metrics.organization, --influxdb.metrics.token, --influxdb.metrics.bucket are only available for influxdb-v2")
			} else if enableExportV2 && v1FlagIsSet {
				Fatalf("Flags --influxdb.metrics.username, --influxdb.metrics.password are only available for influxdb-v1")
			}
		}

		var (
			endpoint = ctx.String(MetricsInfluxDBEndpointFlag.Name)
			database = ctx.String(MetricsInfluxDBDatabaseFlag.Name)
			username = ctx.String(MetricsInfluxDBUsernameFlag.Name)
			password = ctx.String(MetricsInfluxDBPasswordFlag.Name)

			token        = ctx.String(MetricsInfluxDBTokenFlag.Name)
			bucket       = ctx.String(MetricsInfluxDBBucketFlag.Name)
			organization = ctx.String(MetricsInfluxDBOrganizationFlag.Name)
		)

		if enableExport {
			tagsMap := SplitTagsFlag(ctx.String(MetricsInfluxDBTagsFlag.Name))

			log.Info("Enabling metrics export to InfluxDB")

			go influxdb.InfluxDBWithTags(metrics.DefaultRegistry, 10*time.Second, endpoint, database, username, password, "geth.", tagsMap)
		} else if enableExportV2 {
			tagsMap := SplitTagsFlag(ctx.String(MetricsInfluxDBTagsFlag.Name))

			log.Info("Enabling metrics export to InfluxDB (v2)")

			go influxdb.InfluxDBV2WithTags(metrics.DefaultRegistry, 10*time.Second, endpoint, token, bucket, organization, "geth.", tagsMap)
		}

		if ctx.IsSet(MetricsHTTPFlag.Name) {
			address := net.JoinHostPort(ctx.String(MetricsHTTPFlag.Name), fmt.Sprintf("%d", ctx.Int(MetricsPortFlag.Name)))
			log.Info("Enabling stand-alone metrics HTTP endpoint", "address", address)
			exp.Setup(address)
		} else if ctx.IsSet(MetricsPortFlag.Name) {
			log.Warn(fmt.Sprintf("--%s specified without --%s, metrics server will not start.", MetricsPortFlag.Name, MetricsHTTPFlag.Name))
		}
	}
}

func SplitTagsFlag(tagsFlag string) map[string]string {
	tags := strings.Split(tagsFlag, ",")
	tagsMap := map[string]string{}

	for _, t := range tags {
		if t != "" {
			kv := strings.Split(t, "=")

			if len(kv) == 2 {
				tagsMap[kv[0]] = kv[1]
			}
		}
	}

	return tagsMap
}

// MakeChainDatabase opens a database using the flags passed to the client and will hard crash if it fails.
func MakeChainDatabase(ctx *cli.Context, stack *node.Node, readonly bool) ethdb.Database {
	var (
		cache   = ctx.Int(CacheFlag.Name) * ctx.Int(CacheDatabaseFlag.Name) / 100
		handles = MakeDatabaseHandles(ctx.Int(FDLimitFlag.Name))
		err     error
		chainDb ethdb.Database
	)
	switch {
	case ctx.IsSet(RemoteDBFlag.Name):
		log.Info("Using remote db", "url", ctx.String(RemoteDBFlag.Name), "headers", len(ctx.StringSlice(HttpHeaderFlag.Name)))
		client, err := DialRPCWithHeaders(ctx.String(RemoteDBFlag.Name), ctx.StringSlice(HttpHeaderFlag.Name))
		if err != nil {
			break
		}
		chainDb = remotedb.New(client)
	case ctx.String(SyncModeFlag.Name) == "light":
		chainDb, err = stack.OpenDatabase("lightchaindata", cache, handles, "", readonly)
	default:
		chainDb, err = stack.OpenDatabaseWithFreezer("chaindata", cache, handles, ctx.String(AncientFlag.Name), "", readonly)
	}
	if err != nil {
		Fatalf("Could not open database: %v", err)
	}
	return chainDb
}

// tryMakeReadOnlyDatabase try to open the chain database in read-only mode,
// or fallback to write mode if the database is not initialized.
func tryMakeReadOnlyDatabase(ctx *cli.Context, stack *node.Node) ethdb.Database {
	// If the database doesn't exist we need to open it in write-mode to allow
	// the engine to create files.
	readonly := true
	if rawdb.PreexistingDatabase(stack.ResolvePath("chaindata")) == "" {
		readonly = false
	}
	return MakeChainDatabase(ctx, stack, readonly)
}

func IsNetworkPreset(ctx *cli.Context) bool {
	for _, flag := range NetworkFlags {
		bFlag, _ := flag.(*cli.BoolFlag)
		if ctx.IsSet(bFlag.Name) {
			return true
		}
	}
	return false
}

func DialRPCWithHeaders(endpoint string, headers []string) (*rpc.Client, error) {
	if endpoint == "" {
		return nil, errors.New("endpoint must be specified")
	}
	if strings.HasPrefix(endpoint, "rpc:") || strings.HasPrefix(endpoint, "ipc:") {
		// Backwards compatibility with geth < 1.5 which required
		// these prefixes.
		endpoint = endpoint[4:]
	}
	var opts []rpc.ClientOption
	if len(headers) > 0 {
		customHeaders := make(http.Header)
		for _, h := range headers {
			kv := strings.Split(h, ":")
			if len(kv) != 2 {
				return nil, fmt.Errorf("invalid http header directive: %q", h)
			}
			customHeaders.Add(kv[0], kv[1])
		}
		opts = append(opts, rpc.WithHeaders(customHeaders))
	}
	return rpc.DialOptions(context.Background(), endpoint, opts...)
}

// genesisForCtxChainConfig returns the corresponding Genesis for a non-default flag chain value.
// If no --<chain> flag is set in the global context, a nil value is returned.
// It does not handle genesis for --dev mode, since that mode includes but also exceeds
// chain configuration.
func genesisForCtxChainConfig(ctx *cli.Context) *genesisT.Genesis {
	if ctx.Bool(DeveloperFlag.Name) {
		Fatalf("Developer chains are ephemeral")
	}
	// One network: nil means the stored chain, or the Igneum default genesis
	// (params.DefaultGenesisBlock) for an empty database.
	return nil
}

func MakeGenesis(ctx *cli.Context) *genesisT.Genesis {
	if ctx.Bool(DeveloperFlag.Name) || ctx.Bool(DeveloperPoWFlag.Name) {
		Fatalf("Developer chains are ephemeral")
	}
	return genesisForCtxChainConfig(ctx)
}

// MakeChain creates a chain manager from set command line flags.
func MakeChain(ctx *cli.Context, stack *node.Node, readonly bool) (*core.BlockChain, ethdb.Database) {
	var (
		gspec   = MakeGenesis(ctx)
		chainDb = MakeChainDatabase(ctx, stack, readonly)
	)
	// The engine comes from the genesis given, else from the chain
	// configuration stored with the chain (as eth.New reads it).
	chainConfig, err := core.LoadChainConfig(chainDb, gspec)
	if err != nil {
		Fatalf("Failed to load the chain configuration: %v", err)
	}
	var (
		igneumConfig *ctypes.IgneumConfig
		chainID      uint64
	)
	if chainConfig != nil && chainConfig.GetConsensusEngineType() == ctypes.ConsensusEngineT_Igneum {
		igneumConfig = chainConfig.GetIgneumConfig()
	}
	if chainConfig != nil && chainConfig.GetChainID() != nil {
		chainID = chainConfig.GetChainID().Uint64()
	}
	// A read-only chain (export, dump) verifies no seal: its engine runs in
	// fake mode, so a development-only algorithm (null) needs no --dev there.
	// The chain id carries the engine's gates on this path as on the node's.
	engine, err := ethconfig.CreateConsensusEngine(chainID, igneumConfig, ctx.Bool(DeveloperFlag.Name) || ctx.Bool(DeveloperPoWFlag.Name), ctx.Bool(FakePoWFlag.Name) || readonly, nil, false)
	if err != nil {
		Fatalf("Failed to create the consensus engine: %v", err)
	}
	if gcmode := ctx.String(GCModeFlag.Name); gcmode != gcModeFull && gcmode != gcModeArchive {
		Fatalf("--%s must be either 'full' or 'archive'", GCModeFlag.Name)
	}
	scheme, err := rawdb.ParseStateScheme(ctx.String(StateSchemeFlag.Name), chainDb)
	if err != nil {
		Fatalf("%v", err)
	}
	cache := &core.CacheConfig{
		TrieCleanLimit:      ethconfig.Defaults.TrieCleanCache,
		TrieCleanNoPrefetch: ctx.Bool(CacheNoPrefetchFlag.Name),
		TrieDirtyLimit:      ethconfig.Defaults.TrieDirtyCache,
		TrieDirtyDisabled:   ctx.String(GCModeFlag.Name) == gcModeArchive,
		TrieTimeLimit:       ethconfig.Defaults.TrieTimeout,
		SnapshotLimit:       ethconfig.Defaults.SnapshotCache,
		Preimages:           ctx.Bool(CachePreimagesFlag.Name),
		StateScheme:         scheme,
		StateHistory:        ctx.Uint64(StateHistoryFlag.Name),
	}
	if cache.TrieDirtyDisabled && !cache.Preimages {
		cache.Preimages = true
		log.Info("Enabling recording of key preimages since archive mode is used")
	}
	if !ctx.Bool(SnapshotFlag.Name) {
		cache.SnapshotLimit = 0 // Disabled
	}
	// If we're in readonly, do not bother generating snapshot data.
	if readonly {
		cache.SnapshotNoBuild = true
	}

	if ctx.IsSet(CacheFlag.Name) || ctx.IsSet(CacheTrieFlag.Name) {
		cache.TrieCleanLimit = ctx.Int(CacheFlag.Name) * ctx.Int(CacheTrieFlag.Name) / 100
	}
	if ctx.IsSet(CacheFlag.Name) || ctx.IsSet(CacheGCFlag.Name) {
		cache.TrieDirtyLimit = ctx.Int(CacheFlag.Name) * ctx.Int(CacheGCFlag.Name) / 100
	}
	vmcfg := vm.Config{EnablePreimageRecording: ctx.Bool(VMEnableDebugFlag.Name)}

	// Disable transaction indexing/unindexing by default.
	chain, err := core.NewBlockChain(chainDb, cache, gspec, nil, engine, vmcfg, nil, nil)
	if err != nil {
		Fatalf("Can't create BlockChain: %v", err)
	}
	return chain, chainDb
}

// MakeConsolePreloads retrieves the absolute paths for the console JavaScript
// scripts to preload before starting.
func MakeConsolePreloads(ctx *cli.Context) []string {
	// Skip preloading if there's nothing to preload
	if ctx.String(PreloadJSFlag.Name) == "" {
		return nil
	}
	// Otherwise resolve absolute paths and return them
	var preloads []string

	for _, file := range strings.Split(ctx.String(PreloadJSFlag.Name), ",") {
		preloads = append(preloads, strings.TrimSpace(file))
	}
	return preloads
}

// MakeTrieDatabase constructs a trie database based on the configured scheme.
func MakeTrieDatabase(ctx *cli.Context, disk ethdb.Database, preimage bool, readOnly bool, isVerkle bool) *triedb.Database {
	config := &triedb.Config{
		Preimages: preimage,
		IsVerkle:  isVerkle,
	}
	scheme, err := rawdb.ParseStateScheme(ctx.String(StateSchemeFlag.Name), disk)
	if err != nil {
		Fatalf("%v", err)
	}
	if scheme == rawdb.HashScheme {
		// Read-only mode is not implemented in hash mode,
		// ignore the parameter silently. TODO(rjl493456442)
		// please config it if read mode is implemented.
		config.HashDB = hashdb.Defaults
		return triedb.NewDatabase(disk, config)
	}
	if readOnly {
		config.PathDB = pathdb.ReadOnly
	} else {
		config.PathDB = pathdb.Defaults
	}
	return triedb.NewDatabase(disk, config)
}
