package igneum

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/consensus/igneum/pow"
	"github.com/ethereum/go-ethereum/consensus/igneum/pow/pi"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/crypto"
)

// The proof pi in the validity rule (SPEC-R0A-v0.md 0.9.1; the project's order of
// 11:0x UK, 11 October 2026), behind the flag --igneum.pi-verify:
//
//   - a keyed seal (a block carrying shares) must commit to a proof in its
//     header (PiHash, a tail field outside the seal hash) and carry the
//     proof's bytes in its body (Body.Pi); the commitment is checked by the
//     body validator on every node, flag or not;
//   - with the flag on, the proof is verified by the linked verifier
//     (pow/pi, build tag ignpi; the hash team's staticlib over risc0 3.0.6
//     with PI_IMAGE_ID pinned) against the header's BondKey, the messages
//     m_i of the walk and the carried shares; a block whose proof fails any
//     check is INVALID as a block with a bad seal, at the first failing
//     check's own code (pi.CodeName);
//   - the order on the node: the signature and the eligibility at the
//     parent's state (F-37), then the walk (verifySeal, which yields the m_i
//     and keeps them for the body stage), then the proof in the body stage
//     (its size and system byte first, the STARK and the journal last). The
//     journal needs the m_i, which only the walk yields, so the proof cannot
//     run before the walk; what runs before it is the signature;
//   - with the flag off (devnet5 at 17:00 UK: the secret-reveal claim as the
//     stand-in), no proof is verified and none is required: a block may carry
//     one, checked for its commitment only.
//
// The sealer under the flag: after its nonce, the drawn key's node proves
// its shares with the prover CLI (pi-prove <domain> <sk-file> <m-file>
// <out.bin>, the hash team's; the secret read from a file this node writes
// under its own directory with mode 0600, never printed), attaches PiHash
// and the receipt, and only then announces the block. Without a prover path
// the node seals no keyed block and says so once.

var (
	errPiMissing    = errors.New("igneum pi: a keyed seal without a proof commitment")
	errPiUnexpected = errors.New("igneum pi: a proof commitment on an unkeyed seal")
	errPiBody       = errors.New("igneum pi: the body carries no proof for the header's commitment")
	errPiRefused    = errors.New("igneum pi: the proof is refused")
	errPiNoMessages = errors.New("igneum pi: the walk yields no messages for this block (no keyed entry with messages)")
	errPiNoProver   = errors.New("igneum pi: no prover on this node (--igneum.pi-prover): a keyed block cannot be sealed here")
)

// piMessages keeps the messages m_i the header stage's replay yielded, by
// block hash, for the body stage (a bounded ring: the fetcher's and the
// sync's windows are far under it).
// sealClaims is the first-finder rule of a template (devnet7's stall at
// 350, 11 October 2026): the first thread to find a seal for a template
// claims it and proves; later finders on the same template discard without
// proving (a template yields one block, and a proof is 3 to 15 s of CPU).
// Bounded by the last 256 templates.
type sealClaims struct {
	mu    sync.Mutex
	set   map[common.Hash]bool
	order []common.Hash
}

// claim says whether the caller is the first finder for the seal hash.
func (c *sealClaims) claim(sealHash common.Hash) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.set == nil {
		c.set = make(map[common.Hash]bool)
	}
	if c.set[sealHash] {
		return false
	}
	if len(c.order) >= 256 {
		delete(c.set, c.order[0])
		c.order = c.order[1:]
	}
	c.set[sealHash] = true
	c.order = append(c.order, sealHash)
	return true
}

type piMessages struct {
	mu    sync.Mutex
	m     map[common.Hash][]byte
	order []common.Hash
}

const piMessagesKeep = 4096

func (p *piMessages) put(h common.Hash, m []byte) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.m == nil {
		p.m = make(map[common.Hash][]byte)
	}
	if _, ok := p.m[h]; !ok {
		p.order = append(p.order, h)
		if len(p.order) > piMessagesKeep {
			delete(p.m, p.order[0])
			p.order = p.order[1:]
		}
	}
	p.m[h] = m
}

func (p *piMessages) get(h common.Hash) ([]byte, bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	m, ok := p.m[h]
	return m, ok
}

// PiVerifying reports whether this node verifies the proof pi by its flag.
func (e *Engine) PiVerifying() bool { return e.config.PiVerify }

// piVerifyAt is the ONE reader of whether a keyed block's proof is checked
// at a height (F-461): the node's flag, or the chain's rule from the forks
// entry's PiVerifyV1 (the first height from which every node of the chain
// verifies, so a bad proof splits nobody). The sealer's attachPi reads it
// for its own template's height: a sealer under the chain's rule attaches
// a proof or seals nothing keyed.
func (e *Engine) piVerifyAt(height uint64) bool {
	if e.config.PiVerify {
		return true
	}
	f := e.forks
	return f != nil && f.PiVerifyV1 > 0 && height >= f.PiVerifyV1
}

// SetPi turns the proof's verification on or off and names the prover and
// the work directory for this node's own seals. With verify on, the binary
// must carry the verifier (tag ignpi) with the pinned image id, else the
// node refuses (a node that accepts what it cannot check is no verifier).
func (e *Engine) SetPi(verify bool, prover, workDir string) error {
	if verify {
		if !pi.Built {
			return errors.New("igneum pi: --igneum.pi-verify needs a node built with the verifier (build tag ignpi; the staticlib of pow/pi/ffi)")
		}
		if got := pi.LibraryImageIDHex(); got != pi.ImageIDHex {
			return fmt.Errorf("igneum pi: the linked verifier pins image id %s, the node's constant is %s", got, pi.ImageIDHex)
		}
	}
	e.config.PiVerify = verify
	e.config.PiProver = prover
	e.config.PiWorkDir = workDir
	if verify {
		e.log.Info("Proof pi verification ON", "imageId", pi.ImageIDHex[:16], "prover", prover != "")
	}
	return nil
}

// verifyPiHeader is the header stage's part under the flag: a keyed seal
// carries a commitment, an unkeyed one carries none. The bytes are the
// body's, checked in VerifyPi.
func (e *Engine) verifyPiHeader(header *types.Header) error {
	if !e.piVerifyAt(header.Number.Uint64()) {
		return nil
	}
	keyed := len(header.Shares) > 0
	if keyed && header.PiHash == nil {
		return errPiMissing
	}
	if !keyed && header.PiHash != nil {
		return errPiUnexpected
	}
	return nil
}

// VerifyPi is the body stage's check of a keyed block's proof under the flag
// (core.BlockValidator calls it after the commitment check): the bound and
// the system byte, then the verifier against the header's BondKey, the walk's
// messages and the carried shares. A chain without the flag verifies nothing
// here. The messages come from the header stage's replay when it ran on this
// node (the dry run, an announced block, a sync), else from a replay now.
func (e *Engine) VerifyPi(block *types.Block) error {
	if !e.piVerifyAt(block.NumberU64()) {
		return nil
	}
	header := block.Header()
	if len(header.Shares) == 0 {
		return nil
	}
	if header.PiHash == nil {
		return errPiMissing
	}
	proof := block.Pi()
	if len(proof) == 0 {
		return errPiBody
	}
	// This node's own receipt (the resident prover's output over its own
	// messages, piserve.go): trusted at its own import; the hash is the
	// header's commitment, already checked against the body by the
	// validator, so a different proof under the same commitment cannot
	// pass here.
	if e.config.PiResident && e.own.has(*header.PiHash) && types.PiHash(proof) == *header.PiHash {
		return nil
	}
	m, ok := e.piM.get(block.Hash())
	if !ok {
		algo, err := e.algoFor(header)
		if err != nil {
			return err
		}
		k, isM := algo.(pow.KeyedM)
		if !isM {
			return errPiNoMessages
		}
		var mix common.Hash
		mix, _, _, m = k.ComputeKeyedM(e.SealHash(header), header.Number.Uint64(), header.Nonce.Uint64(), nil, header.Shares)
		if m == nil || mix != header.MixDigest {
			return fmt.Errorf("%w: %v", errInvalidPoW, errKeyedReplay)
		}
	}
	return e.verifyPiBytes(header, proof, m)
}

// verifyPiBytes is the proof's check with the messages given: the regression
// on the hash team's rows drives it with the rows' own messages.
func (e *Engine) verifyPiBytes(header *types.Header, proof, m []byte) error {
	if header.BondKey == nil {
		return errBondKeyMissing
	}
	if n := len(proof); n > pi.MaxBytes(pi.DomainSeal) {
		return fmt.Errorf("%w: %s (%d bytes over %d)", errPiRefused, pi.CodeName(pi.EBound), n, pi.MaxBytes(pi.DomainSeal))
	}
	if proof[0] != pi.System {
		return fmt.Errorf("%w: %s (identifier %d)", errPiRefused, pi.CodeName(pi.ESystem), proof[0])
	}
	k := len(header.Shares) / pow.ShareBytes
	if k == 0 || len(m) != k*32 || uint64(len(header.Shares)) != uint64(k)*pow.ShareBytes {
		return fmt.Errorf("%w: %s (K %d, %d message bytes)", errPiRefused, pi.CodeName(pi.EArg), k, len(m))
	}
	ms := make([][32]byte, k)
	ss := make([][32]byte, k)
	for i := 0; i < k; i++ {
		copy(ms[i][:], m[32*i:32*i+32])
		copy(ss[i][:], header.Shares[pow.ShareBytes*uint64(i):pow.ShareBytes*uint64(i)+32])
	}
	var pk [32]byte
	copy(pk[:], header.BondKey[:])
	t0 := time.Now()
	code := pi.Verify(pi.DomainSeal, pk, ms, ss, proof)
	if code != pi.OK {
		return fmt.Errorf("%w: %s (code %d, %v)", errPiRefused, pi.CodeName(code), code, time.Since(t0))
	}
	return nil
}

// keepPiMessages records the walk's messages for the body stage.
func (e *Engine) keepPiMessages(header *types.Header, m []byte) {
	if m != nil && e.piVerifyAt(header.Number.Uint64()) {
		e.piM.put(header.Hash(), m)
	}
}

// attachPi completes this node's own keyed seal under the flag: the proof
// over the found nonce's messages and shares through the prover, the
// commitment into the header, the receipt into the body. Without the flag
// the block is returned as sealed. A failure returns an error and no block:
// the seal is dropped (never announced without its proof).
func (e *Engine) attachPi(block *types.Block, header *types.Header) (*types.Block, error) {
	if !e.piVerifyAt(header.Number.Uint64()) || len(header.Shares) == 0 {
		return block.WithSeal(header), nil
	}
	if e.config.PiProver == "" {
		return nil, errPiNoProver
	}
	sk, _ := e.bondSecret()
	algo := e.sealAlgoFor(header)
	if algo == nil {
		// No anchor on this node's own chain: no proof under the
		// document's programme (F-67); the template is given up.
		return nil, errProgramAncestor
	}
	k, isM := algo.(pow.KeyedM)
	if sk == nil || !isM {
		return nil, errPiNoMessages
	}
	sealHash := e.SealHash(header)
	mix, _, shares, m := k.ComputeKeyedM(sealHash, header.Number.Uint64(), header.Nonce.Uint64(), sk, nil)
	if m == nil || mix != header.MixDigest || !bytes.Equal(shares, header.Shares) {
		return nil, fmt.Errorf("%w: the prover's replay differs from the seal", errKeyedReplay)
	}
	var proof []byte
	var err error
	resident := e.config.PiResident
	if resident {
		proof, err = e.provePiResident(sk, m, sealHash)
		if err != nil && !errors.Is(err, errPiServeRefused) {
			// The resident prover cannot run on this host (F-89, 13
			// October 2026: two sealer pods on a driver the CUDA prover
			// build cannot use dropped every keyed seal for hours with
			// "the resident prover exited before its ready line" while
			// their templates kept assembling). The seal is not lost: the
			// per-seal prover proves it, loudly, so a pod on a bad host
			// still seals (slower) and its log says why on every seal.
			fallback := e.config.PiProverFallback
			if fallback == "" {
				fallback = e.config.PiProver
			}
			e.log.Error("Resident prover unavailable; this seal proves on the fallback prover (the host cannot run the resident build: read the prover's stderr above)", "err", err, "prover", fallback, "sealhash", sealHash)
			proof, err = e.provePiWith(fallback, sk, m, sealHash)
			resident = false
		}
	} else {
		proof, err = e.provePi(sk, m, sealHash)
	}
	if err != nil {
		return nil, err
	}
	h := types.PiHash(proof)
	if resident {
		// This node's own receipt over its own messages: trusted at its
		// own import (VerifyPi), verified by every other node as before.
		e.own.add(h)
	}
	header = types.CopyHeader(header)
	header.PiHash = &h
	return block.WithPi(proof).WithSeal(header), nil
}

// SetPiResident chooses the resident prover (piserve.go) over the per-seal
// process; the eth backend's call from --igneum.pi-resident.
func (e *Engine) SetPiResident(on bool) {
	e.config.PiResident = on
	if on {
		e.log.Info("Proof pi prover RESIDENT (one process for the sealer's life; own receipts trusted at own import)")
	}
}

// provePi runs the prover CLI for domain 0: pi-prove 0 <sk-file> <m-file>
// <out.bin>. The secret's file lives under the work directory with mode 0600
// and is written once; the messages and the receipt are per seal and removed.
func (e *Engine) provePi(sk *[32]byte, m []byte, sealHash common.Hash) ([]byte, error) {
	return e.provePiWith(e.config.PiProver, sk, m, sealHash)
}

// SetPiProverFallback names the second prover CLI (the CPU build) for the
// F-89 fallback; the eth backend's call from --igneum.pi-prover-fallback.
func (e *Engine) SetPiProverFallback(path string) { e.config.PiProverFallback = path }

// SetVerifierCore pins the header-verification workers to a core at
// elevated priority (verifycore_linux.go); -1 is off.
func (e *Engine) SetVerifierCore(core int) { e.config.VerifierCore = core }

// provePiWith is provePi on the given prover CLI.
func (e *Engine) provePiWith(prover string, sk *[32]byte, m []byte, sealHash common.Hash) ([]byte, error) {
	dir := e.config.PiWorkDir
	if dir == "" {
		dir = os.TempDir()
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("igneum pi: work directory: %w", err)
	}
	// The secret's file is named by its public key and its content checked
	// against the live secret before every proof (the node team's read of
	// 22:0x UK, 11 October 2026): the first form wrote "bond-sk" once and
	// kept whatever was there, so a bond seed changed in place, or two
	// sealers on one work directory, proved every seal under the wrong
	// key and the node's own import refused the blocks.
	_, pk := e.bondSecret()
	name := "bond-sk"
	if pk != nil {
		name = "bond-sk-" + pk.Hex()[2:18]
	}
	skFile := filepath.Join(dir, name)
	if cur, err := os.ReadFile(skFile); err != nil || !bytes.Equal(cur, sk[:]) {
		if err := os.WriteFile(skFile, sk[:], 0o600); err != nil {
			return nil, fmt.Errorf("igneum pi: the secret's file: %w", err)
		}
	}
	// The files are named per SEAL, by the seal hash and the messages'
	// digest (which binds the nonce): two finders on one template wrote and
	// removed the same files under each other, so one read no receipt and
	// the other carried a receipt proved over the other's nonce, refused at
	// the journal (devnet7's stall at 350 and security's 1145, 11 October
	// 2026).
	tag := sealHash.Hex()[2:14] + "-" + common.BytesToHash(crypto.Keccak256(m)).Hex()[2:10]
	mFile := filepath.Join(dir, "m-"+tag+".bin")
	outFile := filepath.Join(dir, "pi-"+tag+".bin")
	if err := os.WriteFile(mFile, m, 0o600); err != nil {
		return nil, fmt.Errorf("igneum pi: the messages' file: %w", err)
	}
	defer os.Remove(mFile)
	defer os.Remove(outFile)
	t0 := time.Now()
	cmd := exec.Command(prover, "0", skFile, mFile, outFile)
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("igneum pi: the prover failed: %v (%s)", err, bytes.TrimSpace(stderr.Bytes()))
	}
	proof, err := os.ReadFile(outFile)
	if err != nil {
		return nil, fmt.Errorf("igneum pi: the prover wrote no receipt: %w", err)
	}
	if len(proof) == 0 || proof[0] != pi.System || len(proof) > pi.MaxBytes(pi.DomainSeal) {
		return nil, fmt.Errorf("igneum pi: the prover's receipt is not a domain-0 proof (%d bytes)", len(proof))
	}
	e.log.Info("Proof pi made", "sealhash", sealHash, "bytes", len(proof), "elapsed", time.Since(t0))
	return proof, nil
}
