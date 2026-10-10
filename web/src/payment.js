export function paymentRequest(order, chain, account, now = Date.now()) {
  if (account.toLowerCase() !== order.wallet.toLowerCase())
    throw new Error("Reconnect the wallet used for this order.");
  if (now >= Date.parse(order.expires))
    throw new Error("This quote has expired. Do not pay it.");
  if (order.status !== "awaiting_payment")
    throw new Error("A payment is already being checked.");
  if (!chain || chain.id !== order.chainId)
    throw new Error("Wrong payment network.");
  const amount = BigInt(order.amount);
  if (amount <= 0n || amount >= 1n << 256n)
    throw new Error("Invalid payment amount.");
  if (order.asset === "ETH")
    return {
      from: account,
      to: order.recipient,
      value: `0x${amount.toString(16)}`,
    };
  const token =
    order.asset === "USDC"
      ? chain.usdc
      : order.asset === "NOCK_BASE" && chain.id === 8453
        ? chain.nock
        : null;
  if (!token || !/^0x[0-9a-fA-F]{40}$/.test(token))
    throw new Error("Unsupported payment asset.");
  return {
    from: account,
    to: token,
    value: "0x0",
    data: `0xa9059cbb${order.recipient.slice(2).toLowerCase().padStart(64, "0")}${amount.toString(16).padStart(64, "0")}`,
  };
}
export function displayAmount(amount, asset) {
  if (asset === "NOCK") amount = (BigInt(amount) * 152587890625n).toString();
  const d =
    asset === "ETH"
      ? 18
      : ["NOCK", "NOCK_BASE"].includes(asset)
        ? 16
        : asset === "BTC"
          ? 8
          : asset === "XMR"
            ? 12
            : 6;
  const str = amount.padStart(d + 1, "0");
  return `${str.slice(0, -d)}.${str.slice(-d)}`.replace(/\.?0+$/, "") || "0";
}

export const assetLabel = (asset) => (asset === "NOCK_BASE" ? "$NOCK" : asset);
export const isAddressPayment = (asset) =>
  ["BTC", "XMR", "NOCK"].includes(asset);
export const paymentPrefix = (asset) =>
  ({ BTC: "bitcoin", XMR: "monero", NOCK: "nock" })[asset];
export const addressNetworkName = (asset) =>
  ({ BTC: "Bitcoin", XMR: "Monero", NOCK: "Nockchain" })[asset];

export function addressPaymentURI(order, now = Date.now()) {
  if (
    !["BTC", "XMR"].includes(order.asset) ||
    !order.recipient ||
    !["awaiting_payment", "confirming"].includes(order.status)
  )
    throw new Error("Payment is unavailable.");
  if (now >= Date.parse(order.expires))
    throw new Error("This quote has expired. Do not pay it.");
  const prefix = order.asset === "XMR" ? "monero" : "bitcoin";
  const remaining =
    BigInt(order.amount) - BigInt(order[prefix + "Received"] || "0");
  if (remaining <= 0n) throw new Error("Payment is already detected.");
  const amount = displayAmount(remaining.toString(), order.asset);
  const label = encodeURIComponent(order.planet);
  return order.asset === "XMR"
    ? `monero:${order.recipient}?tx_amount=${amount}&tx_description=${label}`
    : `bitcoin:${order.recipient}?amount=${amount}&label=${label}`;
}
