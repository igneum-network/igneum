package params

// IsIgneumChainOfRecord reports whether a chain id is a chain of record: the
// public test network, the research chain and every older chain that carries
// an entry in IgneumChainRuleForks (a rule whose form changed on a running
// chain is a chain of record by construction). The research chain (7358) ran
// the second form from genesis and has no rule-fork entry, so it is named
// here. F-771: the lab test double of the shielded pool refuses any id for
// which this holds; the pool's gate at zero is the guard of substance, this
// is the second belt.
func IsIgneumChainOfRecord(id uint64) bool {
	if _, ok := IgneumChainRuleForks[id]; ok {
		return true
	}
	return id == igneumResearchChainOfRecordID
}

// igneumResearchChainOfRecordID is the research chain (7358).
const igneumResearchChainOfRecordID uint64 = 7358
