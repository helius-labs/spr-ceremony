module github.com/helius-labs/spr-ceremony/adapters/zolana

go 1.27.3

require (
	github.com/helius-labs/spr-ceremony v0.0.0
	github.com/helius-labs/zolana/prover/server v0.0.0
)

replace github.com/helius-labs/spr-ceremony => ../..

replace github.com/reilabs/gnark-lean-extractor/v3 => github.com/Lightprotocol/gnark-lean-extractor/v3 v3.0.0-20250920122823-aa0219463107
