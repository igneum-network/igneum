# Igneum pool reference service (skeleton)

The reference implementation of the pool specification in `docs/ecosystem/DESIGN.md`, section 2: solo mining and independent pools from launch, fresh session authentication, bounded inputs, transparent share accounting, durable liabilities in PostgreSQL, signed payment intents before broadcast, restart and reorganisation reconciliation. Go, standard library only at this stage.

Status: the service runs end to end on the dev chain (getwork, IS/1 stratum, shares, block solutions accepted by the node, credits, maturity) with the dev chain's `null` algorithm; the real validator slots in behind `validate.Validator` after Phase Zero's selection; the PostgreSQL driver waits on E-11 (file journal and memory stores in the meantime).

## Layout

| Path | What |
|---|---|
| `internal/limits` | every bound an input must satisfy |
| `internal/session` | fresh session authentication, retained jobs, duplicate detection, share rate |
| `internal/shares` | the PPLNS window by weight and the replayable distribution record |
| `internal/ledger` | the append-only journal, derived balances, maturity, orphan reversal, debits; the memory and file-journal stores (the PostgreSQL store of the first week is in the box history) |
| `internal/payout` | signed payment intents: build, hash, sign, dispatch, registry |
| `internal/reconcile` | found blocks checked against the canonical chain until maturity |
| `internal/stratum` | IS/1: frame bound, strict decoding, the message set and the server (subscribe, authorize, notify, submit) with rejection reasons |
| `internal/validate` | the validator interface the pool verifies every share with; the devnet `null` stand-in; target and difficulty helpers |
| `internal/jobsource` | the getwork client (`igneum_getWork`, `eth_getWork`, `eth_submitWork`) |
| `internal/discovery` | the disclosure document and its handler |
| `internal/rewards` | `igneum_blockReward` reader: the subsidy and the priority-fee mean for the earnings fields |
| `internal/service` | the wiring: getwork poll to jobs, shares to the window, solutions to the node, canonical match to credits, reconciliation, rewards refresh |
| `cmd/igneum-pool` | the service: stratum listener, disclosure and health endpoints, the ledger over a file journal (the PostgreSQL store of the first week is in the box history), or memory |
| `cmd/ledger-adapter` | the TP-15 adapter (`tests/pool/adapters.json`) |

## Build and test (on the build box, never on a laptop)

```
cd pool
gofmt -l .
go vet ./...
go test ./...
go build ./cmd/igneum-pool
```

## Running the skeleton

```
./igneum-pool -disclosure disclosure.json -listen-http 127.0.0.1:8080
curl -s http://127.0.0.1:8080/.well-known/igneum-pool.json
```



## Invariants the tests hold

- fee + credits + remainder = reward + carry in, exactly, in wei; the fee is applied once; the remainder is recorded, never absorbed.
- The distribution record's inputs hash changes with any share weight or the fee, and not otherwise.
- A block credited twice is refused; a refused batch leaves balances untouched; a debit above payable is refused; the same intent cannot debit twice; replaying the journal rebuilds identical balances.
- A block orphaned before maturity reverses to zero liability; an orphan after maturity is an incident, not a silent netting.
- An unsigned or tampered intent never reaches the broadcaster; one intent per id and per nonce.
- A frame over the bound is refused before it is buffered; unknown fields are refused.
- A disclosure document without a contact, an endpoint, a payout-gas policy, a minimum payout or a records URL is refused; a fee cannot exceed the whole.

## The igneumhash validator build (box only)

`internal/validate/igneumhash.go` (build tag `igneumhash`) imports the node's `consensus/igneum/pow` and `pow/igneumhash` packages from this repository, the same code the node verifies with. The default vendored build does not see it; the box builds it with its own modfile so `go.mod` and `vendor/` stay as they are:

```
cd pool
GOFLAGS=-mod=mod go build -modfile=go.ih.mod -tags igneumhash -o igneum-pool.ih ./cmd/igneum-pool
```

`go.ih.mod` is `go.mod` plus the node module by `replace ../node`; `go.ih.sum` is written beside it by the build and is not committed.

## The notify's coordinate fields (the wire of record)

A coordinate job (VectorWork) carries, beside `job_id`, `height`, `header`, `boundary` and `share_target` (64 hex
digits, a 32-byte big-endian number the share's hash must be at or under; the step's target is the lane's share
target over 2^step): `coord` and `coord_name`; `difficulty`, the lane's block difficulty as the node states it (a
decimal string); `reward_wei`, the lane's reward per block (decimal wei); `last_height`, the lane's last block;
`paid_coords`, the chain's pay list at the job's height by index (0.1.76; a coordinate absent from it is retired or
unpaid); and, from 0.1.84, `lane_worth`.

`lane_worth` (F-496 under the payout per coordinate of 0.1.85; 0.1.86, 17 October 2026) is the coordinate's
marginal pay per unit of share weight: its part of the last closed pay window's income over the pool's carried share
weight on it in that window, in wei per unit of share weight, a decimal string in scientific form (`printf %.4e`),
refreshed at each window's close (the window's `coords` rows on `/payouts` are its source). Under the per-coordinate
payout a member earns on a lane its share-weight rate there times `lane_worth`, so a router ranks its lanes by that
product rather than by attempts a second or by `reward_wei` over `difficulty`. The field is absent on a one-form
chain, on a coordinate the pool does not serve, before the pool's first window closes, and on a coordinate the last
window carried no work on.

## The payout per coordinate (0.1.85, the rule of record)

The pooled form pays every window (64 blocks on the public test network, `-payout-window`) from the pool key. From
0.1.91 (F-502, 17 October 2026) a window's income is the chain's credit to the key over the window's heights (the
previous window's last carried block to this window's, contiguous, read block by block from the archive source as the
key's balance steps with the pool's own sends added back), never a balance delta between closes; the chain pays the
key for carried work by its own pay window, a lag the record names and does not correct. From 0.1.90 (F-503) a window
is the carried lines with a carry number in (the previous window's edge, this window's edge], the carry's order in the
journal, never a height, so a block credited after a close lands in the next window. From 0.1.85 (F-497) the income
splits PER COORDINATE: each
paid coordinate takes p_c over the paid coordinates' sum of the income (p_c the chain's pay share of the coordinate,
the balanced reward being homogeneous of degree one in the key's work, so the split is exact whatever lanes the
window's blocks landed on), and within a coordinate each member is paid its carried share weight on that
coordinate less its fee there over the pool's carried weight on it; a coordinate with no carried work in the window
takes nothing. The fee comes off each coordinate's part by the same ratio; every division floors in wei and the
remainder (the income less the dues less the fee) is the pool's, left in the key and named on the window. A
member's incentive then equals the pool's and the chain's: a diverse-hardware chain pays each lane's hardware for
its lane, and a member's marginal pay per unit of share weight on a coordinate is p_c times the income over the
pool's carried weight there. On a chain with one coordinate, or none named, the rule is the pool-wide payout as
before (every member by its weight over the total). /payouts names the rule each window closed under, the pay
shares it split by, the remainder, and each member's parts per coordinate.

## The record under the journal's lock (0.1.92, F-510)

A window closed and paid twice across a handover on 17 October 2026 (block 3,371's four sends, 81.396 IGN): the
stopping generation made the sends and ended before it wrote the record; the next generation read the record at its
start, before the old had let the journal go, and closed the window again. From 0.1.92:

- the record is read only under the journal's lock (`payoutLoadRecord`, called by `HandoverJournal` after the lock,
  or by `PayoutStart` on a start without a handover); the run closes nothing before the record is read;
- a close's sends and its record write run to the end whatever the stop (the context outlives the cancel) and the
  process waits for a run in flight (up to 75 s) before it ends and lets the journal go; the handover scripts wait
  90 s for the old generation;
- at every start the key's sends the chain carries in the blocks after the last close are reconciled against the
  record: a send no row and no earlier reconciliation named is an ADVANCE to the member it went to, named on the last
  window (`advances`: member, tx, block, value_wei) and taken off that member's next dues (`advance_wei` on the row
  that paid it off, `advance_out_wei` what is still owed, restored at a restart from the member's last row by
  assignment plus the advances found after it). An unreadable chain holds the reconciliation and the closes until it
  reads.

Economics' rule by the address (0.1.93): an advance to an address with no member row in the record (the fee
address, a stranger) is the pool's LOSS, named on the window's `losses` list (member, tx, block, value_wei, gas_wei),
never owed; an advance to a member key is owed as long as the key exists. Every window names
`advances_outstanding_wei`, the sum every member still owes after its close (the key's receivable), and an advance
row names its send's `gas_wei`, so a reader closes the key's balance identity from the record alone: balance +
advances outstanding + gas paid = carries + the fee held + the remainders.

The recompute tool (`tools/payout-recompute.py`) writes the corrected record in the same form.

## A seq takes a carry number once (0.1.94)

The settle's churn (the node's account dropping and re-listing old shares) writes carried, uncarried, carried lines for
one seq. Before 0.1.94 the third line took a new carry number above the pay window's edge and the share was paid again
by weight (window 7,404 to 7,467 of 17 October 2026: 8,639 old lines re-paid beside 914 new). From 0.1.94 a seq's
carry number is taken once, at its first carried line, and kept: a later carried line restores the row when it still
stands (the open window, paid once there) or, when the row was pruned at a paid close, is counted as
`restored_paid_work` on the next window, named and never paid again; after a paid close a seq is named once as moved
back and once as restored, its further churn names nothing. The rule reads the journal in file order alone, so every replay numbers the same.

## A row with dues is never "nothing due" (0.1.95, F-540)

A member's due at a close is paid (`sent`, then `paid`), carried (`carried`, under the minimum), or applied to the
advance the member owes (`applied to the advance`: the whole due went to the advance, `advance_wei` on the row); a
due above the advance applies as much as the advance holds and sends or carries the rest, `advance_wei` named. The
window names `applied_wei`, the sum of its rows' applied dues, so the identity reads dues + fee + remainder + applied
= the chain's payment to the key over the window. `nothing due` is a row with no due at all.

## The pool sends what the balance allows (0.1.95)

A due above the key's balance at the close (less a gas reserve for the window's sends) is sent in part and the rest
carried: the row is `partial` with `paid_wei` the part sent and `carry_out_wei` the rest; when the available part is
under the minimum payout nothing is sent and the whole due is carried, named. `carry_out_wei` is the carry by
assignment whenever a row carries it (the restore reads it before the row's status).

## A paid lane without a worker is named (0.1.96)

On a minimum-rule chain the pool key's breadth weight is the minimum over the paid lanes of its work, the floor
decaying over 256 blocks, so the pool earns NOTHING while any paid lane has no hashing worker (17 October 2026: the
public test network pool from 7,559 after its only raster worker left lane 5; the research chain pool from 14,157
with no lane-27 worker). From 0.1.96 `pay_list.lanes_without_worker` on /shares lists every offered lane with no
accepted share of the pool's for 64 blocks (coord, since_seconds, never), and the log says once per lane when it
falls silent and once when it has a worker again. The notify's `lane_workers` (beside `lane_worth`, always present
on a coordinate job) is the count of the pool's workers with an accepted share on that coordinate in the last 64
blocks: 0 names an uncovered paid lane, which a card keeps covered before moving for reward (the product team's router rule).
