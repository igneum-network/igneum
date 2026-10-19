package service

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// TestLoadWorkerStepsBothForms pins the 0.1.20 roll of 20:47Z, 12 October 2026: 0.1.15 wrote worker-steps.json
// as plain numbers, 0.1.20 read the {step, pace} object form only, and every worker restarted at step 0 (the
// fleet's address at the chain's cap at its next height). Both forms load; a value in neither form is skipped.
func TestLoadWorkerStepsBothForms(t *testing.T) {
	dir := t.TempDir()
	body := `{"worker:old":17,"worker:new":{"step":3,"pace":34.5},"worker:odd":"x"}`
	if err := os.WriteFile(filepath.Join(dir, workerStepsFile), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	s := &Service{now: time.Now}
	s.cfg.ControlDir = dir
	s.loadWorkerSteps()
	if ws := s.workerSteps["worker:old"]; ws == nil || ws.ruleStep != 17 || ws.pace != 0 {
		t.Fatalf("the number form: %+v", ws)
	}
	if ws := s.workerSteps["worker:new"]; ws == nil || ws.ruleStep != 3 || ws.pace != 34.5 {
		t.Fatalf("the object form: %+v", ws)
	}
	if _, has := s.workerSteps["worker:odd"]; has {
		t.Fatalf("a value in neither form loaded")
	}
	// what 0.1.21 writes reads back the same
	s.saveWorkerSteps()
	r := &Service{now: time.Now}
	r.cfg.ControlDir = dir
	r.loadWorkerSteps()
	if ws := r.workerSteps["worker:old"]; ws == nil || ws.ruleStep != 17 {
		t.Fatalf("after save: %+v", ws)
	}
}

// TestStepKeyAndJobKey pins the project's ruling of 09:37 UK, 12 October 2026, on
// G-33: the five-minute rule is keyed by WORKER NAME across addresses, so an
// install's fee session (the fee address, the same worker name) carries its
// member session's step; the job itself is one per address and worker; a
// session with no worker name keys both by its address.
func TestStepKeyAndJobKey(t *testing.T) {
	if stepKey("0xMEMBER", "mac-1") != stepKey("0xF4B0", "mac-1") {
		t.Fatalf("the fee session's step is not its member's")
	}
	if stepKey("0xmember", "mac-1") == stepKey("0xmember", "mac-2") {
		t.Fatalf("two installs under one address share a step")
	}
	if stepKey("0xaaaa", "") == stepKey("0xbbbb", "") {
		t.Fatalf("two nameless sessions under different addresses share a step")
	}
	if jobKeyOf("0xAAAA", "w") != "0xaaaa/w" || jobKeyOf("0xAAAA", "") != "0xaaaa" {
		t.Fatalf("job keys: %q %q", jobKeyOf("0xAAAA", "w"), jobKeyOf("0xAAAA", ""))
	}
}
