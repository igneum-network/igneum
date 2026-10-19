# Light indexer and the outcomes dashboard (version 0)

The plan (page 29) asks for a small published dashboard of outcomes. This is the chain-derived half of it (docs/ecosystem/DESIGN.md, section 7): a Go program, standard library only, that walks the node's canonical chain into an append-only row file, reconciles reorganisations to the common ancestor, reads `igneum_supply`, and writes `dashboard.json`. Blockscout is the explorer people browse; this is the source the dashboard reads, cross-checked against Blockscout.

Every cell carries `state` (`measured`, `not_yet_measured`, `curated`), `source` and, where it applies, `window`. A measure the chain cannot give is `not_yet_measured` with its real source named; nothing is estimated in its place. The document's `claims` line says what it is: measures, not rankings.

## Run

```
cd indexer
go vet ./... && go test ./...
go build -o igneum-indexer ./cmd/igneum-indexer
./igneum-indexer -rpc http://127.0.0.1:8545/ -state ./igneum-work/work/indexer -window 1000 -listen 127.0.0.1:8090
curl -s http://127.0.0.1:8090/dashboard.json
```

`-once` walks to the head, writes the file and exits. The row file (`rows.jsonl`) is the whole history: a reorganisation appends removal rows, so a restart replays to the same view.

## Measures in version 0

| Dimension | Measure | State now |
|---|---|---|
| mining | independent participation (distinct coinbase addresses), entry and exit between windows | measured |
| mining | replacement economics, accepted work per wall-kWh, realised fee burden | not yet measured (their sources named) |
| reliability | reorganisations observed, mean block interval | measured |
| reliability | incidents, payout integrity, rejected and stale work, independently observed availability | not yet measured |
| ownership | transaction success (receipt status over the window) | measured |
| ownership | safe custody, independent wallet access, exit liquidity | not yet measured |
| adoption | active addresses, retained users (two consecutive windows; Labs and subsidised exclusions still to add) | measured |
| adoption | useful external transactions, independent applications | not yet measured |
| independence | control concentration (largest and top-three producer share) | measured |
| independence | replaceable services, maintainer diversity, funded continuity | not yet measured |
| market position | valuation and liquidity comparison within the frozen peer set | not yet measured |
| supply | issued, fund issued, burned, net from `igneum_supply` | measured |
