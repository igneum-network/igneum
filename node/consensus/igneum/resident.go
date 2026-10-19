package igneum

// SetResidentDataset makes a VERIFYING node hold the full-dataset
// contexts (the sealer's form: the adapter builds the dataset once per
// epoch and every verification reads items instead of reconstructing them
// from the cache). The verifier ruling through main, 13 October 2026,
// 01:3x UK: the resident dataset is the verifier of record's path for the
// tensor coordinate's streamed tile, the light contexts the fallback.
// --igneum.resident-dataset (on by default); off keeps the light contexts.
// The sealer's own full-dataset switch (Seal) shares the once, so a node
// that both seals and verifies builds the dataset one time.
func (e *Engine) SetResidentDataset(on bool) {
	if !on {
		return
	}
	e.fullDataset.Do(func() {
		if s, ok := e.algo.(interface{ SetSingleDataset(bool) }); ok {
			// The verifier's resident form holds ONE dataset (no lookahead
			// build beside the head's; the minimum node's line).
			s.SetSingleDataset(true)
		}
		if f, ok := e.algo.(interface{ SetFullDataset(bool) }); ok {
			f.SetFullDataset(true)
			e.log.Info("Verifying on full-dataset contexts (the resident dataset, one at a time)")
		}
	})
}
