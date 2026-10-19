package stratum

import (
	"bufio"
	"bytes"
	"errors"
	"strings"
	"testing"

	"igneum.network/pool/internal/limits"
)

func TestReadFrameBound(t *testing.T) {
	short := `{"id":1,"method":"mining.subscribe","params":[]}` + "\r\n"
	r := bufio.NewReader(strings.NewReader(short))
	frame, err := ReadFrame(r)
	if err != nil {
		t.Fatal(err)
	}
	if string(frame) != strings.TrimRight(short, "\r\n") {
		t.Fatalf("frame %q", frame)
	}

	// Exactly at the bound (including the newline) is accepted.
	exact := strings.Repeat("a", limits.MaxFrameBytes-1) + "\n"
	if _, err := ReadFrame(bufio.NewReader(strings.NewReader(exact))); err != nil {
		t.Fatalf("exact bound: %v", err)
	}

	// One byte over is refused.
	over := strings.Repeat("a", limits.MaxFrameBytes) + "\n"
	if _, err := ReadFrame(bufio.NewReader(strings.NewReader(over))); !errors.Is(err, ErrFrameTooLarge) {
		t.Fatalf("over bound: %v", err)
	}

	// A line with no newline and far over the bound is refused before it is buffered.
	huge := bytes.Repeat([]byte("b"), 10*limits.MaxFrameBytes)
	if _, err := ReadFrame(bufio.NewReader(bytes.NewReader(huge))); !errors.Is(err, ErrFrameTooLarge) {
		t.Fatalf("huge: %v", err)
	}
}

func TestDecodeStrict(t *testing.T) {
	if _, err := Decode([]byte(`{"id":1,"method":"mining.submit","params":["a"]}`)); err != nil {
		t.Fatal(err)
	}
	if _, err := Decode([]byte(`{"id":1,"method":"mining.submit","params":[],"extra":1}`)); !errors.Is(err, ErrBadFrame) {
		t.Fatalf("unknown field: %v", err)
	}
	if _, err := Decode([]byte(`{"id":1,"params":[]}`)); !errors.Is(err, ErrBadFrame) {
		t.Fatalf("no method: %v", err)
	}
	if _, err := Decode([]byte(`not json`)); !errors.Is(err, ErrBadFrame) {
		t.Fatalf("not json: %v", err)
	}
}
