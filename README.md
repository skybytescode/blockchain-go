# blockchain-go

[![CI](https://github.com/skybytescode/blockchain-go/actions/workflows/ci.yml/badge.svg)](https://github.com/skybytescode/blockchain-go/actions/workflows/ci.yml)

A small blockchain written from scratch in Go: ed25519-signed transactions in
a UTXO model, blocks with Merkle roots, and nodes that discover each other and
gossip transactions and blocks over gRPC. Blocks are produced by trusted
validators (proof of authority).

## How it works

- **Keys and addresses** ([`crypto`](crypto)): ed25519 key pairs; an address
  is the last 20 bytes of the public key.
- **Transactions** ([`types/transaction.go`](types/transaction.go)) spend
  earlier outputs (UTXOs) and create new ones. Every input is signed over the
  transaction with all signatures removed, so multi-input transactions verify
  the same way for every signer.
- **Blocks** ([`types/block.go`](types/block.go)) carry a header with the
  previous block's hash and the Merkle root of their transactions, signed by
  the validator.
- **Chain** ([`node/chain.go`](node/chain.go)) validates every block and
  transaction before applying it:
  - the block is signed and points at the current head;
  - every input carries a valid signature from the key that **owns** the
    output it spends;
  - the output exists and is unspent, and is not spent twice within the
    same transaction or block;
  - output amounts are positive and never exceed the inputs.
- **Nodes** ([`node/node.go`](node/node.go)):
  - connect through a handshake and learn about further peers from each
    other's peer lists;
  - validate incoming transactions, keep them in a mempool that rejects
    conflicting spends, and forward them;
  - a validator node turns pending transactions into a signed block every
    block interval and sends it out. Peers accept blocks only from
    configured validator keys, add them and pass them on.

## Run the demo

```bash
make run
```

This starts a validator on `:7000` and two peers on `:7001` and `:7002`,
connected in a line, and a demo wallet that owns the 1000 genesis coins. The
wallet first tries to steal the genesis coins with its own key, which the node
rejects. It then sends signed payments to `:7002`. Each one travels to the
validator, ends up in a block, and the block travels back to every node:

```
WARN  rejected tx    {"node": ":7002", "reason": "input 0 of tx 5e3d… spends an output not owned by its key"}
INFO  accepted tx    {"node": ":7002", "hash": "e6a62826f3", "mempool": 1}
INFO  accepted tx    {"node": ":7001", "hash": "e6a62826f3", "mempool": 1}
INFO  accepted tx    {"node": ":7000", "hash": "e6a62826f3", "mempool": 1}
INFO  created block  {"node": ":7000", "height": 1, "hash": "e78147e1e5", "txs": 1}
INFO  added block    {"node": ":7002", "height": 1, "hash": "e78147e1e5", "txs": 1}
INFO  added block    {"node": ":7001", "height": 1, "hash": "e78147e1e5", "txs": 1}
```

## Tests

```bash
make test   # go test -race ./...
```

- Unit tests cover keys, signing, Merkle roots and block and transaction
  validation, including the attacks the chain must refuse: spending someone
  else's coins, negative outputs, double spends, and malformed or unsigned
  input that used to crash the node.
- Network tests start a validator and two peers in-process and check that a
  transaction sent to the far node becomes the same block on every node, and
  that invalid transactions and blocks from untrusted signers are refused.

[GitHub Actions](.github/workflows/ci.yml) checks formatting, that the
generated gRPC code matches `proto/types.proto`, runs `go vet`, staticcheck and
the tests with the race detector, and builds the binary on every push and pull
request.

## History

The first version (2024) covered keys, transactions, blocks, the chain and
peer discovery, but did not build from a clean checkout, and its validator
never produced blocks. This version makes it build, finishes block production
and propagation, and fixes the security bugs the new tests found:

| Bug | Effect |
|---|---|
| Inputs were not checked against the owner of the output they spend | Anyone could spend anyone's coins |
| Negative output amounts were accepted | Coins could be created from nothing |
| Unsigned or malformed transactions panicked | Any peer could crash a node |
| Spent outputs were looked up by loop index, and used before the error check | Wrong coin checked; nil-pointer crash |
| No double-spend check within a block or transaction | The same coin could be spent twice |
| Multi-input transactions always failed verification; verifying a block rewrote its Merkle root | Valid payments refused; side effects |
| Any key could sign a block, and nodes accepted unvalidated transactions | Forged blocks, junk in the mempool |

## Limitations

This is a learning project, not a production chain. State lives in memory,
a node that joins late does not download earlier blocks, there is a single
round of gossip per message with no peer scoring, and the validator set is
fixed at start-up.
