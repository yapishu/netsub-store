import React, { useEffect, useState, useRef } from "react";
import { createRoot } from "react-dom/client";
import "@urbit/sigil-js";
import {
  paymentRequest,
  displayAmount,
  addressPaymentURI,
  assetLabel,
  isAddressPayment,
  paymentPrefix,
  addressNetworkName,
} from "./payment";
import { olderUnpaid } from "./orders";
import "./style.css";

let csrf = "";
async function api(path, body, method = body === undefined ? "GET" : "POST") {
  const res = await fetch(`/api${path}`, {
    method,
    credentials: "same-origin",
    headers: { "Content-Type": "application/json", "X-CSRF-Token": csrf },
    ...(body === undefined ? {} : { body: JSON.stringify(body) }),
  });
  const data = await res.json();
  if (!res.ok) {
    const error = new Error(
      data.error || "The store is unavailable. Please try again.",
    );
    error.status = res.status;
    throw error;
  }
  if (data.csrf) csrf = data.csrf;
  return data;
}
const short = (s) => `${s.slice(0, 6)}…${s.slice(-4)}`;
function Sigil({ name, size = 72, tier = "standard" }) {
  const ref = useRef(null);
  useEffect(() => {
    const el = ref.current;
    for (const [key, value] of Object.entries({
      point: name,
      size: String(size),
      foreground: {
        premium: "var(--accent)",
        unique: "var(--unique)",
        rare: "var(--rare)",
        standard: "var(--text)",
      }[tier],
      background: "var(--sigil-background, var(--background))",
      space: "default",
    }))
      el.setAttribute(key, value);
  }, [name, size, tier]);
  return <urbit-sigil ref={ref} role="img" aria-label={`Sigil of ${name}`} />;
}
function PaymentIcons({ config, random }) {
  const assets = [
    ["usdc", "USDC"],
    ...(!random ? [["eth", "Ethereum"]] : []),
    ...(config.bitcoin?.enabled ? [["btc", "Bitcoin"]] : []),
    ...(config.monero?.enabled ? [["xmr", "Monero"]] : []),
    ...(config.nockchain?.enabled || config.baseNock?.enabled
      ? [
          [
            "nock",
            config.nockchain?.enabled && config.baseNock?.enabled
              ? "NOCK (Nockchain and Base)"
              : config.nockchain?.enabled
                ? "NOCK (Nockchain)"
                : "$NOCK (Base)",
          ],
        ]
      : []),
  ];
  return (
    <ul className="payment-icons" aria-label="Supported currencies">
      {assets.map(([symbol, name]) => (
        <li key={symbol} title={name}>
          <img
            className={symbol === "nock" ? "nock-icon" : undefined}
            src={`/assets/crypto/${symbol}.${symbol === "nock" ? "png" : "svg"}`}
            alt={name}
            width="24"
            height="24"
          />
        </li>
      ))}
    </ul>
  );
}

function App() {
  const [config, setConfig] = useState(null),
    [session, setSession] = useState(null),
    [error, setError] = useState(""),
    [busy, setBusy] = useState(false),
    [provider, setProvider] = useState(null),
    [walletModal, setWalletModal] = useState(false),
    [orders, setOrders] = useState([]),
    [showOrders, setShowOrders] = useState(false),
    [showOlder, setShowOlder] = useState(false);
  const admin = location.pathname === "/admin";
  const run = async (fn) => {
    setBusy(true);
    setError("");
    try {
      return await fn();
    } catch (e) {
      setError(e.message);
    } finally {
      setBusy(false);
    }
  };
  useEffect(() => {
    api("/config")
      .then(setConfig)
      .catch((e) => setError(e.message));
    api("/auth/session")
      .then(setSession)
      .catch(() => {});
  }, []);
  async function loadOrders() {
    if (!session?.wallet) return;
    try {
      setOrders(await api("/orders"));
    } catch (error) {
      if (error.status === 401) {
        setSession(null);
        setOrders([]);
      }
      throw error;
    }
  }
  useEffect(() => {
    if (!session?.wallet) {
      setOrders([]);
      return;
    }
    loadOrders().catch((e) => setError(e.message));
    const t = setInterval(() => {
      if (!document.hidden) loadOrders().catch(() => {});
    }, 10000);
    return () => clearInterval(t);
  }, [session]);
  useEffect(() => {
    if (!provider || !session?.wallet) return;
    const changed = (accounts) => {
      if (accounts[0]?.toLowerCase() === session.wallet.toLowerCase()) return;
      api("/auth/logout", {}).catch(() => {});
      setSession(null);
      setProvider(null);
      setOrders([]);
    };
    provider.on?.("accountsChanged", changed);
    return () => provider.removeListener?.("accountsChanged", changed);
  }, [provider, session]);
  async function connect(mobile) {
    await run(async () => {
      let p = window.ethereum;
      if (mobile) {
        if (!config.walletConnectId)
          throw new Error(
            "Mobile wallet pairing is not configured. Open this site in your wallet’s browser.",
          );
        setWalletModal(false);
        const { EthereumProvider } =
          await import("@walletconnect/ethereum-provider");
        p = await EthereumProvider.init({
          projectId: config.walletConnectId,
          chains: [1],
          optionalChains: [8453],
          showQrModal: true,
          metadata: {
            name: "networked subject_",
            description: "Urbit planets",
            url: location.origin,
            icons: [],
          },
        });
        await p.enable();
      }
      if (!p)
        throw new Error(
          "No browser wallet found. Use a mobile wallet or open this page in your wallet’s browser.",
        );
      const accounts = await p.request({ method: "eth_requestAccounts" });
      if (!accounts.length) throw new Error("No wallet account selected.");
      const c = await api("/auth/challenge", { address: accounts[0], admin });
      const message =
        "0x" +
        Array.from(new TextEncoder().encode(c.message), (b) =>
          b.toString(16).padStart(2, "0"),
        ).join("");
      const signature = await p.request({
        method: "personal_sign",
        params: [message, accounts[0]],
      });
      const login = await api("/auth/login", { challenge: c.id, signature });
      setProvider(p);
      setSession(login);
      setWalletModal(false);
    });
  }
  async function logout() {
    await run(async () => {
      await api("/auth/logout", {});
      setSession(null);
      setOrders([]);
      setProvider(null);
      await provider?.disconnect?.();
    });
  }
  return (
    <>
      <header>
        <a className="brand" href="/">
          networked subject<span>_</span>
        </a>
        <nav>
          <a href={admin ? "/" : "https://subject.network"}>
            {admin ? "Storefront" : "About"}
          </a>
          {session ? (
            <button className="wallet" onClick={logout}>
              {session.admin
                ? "Admin · sign out"
                : `${short(session.wallet)} · sign out`}
            </button>
          ) : (
            !admin && (
              <button className="wallet" onClick={() => setWalletModal(true)}>
                Connect wallet
              </button>
            )
          )}
        </nav>
      </header>
      <main>
        {error && (
          <div className="notice error" role="alert">
            {error}
            <button aria-label="Dismiss error" onClick={() => setError("")}>
              Close
            </button>
          </div>
        )}
        {config?.demo && (
          <div className="notice">Preview · Checkout disabled</div>
        )}
        {!config ? (
          <p className="loading">Loading…</p>
        ) : admin ? (
          <Admin
            session={session}
            setSession={setSession}
            run={run}
            busy={busy}
            config={config}
            connect={() => setWalletModal(true)}
          />
        ) : (
          <>
            {showOrders && (
              <section className="purchases">
                <div className="section-heading">
                  <h2>Your planets</h2>
                  <button className="link" onClick={() => setShowOrders(false)}>
                    Close
                  </button>
                </div>
                <label className="check">
                  <input
                    type="checkbox"
                    checked={showOlder}
                    onChange={(e) => setShowOlder(e.target.checked)}
                  />{" "}
                  Show older unpaid orders
                </label>
                {orders.filter((o) => showOlder || !olderUnpaid(o)).length ===
                0 ? (
                  <p>No recent unpaid orders or purchases.</p>
                ) : (
                  orders
                    .filter((o) => showOlder || !olderUnpaid(o))
                    .map((o) => (
                      <OrderCard
                        key={o.id}
                        order={o}
                        config={config}
                        provider={provider}
                        run={run}
                        refresh={loadOrders}
                        busy={busy}
                        connect={() => setWalletModal(true)}
                      />
                    ))
                )}
              </section>
            )}
            <Storefront
              config={config}
              session={session}
              provider={provider}
              run={run}
              busy={busy}
              connect={() => setWalletModal(true)}
              orders={orders}
              refresh={loadOrders}
              setShowOrders={setShowOrders}
            />
          </>
        )}
      </main>
      <footer>
        <a
          href="https://docs.urbit.org/user-manual/running/booting"
          target="_blank"
          rel="noreferrer"
        >
          Booting a planet
        </a>
        {!admin && session?.wallet && (
          <button className="link" onClick={() => setShowOrders(!showOrders)}>
            Purchases
          </button>
        )}
        <a href="/admin">Admin</a>
      </footer>
      {walletModal && (
        <WalletDialog onClose={() => setWalletModal(false)} busy={busy}>
          <button
            className="close"
            aria-label="Close wallet selection"
            disabled={busy}
            onClick={() => setWalletModal(false)}
          >
            Close
          </button>
          <h2>Connect wallet</h2>
          <p>Sign a message to sign in. No payment is made.</p>
          <button autoFocus disabled={busy} onClick={() => connect(false)}>
            Browser wallet
          </button>
          <button
            disabled={busy || !config?.walletConnectId}
            onClick={() => connect(true)}
          >
            WalletConnect
          </button>
          {!config?.walletConnectId && (
            <small>On mobile, open this page in your wallet’s browser.</small>
          )}
          {busy && <p role="status">Check your wallet…</p>}
          {error && <p className="error-text">{error}</p>}
        </WalletDialog>
      )}
    </>
  );
}
function WalletDialog({ children, onClose, busy }) {
  const ref = useRef(null);
  useEffect(() => {
    const dialog = ref.current;
    dialog.showModal();
    return () => dialog.close();
  }, []);
  return (
    <dialog
      ref={ref}
      className="modal"
      aria-label="Connect wallet"
      onCancel={(event) => {
        if (busy) event.preventDefault();
      }}
      onClose={onClose}
      onClick={(event) => {
        if (busy || event.target !== ref.current) return;
        const bounds = ref.current.getBoundingClientRect();
        if (
          event.clientX < bounds.left ||
          event.clientX > bounds.right ||
          event.clientY < bounds.top ||
          event.clientY > bounds.bottom
        )
          onClose();
      }}
    >
      {children}
    </dialog>
  );
}
function Storefront({
  config,
  session,
  provider,
  run,
  busy,
  connect,
  orders,
  refresh,
  setShowOrders,
}) {
  const [planets, setPlanets] = useState([]),
    [total, setTotal] = useState(0),
    [filter, setFilter] = useState("all"),
    [query, setQuery] = useState(""),
    [offset, setOffset] = useState(0),
    [selected, setSelected] = useState(null),
    [email, setEmail] = useState(""),
    [chain, setChain] = useState(8453),
    [asset, setAsset] = useState(config.bitcoin?.enabled ? "BTC" : "USDC"),
    [order, setOrder] = useState(null),
    [loading, setLoading] = useState(true),
    [fetchError, setFetchError] = useState("");
  useEffect(() => {
    let alive = true;
    setLoading(true);
    const t = setTimeout(() => {
      api(
        `/planets?q=${encodeURIComponent(query)}&filter=${filter}&offset=${offset}`,
      )
        .then((d) => {
          if (alive) {
            setPlanets(d.planets);
            setTotal(d.total);
            setFetchError("");
          }
        })
        .catch((e) => alive && setFetchError(e.message))
        .finally(() => alive && setLoading(false));
    }, 180);
    return () => {
      alive = false;
      clearTimeout(t);
    };
  }, [query, filter, offset, orders.length]);
  const checkoutRef = useRef(null);
  const selectedButton = useRef(null);
  const previousWallet = useRef(session?.wallet);
  useEffect(() => {
    if (previousWallet.current === session?.wallet) return;
    setOrder(null);
    if (previousWallet.current) {
      setSelected(null);
      setEmail("");
    }
    previousWallet.current = session?.wallet;
  }, [session?.wallet]);
  useEffect(() => {
    if (!selected) return;
    checkoutRef.current?.focus({ preventScroll: true });
    if (window.matchMedia("(max-width: 720px)").matches) {
      checkoutRef.current?.scrollIntoView({ block: "start" });
    }
  }, [selected]);
  function closeCheckout() {
    setSelected(null);
    selectedButton.current?.focus({ preventScroll: true });
  }
  const current = orders.find((o) => o.id === order?.id) || order;
  function planetPrice(planet) {
    if (planet?.random) return config.randomUsdc;
    const prefix = planet?.nameAndSigil
      ? "premiumSigil"
      : ["premium", "unique", "rare"].includes(planet?.tier)
        ? planet.tier
        : "";
    const suffix = "Usdc";
    return config.prices[prefix ? prefix + suffix : suffix.toLowerCase()];
  }
  const price = planetPrice(selected);
  const addressPayment = isAddressPayment(asset);
  const currencyName = addressNetworkName(asset);
  const paymentConfig =
    asset === "NOCK"
      ? config.nockchain
      : asset === "XMR"
        ? config.monero
        : config.bitcoin;
  const quotedPrice = asset !== "USDC";
  const paymentChain = addressPayment
    ? 0
    : asset === "NOCK_BASE"
      ? 8453
      : chain;
  async function reserve(e) {
    e.preventDefault();
    if (!session?.wallet) {
      connect();
      return;
    }
    await run(async () => {
      const o = await api("/orders", {
        planet: selected.random ? "" : selected.name,
        random: !!selected.random,
        email,
        chainId: paymentChain,
        asset,
      });
      setOrder(o);
      await refresh();
    });
  }
  return (
    <section className="catalog" id="planets">
      <div className="section-heading catalog-heading">
        <h1>Urbit planets</h1>
        <span className="stock">
          {config.star} · {total.toLocaleString()} available
        </span>
      </div>
      <div className={"store-grid" + (selected ? " has-selection" : "")}>
        <div className="catalog-list">
          <div className="tools">
            <div className="tabs">
              {["all", "standard", "rare", "unique", "premium"].map((f) => (
                <button
                  key={f}
                  data-tier={f}
                  className={filter === f ? "active" : ""}
                  aria-pressed={filter === f}
                  onClick={() => {
                    setFilter(f);
                    setOffset(0);
                  }}
                >
                  {f[0].toUpperCase() + f.slice(1)}
                </button>
              ))}
            </div>
            <button
              className="random-choice"
              onClick={(event) => {
                selectedButton.current = event.currentTarget;
                setSelected({ random: true });
                setOrder(null);
                setAsset(config.bitcoin?.enabled ? "BTC" : "USDC");
              }}
            >
              Random · $5
            </button>
            <input
              aria-label="Search planet names"
              placeholder="Search a name…"
              value={query}
              onChange={(e) => {
                setQuery(e.target.value);
                setOffset(0);
              }}
            />
          </div>
          {fetchError && <p role="alert">{fetchError}</p>}
          <div
            className={"planet-grid" + (loading ? " loading-grid" : "")}
            aria-busy={loading}
          >
            {planets.map((p) => (
              <button
                key={p.name}
                className={`planet ${selected?.name === p.name ? "selected" : ""} ${p.tier}`}
                onClick={(event) => {
                  selectedButton.current = event.currentTarget;
                  setSelected(p);
                  setOrder(null);
                }}
                aria-pressed={selected?.name === p.name}
              >
                <Sigil name={p.name} size={64} tier={p.tier} />
                <span className="planet-name">{p.name}</span>
                <span className="planet-price">
                  {planetPrice(p)} USDC
                  {p.tier !== "standard" && (
                    <span className="tier-label">
                      {p.nameAndSigil ? "name + sigil" : p.tier}
                    </span>
                  )}
                </span>
              </button>
            ))}
          </div>
          {loading && planets.length === 0 && (
            <p className="empty" role="status">
              Loading planets…
            </p>
          )}
          {!loading && planets.length === 0 && (
            <div className="empty">
              No matching planets. Try another search.
            </div>
          )}
          <div className="pagination">
            <button
              disabled={offset === 0}
              onClick={() => setOffset(Math.max(0, offset - 48))}
            >
              ← Previous
            </button>
            <span>
              {total ? offset + 1 : 0}–{Math.min(offset + 48, total)} / {total}
            </span>
            <button
              disabled={offset + 48 >= total}
              onClick={() => setOffset(offset + 48)}
            >
              Next →
            </button>
          </div>
        </div>
        {selected && (
          <aside
            className="checkout"
            ref={checkoutRef}
            tabIndex={-1}
            aria-label="Checkout"
          >
            <div className="section-heading">
              <h2>Checkout</h2>
              <button className="link" onClick={closeCheckout}>
                Close
              </button>
            </div>
            <PaymentIcons config={config} random={selected.random} />
            {current ? (
              <>
                <div className="chosen">
                  <Sigil name={current.planet} size={56} tier={current.tier} />
                  <h2 className="chosen-name">{current.planet}</h2>
                </div>
                <PlanetInfo />
                <OrderCard
                  order={current}
                  config={config}
                  provider={provider}
                  run={run}
                  refresh={refresh}
                  busy={busy}
                  connect={connect}
                />
                <button className="link" onClick={() => setShowOrders(true)}>
                  View purchases
                </button>
              </>
            ) : (
              <form onSubmit={reserve}>
                <div className="chosen">
                  {!selected.random && (
                    <Sigil
                      name={selected.name}
                      size={56}
                      tier={selected.tier}
                    />
                  )}
                  <h2 className="chosen-name">
                    {selected.random ? "Random standard planet" : selected.name}
                  </h2>
                </div>
                <PlanetInfo />
                {selected.random && (
                  <p className="caption">
                    A standard name, assigned when you reserve.
                  </p>
                )}
                <div className="payment-options">
                  <label>
                    Network
                    <select
                      value={paymentChain}
                      disabled={addressPayment || asset === "NOCK_BASE"}
                      onChange={(e) => setChain(Number(e.target.value))}
                    >
                      {addressPayment && (
                        <option value={0}>{currencyName}</option>
                      )}
                      {config.chains.map((c) => (
                        <option key={c.id} value={c.id}>
                          {c.name}
                        </option>
                      ))}
                    </select>
                  </label>
                  <label>
                    Currency
                    <select
                      disabled={
                        selected.random &&
                        !config.bitcoin?.enabled &&
                        !config.monero?.enabled &&
                        !config.nockchain?.enabled &&
                        !config.baseNock?.enabled
                      }
                      value={asset}
                      onChange={(e) => setAsset(e.target.value)}
                    >
                      <option>USDC</option>
                      {!selected.random && <option>ETH</option>}
                      {config.bitcoin?.enabled && <option>BTC</option>}
                      {config.monero?.enabled && <option>XMR</option>}
                      {config.nockchain?.enabled && <option>NOCK</option>}
                      {config.baseNock?.enabled && (
                        <option value="NOCK_BASE">$NOCK (Base)</option>
                      )}
                    </select>
                  </label>
                </div>
                <label>
                  Delivery email
                  <input
                    type="email"
                    required
                    autoComplete="email"
                    placeholder="you@example.com"
                    value={email}
                    onChange={(e) => setEmail(e.target.value)}
                  />
                </label>
                <div className="total">
                  <span>Total</span>
                  <b>
                    {quotedPrice ? `$${price}` : price}{" "}
                    <small>{quotedPrice ? "USD" : assetLabel(asset)}</small>
                  </b>
                </div>
                {addressPayment && (
                  <p className="caption">
                    {asset} amount is fixed when you reserve. Payment confirms
                    after {paymentConfig.confirmations} {currencyName}{" "}
                    confirmations{asset === "XMR" ? " and funds unlock" : ""}.
                  </p>
                )}
                {asset === "ETH" && (
                  <p className="caption">
                    The live ETH amount is fixed when you reserve, for 20
                    minutes.
                  </p>
                )}
                {asset === "NOCK_BASE" && (
                  <p className="caption">
                    $NOCK amount is fixed when you reserve. Pay on Base from
                    your connected wallet.
                  </p>
                )}
                <p className="caption">
                  Plus network fees. Hosting not included.
                </p>
                <button className="primary" disabled={busy || !config.open}>
                  {config.demo
                    ? "Checkout disabled"
                    : !config.open
                      ? "Store paused"
                      : session?.wallet
                        ? "Reserve planet"
                        : "Connect wallet"}
                </button>
              </form>
            )}
          </aside>
        )}
      </div>
    </section>
  );
}
function PlanetInfo() {
  return (
    <div className="planet-info">
      <a
        href="https://docs.urbit.org/urbit-id/l2"
        target="_blank"
        rel="noreferrer"
      >
        L2 planet
      </a>
      <p>
        Includes planet code redeemable on{" "}
        <a href="https://bridge.urbit.org" target="_blank" rel="noreferrer">
          bridge.urbit.org
        </a>{" "}
        and email with info
      </p>
      <p>
        Please wait up to 1 hour for the batch to process and the planet code to
        become redeemable
      </p>
    </div>
  );
}
function OrderCard({
  order: o,
  config,
  provider,
  run,
  refresh,
  busy,
  connect,
}) {
  const [creds, setCreds] = useState(null),
    [hash, setHash] = useState(
      () => sessionStorage.getItem(`payment:${o.id}`) || "",
    ),
    [sending, setSending] = useState(false);
  const addressPayment = isAddressPayment(o.asset);
  const currencyName = addressNetworkName(o.asset);
  const chain = config.chains.find((c) => c.id === o.chainId),
    expired = Date.now() >= Date.parse(o.expires);
  async function pay() {
    if (!provider) {
      connect();
      return;
    }
    setSending(true);
    await run(async () => {
      await provider.request({
        method: "wallet_switchEthereumChain",
        params: [{ chainId: `0x${o.chainId.toString(16)}` }],
      });
      const accounts = await provider.request({ method: "eth_accounts" });
      const actual = await provider.request({ method: "eth_chainId" });
      if (Number(actual) !== o.chainId)
        throw new Error("Switch to the requested network in your wallet.");
      const tx = paymentRequest(
        o,
        { ...chain, nock: config.baseNock?.contract },
        accounts[0] || "",
      );
      const h = await provider.request({
        method: "eth_sendTransaction",
        params: [tx],
      });
      sessionStorage.setItem(`payment:${o.id}`, h);
      setHash(h);
      await api(`/orders/${o.id}/payment`, { hash: h });
      sessionStorage.removeItem(`payment:${o.id}`);
      await refresh();
    });
    setSending(false);
  }
  const status =
    {
      awaiting_address: `Preparing ${currencyName} payment address`,
      awaiting_payment: expired ? "Quote expired" : "Ready for payment",
      confirming: "Confirming payment",
      paid: "Preparing your planet",
      prepared: "Preparing your planet",
      spawn_submitted: "Spawning your planet",
      accept_submitted: "Preparing your planet code",
      delivered: "Your planet code is ready",
      review_payment: "Payment needs attention",
      review_fulfillment: "Fulfillment needs attention",
      expired: "Order expired",
    }[o.status] || o.status;
  return (
    <article className="order">
      <div className="order-head">
        <strong>{o.planet}</strong>
        <span className={o.status === "delivered" ? "badge success" : "badge"}>
          {status}
        </span>
      </div>
      <p>
        {displayAmount(o.amount, o.asset)} {assetLabel(o.asset)} ·{" "}
        {addressPayment ? currencyName : chain?.name}
      </p>
      <small>Delivery: {o.email}</small>
      {o.error && <p className="error-text">{o.error}</p>}
      {o.txHash && !addressPayment && (
        <a
          className="tx-link"
          href={`https://${o.chainId === 8453 ? "basescan.org" : "etherscan.io"}/tx/${o.txHash}`}
          target="_blank"
          rel="noreferrer"
        >
          View payment
        </a>
      )}
      {!addressPayment &&
        o.status === "awaiting_payment" &&
        !expired &&
        !hash && (
          <button className="primary" disabled={busy || sending} onClick={pay}>
            {sending
              ? "Check your wallet…"
              : provider
                ? "Pay now"
                : "Reconnect wallet"}
          </button>
        )}
      {!addressPayment &&
        ["awaiting_payment", "confirming", "review_payment"].includes(
          o.status,
        ) && (
          <details open={!!hash}>
            <summary>Already paid?</summary>
            <p>
              Paste the transaction hash from your wallet. Do not send another
              payment.
            </p>
            <input
              aria-label="Payment transaction hash"
              placeholder="0x…"
              value={hash}
              onChange={(e) => setHash(e.target.value)}
            />
            <button
              disabled={busy || !hash}
              onClick={() =>
                run(async () => {
                  await api(`/orders/${o.id}/payment`, { hash });
                  sessionStorage.removeItem(`payment:${o.id}`);
                  await refresh();
                })
              }
            >
              Check payment
            </button>
          </details>
        )}
      {addressPayment &&
        ["awaiting_payment", "confirming"].includes(o.status) && (
          <AddressPayment order={o} expired={expired} run={run} />
        )}
      {o.status === "delivered" && (
        <>
          <p className="caption">
            {o.emailSent
              ? "Email sent."
              : "Email pending. Your planet code is available here."}
          </p>
          <div className="download-actions">
            <button
              onClick={() =>
                run(async () =>
                  setCreds(
                    creds ? null : await api(`/orders/${o.id}/planet-code`),
                  ),
                )
              }
            >
              {creds ? "Hide planet code" : "Reveal planet code"}
            </button>
            <a
              className="button"
              href={`/api/orders/${o.id}/planet-code?download=1`}
            >
              Download backup
            </a>
          </div>
          {creds && (
            <div className="credentials">
              <label>
                Planet code<code>{creds.code}</code>
              </label>
              <a
                className="button"
                href={creds.url}
                target="_blank"
                rel="noreferrer"
              >
                Redeem on Bridge
              </a>
              <p className="caption">
                Keep this code private until redeemed. Bridge creates your
                master ticket in your browser.
              </p>
            </div>
          )}
        </>
      )}
      {expired && o.status === "awaiting_payment" && (
        <p className="caption">
          Do not pay this expired quote.{" "}
          {addressPayment
            ? `${currencyName} payment detection continues automatically.`
            : "If you paid before expiry, submit the transaction above."}{" "}
          The name stays reserved for 24 hours to allow payment recovery.
        </p>
      )}
    </article>
  );
}
function AddressPayment({ order, expired, run }) {
  const monero = order.asset === "XMR";
  const nativeNock = order.asset === "NOCK";
  const name = addressNetworkName(order.asset);
  const prefix = paymentPrefix(order.asset);
  const received = BigInt(order[prefix + "Received"] || "0");
  const due = BigInt(order.amount);
  const remaining = received >= due ? 0n : due - received;
  return (
    <div className="address-payment">
      {!expired && remaining > 0n && (
        <>
          <p>
            Send {displayAmount(remaining.toString(), order.asset)}{" "}
            {order.asset} to this address before{" "}
            {new Date(order.expires).toLocaleTimeString([], {
              hour: "numeric",
              minute: "2-digit",
            })}
            .
          </p>
          <code>{order.recipient}</code>
          <div className="download-actions">
            <button
              onClick={() =>
                run(() => navigator.clipboard.writeText(order.recipient))
              }
            >
              Copy address
            </button>
            {nativeNock ? (
              <button
                onClick={() =>
                  run(() =>
                    navigator.clipboard.writeText(
                      displayAmount(remaining.toString(), order.asset),
                    ),
                  )
                }
              >
                Copy amount
              </button>
            ) : (
              <a className="button" href={addressPaymentURI(order)}>
                Open {name} wallet
              </a>
            )}
          </div>
        </>
      )}
      {received > 0n && (
        <p>
          {displayAmount(received.toString(), order.asset)} {order.asset}{" "}
          detected · {order[prefix + "Confirmations"] || 0}/
          {order[prefix + "Required"]} confirmations
          {monero ? " · Waiting for funds to unlock" : ""}
        </p>
      )}
      <p className="caption">
        Payment is detected automatically.{" "}
        {remaining === 0n
          ? "Do not send another payment."
          : nativeNock
            ? "Send only native NOCK on Nockchain. The payment must be mined before the quote expires."
            : `Send only ${name} on-chain.`}
      </p>
    </div>
  );
}
function Admin({ session, setSession, run, busy, config, connect }) {
  const [data, setData] = useState(null),
    [saved, setSaved] = useState(""),
    [showOlder, setShowOlder] = useState(false);
  async function refresh() {
    setData(await api("/admin"));
  }
  useEffect(() => {
    if (session?.admin) run(refresh);
  }, [session]);
  if (!session?.admin)
    return (
      <section className="admin-login">
        <h1>Admin</h1>
        {config.adminLogin?.wallet && (
          <div className="admin-wallet-login">
            <p>
              Connect the admin wallet and sign a message to manage the store.
            </p>
            <button className="primary" disabled={busy} onClick={connect}>
              Sign in with wallet
            </button>
          </div>
        )}
      </section>
    );
  if (!data) return <p>Loading…</p>;
  const cfg = data.settings;
  const update = (key, v) => {
    setSaved("");
    setData({ ...data, settings: { ...cfg, [key]: v } });
  };
  return (
    <section className="admin">
      <h1>Store settings</h1>
      <div className="admin-stats">
        <div>
          <b>{data.available}</b>
          <span>available planets</span>
        </div>
        <div>
          <b>{data.orders.filter((o) => o.status === "delivered").length}</b>
          <span>delivered orders</span>
        </div>
        <div>
          <b>{data.sendgridConfigured ? "Connected" : "Not configured"}</b>
          <span>Email delivery</span>
        </div>
      </div>
      <div className="admin-columns">
        <form
          className="panel"
          onSubmit={(e) => {
            e.preventDefault();
            run(async () => {
              await api("/admin/settings", cfg, "PUT");
              setSaved("Settings saved.");
              await refresh();
            });
          }}
        >
          <h2>Store & pricing</h2>
          <label className="check">
            <input
              type="checkbox"
              checked={cfg.open}
              onChange={(e) => update("open", e.target.checked)}
            />{" "}
            Accept new orders
          </label>
          <label>
            Payment recipient
            <input
              required
              value={cfg.recipient}
              placeholder="0x…"
              aria-describedby="payment-recipient-help"
              onChange={(e) => update("recipient", e.target.value)}
            />
          </label>
          <p className="caption" id="payment-recipient-help">
            Receives ETH, USDC, and $NOCK on Base. This address is independent
            of the key used to spawn planets. Changes apply to new orders.
          </p>
          <p className="caption">
            Set tier prices in USD. ETH, BTC, and XMR use CoinGecko quotes
            cached for one minute; NOCK uses Nockblocks. USDC is priced at $1.
            Each reservation fixes its coin amount for 20 minutes.
          </p>
          <div className="price-grid">
            {[
              ["usdc", "Standard · USD"],
              ["rareUsdc", "Rare · USD"],
              ["uniqueUsdc", "Unique · USD"],
              ["premiumUsdc", "Premium · USD"],
              ["premiumSigilUsdc", "Name + sigil · USD"],
            ].map(([k, label]) => (
              <label key={k}>
                {label}
                <input
                  required
                  inputMode="decimal"
                  value={cfg.prices[k]}
                  onChange={(e) =>
                    update("prices", { ...cfg.prices, [k]: e.target.value })
                  }
                />
              </label>
            ))}
          </div>
          <details className="rarity-settings">
            <summary>Categories</summary>
            {data.categories.map((category) => (
              <label className="category-setting" key={category.name}>
                <span>
                  {category.name === "sigils"
                    ? "Sigil patterns"
                    : category.name.replaceAll("_", " ")}
                  <small>
                    {category.available.toLocaleString()} available /{" "}
                    {category.generated.toLocaleString()} generated
                  </small>
                </span>
                <select
                  aria-label={`${category.name} tier`}
                  value={
                    cfg.premiumStrategies.includes(category.name)
                      ? "premium"
                      : cfg.uniqueStrategies.includes(category.name)
                        ? "unique"
                        : cfg.rareStrategies.includes(category.name)
                          ? "rare"
                          : "standard"
                  }
                  onChange={(event) => {
                    const tier = event.target.value;
                    setSaved("");
                    setData({
                      ...data,
                      settings: {
                        ...cfg,
                        rareStrategies: [
                          ...cfg.rareStrategies.filter(
                            (x) => x !== category.name,
                          ),
                          ...(tier === "rare" ? [category.name] : []),
                        ],
                        uniqueStrategies: [
                          ...cfg.uniqueStrategies.filter(
                            (x) => x !== category.name,
                          ),
                          ...(tier === "unique" ? [category.name] : []),
                        ],
                        premiumStrategies: [
                          ...cfg.premiumStrategies.filter(
                            (x) => x !== category.name,
                          ),
                          ...(tier === "premium" ? [category.name] : []),
                        ],
                      },
                    });
                  }}
                >
                  <option value="standard">Standard</option>
                  <option value="rare">Rare</option>
                  <option value="unique">Unique</option>
                  <option value="premium">Premium</option>
                </select>
              </label>
            ))}
          </details>
          <button className="primary" disabled={busy}>
            Save settings
          </button>
          {saved && <p role="status">{saved}</p>}
        </form>
        <div className="panel">
          <h2>Inventory</h2>
          <p className="caption">Receipt copy: {data.receiptEmail}</p>
          <p>
            Name categories: {data.rarityConfigured ? "ready" : "unavailable"}
          </p>
          <p>{data.pendingSpawns} spawns awaiting roller confirmation</p>
          <p className="caption">
            Updated{" "}
            {data.inventoryUpdated
              ? new Date(data.inventoryUpdated).toLocaleString()
              : "—"}
          </p>
          <button
            disabled={busy}
            onClick={() =>
              run(async () => {
                await api("/admin/inventory", {});
                await refresh();
              })
            }
          >
            Refresh inventory
          </button>
        </div>
      </div>
      <EmailTest data={data} run={run} busy={busy} />
      <div className="section-heading">
        <h2>Orders</h2>
        <a href="/api/admin/payment-ledger" download>
          Download payment destinations
        </a>
        <button disabled={busy} onClick={() => run(refresh)}>
          Refresh
        </button>
      </div>
      <label className="check">
        <input
          type="checkbox"
          checked={showOlder}
          onChange={(e) => setShowOlder(e.target.checked)}
        />{" "}
        Show older unpaid orders (
        {data.orders.filter((o) => olderUnpaid(o)).length})
      </label>
      <div className="table-scroll">
        <table>
          <thead>
            <tr>
              <th>Planet / buyer</th>
              <th>Payment</th>
              <th>Progress</th>
              <th>Actions</th>
            </tr>
          </thead>
          <tbody>
            {data.orders
              .filter((o) => showOlder || !olderUnpaid(o))
              .map((o) => (
                <tr key={o.id}>
                  <td>
                    <b>{o.planet}</b>
                    <small className="order-id">Order: {o.id}</small>
                    <small>
                      {short(o.wallet)}
                      <br />
                      {o.email}
                    </small>
                  </td>
                  <td>
                    {displayAmount(o.amount, o.asset)} {assetLabel(o.asset)}
                    <small>
                      {isAddressPayment(o.asset)
                        ? addressNetworkName(o.asset)
                        : o.chainId === 1
                          ? "Ethereum"
                          : "Base"}
                    </small>
                    <small className="payment-destination">
                      To: {o.recipient || "Address pending"}
                    </small>
                    {o.txHash && (
                      <small className="payment-destination">
                        Transaction: {o.txHash}
                      </small>
                    )}
                    {isAddressPayment(o.asset) && (
                      <small>
                        {o[paymentPrefix(o.asset) + "Confirmations"] || 0}/
                        {o[paymentPrefix(o.asset) + "Required"]} confirmations
                      </small>
                    )}
                  </td>
                  <td>
                    {o.status.replaceAll("_", " ")}
                    <small>{o.error}</small>
                    <small>{o.receiptError}</small>
                  </td>
                  <td>
                    {(o.status === "review_payment" ||
                      o.status === "review_fulfillment" ||
                      o.status.endsWith("_submitted")) && (
                      <button
                        disabled={busy}
                        onClick={() =>
                          run(async () => {
                            await api(`/admin/orders/${o.id}/recheck`, {});
                            await refresh();
                          })
                        }
                      >
                        Recheck state
                      </button>
                    )}
                    {o.paidAt && !o.paidAt.startsWith("0001-") && (
                      <button
                        disabled={busy}
                        onClick={() =>
                          run(async () => {
                            await api(`/admin/orders/${o.id}/receipt`, {});
                            await refresh();
                          })
                        }
                      >
                        Resend receipt
                      </button>
                    )}
                    {o.status === "delivered" && (
                      <button
                        disabled={busy}
                        onClick={() =>
                          run(async () => {
                            await api(`/admin/orders/${o.id}/email`, {});
                            await refresh();
                          })
                        }
                      >
                        Resend email
                      </button>
                    )}
                    {["awaiting_payment", "review_payment"].includes(
                      o.status,
                    ) &&
                      Date.now() > Date.parse(o.expires) + 86400000 && (
                        <button
                          disabled={busy}
                          onClick={() =>
                            run(async () => {
                              await api(`/admin/orders/${o.id}/release`, {});
                              await refresh();
                            })
                          }
                        >
                          Close unpaid order
                        </button>
                      )}
                  </td>
                </tr>
              ))}
          </tbody>
        </table>
        {!data.orders.length && <p className="empty">No orders yet.</p>}
      </div>
      <Activity run={run} busy={busy} />
    </section>
  );
}
function Activity({ run, busy }) {
  const [events, setEvents] = useState([]);
  async function load(before) {
    const next = await api(`/admin/events${before ? `?before=${before}` : ""}`);
    setEvents((previous) => (before ? [...previous, ...next] : next));
  }
  return (
    <details
      className="activity"
      onToggle={(event) => {
        if (event.currentTarget.open && !events.length) run(() => load());
      }}
    >
      <summary>Activity log</summary>
      <button disabled={busy} onClick={() => run(() => load())}>
        Refresh
      </button>
      <div className="table-scroll">
        <table>
          <thead>
            <tr>
              <th>Time</th>
              <th>Event</th>
              <th>Planet / order</th>
              <th>Result</th>
            </tr>
          </thead>
          <tbody>
            {events.map((event) => (
              <tr key={event.id}>
                <td>{new Date(event.at).toLocaleString()}</td>
                <td>
                  {event.kind}
                  <small>{event.action}</small>
                </td>
                <td>
                  {event.planet}
                  <small className="order-id">{event.orderId}</small>
                </td>
                <td>
                  {event.status || event.httpStatus || ""}
                  <small>{event.error || event.receiptError}</small>
                </td>
              </tr>
            ))}
          </tbody>
        </table>
      </div>
      {events.length >= 100 && (
        <button
          disabled={busy}
          onClick={() => run(() => load(events.at(-1).id))}
        >
          Load earlier
        </button>
      )}
    </details>
  );
}
createRoot(document.getElementById("root")).render(<App />);

function EmailTest({ data, run, busy }) {
  const [email, setEmail] = useState(
    data.receiptEmail?.toLowerCase() !== data.emailFrom?.toLowerCase()
      ? data.receiptEmail || ""
      : "",
  );
  const [kind, setKind] = useState("delivery");
  const [previews, setPreviews] = useState(null);
  const [status, setStatus] = useState("");
  useEffect(() => {
    run(async () => setPreviews(await api("/admin/email-preview")));
  }, []);
  const preview = previews?.[kind];
  return (
    <section className="panel email-test">
      <h2>Test email</h2>
      <p className="caption">
        From: {data.emailFrom || "Sender not configured"}. Send to another inbox
        you control to check delivery.
      </p>
      <p className="caption">
        Preview the customer template with sample data. Test messages contain an
        unusable sample code.
      </p>
      <form
        onSubmit={(e) => {
          e.preventDefault();
          setStatus("");
          run(async () => {
            const result = await api("/admin/test-email", { email, kind });
            setStatus(result.message);
          });
        }}
      >
        <div className="email-test-fields">
          <label>
            Send test to
            <input
              type="email"
              required
              value={email}
              onChange={(e) => setEmail(e.target.value)}
            />
          </label>
          <label>
            Template
            <select value={kind} onChange={(e) => setKind(e.target.value)}>
              <option value="delivery">Planet code delivery</option>
              <option value="receipt">Payment receipt</option>
            </select>
          </label>
        </div>
        {preview && (
          <details>
            <summary>Preview: {preview.subject}</summary>
            <pre className="email-preview">{preview.text}</pre>
          </details>
        )}
        <button disabled={busy || !data.sendgridConfigured || data.demo}>
          Send test email
        </button>
        {(!data.sendgridConfigured || data.demo) && (
          <p className="caption">
            Email sending requires a live store with SendGrid configured.
          </p>
        )}
        {status && <p role="status">{status}</p>}
      </form>
    </section>
  );
}
