// Admin UI contract; cryptographic authorization is covered by Go integration tests.
import { chromium } from "playwright-core";
import assert from "node:assert/strict";
const base = process.env.PREVIEW_URL || "http://localhost:8080";
const admin = "0x1111111111111111111111111111111111111111";
const browser = await chromium.launch({
  executablePath: process.env.CHROME_PATH || "/usr/bin/google-chrome",
  headless: true,
});
try {
  for (const width of [1440, 390]) {
    const page = await browser.newPage({ viewport: { width, height: 1000 } });
    const errors = [];
    page.on("pageerror", (e) => errors.push(e.message));
    const config = await (await page.request.get(base + "/api/config")).json();
    await page.route("**/api/config", (r) =>
      r.fulfill({
        json: { ...config, adminLogin: { wallet: true, password: false } },
      }),
    );
    await page.route("**/api/auth/session", (r) =>
      r.fulfill({ status: 401, json: { error: "Sign in required" } }),
    );
    await page.route("**/api/auth/challenge", (r) => {
      const body = r.request().postDataJSON();
      assert.equal(body.admin, true);
      assert.equal(body.address, admin);
      return r.fulfill({
        json: {
          id: "admin-challenge",
          message: "Sign in to administer this store",
        },
      });
    });
    await page.route("**/api/auth/login", (r) => {
      assert.deepEqual(r.request().postDataJSON(), {
        challenge: "admin-challenge",
        signature: "signed-admin-message",
      });
      return r.fulfill({
        json: { wallet: admin, admin: true, csrf: "synthetic" },
      });
    });
    await page.route("**/api/orders", (r) => r.fulfill({ json: [] }));
    await page.route("**/api/admin", (r) =>
      r.fulfill({
        json: {
          settings: {
            open: false,
            recipient: admin,
            prices: config.prices,
            rareStrategies: [],
            uniqueStrategies: [],
            premiumStrategies: [],
          },
          orders: [],
          categories: [],
          available: 6,
          sendgridConfigured: false,
          receiptEmail: "store@example.com",
          rarityConfigured: true,
          pendingSpawns: 0,
        },
      }),
    );
    await page.addInitScript((admin) => {
      window.ethereum = {
        request: async ({ method }) => {
          if (method === "eth_requestAccounts") return [admin];
          if (method === "personal_sign") return "signed-admin-message";
          throw Error(method);
        },
        on: () => {},
        removeListener: () => {},
      };
    }, admin);
    await page.goto(base + "/admin");
    await page
      .getByRole("button", { name: "Sign in with wallet", exact: true })
      .waitFor();
    assert.equal(await page.getByLabel("Admin password").count(), 0);
    await page.screenshot({
      path: `../.impeccable/review/admin-wallet-${width}.png`,
      fullPage: true,
    });
    await page
      .getByRole("button", { name: "Sign in with wallet", exact: true })
      .click();
    await page
      .getByRole("button", { name: "Browser wallet", exact: true })
      .click();
    await page
      .getByRole("heading", { name: "Store settings", exact: true })
      .waitFor();
    assert.equal(
      await page.getByLabel("Payment recipient").inputValue(),
      admin,
    );
    assert.equal(await page.getByLabel("Accept new orders").isChecked(), false);
    assert.equal(
      await page.evaluate(
        () => document.documentElement.scrollWidth > innerWidth,
      ),
      false,
    );
    assert.deepEqual(errors, []);
    await page.close();
  }
  console.log(
    "Admin wallet sign-in reaches settings on desktop/mobile without a password.",
  );
} finally {
  await browser.close();
}
