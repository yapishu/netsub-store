# Bitcoin and Monero wallet setup

Both payment methods are optional. A full node supplies blockchain data; a receiving wallet supplies addresses and identifies payments. The store does not need either currency's spending keys. Leave the corresponding RPC URL empty to hide that payment method.

The example LAN addresses below are placeholders. Use endpoints reachable **from the store container**: `127.0.0.1` inside that container refers to the container itself. Keep RPC ports on the private network, accessible only to the store and your administration host.

## Current LAN endpoints

The store `.env` selects the loaded Bitcoin wallet `cormorant`. That wallet currently has no receiving descriptors. Use `cormorant` instead of `netsub-store` in the Bitcoin import commands below and skip wallet creation; import a public receiving descriptor from the wallet that holds your funds.

The Monero daemon endpoint is `http://192.168.88.252:18089`. Its restricted RPC is synchronized and requires no daemon credentials. A dedicated mainnet receiving wallet runs as the `netsub-monero-wallet.service` user service on this store host. Its authenticated view-only RPC binds to the Docker bridge at `http://172.17.0.1:18082`; the store’s `.env` contains the RPC login.

Wallet runtime files are under `~/.local/share/netsub-store-monero/runtime/`. The private spending wallet and recovery file are under `~/.local/share/netsub-store-monero/backup/`, outside the repository. Back up that directory offline; `recovery.json` contains the seed, wallet password, public address, and restore height. The running RPC wallet has no private spend key. Check it with `systemctl --user status netsub-monero-wallet.service`.

The wallet RPC credentials are generated for the receiving service; they are independent of the daemon and of the wallet-file password. The store requires 10 confirmations and unlocked funds for Monero payments.


## Bitcoin: add a watch-only wallet to Bitcoin Core

This setup uses the wallet support in your existing Bitcoin Core node. There is no separate wallet RPC process.

1. In your hardware or desktop wallet, create a dedicated account for store receipts and export its **public receiving descriptor**. This setup uses a native SegWit `wpkh(...)` descriptor with a receiving branch and a wildcard, ending in `/0/*)` before its checksum. It contains an extended public key, not a seed or private key. Keep the spending wallet and its backup; they control withdrawals. Use the dedicated account only for store address allocation so another app does not issue the same addresses.

2. On the node host, create the empty watch-only wallet once:

   ```sh
   bitcoin-cli -named createwallet wallet_name=netsub-store disable_private_keys=true blank=true descriptors=true load_on_startup=true
   ```

   If this wallet already exists, load it instead of creating another:

   ```sh
   bitcoin-cli -named loadwallet filename=netsub-store load_on_startup=true
   ```

3. Save this JSON as `bitcoin-descriptor.json`, replacing the descriptor placeholder with your complete public descriptor, including its `#checksum`. Keep the array on one line for `-stdin`:

   ```json
   [{"desc":"YOUR_PUBLIC_RECEIVING_DESCRIPTOR_WITH_CHECKSUM","timestamp":"now","active":true,"internal":false,"range":[0,1000],"next_index":0}]
   ```

   `timestamp: "now"` applies to a fresh account with no prior payments. For an existing account, use its creation timestamp and the next unused address index, and allow the node to rescan. A pruned node needs the relevant historical blocks available for that rescan.

   Import the descriptor:

   ```sh
   bitcoin-cli -rpcwallet=netsub-store -stdin importdescriptors < bitcoin-descriptor.json
   bitcoin-cli -rpcwallet=netsub-store getwalletinfo
   bitcoin-cli -rpcwallet=netsub-store getnewaddress setup-check bech32
   ```

   The import returns `success: true`; wallet info shows `private_keys_enabled: false` and `scanning: false` once ready. Verify the test receiving address belongs to the intended spending wallet. The store requests its own labeled address for each order.

4. Enable authenticated RPC access on the node for the store host. Bitcoin Core's `rpcauth` utility generates an `rpcauth=` line and the password used by the client. Add that line to the node's configuration with `server=1`, an appropriate LAN `rpcbind`, and `rpcallowip` restricted to the actual source address. Restart the node when changing these settings. If RPC already works from the store host, use that existing configuration.

5. Set these values in the store's `.env`:

   ```dotenv
   BITCOIN_RPC_URL=http://192.168.1.50:8332
   BITCOIN_RPC_USER=store
   BITCOIN_RPC_PASSWORD=YOUR_RPC_PASSWORD
   BITCOIN_RPC_WALLET=netsub-store
   BITCOIN_NETWORK=main
   BITCOIN_CONFIRMATIONS=6
   ```

   The URL is the node's base URL; the store adds `/wallet/netsub-store`. Bitcoin becomes the default checkout currency when enabled. USD tier prices convert to a fixed BTC amount when the buyer reserves a planet.

Bitcoin Core references: [createwallet](https://bitcoincore.org/en/doc/30.0.0/rpc/wallet/createwallet/), [importdescriptors](https://bitcoincore.org/en/doc/30.0.0/rpc/wallet/importdescriptors/), [RPC authentication](https://github.com/bitcoin/bitcoin/tree/master/share/rpcauth).

## Monero: run a view-only wallet RPC beside your node

`monerod` alone cannot identify incoming payments to your wallet. `monero-wallet-rpc` uses the private view key to identify them. It can run on the node host or another host that reaches your node.

1. Create or select a dedicated receiving wallet in your normal Monero wallet application. Keep that spending wallet and its seed backed up. In the Monero GUI, **Settings → Wallet → Create a view-only wallet** exports a wallet without the spending key. Copy the resulting view-only wallet files to the host running wallet RPC.

   The CLI alternative prompts for your wallet's primary public address and private view key:

   ```sh
   monero-wallet-cli --generate-from-view-key /srv/monero/store-view --daemon-address http://192.168.88.252:18089
   ```

   Supply a restore height no later than the receiving wallet's first use. The view key belongs in the wallet files, not in the store's `.env`. The store uses account `0` unless configured otherwise.

2. Create a private configuration file for `monero-wallet-rpc`, for example `/srv/monero/store-rpc.conf`:

   ```ini
   wallet-file=/srv/monero/store-view
   password-file=/srv/monero/store-view.password
   daemon-address=http://192.168.88.252:18089
   trusted-daemon=1
   rpc-bind-ip=192.168.88.252
   rpc-bind-port=18082
   confirm-external-bind=1
   rpc-login=store:YOUR_WALLET_RPC_PASSWORD
   ```

   Use the wallet-RPC host's own address for `rpc-bind-ip`; it need not be the daemon host. The password file contains the **view-only wallet file's password**, which is separate from the RPC login password. Restrict both files to their owning service account. If the daemon requires login, also set `daemon-login=USER:PASSWORD` in this wallet configuration.

   Run the wallet service and keep it running, for example under your service manager:

   ```sh
   monero-wallet-rpc --config-file /srv/monero/store-rpc.conf
   ```

   It refreshes against your node. Wait for the node and wallet to finish synchronizing. For a local-only wallet service, use `rpc-bind-ip=127.0.0.1` and omit `confirm-external-bind`; the store still needs a route to that service.

3. Set the store's `.env` values:

   ```dotenv
   MONERO_WALLET_RPC_URL=http://192.168.88.252:18082
   MONERO_WALLET_RPC_USER=store
   MONERO_WALLET_RPC_PASSWORD=YOUR_WALLET_RPC_PASSWORD
   MONERO_DAEMON_RPC_URL=http://192.168.88.252:18089
   MONERO_DAEMON_RPC_USER=
   MONERO_DAEMON_RPC_PASSWORD=
   MONERO_NETWORK=mainnet
   MONERO_ACCOUNT_INDEX=0
   MONERO_CONFIRMATIONS=10
   ```

   Both URLs are base URLs, without `/json_rpc`. The daemon must be the same node the wallet uses. Fill the daemon credentials only when that endpoint requires them. The store uses Digest authentication, creates a subaddress per order, and waits for at least 10 confirmations **and unlocked funds**. Your private spend key stays in your spending wallet.

4. Keep the view-only wallet files and the store database backed up. The wallet saves the labeled subaddresses before the store exposes them. When restoring the spending wallet, scan enough subaddresses to cover those generated by the store; account/subaddress indices are retained in each order's database record. The spending wallet remains responsible for withdrawals and its own balance tracking.

Monero references: [view-only wallet creation](https://www.getmonero.org/resources/user-guides/view_only.html), [wallet-RPC service options](https://docs.getmonero.org/interacting/monero-wallet-rpc-reference/), [wallet RPC methods](https://docs.getmonero.org/rpc-library/wallet-rpc/).

## Apply settings and verify

Recreate the store after editing `.env`:

```sh
docker compose up -d --wait
```

An enabled currency appears in checkout. A reservation must obtain a receiving address and a current exchange-rate quote; an unavailable or synchronizing wallet blocks that currency's reservation. Keep the store and wallets running so payments are detected before the 20-minute quote expires. Confirmation and unlocking may finish afterward. Late or insufficient payments go to operator review.

Both currencies use the store's Ethereum-wallet sign-in for order recovery and a delivery email. Payments come from the buyer's separate Bitcoin or Monero wallet. A full live purchase verifies the receiving wallet, payment detection, receipt email, and planet-code delivery together.

## Find and recover payments

Open `/admin` to see each order's receiving address and transaction ID. **Download payment destinations** exports all order destinations, including hidden unpaid orders. Keep this file and a consistent backup of the store database with your wallet backups. Addresses identify where funds are held; spending requires the corresponding wallet's keys.

- **ETH, USDC, and $NOCK on Base:** each order stores the payment recipient selected at reservation time and its chain ID (`1` Ethereum, `8453` Base). Open the wallet controlling that address on the matching network. USDC and Base NOCK are tokens in that wallet. Changing the admin payment recipient does not move earlier payments. The admin login wallet and star spawn key do not automatically control this receiving address.
- **Bitcoin:** the ledger records the order address, network, transaction IDs, and Core label `netsub-order:ORDER_ID`. Spend using the wallet whose receiving descriptor is imported into Core. A watch-only descriptor cannot spend; recover from that spending wallet's seed or backup. The configured `cormorant` wallet requires a receiving descriptor before it can allocate store addresses.
- **Monero:** use the spending wallet under `~/.local/share/netsub-store-monero/backup/`, or restore its seed using the address and restore height in `recovery.json`. Keep that directory backed up offline. The ledger exports account and subaddress indices; scan enough subaddresses to include the highest issued index. The running view-only RPC wallet only detects incoming payments and cannot spend them.
- **Native NOCK:** restore the private wallet matching the exported receiving zpub. Each order's ledger row includes its public receiving branch, non-hardened child index, address, lock root, and transaction IDs. Derive that child from the matching private branch to spend. The zpub alone cannot recover spending keys. Keep the original wallet seed/private-key backup and the path used to export the receiving branch.

The store does not automatically sweep these payments into a single wallet. Planet-code recovery is separate from payment-fund recovery.
