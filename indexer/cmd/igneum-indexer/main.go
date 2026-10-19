// Command igneum-indexer is the light indexer: it walks the node's
// canonical chain into an append-only row file, reconciles
// reorganisations, and writes the outcomes dashboard JSON (version 0)
// to a file and to a loopback HTTP endpoint. It is not the explorer and
// not a source of claims: every cell carries its source and state.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"igneum.network/indexer/internal/chain"
	"igneum.network/indexer/internal/dashboard"
	"igneum.network/indexer/internal/rpc"
)

func main() {
	var (
		rpcURL   = flag.String("rpc", "http://127.0.0.1:8545/", "node JSON-RPC HTTP URL")
		state    = flag.String("state", "", "state directory (required)")
		network  = flag.String("network", "igneum-devnet", "network label written into the dashboard")
		window   = flag.Int("window", 1000, "blocks per dashboard window")
		interval = flag.Duration("interval", 15*time.Second, "poll interval")
		batch    = flag.Int("batch", 500, "blocks fetched per poll at most")
		listen   = flag.String("listen", "127.0.0.1:8090", "loopback address serving /dashboard.json and /healthz; empty disables")
		once     = flag.Bool("once", false, "walk to the head, write the dashboard, exit")
		chains   = flag.String("chains", "", "dev/chain/igneum-chains.json: the published contracts and the deployer of the network named by -network-short")
		short    = flag.String("network-short", "", "the network's short name in the chains file (empty: the first entry with contracts)")
		exclude  = flag.String("exclude", "", "comma-separated addresses excluded from the external measures beside the deployer (seat test keys)")
	)
	flag.Parse()
	if *state == "" {
		fmt.Fprintln(os.Stderr, "igneum-indexer: -state is required")
		os.Exit(2)
	}
	if err := os.MkdirAll(*state, 0o700); err != nil {
		log.Fatal(err)
	}
	store, err := chain.Open(filepath.Join(*state, "rows.jsonl"))
	if err != nil {
		log.Fatalf("open rows: %v", err)
	}
	client := rpc.New(*rpcURL)
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	var chainID uint64
	var q rpc.Quantity
	if err := client.Call(ctx, "eth_chainId", []interface{}{}, &q); err == nil {
		chainID = q.Uint64()
	}

	contracts, labs := curated(*chains, *short)
	for _, a := range strings.Split(*exclude, ",") {
		if a = strings.TrimSpace(a); a != "" {
			labs = append(labs, a)
		}
	}
	if len(contracts) > 0 {
		log.Printf("curated: %d contracts, %d Labs addresses excluded", len(contracts), len(labs))
	}
	var (
		mu      sync.RWMutex
		current []byte
	)
	publish := func() error {
		var supply *rpc.Supply
		if s, err := client.SupplyAt(ctx, "latest"); err == nil {
			supply = s
		}
		doc := dashboard.Build(dashboard.Input{
			Network: *network, ChainID: chainID, Rows: store.Rows(), Window: *window,
			Reorgs: store.Reorgs, Supply: supply, Now: time.Now(), Contracts: contracts, Labs: labs,
		})
		b, err := json.MarshalIndent(doc, "", "  ")
		if err != nil {
			return err
		}
		tmp := filepath.Join(*state, "dashboard.json.tmp")
		if err := os.WriteFile(tmp, b, 0o644); err != nil {
			return err
		}
		if err := os.Rename(tmp, filepath.Join(*state, "dashboard.json")); err != nil {
			return err
		}
		mu.Lock()
		current = b
		mu.Unlock()
		return nil
	}

	if *listen != "" && !*once {
		mux := http.NewServeMux()
		mux.HandleFunc("/dashboard.json", func(w http.ResponseWriter, r *http.Request) {
			mu.RLock()
			b := current
			mu.RUnlock()
			if b == nil {
				http.Error(w, "not yet built", http.StatusServiceUnavailable)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			w.Write(b) //nolint:errcheck
		})
		mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) { fmt.Fprintln(w, "ok") })
		srv := &http.Server{Addr: *listen, Handler: mux, ReadHeaderTimeout: 5 * time.Second}
		go func() {
			if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
				log.Fatal(err)
			}
		}()
		go func() {
			<-ctx.Done()
			c, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			srv.Shutdown(c) //nolint:errcheck
		}()
	}

	for {
		for {
			added, err := store.Step(ctx, client, *batch)
			if err != nil {
				if ctx.Err() != nil {
					return
				}
				log.Printf("step: %v", err)
				break
			}
			if added == 0 {
				break
			}
			log.Printf("indexed %d blocks; height %d; reorganisations %d", added, store.Height(), store.Reorgs)
		}
		if err := publish(); err != nil {
			log.Printf("publish: %v", err)
		}
		if *once {
			return
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(*interval):
		}
	}
}

// curated reads the published contracts and the deployer (a Labs address)
// of one network from dev/chain/igneum-chains.json. Missing file: none.
func curated(path, short string) (map[string]string, []string) {
	if path == "" {
		return nil, nil
	}
	b, err := os.ReadFile(path)
	if err != nil {
		log.Printf("chains file: %v", err)
		return nil, nil
	}
	var doc struct {
		Networks []struct {
			Short      string                     `json:"short"`
			Contracts  map[string]json.RawMessage `json:"contracts"`
			TestAssets map[string]json.RawMessage `json:"test_assets"`
			SeatKeys   []string                   `json:"seat_test_addresses"`
		} `json:"networks"`
	}
	if err := json.Unmarshal(b, &doc); err != nil {
		log.Printf("chains file: %v", err)
		return nil, nil
	}
	for _, n := range doc.Networks {
		if short != "" && n.Short != short {
			continue
		}
		if len(n.Contracts) == 0 {
			continue
		}
		contracts := map[string]string{}
		var labs []string
		for _, set := range []map[string]json.RawMessage{n.Contracts, n.TestAssets} {
			for name, raw := range set {
				var addr string
				if json.Unmarshal(raw, &addr) != nil || len(addr) != 42 {
					continue
				}
				if name == "deployer" {
					labs = append(labs, addr)
					continue
				}
				contracts[name] = addr
			}
		}
		labs = append(labs, n.SeatKeys...)
		return contracts, labs
	}
	return nil, nil
}
