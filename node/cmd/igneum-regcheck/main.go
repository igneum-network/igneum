// igneum-regcheck runs a registry document through the node's own admission
// (consensus/igneum.AdmitRegistryDocument, the import's code path) against
// the registration in force, before the signer signs it (the project's rule of
// 20:19 UK, 15 October 2026: no document is signed without this line).
//
// Usage: igneum-regcheck <current.json> <document.json> <sender address> <landing height> [--rpc <url>] [--armed <c,c,...>] [--nodes <url,url,...>]
// Prints "ADMITTED <coordinates> <sha256 of the document bytes>" and exits 0,
// or "REFUSED: <the node's reason>" and exits 1. The landing height is the
// highest block the transaction may land in for the document's heights to
// hold (the notice and the activations are checked against it).
//
// THE PRE-FLIGHT (F-221, 16 October 2026: the research chain stalled at
// 8,639 because a succession's activation took the running sealers' pay
// weight at A while no node yet sealed on the successors, a block only a
// successor could build): with --rpc, every pay change the document adds
// above the landing is read against the chain: the set of coordinates
// paid from that height must contain one that sealed in the last 64 blocks,
// or be named in --armed (the coordinates whose sealers are armed to move
// at A-1 by move-at-height); otherwise "PRE-FLIGHT REFUSED" and exit 2. A
// pre-flight read, not a stall. With --nodes (every node of record's RPC), a
// document carrying a coordinate's in-place documentChanges is refused until
// every node answers a registry view with the document-change entry
// (LANE-MODIFICATION.md section 1a): a node before the entry would refuse the
// landing and the chain would fork at the change's height.
package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"math/big"
	"net/http"
	"os"
	"strconv"
	"strings"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/consensus/igneum"
	"github.com/ethereum/go-ethereum/consensus/igneum/pow"
	"github.com/ethereum/go-ethereum/params/types/ctypes"
)

func main() {
	if len(os.Args) < 5 {
		fmt.Fprintln(os.Stderr, "usage: igneum-regcheck <current.json> <document.json> <sender address> <landing height> [--rpc <url>] [--armed <c,c,...>]")
		os.Exit(2)
	}
	curRaw, err := os.ReadFile(os.Args[1])
	if err != nil {
		fmt.Printf("REFUSED: %v\n", err)
		os.Exit(1)
	}
	var current ctypes.IgneumVector
	if err := json.Unmarshal(curRaw, &current); err != nil {
		fmt.Printf("REFUSED: the current registration: %v\n", err)
		os.Exit(1)
	}
	doc, err := os.ReadFile(os.Args[2])
	if err != nil {
		fmt.Printf("REFUSED: %v\n", err)
		os.Exit(1)
	}
	sender := common.HexToAddress(os.Args[3])
	landing, err := strconv.ParseUint(os.Args[4], 10, 64)
	if err != nil {
		fmt.Printf("REFUSED: the landing height: %v\n", err)
		os.Exit(1)
	}
	var rpcURL string
	var nodes []string
	armed := map[uint8]bool{}
	for i := 5; i < len(os.Args); i++ {
		switch os.Args[i] {
		case "--rpc":
			if i+1 < len(os.Args) {
				rpcURL = os.Args[i+1]
				i++
			}
		case "--nodes":
			if i+1 < len(os.Args) {
				for _, s := range strings.Split(os.Args[i+1], ",") {
					if s = strings.TrimSpace(s); s != "" {
						nodes = append(nodes, s)
					}
				}
				i++
			}
		case "--armed":
			if i+1 < len(os.Args) {
				for _, s := range strings.Split(os.Args[i+1], ",") {
					if c, err := strconv.ParseUint(strings.TrimSpace(s), 10, 8); err == nil {
						armed[uint8(c)] = true
					}
				}
				i++
			}
		}
	}
	given, err := igneum.AdmitRegistryDocument(&current, doc, sender, landing)
	if err != nil {
		fmt.Printf("REFUSED: %v\n", err)
		os.Exit(1)
	}
	// The sponsored mark's gate by the chain id (sponsored.go): with --rpc
	// the chain id is read from the node; without it a sponsored
	// coordinate is refused here, since no chain can be named for it.
	if sponsoredAny(given) {
		if rpcURL == "" {
			fmt.Println("REFUSED: the document marks a coordinate sponsored and no --rpc names the chain whose sponsored gate admits it")
			os.Exit(1)
		}
		id, err := rpcChainID(rpcURL)
		if err != nil {
			fmt.Printf("REFUSED: the chain id could not be read for the sponsored gate: %v\n", err)
			os.Exit(1)
		}
		if err := igneum.SponsoredGateRefusal(id, landing, given); err != nil {
			fmt.Printf("REFUSED: %v\n", err)
			os.Exit(1)
		}
	}
	// The appended changes' forms BUILT as the engine would build them
	// (section 1a's cut 3: an unbuildable document is refused here, never
	// at the landing), and each against the released worker set's list
	// (the kernel-readiness rule: the notice covers a dataset build, not a
	// kernel port).
	for i, c := range given.Coordinates {
		have := 0
		if i < len(current.Coordinates) {
			have = len(current.Coordinates[i].DocumentChanges)
		}
		for j := have; j < len(c.DocumentChanges); j++ {
			d := c.DocumentChanges[j]
			if d.Void {
				continue
			}
			if !releasedForm(c.Algorithm, d.AlgorithmParams) {
				fmt.Printf("REFUSED: coordinate %d (%s) documentChanges[%d]: the document's form is outside the released workers' list (%s): the notice covers a dataset build, not a kernel port\n", i, c.Name, j, releasedFormsLine())
				os.Exit(1)
			}
			// The engine's own constructor on the chains of record: every
			// chain of record runs under --dev (the research chain, the
			// public test network), whose engine builds through NewDev
			// (the C reference's documents are development-only names
			// outside it); pow.New refused "igneumhash" on the proof
			// chain's first run of this tool.
			a, err := pow.NewDev(c.Algorithm, d.AlgorithmParams)
			if err != nil {
				fmt.Printf("REFUSED: coordinate %d (%s) documentChanges[%d] at %d: the document does not build as the engine builds it: %v\n", i, c.Name, j, d.Height, err)
				os.Exit(1)
			}
			if cl, ok := a.(interface{ Close() }); ok {
				cl.Close()
			}
			// A keyed document's UNKEYED TWIN, built as the engine builds
			// it beside the form (vector.buildTwin: pow.UnkeyedTwin, the
			// same constructor): F-580 (17 October 2026) admitted a keyed
			// entry here that no node could build ("the unkeyed twin:
			// params rejected"), since the twin is derived, not written in
			// the document; the twin's build is the admission's last word.
			if msg := twinBuilds(c.Algorithm, d.AlgorithmParams); msg != "" {
				fmt.Printf("REFUSED: coordinate %d (%s) documentChanges[%d] at %d: %s\n", i, c.Name, j, d.Height, msg)
				os.Exit(1)
			}
		}
	}
	fmt.Printf("ADMITTED %d coordinates at a landing of %d or under, document sha256 %x\n", len(given.Coordinates), landing, sha256.Sum256(doc))
	// The in-place entry's own gate (LANE-MODIFICATION.md section 1a, the
	// chair's cell on 0.10.41): a document carrying a coordinate's
	// documentChanges lands on no chain until every node of record answers
	// a registry view with the document-change entry (a node before the
	// entry refuses such a landing and the chain forks between versions at
	// the change's height); the nodes of record are named with --nodes.
	if addsDocumentChanges(&current, given) {
		if len(nodes) == 0 {
			fmt.Println("PRE-FLIGHT REFUSED: the document carries documentChanges and no --nodes were given to read every node of record's registry view (a node without the document-change entry would refuse the landing and fork at the change's height)")
			os.Exit(2)
		}
		for _, n := range nodes {
			ok, err := rpcDocumentChangeEntry(n)
			if err != nil {
				fmt.Printf("PRE-FLIGHT REFUSED: node %s: the registry view: %v\n", n, err)
				os.Exit(2)
			}
			if !ok {
				fmt.Printf("PRE-FLIGHT REFUSED: node %s answers a registry view without the document-change entry (a version before the entry): the landing would be refused there and the chain would fork at the change's height; every node of record rolls first\n", n)
				os.Exit(2)
			}
		}
		fmt.Printf("PRE-FLIGHT: the document carries documentChanges; every node of record named (%d) answers the document-change entry\n", len(nodes))
	}
	if rpcURL == "" {
		if len(newPayHeights(&current, given, landing)) > 0 {
			fmt.Println("PRE-FLIGHT SKIPPED: the document changes the paid set above the landing and no --rpc was given to read the chain's sealers (F-221)")
		}
		return
	}
	preflight(rpcURL, &current, given, landing, armed)
}

// releasedForms is the released worker set's list (the kernel-readiness
// rule, LANE-MODIFICATION.md section 1): the IgneumHash versions the
// released workers run (Ember's kernels of record: version 15 the
// bandwidth walk, 18 the raster, 21 the Anvil-R programme, 25 the Anvil sp4
// programme) and the graph coordinate. Kept with the worker releases.
var releasedForms = map[string][]float64{
	"igneumhash":   {15, 18, 21, 25},
	"igneum-graph": nil,
}

// releasedForm reports whether a document is one the released workers run.
// twinBuilds derives a keyed document's unkeyed twin (pow.UnkeyedTwin) and
// builds it through the engine's constructor; "" when the document is
// unkeyed (no twin) or the twin builds, else the refusal's words.
func twinBuilds(algorithm string, params []byte) string {
	twin, err := pow.UnkeyedTwin(params)
	if err != nil {
		return fmt.Sprintf("the unkeyed twin cannot be derived: %v", err)
	}
	if twin == nil {
		return ""
	}
	u, err := pow.NewDev(algorithm, twin)
	if err != nil {
		return fmt.Sprintf("the document's unkeyed twin does not build as the engine builds it: %v", err)
	}
	if cl, ok := u.(interface{ Close() }); ok {
		cl.Close()
	}
	return ""
}

// sponsoredAny reports whether the document marks any coordinate sponsored.
func sponsoredAny(v *ctypes.IgneumVector) bool {
	for _, c := range v.Coordinates {
		if c.Sponsored {
			return true
		}
	}
	return false
}

// rpcChainID reads the node's chain id (eth_chainId).
func rpcChainID(url string) (uint64, error) {
	var hex string
	if err := rpcCall(url, "eth_chainId", nil, &hex); err != nil {
		return 0, err
	}
	id, ok := new(big.Int).SetString(strings.TrimPrefix(hex, "0x"), 16)
	if !ok {
		return 0, fmt.Errorf("eth_chainId answered %q", hex)
	}
	return id.Uint64(), nil
}

func releasedForm(algorithm string, params []byte) bool {
	versions, ok := releasedForms[algorithm]
	if !ok {
		return false
	}
	if versions == nil {
		return true
	}
	var m map[string]any
	if json.Unmarshal(params, &m) != nil {
		return false
	}
	v, ok := m["version"].(float64)
	if !ok {
		return false
	}
	for _, r := range versions {
		if r == v {
			return true
		}
	}
	return false
}

func releasedFormsLine() string {
	var parts []string
	for a, vs := range releasedForms {
		if vs == nil {
			parts = append(parts, a)
			continue
		}
		var s []string
		for _, v := range vs {
			s = append(s, strconv.Itoa(int(v)))
		}
		parts = append(parts, a+" versions "+strings.Join(s, ","))
	}
	return strings.Join(parts, "; ")
}

// ReferenceBoundFactor bounds an appended change's reference to a factor of
// the lane's running difficulty at the landing (the security review's read, cut
// 4): two doublings, the span the controller re-converges across inside
// two half-lives even without the re-anchoring at A, and with it the guard
// on the registrant's arithmetic.
const ReferenceBoundFactor = 4

// referenceBound judges an appended change's reference against the lane's
// running difficulty: within the factor both ways.
func referenceBound(dRef uint64, running *big.Int) (bool, string) {
	if running == nil || running.Sign() <= 0 {
		return false, "the lane's running difficulty is not read"
	}
	lo := new(big.Int).Div(running, big.NewInt(ReferenceBoundFactor))
	hi := new(big.Int).Mul(running, big.NewInt(ReferenceBoundFactor))
	d := new(big.Int).SetUint64(dRef)
	line := fmt.Sprintf("the reference %d against the running difficulty %s (the bound a factor of %d: %s to %s)", dRef, running, ReferenceBoundFactor, lo, hi)
	return d.Cmp(lo) >= 0 && d.Cmp(hi) <= 0, line
}

// addsDocumentChanges reports whether the document carries an in-place
// document change the registration in force does not.
func addsDocumentChanges(current, given *ctypes.IgneumVector) bool {
	for i, c := range given.Coordinates {
		have := 0
		if i < len(current.Coordinates) {
			have = len(current.Coordinates[i].DocumentChanges)
		}
		if len(c.DocumentChanges) > have {
			return true
		}
	}
	return false
}

// rpcDocumentChangeEntry reads whether a node's registry view carries the
// document-change entry (false on a node before it: the field absent).
func rpcDocumentChangeEntry(url string) (bool, error) {
	var view struct {
		DocumentChangeEntry bool `json:"documentChangeEntry"`
	}
	if err := rpcCall(url, "igneum_vectorRegistry", []any{"latest"}, &view); err != nil {
		return false, err
	}
	return view.DocumentChangeEntry, nil
}

// newPayHeights is every pay change the document adds above the landing.
func newPayHeights(current, given *ctypes.IgneumVector, landing uint64) []uint64 {
	seen := map[uint64]bool{}
	for _, p := range current.PayChanges {
		seen[p.Height] = true
	}
	var out []uint64
	for _, p := range given.PayChanges {
		if p.Height > landing && !seen[p.Height] {
			out = append(out, p.Height)
		}
	}
	return out
}

// paidAt is the set of coordinates with pay weight at height under the
// document (the last pay change at or under the height, else the base
// shares, else every coordinate).
func paidAt(v *ctypes.IgneumVector, height uint64) []uint8 {
	shares := v.PayShares
	for _, p := range v.PayChanges {
		if p.Height <= height {
			shares = p.PayShares
		}
	}
	var out []uint8
	for i := range v.Coordinates {
		if len(shares) == 0 || (i < len(shares) && shares[i] > 0) {
			out = append(out, uint8(i))
		}
	}
	return out
}

func preflight(url string, current, given *ctypes.IgneumVector, landing uint64, armed map[uint8]bool) {
	// The appended changes' references against the lanes' running
	// difficulties at the head (cut 4's bound).
	for i, c := range given.Coordinates {
		have := 0
		if i < len(current.Coordinates) {
			have = len(current.Coordinates[i].DocumentChanges)
		}
		for j := have; j < len(c.DocumentChanges); j++ {
			d := c.DocumentChanges[j]
			if d.Void {
				continue
			}
			running, err := rpcRunningDifficulty(url, uint8(i))
			if err != nil {
				fmt.Printf("PRE-FLIGHT REFUSED: coordinate %d's running difficulty: %v\n", i, err)
				os.Exit(2)
			}
			ok, line := referenceBound(d.DRef, running)
			if !ok {
				fmt.Printf("PRE-FLIGHT REFUSED: coordinate %d (%s) documentChanges[%d]: %s: a reference outside the bound is a difficulty step at the change's height\n", i, c.Name, j, line)
				os.Exit(2)
			}
			fmt.Printf("PRE-FLIGHT: coordinate %d (%s) documentChanges[%d]: %s\n", i, c.Name, j, line)
		}
	}
	heights := newPayHeights(current, given, landing)
	if len(heights) == 0 {
		fmt.Println("PRE-FLIGHT: the document adds no pay change above the landing; the sealing set is unchanged")
		return
	}
	head, err := rpcHead(url)
	if err != nil {
		fmt.Printf("PRE-FLIGHT REFUSED: the chain's head: %v\n", err)
		os.Exit(2)
	}
	const window = 64
	sealed := map[uint8]int{}
	from := uint64(0)
	if head > window {
		from = head - window + 1
	}
	for h := from; h <= head; h++ {
		c, err := rpcCoord(url, h)
		if err != nil {
			fmt.Printf("PRE-FLIGHT REFUSED: block %d's coordinate: %v\n", h, err)
			os.Exit(2)
		}
		sealed[c]++
	}
	refused := false
	for _, a := range heights {
		ok, line := judge(a, paidAt(given, a), sealed, armed, window)
		if ok {
			fmt.Println("PRE-FLIGHT:", line)
		} else {
			fmt.Println("PRE-FLIGHT REFUSED:", line, "(no running sealer would build the block at that height: arm move-at-height on the sealing nodes for the successor coordinates before the send, and name them with --armed)")
			refused = true
		}
	}
	if refused {
		os.Exit(2)
	}
}

// judge is the pre-flight's rule for one pay change: the set paid from
// height a passes when one of its coordinates sealed in the window or is
// named armed; the line names the set, the sealing coordinates with their
// counts and the armed ones.
func judge(a uint64, set []uint8, sealed map[uint8]int, armed map[uint8]bool, window int) (bool, string) {
	var names, live, arm []string
	ok := false
	for _, c := range set {
		names = append(names, strconv.Itoa(int(c)))
		if sealed[c] > 0 {
			live = append(live, fmt.Sprintf("%d (%d of the last %d blocks)", c, sealed[c], window))
			ok = true
		}
		if armed[c] {
			arm = append(arm, strconv.Itoa(int(c)))
			ok = true
		}
	}
	return ok, fmt.Sprintf("the paid set from %d is {%s}; sealing now: %s; armed for A-1: %s", a, strings.Join(names, ","), orNone(live), orNone(arm))
}

func orNone(s []string) string {
	if len(s) == 0 {
		return "none"
	}
	return strings.Join(s, ", ")
}

func rpcCall(url, method string, params []any, out any) error {
	body, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 1, "method": method, "params": params})
	resp, err := http.Post(url, "application/json", bytes.NewReader(body))
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	var r struct {
		Result json.RawMessage `json:"result"`
		Error  *struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&r); err != nil {
		return err
	}
	if r.Error != nil {
		return fmt.Errorf("%s", r.Error.Message)
	}
	return json.Unmarshal(r.Result, out)
}

func rpcHead(url string) (uint64, error) {
	var hex string
	if err := rpcCall(url, "eth_blockNumber", nil, &hex); err != nil {
		return 0, err
	}
	return strconv.ParseUint(strings.TrimPrefix(hex, "0x"), 16, 64)
}

// rpcRunningDifficulty is the lane's next difficulty at the head (the
// controller's output for coordinate c, igneum_vectorWork's
// nextDifficulty, a decimal number as big.Int marshals).
func rpcRunningDifficulty(url string, c uint8) (*big.Int, error) {
	var info struct {
		Next []*big.Int `json:"nextDifficulty"`
	}
	if err := rpcCall(url, "igneum_vectorWork", []any{"latest"}, &info); err != nil {
		return nil, err
	}
	if int(c) >= len(info.Next) || info.Next[c] == nil {
		return nil, fmt.Errorf("no next difficulty for coordinate %d in the vector work view", c)
	}
	return info.Next[c], nil
}

func rpcCoord(url string, height uint64) (uint8, error) {
	var info struct {
		Coord uint8 `json:"coord"`
	}
	if err := rpcCall(url, "igneum_vectorWork", []any{fmt.Sprintf("0x%x", height)}, &info); err != nil {
		return 0, err
	}
	return info.Coord, nil
}
