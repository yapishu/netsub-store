// Run against isolated preview inventory; all authentication and orders are mocked.
import { chromium } from "playwright-core";
import assert from "node:assert/strict";
import { mkdir } from "node:fs/promises";
const captures = new URL("../../.impeccable/review/", import.meta.url).pathname;
await mkdir(captures, { recursive: true });
const base = process.env.PREVIEW_URL || "http://localhost:8080";
const wallet = "0x1111111111111111111111111111111111111111";
const token = "0x9b5e262cf9bb04869ab40b19af91d2dc85761722";
// Public deterministic Rust reference address; never send funds here.
const address = "4Qo7o9rHwWr1qSKFdvvDe4DvJNsG2hJMnUwPXfExN7NyvkZn7ihJbiq";
const browser = await chromium.launch({
  executablePath: process.env.CHROME_PATH || "/usr/bin/google-chrome",
  headless: true,
});
try {
  const page = await browser.newPage({
    viewport: { width: 1440, height: 1100 },
  });
  page.setDefaultTimeout(15000);
  const errors = [];
  page.on("pageerror", (e) => {
    errors.push(e.message);
    console.log("pageerror", e.message);
  });
  await page.route("**/api/config", async (route) => {
    const config = await (await route.fetch()).json();
    await route.fulfill({
      json: {
        ...config,
        demo: false,
        open: true,
        nockchain: { enabled: true, confirmations: 2 },
        baseNock: { enabled: true, contract: token, decimals: 16 },
      },
    });
  });
  await page.route("**/api/auth/session", (route) =>
    route.fulfill({ status: 401, json: { error: "Sign in required" } }),
  );
  await page.route("**/api/auth/challenge", (route) =>
    route.fulfill({ json: { id: "test", message: "Test sign-in" } }),
  );
  let loggedIn = false;
  await page.route("**/api/auth/session", (route) =>
    route.fulfill(
      loggedIn
        ? { json: { wallet, csrf: "test" } }
        : { status: 401, json: { error: "Sign in required" } },
    ),
  );
  await page.route("**/api/auth/login", (route) => {
    loggedIn = true;
    return route.fulfill({ json: { wallet, csrf: "test" } });
  });
  let order = null;
  await page.route("**/api/orders", (route) => {
    if (route.request().method() === "POST") {
      const body = route.request().postDataJSON();
      const native = body.asset === "NOCK";
      assert.equal(body.chainId, native ? 0 : 8453);
      order = {
        id: "browser-nock",
        wallet,
        planet: "~sampel-palnet",
        tier: "standard",
        email: body.email,
        chainId: body.chainId,
        asset: body.asset,
        status: "awaiting_payment",
        expires: new Date(Date.now() + 1200000).toISOString(),
        amount: native ? "26214401" : "4000000000000000001",
        recipient: native
          ? address
          : "0x2222222222222222222222222222222222222222",
        nockRequired: 2,
        nockReceived: "0",
        nockConfirmations: 0,
      };
      return route.fulfill({ json: order });
    }
    return route.fulfill({ json: order ? [order] : [] });
  });
  await page.route("**/api/orders/browser-nock/payment", (route) => {
    assert.equal(route.request().postDataJSON().hash, "0x" + "a".repeat(64));
    order = { ...order, status: "confirming", txHash: "0x" + "a".repeat(64) };
    return route.fulfill({ json: order });
  });
  await page.addInitScript((wallet) => {
    window.ethereum = {
      request: async ({ method, params }) => {
        if (method === "eth_requestAccounts") return [wallet];
        if (method === "personal_sign") return "test-signature";
        if (method === "wallet_switchEthereumChain") {
          window.requestedChain = params[0].chainId;
          return null;
        }
        if (method === "eth_accounts") return [window.paymentAccount || wallet];
        if (method === "eth_chainId") return window.paymentChain || "0x2105";
        if (method === "eth_sendTransaction") {
          window.sentPayment = params[0];
          return "0x" + "a".repeat(64);
        }
        throw new Error("Unexpected wallet request: " + method);
      },
      on: () => {},
      removeListener: () => {},
    };
  }, wallet);
  await page.goto(base);
  await page.getByRole("button", { name: /sampel-palnet/ }).click();
  await page.getByLabel("Delivery email").fill("buyer@example.com");
  await page.getByLabel(/^Currency/).selectOption("NOCK");
  await page
    .locator(".checkout")
    .getByRole("button", { name: "Connect wallet", exact: true })
    .click();
  await page
    .getByRole("button", { name: "Browser wallet", exact: true })
    .click();
  await page
    .getByRole("button", { name: "Reserve planet", exact: true })
    .waitFor();
  async function capture(prefix) {
    await page.locator('.planet-grid[aria-busy="false"]').waitFor();
    await page.evaluate(() => document.fonts.ready);
    for (const [width, height, name] of [
      [1440, 1100, "desktop"],
      [390, 844, "mobile"],
    ]) {
      await page.setViewportSize({ width, height });
      await page.evaluate(() => scrollTo(0, 0));
      assert.equal(
        await page.evaluate(
          () => document.documentElement.scrollWidth > innerWidth,
        ),
        false,
      );
      await page.screenshot({
        path: captures + `${prefix}-${name}.png`,
        fullPage: true,
      });
    }
  }
  assert.equal(await page.getByLabel(/^Network/).inputValue(), "0");
  assert.ok(await page.getByLabel(/^Network/).isDisabled());
  await page
    .getByRole("button", { name: "Reserve planet", exact: true })
    .click();
  await page
    .getByRole("button", { name: "Copy amount", exact: true })
    .waitFor();
  assert.match(
    await page.locator(".order").innerText(),
    /400.0000152587890625 NOCK/,
  );
  assert.equal(await page.getByLabel("Payment transaction hash").count(), 0);
  assert.equal(
    await page.getByRole("link", { name: /Open .* wallet/ }).count(),
    0,
  );
  await capture("nockchain");
  await page
    .locator(".checkout")
    .getByRole("button", { name: "Close", exact: true })
    .click();
  await page.getByRole("button", { name: /sampel-palnet/ }).click();
  await page.getByLabel(/^Currency/).selectOption("NOCK_BASE");
  assert.equal(await page.getByLabel(/^Network/).inputValue(), "8453");
  assert.ok(await page.getByLabel(/^Network/).isDisabled());
  await page.getByLabel("Delivery email").fill("buyer@example.com");
  await page
    .getByRole("button", { name: "Reserve planet", exact: true })
    .click();
  await page.getByRole("button", { name: "Pay now", exact: true }).waitFor();
  assert.match(
    await page.locator(".order").innerText(),
    /400.0000000000000001 \$NOCK · Base/,
  );
  await capture("base-nock");
  await page.evaluate(() => {
    window.paymentChain = "0x1";
  });
  await page.getByRole("button", { name: "Pay now", exact: true }).click();
  await page
    .getByRole("alert")
    .filter({ hasText: "Switch to the requested network in your wallet." })
    .waitFor();
  assert.equal(await page.evaluate(() => window.sentPayment), undefined);
  await page.evaluate(() => {
    window.paymentChain = "0x2105";
    window.paymentAccount = "0x3333333333333333333333333333333333333333";
  });
  await page.getByRole("button", { name: "Pay now", exact: true }).click();
  await page
    .getByRole("alert")
    .filter({ hasText: "Reconnect the wallet used for this order." })
    .waitFor();
  assert.equal(await page.evaluate(() => window.sentPayment), undefined);
  await page.evaluate(() => {
    window.paymentAccount = null;
  });
  await page.getByRole("button", { name: "Pay now", exact: true }).click();
  await page.getByText("Confirming payment", { exact: true }).waitFor();
  assert.deepEqual(await page.evaluate(() => window.sentPayment), {
    from: wallet,
    to: token,
    value: "0x0",
    data:
      "0xa9059cbb" +
      "2222222222222222222222222222222222222222".padStart(64, "0") +
      4000000000000000001n.toString(16).padStart(64, "0"),
  });
  assert.equal(await page.evaluate(() => window.requestedChain), "0x2105");
  assert.deepEqual(errors, []);
  console.log(
    "Native NOCK checkout and Base token wallet transfer pass on desktop/mobile, including wrong account and network rejection.",
  );
} finally {
  await browser.close();
}
