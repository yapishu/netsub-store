import test from "node:test";
import assert from "node:assert/strict";
import {
  paymentRequest,
  displayAmount,
  addressPaymentURI,
} from "../src/payment.js";
const account = "0x1111111111111111111111111111111111111111";
const order = {
  wallet: account,
  recipient: "0x2222222222222222222222222222222222222222",
  chainId: 8453,
  asset: "USDC",
  amount: "10000001",
  expires: "2099-01-01T00:00:00Z",
  status: "awaiting_payment",
};
const chain = { id: 8453, usdc: "0x833589fCD6eDb6E08f4c7C32D4f71b54bdA02913" };
test("USDC uses direct transfer without token approval", () => {
  const tx = paymentRequest(order, chain, account);
  assert.equal(tx.to, chain.usdc);
  assert.equal(tx.value, "0x0");
  assert.equal(
    tx.data,
    "0xa9059cbb" +
      order.recipient.slice(2).padStart(64, "0") +
      BigInt(order.amount).toString(16).padStart(64, "0"),
  );
});
test("ETH keeps exact wei", () => {
  const tx = paymentRequest(
    { ...order, asset: "ETH", amount: "1000000000000000001" },
    chain,
    account,
  );
  assert.equal(BigInt(tx.value), 1000000000000000001n);
  assert.equal(tx.to, order.recipient);
  assert.equal(tx.data, undefined);
});
test("account changes, network changes, duplicate and expired payments are rejected", () => {
  assert.throws(() => paymentRequest(order, chain, order.recipient));
  assert.throws(() => paymentRequest(order, { id: 1 }, account));
  assert.throws(() =>
    paymentRequest({ ...order, status: "confirming" }, chain, account),
  );
  assert.throws(() =>
    paymentRequest({ ...order, expires: "2000-01-01" }, chain, account),
  );
});
test("amount rendering is exact", () => {
  assert.equal(displayAmount("10000001", "USDC"), "10.000001");
  assert.equal(displayAmount("10000000", "USDC"), "10");
  assert.equal(displayAmount("1", "ETH"), "0.000000000000000001");
  assert.equal(displayAmount("0", "BTC"), "0");
  assert.equal(displayAmount("1", "BTC"), "0.00000001");
});
test("Bitcoin wallet URI requests only the unpaid amount of an active quote", () => {
  const btc = {
    ...order,
    asset: "BTC",
    amount: "10000",
    bitcoinReceived: "2500",
    recipient: "bc1qtest",
    planet: "~sampel-palnet",
  };
  assert.equal(
    addressPaymentURI(btc),
    "bitcoin:bc1qtest?amount=0.000075&label=~sampel-palnet",
  );
  assert.throws(() => addressPaymentURI({ ...btc, bitcoinReceived: "10000" }));
  assert.throws(() => addressPaymentURI({ ...btc, expires: "2000-01-01" }));
  assert.throws(() => addressPaymentURI({ ...btc, status: "delivered" }));
  assert.throws(() => paymentRequest(btc, chain, account));
});

test("Monero preserves atomic precision and encodes the unpaid amount", () => {
  assert.equal(displayAmount("1", "XMR"), "0.000000000001");
  const xmr = {
    ...order,
    asset: "XMR",
    amount: "1234567890123",
    moneroReceived: "234567890122",
    recipient: "monero-subaddress",
    planet: "~sampel-palnet",
  };
  assert.equal(
    addressPaymentURI(xmr),
    "monero:monero-subaddress?tx_amount=1.000000000001&tx_description=~sampel-palnet",
  );
  assert.throws(() =>
    addressPaymentURI({ ...xmr, moneroReceived: xmr.amount }),
  );
  assert.throws(() => addressPaymentURI({ ...xmr, status: "review_payment" }));
});

test("NOCK uses exact native nicks and Base token units", () => {
  assert.equal(displayAmount("1", "NOCK"), "0.0000152587890625");
  assert.equal(displayAmount("65536", "NOCK"), "1");
  assert.equal(displayAmount("1", "NOCK_BASE"), "0.0000000000000001");
  assert.equal(
    displayAmount("18446744073709551615", "NOCK"),
    "281474976710655.9999847412109375",
  );
  assert.throws(() => addressPaymentURI({ ...order, asset: "NOCK" }));
});
test("Base NOCK sends a direct ERC-20 transfer on Base only", () => {
  const network = {
    ...chain,
    nock: "0x9b5e262cf9bb04869ab40b19af91d2dc85761722",
  };
  const quote = { ...order, asset: "NOCK_BASE", amount: "4000000000000000001" };
  const tx = paymentRequest(quote, network, account);
  assert.equal(tx.to, network.nock);
  assert.equal(tx.value, "0x0");
  assert.equal(
    tx.data,
    "0xa9059cbb" +
      order.recipient.slice(2).padStart(64, "0") +
      BigInt(quote.amount).toString(16).padStart(64, "0"),
  );
  assert.throws(() =>
    paymentRequest({ ...quote, chainId: 1 }, { ...network, id: 1 }, account),
  );
  assert.throws(() => paymentRequest(quote, chain, account));
  assert.throws(() =>
    paymentRequest(
      { ...quote, amount: (1n << 256n).toString() },
      network,
      account,
    ),
  );
});
