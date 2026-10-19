# Graph sealer (the graph coordinate's block worker)

The driver that keeps a chain's graph coordinate alive: it reads the coordinate's template from a node
(`igneum_getWork` with the coordinate's index), runs the Cuckaroo29 CUDA solver service (`igneum_graph_serve`,
pow/ store entry `phase-zero/graph/...`) over fresh nonce ranges in short slices, and submits every ticket whose
ticket hash reads at or under the template's target (`igneum_submitTicket`). One instance per card.

Run: `python3 graph_sealer.py --config=sealer.json` with a config carrying the node's RPC (`rpc`), the chain id
(`chain`, the chain's own id: 7321, 7357), the solver binary (`bin`), the card (`device`),
the slice length in seconds (`slice_s`), the RPC names and field names (`getwork`, `getwork_params`, `submit`,
`f_height`, `f_seed`, `f_target`, `f_header`, `f_coord`, `coord_value`, `submit_form`) and the log path (`log`).
The seed is the template's seal hash (`headerHash`); the key binding is docs/igneumhash/vectorwork/GRAPH-SEAL.md.

The node serves the target as a DECIMAL integer; the driver reads it as such (hex only when 0x-prefixed). An
earlier form parsed the decimal digits as hex, which passed every ticket to the node and left the coordinate's
acceptance at chance level once its difficulty rose off the floor (chain 7321, c3b, 13 October 2026, F-107).
