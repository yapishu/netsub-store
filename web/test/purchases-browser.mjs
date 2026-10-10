import { chromium } from "playwright-core";
import assert from "node:assert/strict";
const base = process.env.PREVIEW_URL || "http://localhost:8080";
const wallet = "0x1111111111111111111111111111111111111111";
const browser = await chromium.launch({ executablePath: process.env.CHROME_PATH || "/usr/bin/google-chrome", headless: true });
try {
  for (const width of [1440, 390]) {
    const page = await browser.newPage({ viewport: { width, height: 1000 } });
    const errors = [];
    page.on("pageerror", error => errors.push(error.message));
    const common = { wallet, email: "buyer@example.com", amount: "2000000000000000", asset: "ETH", chainId: 1, created: new Date(Date.now()-3600000).toISOString(), expires: new Date(Date.now()-2400000).toISOString() };
    const rows = [
      {...common, id:"pending", planet:"~sampel-palnet", status:"confirming", txHash:"0x"+"a".repeat(64)},
      {...common, id:"delivered", planet:"~ravmel-ropdyl", status:"delivered", paidAt: new Date().toISOString(), emailSent:true},
      {...common, id:"empty", planet:"~older-unpaid", status:"expired", created:new Date(Date.now()-3*3600000).toISOString()},
    ];
    await page.route("**/api/auth/session", route => route.fulfill({json:{wallet,csrf:"synthetic"}}));
    await page.route("**/api/orders", route => route.fulfill({json:rows}));
    await page.goto(base);
    const purchases=page.getByRole("region",{name:"Your purchases"});
    await purchases.getByText("Confirming payment",{exact:true}).waitFor();
    await purchases.getByText("Your planet code is ready",{exact:true}).waitFor();
    assert.equal(await purchases.getByText("~older-unpaid",{exact:true}).count(),0);
    await purchases.getByRole("link",{name:"View payment",exact:true}).waitFor();
    await page.locator("header").getByRole("button",{name:"Purchases (2)",exact:true}).waitFor();
    await purchases.getByRole("button",{name:"Close",exact:true}).click();
    assert.equal(await purchases.count(),0);
    await page.locator("header").getByRole("button",{name:"Purchases (2)",exact:true}).click();
    await purchases.getByText("Confirming payment",{exact:true}).waitFor();
    await purchases.getByLabel("Show older unpaid orders").check();
    await purchases.getByText("~older-unpaid",{exact:true}).waitFor();
    await purchases.getByLabel("Show older unpaid orders").uncheck();
    rows[0].status="paid";
    await purchases.getByRole("button",{name:"Refresh",exact:true}).click();
    await purchases.getByText("Preparing your planet",{exact:true}).waitFor();
    await page.reload();
    await purchases.getByText("Preparing your planet",{exact:true}).waitFor();
    assert.equal(await page.evaluate(()=>document.documentElement.scrollWidth>innerWidth),false);
    await page.screenshot({path:new URL(`../../.impeccable/review/purchases-${width}.png`,import.meta.url).pathname,fullPage:true});
    assert.deepEqual(errors,[]);
    await page.close();
  }
  console.log("Saved purchases open after session restoration; pending payments, delivered codes, filtering, refresh, and header access pass on desktop/mobile.");
} finally { await browser.close(); }
