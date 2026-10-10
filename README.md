# Networked Subject planet store

A Go service and bundled React checkout for selling Urbit L2 planet codes redeemable on Bridge. It uses the Perigee library, browser wallets and WalletConnect, ETH/USDC payments on Ethereum or Base, gonetia name categories, and SendGrid delivery. Optional Bitcoin and Monero payments use wallet RPC services. Native NOCK uses a dedicated public receiving branch; $NOCK on Base uses the connected EVM wallet. See [NOCK payment setup](docs/nock-payments.md).

The storefront follows Networked Subject’s charcoal palette and JetBrains Mono typography. `/admin` controls availability, payment recipient, standard, rare, unique, and premium prices, category tiers, inventory refresh, receipts, activity history, and order recovery.

## Run with Docker Compose

The build downloads pinned public Go modules for Perigee and `github.com/yapishu/nockchain-go`. Docker Compose builds from this repository; no adjacent source checkouts are required.

```sh
cp .env.example .env
chmod 600 .env
# Fill in .env using the configuration table below.
go run ./cmd/store encryption-key
docker compose up -d --build
docker compose logs -f store
```

The encryption-key command prints a key to put in `.env`. You can also run `docker compose run --rm --no-deps store encryption-key` after building the image. Admin sign-in requires a signature from `ADMIN_WALLET_ADDRESS`.

Compose binds `127.0.0.1:8080`, runs as a non-root user with a read-only root filesystem, and keeps the database in a named volume. Put an HTTPS reverse proxy in front of it and set `PUBLIC_ORIGIN` to that exact public origin, without a trailing slash. An example Caddyfile is in `deploy/Caddyfile.example`. Configure your proxy to rate-limit public traffic as well; the application intentionally ignores forwarded IP headers so clients cannot spoof its rate limits. Behind a proxy, application IP limits apply to the proxy address.

Set `ADMIN_WALLET_ADDRESS` to the public EVM wallet allowed to administer the store. Open `/admin`, sign an admin-access message with that wallet, set the payment recipient and your prices, then enable **Accept new orders**. The store starts paused independently of `DEMO`; real operation does not automatically open sales. Run one supervised small purchase before opening sales.

## Configuration

| Variable                       | Purpose                                                                                                                                                                                                                  |
| ------------------------------ | ------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------ |
| `PUBLIC_ORIGIN`                | Exact HTTPS origin for authentication and CSRF validation. HTTP is allowed only on localhost.                                                                                                                            |
| `STAR`                         | Dispensing star’s `@p`; its dominion must be `l2` or `spawn`.                                                                                                                                                            |
| `SPAWN_PRIVATE_KEY`            | Hex private key for the star’s **spawn proxy or owner**. A management-only key cannot spawn. Prefer a dedicated spawn proxy. Set this or `SPAWN_MNEMONIC`, not both.                                                     |
| `SPAWN_MNEMONIC`               | BIP39 word seed for the spawn proxy or owner, as an alternative to a raw private key. The server derives `m/44'/60'/0'/0/0` locally and verifies the address’s spawning permission. This is not the Urbit master ticket. |
| `SPAWN_MNEMONIC_PASSPHRASE`    | Optional BIP39 passphrase used when generating that proxy wallet; leave empty if none is used.                                                                                                                           |
| `DATA_ENCRYPTION_KEY`          | Random 32-byte hex key for AES-GCM encryption of planet codes. Back this up separately from the database.                                                                                                          |
| `ADMIN_WALLET_ADDRESS` | Public EVM address authorized for admin signature login. Admin challenges explicitly state their purpose and are origin-bound, expiring, and single-use. |
| `COINGECKO_DEMO_API_KEY` | Optional Demo key for market prices; empty uses the keyless endpoint. |
| `SENDGRID_API_KEY`             | SendGrid key with Mail Send permission.                                                                                                                                                                                  |
| `RECEIPT_EMAIL` | Receipt-copy destination; defaults to `matwet@subject.network`. Receives no planet codes. |
| `EMAIL_FROM`                   | Verified SendGrid sender email address.                                                                                                                                                                                  |
| `WALLETCONNECT_PROJECT_ID`     | Reown project ID for mobile wallet QR/deep-link pairing. Without it, injected browser wallets and mobile wallet browsers work. Allow your domain in the project dashboard.                                               |
| `ETH_RPC_URL`, `BASE_RPC_URL`  | Chain RPC endpoints. RPC verification requires support for the `finalized` block tag and checks the reported chain ID.                                                                                                   |
| `ETHERSCAN_API_KEY`            | Etherscan V2 key for chains without a configured RPC. The selected plan must support Ethereum and Base proxy endpoints.                                                                                                  |
| `GONETIA_DIR`                  | Native runs: directory containing gonetia’s seven `*_planets.txt` outputs. The container generates and sets this automatically.                                                                                           |
| `ROLLER_URL`                   | Optional Perigee roller URL; defaults to `https://roller.urbit.org/v1/roller`.                                                                                                                                           |
| `LISTEN`, `DB_PATH`, `WEB_DIR` | Native server bind, bbolt database path, and built frontend directory. Compose sets these.                                                                                                                               |
| `PORT`                         | Compose’s localhost port; defaults to `8080`.                                                                                                                                                                            |
| `STORE_ENV_FILE`               | Optional Compose environment-file path; defaults to `.env`.                                                                                                                                                              |
| `DEMO`                         | `true` enables preview inventory and disables orders, chain activity, fulfillment, and email. Use a separate database.                                                                                                   |

Local wallet login also works at `http://localhost:<port>` and `http://127.0.0.1:<port>` while `PUBLIC_ORIGIN` remains set to the public domain. Local requests must have a matching host and origin. Challenges and sessions stay bound to the origin where sign-in occurs.

## Payment and delivery

1. A buyer signs a domain-bound, expiring, single-use wallet challenge. The server creates an eight-hour session in an HttpOnly, SameSite=Strict cookie, with Secure enabled on HTTPS. The wallet is the account for retrieving purchases; it does not receive ownership of the planet.
2. Checkout reserves a name and snapshots the price, currency, chain, recipient, buyer wallet, and email for 20 minutes. Values use exact integer units for the selected asset. Existing quotes keep their prices when admin settings change.
3. For EVM payments, the wallet sends ETH directly or calls USDC or Base $NOCK `transfer`. The store never requests token allowances. The frontend submits the transaction hash; a buyer can also paste a replacement or previously sent transaction hash.
4. For EVM payments, the worker checks only submitted hashes, at most 120 times, with backoff from 15 seconds to 10 minutes. A shared one-second minimum spaces EVM API requests across chains and orders. Provider rate-limit responses trigger a shared one-minute cooldown. Address-based payments are scanned automatically while their orders are active. Nockblocks price and payment queries share a five-second minimum between requests; block verification uses batches.
5. Verification checks sender, recipient, exact amount, transaction success, canonical block, block timestamp within the quote, and the actual token contract and transfer log. RPC mode waits for a finalized block. Explorer-only mode waits for 32 Ethereum or 1,800 Base confirmations; confirmation depth is **not** proof of rollup settlement. Prefer finalized RPC verification when settlement finality is required.
6. The verified payment is consumed atomically once. Perigee generates a Bridge planet code and its invite ownership address. The store encrypts the code before submitting a spawn, then completes the ownership handoff to the invite wallet and verifies it through the roller.
7. The buyer can reveal or download the planet code after signing in with the purchasing wallet. The Bridge activation link carries the code in its URL fragment. Codes are returned only by the authenticated `/api/orders/{id}/planet-code` endpoint and are not stored in browser storage.
8. SendGrid emails the planet code, the Bridge redemption link, and batch-processing instructions. There is no boot-file attachment. Click/open tracking is disabled. Failed emails retry up to six times with backoff; `/admin` can resend. “Email sent” means SendGrid accepted it.

The planet code is an invitation, not the buyer's master ticket. Bridge derives the invite wallet, creates a fresh master ticket in the buyer's browser, and transfers ownership and management to that new wallet. The store does not generate or receive that final master ticket. A code must remain private until redeemed because anyone holding it can claim the planet. Allow up to one hour for the roller batch before redemption.

Wallet login supports externally owned Ethereum accounts signing `personal_sign`. Contract wallets requiring EIP-1271 or account-abstraction payment flows are not supported. ETH payments must be direct transactions; USDC payments must be direct transfers of Circle’s native USDC.

Planet-code dispensing supports Urbit L2. Hosting is not included. The purchasing wallet signs in to the store; the planet code controls the invite wallet used for Bridge redemption.

Verified payments also trigger a separate receipt to the buyer and `RECEIPT_EMAIL` (default `matwet@subject.network`). Receipts contain purchase and payment details, with no planet code. Receipt attempts and their retry schedule persist independently of planet-code delivery; `/admin` can resend either message.

## Optional Bitcoin payments

See [Bitcoin and Monero wallet setup](docs/payment-wallets.md) for a full-node-to-store setup. Bitcoin is the default checkout currency when enabled, including random planets.

Set `BITCOIN_RPC_URL` to the LAN node's base URL (for example, `http://192.168.1.50:8332`), `BITCOIN_RPC_USER`, `BITCOIN_RPC_PASSWORD`, and `BITCOIN_RPC_WALLET` to a loaded Bitcoin Core wallet. The wallet needs an active receiving descriptor/keypool; a watch-only descriptor wallet works. Keep the RPC endpoint restricted to the store host/container. The store calls wallet read and receiving-address RPCs and never signs or sends Bitcoin transactions. A node without wallet support cannot issue per-order addresses.

`BITCOIN_NETWORK` is `main` by default; `test`, `signet`, and `regtest` are available for matching nodes. `BITCOIN_CONFIRMATIONS` defaults to 6 and must be at least 1. The node must be synchronized and the wallet must finish scanning before checkout or payment verification proceeds. Leave `BITCOIN_RPC_URL` empty to disable and hide Bitcoin. Existing ETH and USDC checkout remains available when Bitcoin is disabled or unreachable.

BTC quotes convert the selected USD/USDC tier price using [CoinGecko’s USD market prices](https://docs.coingecko.com/reference/simple-price), cached for at most one minute. Amounts round up to whole satoshis and remain fixed for the 20-minute quote. The buyer signs in with the same Ethereum wallet account used by the store, then pays from any Bitcoin wallet using the address or BIP21 link. Each order gets one address, recovered by its wallet label if a request times out. Address assignments, rate, amount, confirmation threshold, and payment outputs persist in the database.

Payment detection uses Bitcoin Core wallet transactions, accepts partial payments that together cover the quote, and waits for the configured confirmations. The node must first receive the payment within the quote window; confirmation can occur afterward. Confirmed outputs are consumed once. Conflicted outputs and payments to another address do not count. Late or insufficient payments require operator review; the store does not issue automatic refunds. Bitcoin integration tests cover a watch-only wallet on an isolated Bitcoin Core regtest node.

## Optional Monero payments

Set `MONERO_WALLET_RPC_URL`, its `MONERO_WALLET_RPC_USER` and `MONERO_WALLET_RPC_PASSWORD`, and `MONERO_DAEMON_RPC_URL`. The wallet endpoint runs `monero-wallet-rpc` with a loaded receiving wallet; the daemon endpoint is the same synchronized `monerod` used by that wallet. Both URLs are base URLs without `/json_rpc`. A view-only wallet is sufficient. The store uses HTTP Digest authentication for wallet RPC and for daemon RPC when its optional user/password pair is supplied. Leave the wallet URL empty to disable XMR independently of Bitcoin.

`MONERO_NETWORK` defaults to `mainnet` and also accepts `stagenet` or `testnet`. `MONERO_ACCOUNT_INDEX` defaults to `0`. `MONERO_CONFIRMATIONS` defaults to `10`, with a minimum of `10`; funds must also be unlocked. Each order receives a unique labeled subaddress. Its account/index, quote, rate, first payment observation, and consumed transfers persist across restarts. The wallet saves the subaddress label before the address reaches the buyer.

Quotes use [CoinGecko’s XMR/USD price](https://docs.coingecko.com/reference/simple-price), cached for at most one minute, and round up to whole atomic units (12 decimal places). The quote lasts 20 minutes. Payments must first be detected by the store before expiry; confirmations and unlocking can finish afterward. Keep the wallet and store running during checkout. Payments first seen after expiry, insufficient payments, sender time locks, and conflicts require operator review. Partial payments are combined; transfers to other subaddresses do not count. A stale wallet, wrong network, or unavailable daemon pauses verification without releasing the reservation. The store never signs or sends XMR.

XMR uses the same Ethereum-wallet sign-in as the other payment methods; the buyer pays from a separate Monero wallet. Receipt and order records associate the payment with that store account and delivery email.

## Names and rarity

The container bundles the `feat/sigil-patterns` branch of [yapishu/gonetia](https://github.com/yapishu/gonetia/tree/feat/sigil-patterns) at a pinned revision with its wordlists and generates name categories for `STAR` at each live startup. Generation runs locally in temporary storage and needs no host files or network access. Available planets come from the roller at startup, every minute, and when inventory is refreshed in `/admin`. Every checkout and first spawn submission checks the roller’s spawned list. Submitted names remain excluded in the local database while the roller catches up, and observed spawns remain excluded across restarts.

For native runs, `scripts/prepare-gonetia.sh '~star'` checks out the same gonetia revision, initializes its wordlist submodule, runs it, and copies its seven result files into `data/gonetia`. Its Go toolchain is downloaded automatically if needed. Generate lists for the same star as `STAR` and set `GONETIA_DIR=data/gonetia`.

The store imports gonetia’s actual outputs rather than reimplementing its classifications. It intersects tags with the roller’s unspawned inventory and validates each inventory name’s parent. Premium names include doubles, only English, and only approximate. Unique contains sigil patterns without a premium name. Rare includes any English, any approximate, and alliteration. Tier precedence is Premium, Unique, Rare, then Standard. USD/USDC prices default to $10 Standard, $15 Rare, $25 Unique, $30 Premium name, and $35 for a Premium name with a sigil pattern. The combined picks stay under Premium and carry a “name + sigil” label. ETH, BTC, XMR, and NOCK quotes convert the matching USD price. USDC uses a $1 unit price. Random checkout chooses only Standard inventory for $5 in any enabled payment currency, including ETH on Ethereum or Base. Existing reservations retain their quoted price and tier. Admin shows generated and available counts per category; overlapping categories are not additive. Sigil classification matches the 66 quadrant patterns from [venetia-sigil](https://github.com/urbitme/venetia-sigil), including wildcard and alternative pattern variants. An unset native `GONETIA_DIR` classifies everything as standard.

## Recovery and operations

- Orders, reservations, encrypted planet codes, payment consumption, and email progress persist in bbolt. One server process owns each database. Restarting invalidates browser sessions; buyers sign in again to access their orders.
- Startup verifies the encryption key, star, and live/preview mode against database metadata. Do not change the encryption key on an existing database. Keep the volume and key together in your recovery procedure.
- Unpaid quotes retain the name for 24 hours after expiry so buyers can attach a payment sent during the quote window. After that, orders with no transaction expire automatically. Payments outside the quote window require operator review and an external refund if appropriate.
- Rejected payments and exhausted payment checks appear as `review_payment`. The buyer can submit the correct hash; an admin can resume verification. An admin can release an unpaid reservation after the recovery window. Paid reservations cannot be released through this action.
- Each fulfillment intent is persisted before its outbound call. A crash or timeout can leave an uncertain submission. The worker reconciles invite ownership instead of resubmitting automatically. After 1,440 reconciliation checks, an admin can use **Recheck state** to resume reads. If an operation was never accepted, inspect the roller queue/state and complete that exact operation with Perigee; the worker recognizes the resulting state. Do not blindly repeat a spawn or acceptance.
- For operator recovery of an interrupted order, stop the store and run `store recover ORDER_ID OUTPUT.json` with the same environment and mounted database. The command writes an owner-only recovery bundle and refuses to overwrite a file. It does not submit transactions. Use the recovered planet code with Bridge or Perigee to complete the outstanding operation, then restart the store to reconcile. Keep this bundle private.
- Failed SendGrid requests do not block planet-code downloads. A timeout after SendGrid accepts a request can result in a duplicate email on retry.
- API mutation attempts, rejected requests, order transitions, payment checks, receipt attempts, and planet-code email attempts append to the database activity history and structured container logs. `/admin` provides paginated activity. Compose rotates container logs at 10 MB across five files; database activity persists with the store volume.
- `/healthz` checks database access. Logs exclude private keys, planet codes, and mail contents. Keep Perigee debug logging disabled in deployment.
- Stop the service before copying `store.db`; back up the entire named volume. `docker compose down` preserves the volume. Do not use `down -v` against data you intend to retain.

## Local preview and development

```sh
ADMIN_WALLET_ADDRESS=0xYOUR_PUBLIC_WALLET scripts/preview.sh
```

Preview is available at `http://localhost:8080`; `/admin` uses the configured wallet signature. It has its own database and encryption key under `data/`. Preview cannot take payments or send emails.

For frontend development, run the backend with `PUBLIC_ORIGIN=http://localhost:5173`, then `npm --prefix web run dev`. Vite proxies `/api` to port 8080. For native production execution, load the variables from `.env` into your process environment, build the frontend, and run `go run ./cmd/store`. The Go process does not parse `.env` itself.

```sh
go test -race ./...
go vet ./...
npm --prefix web ci
npm --prefix web test
npm --prefix web run build
# With scripts/preview.sh running:
CHROME_PATH=/usr/bin/google-chrome npm --prefix web run test:browser
```

The tests cover payment matching and finality, replay protection, reservation pricing, wallet challenge replay, admin cookie/CSRF/rate limits, encrypted planet-code access, ambiguous submissions, Bridge-compatible planet-code fulfillment against a mock roller, and SendGrid request construction. Browser QA covers desktop/mobile rendering, sigils, rarity filtering, disabled preview checkout, admin pricing, wallet sign-in continuity, mocked Bitcoin/Monero checkout, Bitcoin default selection, tier prices/colors, and planet-code reveal and redemption links. Bitcoin Core integration runs against an isolated regtest wallet; Monero integration uses a view-only wallet on an isolated offline fakechain. Live provider credentials, actual wallet pairing, a real payment, and a real spawn must be checked in the deployment environment.

## References

- [Perigee](https://github.com/Native-Planet/perigee)
- [Gonetia](https://github.com/yapishu/gonetia/tree/feat/sigil-patterns) and [Urbit wordlists](https://github.com/ashelkovnykov/urbit-wordlists)
- [Urbit sigil-js](https://github.com/urbit/sigil-js)
- [Etherscan V2 receipts](https://docs.etherscan.io/api-reference/endpoint/ethgettransactionreceipt)
- [Circle USDC contract addresses](https://developers.circle.com/stablecoins/usdc-contract-addresses)
- [SendGrid Mail Send](https://www.twilio.com/docs/sendgrid/api-reference/mail-send/mail-send)

## Market quotes and email tests

Tier prices are in USD. ETH, BTC, and XMR share one batched CoinGecko request, fetched on demand at most once per minute. No request runs while nobody checks out. Set `COINGECKO_DEMO_API_KEY` to use a Demo key, or leave it empty for the keyless public endpoint. The cache also throttles failed requests. Invalid, missing, or market timestamps older than ten minutes block that currency's new quotes; the store does not substitute stale prices. NOCK uses the Nockblocks USD quote with a one-minute cache and the shared five-second provider request spacing. A reservation keeps its saved amount, recipient, and rate for the 20-minute payment window.

CoinGecko's free Demo plan has a monthly request cap. One request every minute continuously would use approximately 43,200 calls in a 30-day month; on-demand fetching reduces idle usage. See [current CoinGecko API plans](https://www.coingecko.com/en/api/pricing).

In `/admin`, **Test email** previews the actual delivery and receipt templates with clearly marked sample data. Enter the recipient, select a template, and click **Send test email**. Success means SendGrid accepted the message; inbox delivery is checked in that mailbox. Test messages use an unusable sample code, send only to the entered address, and do not create orders. Preview mode does not send emails.

Order lists hide unpaid reservations older than two hours by default. **Show older unpaid orders** reveals them; no records are deleted. Orders with payment evidence or requiring review stay visible. Admin displays payment destinations and transaction IDs. **Download payment destinations** exports every order, including hidden orders, with network and wallet derivation details. See [fund recovery](docs/payment-wallets.md#find-and-recover-payments).

## Two reverse proxies

Set `PUBLIC_ORIGIN` to the browser-facing HTTPS origin, without a path or trailing slash. Both proxies preserve that public `Host`, the browser `Origin`, `Cookie`, `Set-Cookie`, and `X-CSRF-Token`. The application authenticates against the public origin regardless of whether the final upstream hop uses HTTP. Same-origin GET requests often omit `Origin`, so the public `Host` must reach the application. `X-Forwarded-Host` alone is insufficient.

For example, a public Caddy proxy can forward to a home proxy over a private tunnel:

```caddyfile
planets.example.com {
    reverse_proxy http://10.0.0.2:8088 {
        header_up Host planets.example.com
    }
}
```

The home Caddy proxy forwards to the local store:

```caddyfile
:8088 {
    reverse_proxy 127.0.0.1:8811 {
        header_up Host planets.example.com
    }
}
```

Use your actual tunnel address and store port. If the public-to-home hop crosses the public internet, use HTTPS or an encrypted tunnel. Both proxies pass API responses without caching. The app ignores forwarded client IP headers; its IP limits therefore group clients behind the final proxy. Configure per-client limits at the public edge. See [Caddy reverse proxy headers](https://caddyserver.com/docs/caddyfile/directives/reverse_proxy#headers).

If the home proxy uses a distinct named HTTPS site such as `pay.subject.network`, the public proxy sends `Host: pay.subject.network` on that hop so Caddy selects the right site. The home proxy sets `header_up Host buy.subject.network` on its upstream connection to the store. Leave the browser’s `Origin` header unchanged. A successful login followed by 401 responses on session or order GET requests indicates that the session cookie or public-host routing needs checking. The client verifies the cookie session before displaying the connected wallet.
