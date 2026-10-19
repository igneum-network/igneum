package main

import (
	"fmt"
	"math/rand"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"testing"
	"time"
)

// TestConsoleCmdNetworkIdentities tests network identity variables at runtime for a geth instance.
// This provides a "production equivalent" integration test for consensus-relevant chain identity values which
// cannot be adequately unit tested because of reliance on cli context variables.
// These tests should cover expected default values and possible flag-interacting values, like --<chain> with --networkid=n.
func TestConsoleCmdNetworkIdentities(t *testing.T) {
	chainIdentityCases := []struct {
		flags       []string
		networkId   int
		chainId     int
		genesisHash string
	}{
		// Default chain value, without and with --networkid flag set.
		// One network (PA-03): the only chains a node can start on without a
		// genesis file are the development chain (--dev, --dev.pow). Its genesis
		// hash varies with the developer account, so it is not checked here
		// (params.TestGenesisHashIgneumDev covers the default dev genesis).
		{[]string{"--dev"}, 7310, 7310, "0x0"},
		{[]string{"--dev", "--networkid", "42"}, 42, 7310, "0x0"},
		{[]string{"--dev.pow"}, 7310, 7310, "0x0"},
	}
	for i, p := range chainIdentityCases {
		// Disable networking, preventing false-negatives if in an environment without networking service
		// or collisions with an existing geth service.
		p.flags = append(p.flags, "--port", "0", "--maxpeers", "0", "--nodiscover", "--nat", "none")

		t.Run(fmt.Sprintf("%d/networkid", i),
			consoleCmdStdoutTest(p.flags, "admin.nodeInfo.protocols.eth.network", p.networkId))
		t.Run(fmt.Sprintf("%d/chainid", i),
			consoleCmdStdoutTest(p.flags, "admin.nodeInfo.protocols.eth.config.chainId", p.chainId))

		// The developer mode block has a dynamic genesis, depending on a parameterized address (coinbase) value.
		if p.genesisHash != "0x0" {
			t.Run(fmt.Sprintf("%d/genesis_hash", i),
				consoleCmdStdoutTest(p.flags, "eth.getBlock(0, false).hash", strconv.Quote(p.genesisHash)))
		}
	}
}

func consoleCmdStdoutTest(flags []string, execCmd string, want interface{}) func(t *testing.T) {
	return func(t *testing.T) {
		flags = append(flags, "--ipcpath", filepath.Join(os.TempDir(), "geth.ipc"), "--exec", execCmd, "console")
		t.Log("flags:", flags)
		geth := runGeth(t, flags...)
		geth.KillTimeout = 20 * time.Second
		geth.Expect(fmt.Sprintf(`%v
`, want))
		geth.ExpectExit()
		if status := geth.ExitStatus(); status != 0 {
			t.Errorf("expected exit status 0, got: %d", status)
		}
	}
}

// TestGethFailureToLaunch tests that geth fail immediately when given invalid run parameters (ie CLI args).
func TestGethFailureToLaunch(t *testing.T) {
	cases := []struct {
		flags            []string
		expectErrorReStr string
	}{
		{
			flags:            []string{"--badnet"},
			expectErrorReStr: "(?ism)incorrect usage.*",
		},
		// The foreign network flags are gone (PA-03).
		{flags: []string{"--classic"}, expectErrorReStr: "(?ism)incorrect usage.*"},
		{flags: []string{"--mainnet"}, expectErrorReStr: "(?ism)incorrect usage.*"},
		{flags: []string{"--sepolia"}, expectErrorReStr: "(?ism)incorrect usage.*"},
		{flags: []string{"--mordor"}, expectErrorReStr: "(?ism)incorrect usage.*"},
		{flags: []string{"--mintme"}, expectErrorReStr: "(?ism)incorrect usage.*"},
		{flags: []string{"--holesky"}, expectErrorReStr: "(?ism)incorrect usage.*"},
		// No flags and an empty datadir: the launch placeholder names no
		// algorithm and the node refuses (PA-05).
		{flags: []string{"--port", "0", "--maxpeers", "0", "--nodiscover", "--nat", "none", "--datadir", t.TempDir()}, expectErrorReStr: "(?ism).*no proof-of-work algorithm is selected.*"},
	}
	for i, c := range cases {
		t.Run(fmt.Sprintf("%d", i), func(t *testing.T) {
			t.Log("flags:", c.flags)
			geth := runGeth(t, c.flags...)
			geth.ExpectRegexp(c.expectErrorReStr)
			geth.ExpectExit()
			if status := geth.ExitStatus(); status == 0 {
				t.Errorf("expected exit status != 0, got: %d", status)
			}
		})
	}
}

// randomStr is used in naming the geth tests' temporary datadir.
func randomStr(n int) string {
	letterBytes := "abcdefghijklmnopqrstuvwxyz"
	b := make([]byte, n)
	for i := range b {
		b[i] = letterBytes[rand.Int63()%int64(len(letterBytes))]
	}
	return string(b)
}

// TestGethStartupLogs tests that geth logs certain things (given some set of flags).
// In these cases, geth is run with a console command to print its name (and tests that it does).
func TestGethStartupLogs(t *testing.T) {
	// semiPersistentDatadir is used to house an adhoc datadir for co-dependent geth test cases.
	// WATCHOUT: For Unix-based operating systems, you're going to have problems if the IPC endpoint is
	// longer than ___ characters.
	semiPersistentDatadir := filepath.Join(os.TempDir(), fmt.Sprintf("geth-test-%x", randomStr(4)))
	defer os.RemoveAll(semiPersistentDatadir)

	type matching struct {
		pattern string // pattern is the pattern to match against geth's stderr log.
		matches bool   // matches defines if the pattern should succeed or fail, ie. if the pattern should exist or should not exist.
	}
	cases := []struct {
		flags    []string
		matchers []matching

		// callback is run after the geth run case completes.
		// It can be used to reset any persistent state to provide a clean slate for the subsequent cases.
		callback func() error
	}{
		// One network (PA-03): a fresh datadir on the development chain writes
		// its genesis; the same datadir run again finds the stored genesis and
		// is recognised as the development chain (F-02's second refusal, the
		// accepting side).
		{
			flags: []string{"--datadir", semiPersistentDatadir, "--dev", "--miner.threads", "-1"},
			matchers: []matching{
				{pattern: "(?ism).+INFO.+Wrote genesis block OK.*", matches: true},
			},
		},
		{
			flags: []string{"--datadir", semiPersistentDatadir, "--dev", "--miner.threads", "-1"},
			matchers: []matching{
				{pattern: "(?ism).+INFO.+Found stored genesis block.*", matches: true},
				{pattern: "(?ism).+Bad developer-mode genesis.*", matches: false},
			},
			callback: func() error {
				return os.RemoveAll(semiPersistentDatadir)
			},
		},
	}
	for i, c := range cases {
		t.Run(fmt.Sprintf("%d", i), func(t *testing.T) {
			caseFlags := append(c.flags, "--exec", "admin.nodeInfo.name", "console")
			t.Log("flags:", caseFlags)
			geth := runGeth(t, caseFlags...)
			geth.KillTimeout = 10 * time.Second
			geth.ExpectRegexp("(?ism).*CoreGeth.*")
			geth.ExpectExit()
			if status := geth.ExitStatus(); status != 0 {
				t.Errorf("expected exit status == 0, got: %d", status)
			}
			for _, match := range c.matchers {
				if matched := regexp.MustCompile(match.pattern).MatchString(geth.StderrText()); matched != match.matches {
					t.Errorf("unexpected stderr output; want: %s (matching?=%v) got: %s", match.pattern, match.matches, geth.StderrText())
				}
			}
			if c.callback != nil {
				if err := c.callback(); err != nil {
					t.Fatal(err)
				}
			}
		})
	}
}
