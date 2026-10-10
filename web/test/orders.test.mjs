import { test } from "node:test";
import assert from "node:assert/strict";
import { olderUnpaid } from "../src/orders.js";

test("old empty reservations hide without hiding payments or review work", () => {
  const now = Date.now();
  const old = {
    created: new Date(now - 3 * 3600000).toISOString(),
    status: "awaiting_payment",
    paidAt: "0001-01-01T00:00:00Z",
  };
  assert.equal(olderUnpaid(old, now), true);
  for (const patch of [
    { created: new Date(now - 2 * 3600000).toISOString() },
    { paidAt: new Date(now).toISOString() },
    { txHash: "0xabc" },
    { moneroReceived: "1" },
    { bitcoinReceived: "100" },
    { nockReceived: "1" },
    { status: "confirming" },
    { status: "review_payment" },
    { status: "paid" },
    { status: "delivered" },
  ])
    assert.equal(
      olderUnpaid({ ...old, ...patch }, now),
      false,
      JSON.stringify(patch),
    );
  assert.equal(olderUnpaid({ ...old, status: "expired" }, now), true);
});
