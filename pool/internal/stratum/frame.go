// Package stratum carries the IS/1 wire framing
// (docs/ecosystem/DESIGN.md, section 2.3): newline-delimited JSON frames
// bounded at limits.MaxFrameBytes, with enumerated rejection reasons.
// Algorithm-specific fields are opaque here; their validation sits
// behind the interface GPU engineer A publishes.
package stratum

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"

	"igneum.network/pool/internal/limits"
)

var (
	ErrFrameTooLarge = errors.New("stratum: frame exceeds the bound")
	ErrBadFrame      = errors.New("stratum: frame is not a request")
)

// Rejection reasons a miner can display.
const (
	ReasonStale         = "stale"
	ReasonDuplicate     = "duplicate"
	// ReasonRefound: the share was accepted on an earlier job of the same header (the client restarted its nonce
	// range on a re-issue); refused in the before-acceptance form, never a bad share
	ReasonRefound = "re-found: this share was accepted on an earlier job of the same header; continue the nonce range across re-issued jobs"
	ReasonLowDifficulty = "low_difficulty"
	ReasonBadNonce      = "bad_nonce"
	ReasonRate          = "rate"
	// the bad-share threshold (DESIGN 2.21): the session closed at the limit of refused shares in the window
	// (the refusal's own reason appended), and an address refused at authorize while it cools
	ReasonBadShares = "bad shares: the session is closed at the pool's threshold"
	// ReasonAdmission prefixes a share refused BEFORE acceptance because the chain could not carry it (PA-53's
	// admission bound and selection rule, 18ddf3eb, as the pool applies it; the project's order of 08:5x UK, 12 October
	// 2026): the reason follows the colon on the wire and the log; never counted toward the bad-share threshold.
	ReasonAdmission = "refused before acceptance"
	ReasonCooling   = "cooling: this worker was closed for bad shares and is refused until its cooling period passes"
	ReasonBound         = "bound"
	ReasonUnauthorised  = "unauthorised"
	ReasonInvalid       = "invalid"
	// ReasonGateHeld is the pool's own fault during a handover (the new generation held a submit past its wait
	// while the journal was being caught up): never counted toward the bad-share threshold, the miner re-submits
	ReasonGateHeld = "pool handover: the share was held past the pool's wait, re-submit"
)

// Request is one frame from a miner.
type Request struct {
	ID     json.RawMessage `json:"id"`
	Method string          `json:"method"`
	Params json.RawMessage `json:"params"`
}

// Response is one frame to a miner.
type Response struct {
	ID     json.RawMessage `json:"id"`
	Result interface{}     `json:"result"`
	Error  *Error          `json:"error"`
}

// Error carries an enumerated reason.
type Error struct {
	Code   int    `json:"code"`
	Reason string `json:"reason"`
}

// ReadFrame reads one newline-delimited frame of at most
// limits.MaxFrameBytes bytes. A longer line is refused before the bound
// is exceeded in memory; the caller closes the session.
func ReadFrame(r *bufio.Reader) ([]byte, error) {
	var frame []byte
	for {
		chunk, err := r.ReadSlice('\n')
		if len(frame)+len(chunk) > limits.MaxFrameBytes {
			return nil, ErrFrameTooLarge
		}
		frame = append(frame, chunk...)
		if err == nil {
			return bytes.TrimRight(frame, "\r\n"), nil
		}
		if errors.Is(err, bufio.ErrBufferFull) {
			continue
		}
		return nil, err
	}
}

// Decode parses a request frame strictly: unknown fields are refused.
func Decode(frame []byte) (Request, error) {
	var req Request
	dec := json.NewDecoder(bytes.NewReader(frame))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&req); err != nil {
		return Request{}, ErrBadFrame
	}
	if req.Method == "" || len(req.Method) > 64 {
		return Request{}, ErrBadFrame
	}
	return req, nil
}
