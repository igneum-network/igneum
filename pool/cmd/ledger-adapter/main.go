// Command ledger-adapter is the TP-15 adapter (tests/pool/ledger.py,
// owner ECO): it applies one event to a durable pool ledger kept in a
// state directory and prints the ledger state as JSON. Crash injection:
// IGNEUM_LEDGER_CRASH_AT=<step> (or -crash-at) exits with code 3 at that
// step of the payout path, before the write that step would make. The
// next invocation recovers: a debit recorded without a broadcast is
// broadcast exactly once (the chain file is checked first, the analogue
// of the nonce lookup in docs/ecosystem/DESIGN.md section 2.11).
//
// Events (one JSON object, -event or stdin):
//
//	{"type":"share","account":"0x..","weight":1}
//	{"type":"block","hash":"0x..","height":10,"reward":"1000"}
//	{"type":"head","height":260,"canonical":{"10":"0x.."}}
//	{"type":"restart"}
//	{"type":"payout","min":"1"}
//	{"type":"state"}
package main

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"math/big"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"time"

	"igneum.network/pool/internal/ledger"
	"igneum.network/pool/internal/limits"
	"igneum.network/pool/internal/reconcile"
	"igneum.network/pool/internal/shares"
)

func main() {
	var (
		dir      = flag.String("state", "", "state directory (required)")
		event    = flag.String("event", "", "the event as JSON; stdin when empty")
		crashAt  = flag.Int("crash-at", 0, "crash before payout step N (1 to 4); IGNEUM_LEDGER_CRASH_AT overrides")
		maturity = flag.Uint64("maturity", limits.DefaultMaturityBlocks, "maturity depth in blocks (SD-15)")
		feeBps   = flag.Uint("fee-bps", 0, "the one pool fee in basis points")
		window   = flag.Uint64("window", 100, "PPLNS window weight")
	)
	flag.Parse()
	if *dir == "" {
		fmt.Fprintln(os.Stderr, "ledger-adapter: -state is required")
		os.Exit(2)
	}
	if v := os.Getenv("IGNEUM_LEDGER_CRASH_AT"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil {
			fmt.Fprintln(os.Stderr, "ledger-adapter: bad IGNEUM_LEDGER_CRASH_AT")
			os.Exit(2)
		}
		*crashAt = n
	}
	raw := []byte(*event)
	if len(raw) == 0 {
		b, err := io.ReadAll(io.LimitReader(os.Stdin, 1<<20))
		if err != nil {
			fmt.Fprintln(os.Stderr, "ledger-adapter:", err)
			os.Exit(2)
		}
		raw = b
	}
	a, err := open(*dir, *maturity, uint32(*feeBps), *window)
	if err != nil {
		fmt.Fprintln(os.Stderr, "ledger-adapter:", err)
		os.Exit(2)
	}
	a.crashAt = *crashAt
	a.exit = os.Exit
	st, err := a.apply(raw)
	if err != nil {
		fmt.Fprintln(os.Stderr, "ledger-adapter:", err)
		os.Exit(1)
	}
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	if err := enc.Encode(st); err != nil {
		os.Exit(1)
	}
}

var errCrash = errors.New("ledger-adapter: injected crash")

// Event is one input.
type Event struct {
	Type      string            `json:"type"`
	Account   string            `json:"account,omitempty"`
	Weight    uint64            `json:"weight,omitempty"`
	Hash      string            `json:"hash,omitempty"`
	Height    uint64            `json:"height,omitempty"`
	Reward    string            `json:"reward,omitempty"`
	Canonical map[string]string `json:"canonical,omitempty"`
	Min       string            `json:"min,omitempty"`
}

// metaRow is one line of meta.jsonl: the non-ledger durable facts.
type metaRow struct {
	T         string            `json:"t"`
	Account   string            `json:"account,omitempty"`
	Weight    uint64            `json:"weight,omitempty"`
	Hash      string            `json:"hash,omitempty"`
	Height    uint64            `json:"height,omitempty"`
	Reward    string            `json:"reward,omitempty"`
	Canonical map[string]string `json:"canonical,omitempty"`
	ID        string            `json:"id,omitempty"`
	Amount    string            `json:"amount,omitempty"`
	State     string            `json:"state,omitempty"`
}

type blockRec struct {
	Height uint64 `json:"height"`
	Reward string `json:"reward"`
	State  string `json:"state"`
}

type intentRec struct {
	Account string `json:"account"`
	Amount  string `json:"amount"`
	State   string `json:"state"`
}

// State is what the adapter prints.
type State struct {
	OK              bool                         `json:"ok"`
	Liability       string                       `json:"liability"`
	Credits         string                       `json:"credits"`
	Payments        string                       `json:"payments"`
	Broadcast       string                       `json:"broadcast"`
	UnsettledDebits int                          `json:"unsettled_debits"`
	Fees            string                       `json:"fees"`
	Immature        string                       `json:"immature"`
	Payable         string                       `json:"payable"`
	Accounts        map[string]map[string]string `json:"accounts"`
	Blocks          map[string]blockRec          `json:"blocks"`
	Intents         map[string]intentRec         `json:"intents"`
	Audit           []map[string]string          `json:"audit"`
	JournalRows     int                          `json:"journal_rows"`
	Head            uint64                       `json:"head"`
	Window          uint64                       `json:"window_weight"`
}

type adapter struct {
	dir       string
	maturity  uint64
	feeBps    uint32
	store     *ledger.File
	book      *ledger.Ledger
	window    *shares.Window
	blocks    map[string]*blockRec
	order     []string
	intents   map[string]*intentRec
	intentSeq int
	chain     map[string]struct{} // intent ids on the fake chain
	head      uint64
	canonical map[uint64]string
	fees      *big.Int
	crashAt   int
	exit      func(int)
}

func open(dir string, maturity uint64, feeBps uint32, window uint64) (*adapter, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	store, err := ledger.OpenFile(filepath.Join(dir, "journal.jsonl"))
	if err != nil {
		return nil, err
	}
	a := &adapter{
		dir: dir, maturity: maturity, feeBps: feeBps, store: store,
		book: ledger.New(store), window: shares.NewWindow(window),
		blocks: map[string]*blockRec{}, intents: map[string]*intentRec{},
		chain: map[string]struct{}{}, canonical: map[uint64]string{}, fees: new(big.Int),
	}
	if err := a.book.Rebuild(context.Background()); err != nil {
		return nil, err
	}
	if err := a.replayMeta(); err != nil {
		return nil, err
	}
	if err := a.replayChain(); err != nil {
		return nil, err
	}
	for h, b := range a.blocks {
		b.State = a.book.BlockState(h)
	}
	return a, nil
}

func readLines(path string, fn func([]byte) error) error {
	fh, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	defer fh.Close()
	r := bufio.NewReader(fh)
	for {
		line, err := r.ReadBytes('\n')
		if err != nil && !errors.Is(err, io.EOF) {
			return err
		}
		if len(line) > 0 && line[len(line)-1] == '\n' {
			if ferr := fn(line); ferr != nil {
				return ferr
			}
		}
		if err != nil {
			return nil
		}
	}
}

func (a *adapter) replayMeta() error {
	return readLines(filepath.Join(a.dir, "meta.jsonl"), func(line []byte) error {
		var m metaRow
		if err := json.Unmarshal(line, &m); err != nil {
			return errors.New("corrupt meta line")
		}
		switch m.T {
		case "share":
			a.window.Add(shares.Share{Account: m.Account, Weight: m.Weight})
		case "block":
			a.blocks[m.Hash] = &blockRec{Height: m.Height, Reward: m.Reward}
			a.order = append(a.order, m.Hash)
			if f, ok := new(big.Int).SetString(m.Amount, 10); ok {
				a.fees.Add(a.fees, f)
			}
		case "head":
			a.head = m.Height
			for k, v := range m.Canonical {
				h, err := strconv.ParseUint(k, 10, 64)
				if err == nil {
					a.canonical[h] = v
				}
			}
		case "intent":
			rec, ok := a.intents[m.ID]
			if !ok {
				rec = &intentRec{Account: m.Account, Amount: m.Amount}
				a.intents[m.ID] = rec
				a.intentSeq++
			}
			rec.State = m.State
		}
		return nil
	})
}

func (a *adapter) replayChain() error {
	return readLines(filepath.Join(a.dir, "chain.jsonl"), func(line []byte) error {
		var m metaRow
		if err := json.Unmarshal(line, &m); err != nil {
			return errors.New("corrupt chain line")
		}
		a.chain[m.ID] = struct{}{}
		return nil
	})
}

func appendLine(path string, v interface{}) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	fh, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return err
	}
	defer fh.Close()
	if _, err := fh.Write(append(b, '\n')); err != nil {
		return err
	}
	return fh.Sync()
}

func (a *adapter) meta(m metaRow) error { return appendLine(filepath.Join(a.dir, "meta.jsonl"), m) }

func (a *adapter) crash(step int) error {
	if a.crashAt == step {
		if a.exit != nil {
			a.exit(3)
		}
		return errCrash
	}
	return nil
}

// apply runs recovery, then the event, then prints the state.
func (a *adapter) apply(raw []byte) (*State, error) {
	ctx := context.Background()
	now := time.Unix(0, 0)
	if err := a.recover(ctx, now); err != nil {
		return nil, err
	}
	var ev Event
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&ev); err != nil {
		return nil, fmt.Errorf("event: %w", err)
	}
	switch ev.Type {
	case "share":
		if !limits.ValidAddress(ev.Account) || ev.Weight == 0 {
			return nil, errors.New("share: account or weight out of bounds")
		}
		if err := a.meta(metaRow{T: "share", Account: ev.Account, Weight: ev.Weight}); err != nil {
			return nil, err
		}
		a.window.Add(shares.Share{Account: ev.Account, Weight: ev.Weight})
	case "block":
		reward, ok := new(big.Int).SetString(ev.Reward, 10)
		if !ok || ev.Hash == "" {
			return nil, errors.New("block: hash or reward out of bounds")
		}
		if _, dup := a.blocks[ev.Hash]; dup {
			return nil, errors.New("block: already recorded")
		}
		d, err := shares.Distribute(shares.Block{Hash: ev.Hash, Height: ev.Height, Reward: reward}, a.window.Snapshot(), a.feeBps, nil)
		if err != nil {
			return nil, err
		}
		if err := a.book.ApplyDistribution(ctx, d, now); err != nil {
			return nil, err
		}
		if err := a.meta(metaRow{T: "block", Hash: ev.Hash, Height: ev.Height, Reward: ev.Reward, Amount: d.Fee.String()}); err != nil {
			return nil, err
		}
		a.blocks[ev.Hash] = &blockRec{Height: ev.Height, Reward: ev.Reward, State: a.book.BlockState(ev.Hash)}
		a.order = append(a.order, ev.Hash)
		a.fees.Add(a.fees, d.Fee)
	case "head":
		if err := a.meta(metaRow{T: "head", Height: ev.Height, Canonical: ev.Canonical}); err != nil {
			return nil, err
		}
		a.head = ev.Height
		for k, v := range ev.Canonical {
			h, err := strconv.ParseUint(k, 10, 64)
			if err != nil {
				return nil, errors.New("head: bad height key")
			}
			a.canonical[h] = v
		}
		if err := a.reconcile(ctx, now); err != nil {
			return nil, err
		}
	case "payout":
		min, ok := new(big.Int).SetString(ev.Min, 10)
		if !ok {
			min = big.NewInt(1)
		}
		if err := a.payout(ctx, min, now); err != nil {
			return nil, err
		}
	case "restart", "state":
		// recovery and the state print are the whole event
	default:
		return nil, errors.New("unknown event type")
	}
	return a.state(), nil
}

// reconcile checks every found block against the canonical view.
func (a *adapter) reconcile(ctx context.Context, now time.Time) error {
	r := reconcile.New(a, a.book, a.maturity)
	for _, h := range a.order {
		if a.book.BlockState(h) == ledger.StateImmature {
			r.Track(reconcile.Found{Hash: h, Height: a.blocks[h].Height})
		}
	}
	if err := r.Run(ctx, now); err != nil {
		return err
	}
	for h, b := range a.blocks {
		b.State = a.book.BlockState(h)
	}
	return nil
}

// Head and HashAt make the adapter the reconciler's chain view.
func (a *adapter) Head(ctx context.Context) (uint64, error) { return a.head, nil }

func (a *adapter) HashAt(ctx context.Context, height uint64) (string, error) {
	if h, ok := a.canonical[height]; ok {
		return h, nil
	}
	for hash, b := range a.blocks {
		if b.Height == height {
			return hash, nil
		}
	}
	return "", nil
}

// payout pays every account at or above min, one intent each, with the
// crash points of the payout path: 1 before the debit, 2 after the debit
// before signing, 3 after signing before the chain write, 4 after the
// chain write before the broadcast record.
func (a *adapter) payout(ctx context.Context, min *big.Int, now time.Time) error {
	accounts := a.accountsWithPayable(min)
	for _, acc := range accounts {
		bal := a.book.Balances(acc)
		amount := new(big.Int).Set(bal.Payable)
		if err := a.crash(1); err != nil {
			return err
		}
		a.intentSeq++
		id := "intent-" + strconv.Itoa(a.intentSeq)
		if err := a.book.RecordDebit(ctx, acc, amount, id, now); err != nil {
			return err
		}
		if err := a.meta(metaRow{T: "intent", ID: id, Account: acc, Amount: amount.String(), State: "debited"}); err != nil {
			return err
		}
		a.intents[id] = &intentRec{Account: acc, Amount: amount.String(), State: "debited"}
		if err := a.settle(id); err != nil {
			return err
		}
	}
	return nil
}

// settle takes an intent from debited to confirmed with the crash points.
func (a *adapter) settle(id string) error {
	rec := a.intents[id]
	if rec.State == "debited" {
		if err := a.crash(2); err != nil {
			return err
		}
		sig := sha256.Sum256([]byte(id + rec.Account + rec.Amount))
		_ = hex.EncodeToString(sig[:])
		if err := a.meta(metaRow{T: "intent", ID: id, State: "signed"}); err != nil {
			return err
		}
		rec.State = "signed"
	}
	if rec.State == "signed" {
		if err := a.crash(3); err != nil {
			return err
		}
		if _, onChain := a.chain[id]; !onChain {
			if err := appendLine(filepath.Join(a.dir, "chain.jsonl"), metaRow{T: "tx", ID: id, Amount: rec.Amount, Account: rec.Account}); err != nil {
				return err
			}
			a.chain[id] = struct{}{}
		}
		if err := a.crash(4); err != nil {
			return err
		}
		if err := a.meta(metaRow{T: "intent", ID: id, State: "broadcast"}); err != nil {
			return err
		}
		rec.State = "broadcast"
	}
	if rec.State == "broadcast" {
		if err := a.meta(metaRow{T: "intent", ID: id, State: "confirmed"}); err != nil {
			return err
		}
		rec.State = "confirmed"
	}
	return nil
}

// recover finishes every intent a crash left between the debit and the
// confirmation. The chain file is checked before any broadcast, so an
// intent is never broadcast twice.
func (a *adapter) recover(ctx context.Context, now time.Time) error {
	ids := make([]string, 0, len(a.intents))
	for id := range a.intents {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		if a.intents[id].State != "confirmed" {
			if err := a.settle(id); err != nil {
				return err
			}
		}
	}
	return nil
}

func (a *adapter) accountsWithPayable(min *big.Int) []string {
	seen := map[string]struct{}{}
	var out []string
	for _, s := range a.window.Snapshot() {
		seen[s.Account] = struct{}{}
	}
	for _, rec := range a.intents {
		seen[rec.Account] = struct{}{}
	}
	for acc := range a.allAccounts() {
		seen[acc] = struct{}{}
	}
	for acc := range seen {
		if acc == shares.CarryAccount {
			continue
		}
		if a.book.Balances(acc).Payable.Cmp(min) >= 0 {
			out = append(out, acc)
		}
	}
	sort.Strings(out)
	return out
}

func (a *adapter) allAccounts() map[string]struct{} {
	out := map[string]struct{}{}
	_ = a.store.Replay(context.Background(), func(e ledger.Entry) error {
		out[e.Account] = struct{}{}
		return nil
	})
	return out
}

func (a *adapter) state() *State {
	st := &State{
		OK:       true,
		Accounts: map[string]map[string]string{},
		Blocks:   map[string]blockRec{},
		Intents:  map[string]intentRec{},
		Audit:    []map[string]string{},
		Head:     a.head,
		Window:   a.window.Total(),
		Fees:     a.fees.String(),
	}
	credits, payments, immature, payable := new(big.Int), new(big.Int), new(big.Int), new(big.Int)
	rows := 0
	_ = a.store.Replay(context.Background(), func(e ledger.Entry) error {
		rows++
		switch e.Kind {
		case ledger.CreditImmature:
			credits.Add(credits, e.Amount)
		case ledger.ReverseCredit:
			credits.Sub(credits, e.Amount)
			st.Audit = append(st.Audit, map[string]string{"kind": string(e.Kind), "account": e.Account, "amount": e.Amount.String(), "ref": e.Ref})
		case ledger.Debit:
			payments.Add(payments, e.Amount)
		case ledger.DebitReverse:
			payments.Sub(payments, e.Amount)
			st.Audit = append(st.Audit, map[string]string{"kind": string(e.Kind), "account": e.Account, "amount": e.Amount.String(), "ref": e.Ref})
		}
		return nil
	})
	for acc := range a.allAccounts() {
		b := a.book.Balances(acc)
		immature.Add(immature, b.Immature)
		payable.Add(payable, b.Payable)
		st.Accounts[acc] = map[string]string{"immature": b.Immature.String(), "payable": b.Payable.String(), "paid": b.Paid.String()}
	}
	broadcast := new(big.Int)
	for id, rec := range a.intents {
		st.Intents[id] = *rec
		if _, ok := a.chain[id]; ok {
			if v, ok := new(big.Int).SetString(rec.Amount, 10); ok {
				broadcast.Add(broadcast, v)
			}
		} else {
			st.UnsettledDebits++
		}
	}
	for h, b := range a.blocks {
		st.Blocks[h] = *b
	}
	st.JournalRows = rows
	st.Liability = a.book.Liability().String()
	st.Credits = credits.String()
	st.Payments = payments.String()
	st.Broadcast = broadcast.String()
	st.Immature = immature.String()
	st.Payable = payable.String()
	return st
}
