// Run against scripts/preview.sh. This test only uses preview inventory.
import { chromium } from "playwright-core";
import assert from "node:assert/strict";
import { privateKeyToAccount } from "viem/accounts";
const previewAdmin = privateKeyToAccount("0x" + "1".repeat(64));
import { mkdir } from "node:fs/promises";
const captures = new URL("../../.impeccable/review/", import.meta.url).pathname;
await mkdir(captures, { recursive: true });
const base = process.env.PREVIEW_URL || "http://localhost:8080";
const browser = await chromium.launch({
  ...(process.env.CHROME_PATH
    ? { executablePath: process.env.CHROME_PATH }
    : {}),
  headless: true,
});
try {
  const page = await browser.newPage({
    viewport: { width: 1440, height: 1100 },
  });
  const errors = [];
  page.on("pageerror", (error) => errors.push(error.message));
  await page.goto(base);
  await page.getByRole("button", { name: /sampel-palnet/ }).waitFor();
  assert.match(await page.locator(".notice").innerText(), /Preview/);
  const sigils = await page
    .locator("urbit-sigil")
    .evaluateAll((elements) =>
      elements.map((el) => el.shadowRoot.querySelector("svg")?.outerHTML),
    );
  assert.ok(sigils.every(Boolean));
  assert.ok(new Set(sigils).size > 3, "Distinct names render distinct sigils");
  assert.ok(
    sigils.every((svg) => svg.includes("var(--sigil-background")),
    "Sigil cutouts retain a background color",
  );
  assert.equal(
    await page
      .locator("body")
      .evaluate((el) => getComputedStyle(el).backgroundColor),
    "rgb(51, 51, 51)",
  );
  assert.match(
    await page.getByRole("button", { name: /sorreg-namtyv/ }).innerText(),
    /\$25 USD\s+unique/i,
  );
  assert.match(
    await page.getByRole("button", { name: /sampel-sampel/ }).innerText(),
    /\$30 USD\s+premium/i,
  );
  assert.match(
    await page.getByRole("button", { name: /daplen-liswyd/ }).innerText(),
    /\$35 USD\s+name \+ sigil/i,
  );
  await page.screenshot({ path: captures + "desktop.png", fullPage: true });
  assert.equal(await page.locator(".checkout").count(), 0);
  await page
    .getByRole("button", { name: "Connect wallet", exact: true })
    .click();
  await page.getByRole("dialog").waitFor();
  await page.screenshot({ path: captures + "wallet.png", fullPage: true });
  await page.keyboard.press("Escape");
  assert.equal(await page.getByRole("dialog").count(), 0);
  await page.getByRole("button", { name: "Random · $5", exact: true }).click();
  await page.getByText("Random standard planet", { exact: true }).waitFor();
  assert.equal(await page.getByLabel(/^Currency/).inputValue(), "USDC");
  assert.equal(await page.getByLabel(/^Currency/).isDisabled(), false);
  await page.getByLabel(/^Currency/).selectOption("ETH");
  await page.getByRole("img", { name: "Ethereum", exact: true }).waitFor();
  assert.match(await page.locator(".total").innerText(), /\$5\s+USD/);
  await page.getByLabel(/^Network/).selectOption("1");
  await page.getByLabel(/^Network/).selectOption("8453");
  await page.getByLabel(/^Currency/).selectOption("USDC");
  await page.getByRole("button", { name: /sampel-palnet/ }).click();
  await page.getByLabel("Delivery email").fill("test@example.com");
  assert.ok(
    await page.getByRole("button", { name: "Checkout disabled" }).isDisabled(),
  );
  await page.screenshot({
    path: captures + "checkout-desktop.png",
    fullPage: true,
  });
  await page.getByRole("button", { name: "Premium", exact: true }).click();
  await page.waitForFunction(
    () => document.querySelectorAll(".planet").length === 2,
  );
  await page
    .locator(".checkout")
    .getByRole("button", { name: "Close", exact: true })
    .click();
  await page.getByRole("button", { name: "Unique", exact: true }).click();
  await page.waitForFunction(
    () => document.querySelectorAll(".planet").length === 1,
  );
  await page.getByRole("button", { name: /sorreg-namtyv/ }).click();
  assert.match(await page.locator(".total").innerText(), /25 USDC/);
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
      path: captures + `unique-checkout-${name}.png`,
      fullPage: true,
    });
  }
  await page.getByRole("button", { name: "Premium", exact: true }).click();
  await page.getByRole("button", { name: /daplen-liswyd/ }).click();
  assert.match(await page.locator(".total").innerText(), /35 USDC/);
  for (const [width, height, name] of [
    [390, 844, "mobile"],
    [1440, 1100, "desktop"],
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
      path: captures + `premium-combined-${name}.png`,
      fullPage: true,
    });
  }
  await page.exposeFunction("signPreviewAdmin", (message) =>
    previewAdmin.signMessage({ message: { raw: message } }),
  );
  await page.addInitScript((address) => {
    window.ethereum = {
      request: async ({ method, params }) => {
        if (method === "eth_requestAccounts") return [address];
        if (method === "personal_sign")
          return window.signPreviewAdmin(params[0]);
        throw Error(method);
      },
      on() {},
      removeListener() {},
    };
  }, previewAdmin.address);
  await page.goto(`${base}/admin`);
  await page.screenshot({ path: captures + "admin-login.png", fullPage: true });
  await page
    .getByRole("button", { name: "Sign in with wallet", exact: true })
    .click();
  await page
    .getByRole("button", { name: "Browser wallet", exact: true })
    .click();
  await page.getByRole("heading", { name: "Store settings" }).waitFor();
  const adminData = await page.evaluate(() =>
    fetch("/api/admin").then((r) => r.json()),
  );
  await page.screenshot({
    path: captures + "admin-desktop.png",
    fullPage: true,
  });
  await page
    .getByLabel("Payment recipient")
    .fill("0x2222222222222222222222222222222222222222");
  await page.getByLabel("Premium · USD", { exact: true }).fill("30");
  await page
    .getByRole("button", { name: "Save settings", exact: true })
    .click();
  await page.getByRole("status").waitFor();
  await page.setViewportSize({ width: 390, height: 844 });
  await page.screenshot({
    path: captures + "admin-mobile.png",
    fullPage: true,
  });
  await page
    .getByRole("button", { name: "Admin · sign out", exact: true })
    .click();
  await page
    .getByRole("button", { name: "Sign in with wallet", exact: true })
    .waitFor();
  await page.goto(base);
  await page.setViewportSize({ width: 390, height: 844 });
  await page.getByRole("button", { name: /sampel-palnet/ }).waitFor();
  assert.equal(
    await page.evaluate(
      () => document.documentElement.scrollWidth > innerWidth,
    ),
    false,
  );
  await page.screenshot({ path: captures + "mobile.png", fullPage: true });
  await page.getByRole("button", { name: /sampel-palnet/ }).click();
  assert.ok(await page.getByLabel("Delivery email").isVisible());
  assert.ok(
    await page
      .locator(".checkout")
      .evaluate((el) => el.getBoundingClientRect().top < innerHeight),
  );
  const colors = await page.locator(".planet.selected").evaluate((el) => ({
    background: getComputedStyle(el).backgroundColor,
    fill: getComputedStyle(
      el.querySelector("urbit-sigil").shadowRoot.querySelector("svg > rect"),
    ).fill,
  }));
  assert.equal(colors.fill, colors.background);
  await page.screenshot({
    path: captures + "checkout-mobile.png",
    fullPage: true,
  });
  await page
    .locator(".checkout")
    .getByRole("button", { name: "Close", exact: true })
    .click();
  assert.equal(await page.locator(".checkout").count(), 0);
  await page.getByLabel("Search planet names").fill("zzzzzz");
  await page.getByText("No matching planets. Try another search.").waitFor();
  // Simulated wallet sign-in: no payment or order reaches the server.
  const wallet = "0x1111111111111111111111111111111111111111";
  await page.route("**/api/config", async (route) => {
    const response = await route.fetch();
    const config = await response.json();
    await route.fulfill({
      json: {
        ...config,
        demo: false,
        open: true,
        bitcoin: { enabled: true, confirmations: 6 },
        monero: { enabled: true, confirmations: 10 },
      },
    });
  });
  await page.route("**/api/auth/challenge", (route) =>
    route.fulfill({ json: { id: "test", message: "Sign in test" } }),
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
  await page.route("**/api/orders", (route) => {
    assert.equal(route.request().method(), "GET");
    return route.fulfill({ json: [] });
  });
  await page.addInitScript((wallet) => {
    window.ethereum = {
      request: async ({ method }) => {
        if (method === "eth_requestAccounts") return [wallet];
        if (method === "personal_sign") return "test-signature";
        throw new Error("Unexpected wallet action: " + method);
      },
      on: (event, callback) => {
        if (event === "accountsChanged") window.walletChanged = callback;
      },
      removeListener: () => {},
    };
  }, wallet);
  await page.goto(base);
  await page.getByRole("button", { name: /sampel-palnet/ }).click();
  await page.getByLabel("Delivery email").fill("buyer@example.com");
  assert.equal(await page.getByLabel(/^Currency/).inputValue(), "BTC");
  await page.getByRole("button", { name: "Random · $5", exact: true }).click();
  assert.equal(await page.getByLabel(/^Currency/).inputValue(), "BTC");
  await page.getByRole("button", { name: /sampel-palnet/ }).click();
  await page.getByLabel(/^Currency/).selectOption("ETH");
  await page.getByLabel(/^Network/).selectOption("1");
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
  assert.equal(
    await page.locator(".chosen-name").innerText(),
    "~sampel-palnet",
  );
  assert.equal(
    await page.getByLabel("Delivery email").inputValue(),
    "buyer@example.com",
  );
  assert.equal(await page.getByLabel(/^Network/).inputValue(), "1");
  assert.equal(await page.getByLabel(/^Currency/).inputValue(), "ETH");
  await page.screenshot({
    path: captures + "checkout-connected-mobile.png",
    fullPage: true,
  });
  assert.equal(
    await page.locator("header .brand").textContent(),
    "networked subject_",
  );
  assert.equal(await page.locator("header").getByText("My planets").count(), 0);
  const info = page.locator(".checkout .planet-info");
  assert.equal(
    await info
      .getByRole("link", { name: "L2 planet", exact: true })
      .getAttribute("href"),
    "https://docs.urbit.org/urbit-id/l2",
  );
  assert.match(
    await info.innerText(),
    /Includes planet code redeemable on bridge.urbit.org and email with info/,
  );
  assert.match(await info.innerText(), /Please wait up to 1 hour/);
  let btcOrder = {
    id: "browser-btc",
    wallet,
    planet: "~sampel-palnet",
    tier: "standard",
    email: "buyer@example.com",
    asset: "BTC",
    chainId: 0,
    amount: "10000",
    recipient: "bcrt1qvhd38xpyp3pcwuzq92lmnhxlzu4zswc5pwdesz",
    bitcoinNetwork: "regtest",
    bitcoinReceived: "0",
    bitcoinRequired: 6,
    bitcoinConfirmations: 0,
    status: "awaiting_payment",
    expires: new Date(Date.now() + 1200000).toISOString(),
  };
  await page.unroute("**/api/orders");
  await page.route("**/api/orders", (route) => {
    if (route.request().method() === "POST") {
      const body = route.request().postDataJSON();
      assert.equal(body.asset, "BTC");
      assert.equal(body.chainId, 0);
      return route.fulfill({ json: btcOrder });
    }
    return route.fulfill({ json: [btcOrder] });
  });
  await page.getByLabel(/^Currency/).selectOption("BTC");
  assert.equal(await page.getByLabel(/^Network/).inputValue(), "0");
  await page
    .getByRole("button", { name: "Reserve planet", exact: true })
    .click();
  await page.getByRole("link", { name: "Open Bitcoin wallet" }).waitFor();
  assert.match(
    await page
      .getByRole("link", { name: "Open Bitcoin wallet" })
      .getAttribute("href"),
    /^bitcoin:.*amount=0.0001/,
  );
  assert.equal(await page.getByLabel("Payment transaction hash").count(), 0);
  for (const [width, height, name] of [
    [390, 844, "mobile"],
    [1440, 1100, "desktop"],
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
      path: captures + `bitcoin-${name}.png`,
      fullPage: true,
    });
  }
  btcOrder = {
    ...btcOrder,
    status: "delivered",
    bitcoinReceived: "10000",
    bitcoinConfirmations: 6,
    emailSent: true,
  };
  await page
    .getByRole("button", { name: "Reveal planet code" })
    .waitFor({ timeout: 15000 });
  const code = "~doznec-binwes-samper-siglet-sampel-palnet";
  await page.route("**/api/orders/browser-btc/planet-code", (route) =>
    route.fulfill({
      json: {
        planet: btcOrder.planet,
        code,
        url: "https://bridge.urbit.org/#" + code.slice(1),
      },
    }),
  );
  await page.getByRole("button", { name: "Reveal planet code" }).click();
  await page.getByText(code, { exact: true }).waitFor();
  assert.equal(
    await page
      .getByRole("link", { name: "Redeem on Bridge" })
      .getAttribute("href"),
    "https://bridge.urbit.org/#" + code.slice(1),
  );
  assert.equal(
    await page
      .getByRole("link", { name: "Download backup" })
      .getAttribute("href"),
    "/api/orders/browser-btc/planet-code?download=1",
  );
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
      path: captures + `planet-code-${name}.png`,
      fullPage: true,
    });
  }
  await page
    .locator(".checkout")
    .getByRole("button", { name: "Close", exact: true })
    .click();
  await page.getByRole("button", { name: /sampel-sampel/ }).click();
  await page.getByLabel(/^Currency/).selectOption("XMR");
  assert.equal(await page.getByLabel(/^Network/).inputValue(), "0");
  assert.equal(
    await page
      .getByLabel(/^Network/)
      .locator("option:checked")
      .innerText(),
    "Monero",
  );
  for (const [width, height, name] of [
    [1440, 1100, "desktop"],
    [390, 844, "mobile"],
  ]) {
    await page.setViewportSize({ width, height });
    await page.evaluate(() => scrollTo(0, 0));
    await page.screenshot({
      path: captures + `monero-checkout-${name}.png`,
      fullPage: true,
    });
  }
  const xmrOrder = {
    ...btcOrder,
    id: "browser-xmr",
    planet: "~sampel-sampel",
    tier: "premium",
    asset: "XMR",
    amount: "150000000000",
    recipient: "7" + "A".repeat(94),
    moneroRequired: 10,
    moneroReceived: "0",
    moneroConfirmations: 0,
    status: "awaiting_payment",
    emailSent: false,
  };
  await page.unroute("**/api/orders");
  await page.route("**/api/orders", (route) => {
    if (route.request().method() === "POST") {
      const body = route.request().postDataJSON();
      assert.equal(body.asset, "XMR");
      assert.equal(body.chainId, 0);
      return route.fulfill({ json: xmrOrder });
    }
    return route.fulfill({ json: [btcOrder, xmrOrder] });
  });
  await page
    .getByRole("button", { name: "Reserve planet", exact: true })
    .click();
  await page.getByRole("link", { name: "Open Monero wallet" }).waitFor();
  assert.match(
    await page
      .getByRole("link", { name: "Open Monero wallet" })
      .getAttribute("href"),
    /^monero:.*tx_amount=0.15/,
  );
  assert.equal(await page.getByLabel("Payment transaction hash").count(), 0);
  for (const [width, height, name] of [
    [390, 844, "mobile"],
    [1440, 1100, "desktop"],
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
      path: captures + `monero-${name}.png`,
      fullPage: true,
    });
  }
  await page.evaluate(() =>
    window.walletChanged(["0x2222222222222222222222222222222222222222"]),
  );
  await page.locator(".checkout").waitFor({ state: "detached" });
  await page.route("**/api/auth/session", (route) =>
    route.fulfill({ json: { admin: true, csrf: "test" } }),
  );
  await page.route("**/api/admin", (route) =>
    route.fulfill({
      json: {
        ...adminData,
        orders: [{ ...btcOrder, status: "confirming" }, xmrOrder],
      },
    }),
  );
  await page.goto(`${base}/admin`);
  await page.getByRole("heading", { name: "Store settings" }).waitFor();
  assert.equal(
    await page.getByRole("link", { name: /Open .* wallet/ }).count(),
    0,
  );
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
      path: captures + `admin-payments-${name}.png`,
      fullPage: true,
    });
  }
  assert.deepEqual(errors, []);
  console.log(
    "Desktop/mobile storefront, sigils, rarity, preview checkout, admin login, pricing, wallet checkout continuity, Bitcoin default, Monero checkout, and account reset pass.",
  );
} finally {
  await browser.close();
}
