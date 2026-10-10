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
        json: { ...config, adminLogin: { wallet: true } },
      }),
    );
    let loggedIn = false;
    let rejectSession = true;
    await page.route("**/api/auth/session", (r) =>
      r.fulfill(
        loggedIn && !rejectSession
          ? { json: { wallet: admin, admin: true, csrf: "synthetic" } }
          : { status: 401, json: { error: "Sign in required" } },
      ),
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
      loggedIn = true;
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
          orders: [
            {
              id: "old-empty",
              planet: "~old-empty",
              wallet: admin,
              email: "test@example.com",
              asset: "USDC",
              amount: "10000000",
              status: "expired",
              created: new Date(Date.now() - 3 * 3600000).toISOString(),
              recipient: admin,
            },
            {
              id: "paid-order",
              planet: "~paid-order",
              wallet: admin,
              email: "test@example.com",
              asset: "USDC",
              amount: "10000000",
              status: "delivered",
              paidAt: new Date().toISOString(),
              created: new Date(Date.now() - 3 * 3600000).toISOString(),
              recipient: admin,
            },
          ],
          categories: [],
          available: 6,
          sendgridConfigured: true,
          demo: false,
          receiptEmail: "store@example.com",
          emailFrom: "sender@example.com",
          rarityConfigured: true,
          pendingSpawns: 0,
        },
      }),
    );
    await page.route("**/api/admin/email-preview", (r) =>
      r.fulfill({
        json: {
          delivery: {
            subject: "[TEST] Planet code",
            text: "SAMPLE-CODE-NOT-REDEEMABLE",
          },
          receipt: { subject: "[TEST] Receipt", text: "TEST — no purchase" },
        },
      }),
    );
    await page.route("**/api/admin/test-email", (r) => {
      assert.deepEqual(r.request().postDataJSON(), {
        email: "store@example.com",
        kind: "delivery",
      });
      return r.fulfill({
        json: { message: "SendGrid accepted the test email." },
      });
    });
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
      .getByRole("alert")
      .filter({ hasText: "wallet signature was accepted" })
      .waitFor();
    assert.equal(
      await page
        .getByRole("heading", { name: "Store settings", exact: true })
        .count(),
      0,
    );
    assert.equal(await page.getByRole("dialog").count(), 0);
    rejectSession = false;
    await page
      .getByRole("button", { name: "Sign in with wallet", exact: true })
      .click();
    await page
      .getByRole("button", { name: "Browser wallet", exact: true })
      .click();
    await page
      .getByRole("heading", { name: "Store settings", exact: true })
      .waitFor();
    await page.getByText("~paid-order", { exact: true }).waitFor();
    assert.equal(
      await page.getByText("~old-empty", { exact: true }).count(),
      0,
    );
    await page.getByLabel(/Show older unpaid orders/).check();
    await page.getByText("~old-empty", { exact: true }).waitFor();
    await page.getByLabel(/Show older unpaid orders/).uncheck();
    await page
      .getByText("Preview: [TEST] Planet code", { exact: true })
      .click();
    await page
      .getByText("SAMPLE-CODE-NOT-REDEEMABLE", { exact: true })
      .waitFor();
    await page
      .getByRole("button", { name: "Send test email", exact: true })
      .click();
    await page
      .getByRole("status")
      .filter({ hasText: "SendGrid accepted" })
      .waitFor();
    await page.screenshot({
      path: `../.impeccable/review/admin-settings-${width}.png`,
      fullPage: true,
    });
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
