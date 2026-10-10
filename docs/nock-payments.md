# NOCK payments

The store offers two independently enabled payment methods:

| Method | Receiving destination | Units | Enable |
| --- | --- | --- | --- |
| Native NOCK on Nockchain | A fresh child address from a dedicated V1 public extended key | 65,536 nicks per NOCK | `NOCKCHAIN_ZPUB` |
| $NOCK on Base | The EVM recipient in store settings | 10^16 base units per token | `BASE_NOCK_ENABLED=true` |

Both require `NOCKBLOCKS_API_KEY` for USD quotes. Prices are fixed when an order
is reserved, rounded up to the smallest unit, and stored with the exchange rate.
Native NOCK and the Base token are different networks; addresses and atomic
amounts are not interchangeable.

## Native receiving setup

1. Use a Nockchain V1 wallet to create a dedicated receiving branch. Back up its
   seed or private extended key and record the exact derivation path offline.
   Export that branch's **zpub**, never its zprv, to `NOCKCHAIN_ZPUB`.
2. Configure `NOCKBLOCKS_API_KEY`. The default authenticated index endpoint is
   `https://nockblocks.com/rpc/v1`; quotes use `https://nockblocks.com/rpc`.
3. Keep `NOCKCHAIN_CONFIRMATIONS` at two or higher (maximum 144). Restart the
   service, keep new orders paused, and make a supervised small purchase before
   opening sales. Confirm the receiving wallet can recover and spend from the
   branch's non-hardened children.

The store derives children 0, 1, 2, and so on. It commits each allocation and
order in one database transaction before exposing the address. Back up the
store database alongside the offline wallet recovery information. Restoring an
older database can lose allocated indices; reconcile all issued addresses
before resuming sales. Do not use the same receiving branch in another service.

The checkout displays the public-key hash that wallets send to. Payment lookup
uses its exact single-signature lock root, including semi-anonymous outputs.
The worker checks transaction outputs, block membership, canonical block hashes,
mainnet genesis, index freshness, quote timestamps, and confirmation depth. It
sums matching outputs and consumes each output only once. Late or partial
payments require review; an unavailable index keeps the order reserved. Unpaid
orders expire after a successful scan at least 24 hours after quote expiry.

Nockblocks is a trusted indexer here: these checks do not independently validate
Nockchain consensus or cryptographic transaction proofs. The store only derives
public keys and never signs native transactions or spends received funds.

## Base setup

Set `BASE_NOCK_ENABLED=true`, provide the Nockblocks API key, and configure Base
RPC verification and the EVM receiving address in store settings. The checkout
switches the connected wallet to Base (chain 8453), checks its account and chain,
and sends a direct ERC-20 `transfer` to the fixed token contract:

`0x9b5e262cf9bb04869ab40b19af91d2dc85761722`

The store checks that contract's Transfer log, sender, recipient, exact amount,
canonical block, quote timestamp, and settlement status using the same EVM
verification policy as USDC. No token approval is requested.

## Go library

[`github.com/yapishu/nockchain-go`](https://github.com/yapishu/nockchain-go) contains
the field, Cheetah derivation, extended-key parsing, TIP5, based-noun hashing,
receiving-address, lock-root, and exact native-amount primitives. Go downloads the
pinned public module version recorded in `go.mod` and verifies it with `go.sum`.
Docker builds require no adjacent Nockchain checkout or GitHub credentials.
The library README documents scope, Rust reference vectors, and the security
boundary for offline key work.

## Provider request pacing

Price quotes and active native-payment checks share a single Nockblocks request limiter: no more than one request every five seconds. Rates are cached, and block lookups are batched. Idle orders do not trigger EVM provider scans; EVM verification starts only after a transaction hash is submitted and follows the order retry schedule. EVM calls share a one-second minimum across chains.
