module igneum.network/pool

go 1.27

require (
	github.com/holiman/uint256 v1.2.4 // indirect
	github.com/klauspost/cpuid/v2 v2.0.9 // indirect
	golang.org/x/crypto v0.58.0 // indirect
	golang.org/x/exp v0.0.0-20231110203233-9a3e6036ecaa // indirect
	golang.org/x/sync v0.24.0 // indirect
	golang.org/x/sys v0.49.0 // indirect
	golang.org/x/text v0.43.0 // indirect
)

require (
	github.com/ethereum/go-ethereum v0.0.0-00010101000000-000000000000
	lukechampine.com/blake3 v1.4.1
)

replace github.com/ethereum/go-ethereum => ../node
