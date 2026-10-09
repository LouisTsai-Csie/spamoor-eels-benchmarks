# spamoor-eels-benchmarks

Spamoor plugin that replays [execution-specs](https://github.com/ethereum/execution-specs)
benchmark blocks as continuous network load.

## Scenarios

| Scenario | Replays |
|---|---|
| `account-access` | `test_account_access` — cold `EXTCODESIZE` over CREATE2 JUMPDEST targets |

## Prerequisites

`account-access` reads pre-deployed JUMPDEST contracts; it does not create them.
The chain must already hold the CREATE2 targets for salts `0..N`, deployed
through the canonical factory `0x4e59b44847b379578588920ca78fbf26c0b4956c` with
the initcode whose hash is
`0xb9cdb9047474294c9743cf3944156c844bf91763de66271493caa07a3de77ec5`.

`accountaccess/txspecs.go` records how many targets the current table needs.
`glamsterdam-msf-1` already holds 135,000 of them. On a fresh devnet, deploy
them with spamoor's `factorydeploytx` scenario pointed at that factory.

## Run

```bash
# deploys run_code itself
spamoor --plugin . account-access -h <RPC> -p <PRIVKEY> -c 12 --log-txs -v

# reuse a contract you deployed yourself
spamoor --plugin . account-access -h <RPC> -p <PRIVKEY> \
    --target-address 0x... -t 12
```

`--count 12` runs exactly one block cycle. Without it the block repeats
indefinitely, wrapping back to the first transaction.

## Regenerating the transaction table

`accountaccess/txspecs.go` is generated from the execution-specs gas model, so
the calldata and gas limits are never transcribed by hand. From an
execution-specs checkout:

```bash
PYTHONPATH=$PWD uv run python \
    /path/to/spamoor-eels-benchmarks/gen/gen_txspecs.py \
    --gas 200 --fork Amsterdam \
    --out /path/to/spamoor-eels-benchmarks/accountaccess/txspecs.go
```

`--gas` is the block gas limit in millions. Smaller values need fewer
pre-deployed targets, which is what makes a local devnet practical: 200M needs
62,286 targets (~1.5 GB of code), 10M needs about 3,100 (~76 MB).

## Validating

```bash
spamoor-utils validate-plugin .
```

This only proves the plugin loads and its flags register. Correctness is the
receipt: each transaction is sized to consume its whole gas limit, so a
`gasUsed` well below `Gas` means the loop exited early and the benchmark did
not run. The scenario logs a warning when that happens.
