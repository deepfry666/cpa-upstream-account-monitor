const { test, expect } = require("@playwright/test");

test.use({ channel: "msedge" });

const uiState = {
  version: "0.4.1",
  generated_at: "2026-09-18T08:00:00Z",
  refreshing: false,
  providers: [
    {
      id: "cc-1",
      name: "Command Code GOAT",
      default_name: "Command Code GOAT",
      provider: "command-code-goat",
      base_url: "https://api.commandcode.ai",
      adapter: "commandcode-goat",
      monitored: true,
      key_hint: "****test",
    },
  ],
  report: {
    schema_version: 1,
    generated_at: "2026-09-18T08:00:00Z",
    status: "ok",
    summary: {
      total: 1,
      ok: 1,
      warning: 0,
      critical: 0,
      error: 0,
      unknown: 0,
      stale: 0,
      balances: 0,
      period_quotas: 0,
      quotas: 1,
    },
    alerts: [],
    accounts: [
      {
        id: "cc-1",
        name: "Command Code GOAT",
        provider: "command-code-goat",
        adapter: "commandcode-goat",
        base_url: "https://api.commandcode.ai",
        kind: "quota",
        status: "ok",
        capabilities: [
          "credits",
          "subscription_usage",
          "quota_windows",
          "usage_statistics",
        ],
        checked_at: "2026-09-18T08:00:00Z",
        last_success_at: "2026-09-18T08:00:00Z",
        latency_ms: 321,
        stale: false,
        quantities: [
          {
            name: "availableCredits",
            scope: "account",
            source: "combined",
            remaining: "67.47249",
            total: "70",
            used: "2.52751",
            unit: "credits",
          },
          {
            name: "monthlyCredits",
            scope: "account",
            source: "subscription",
            remaining: "67.47249",
            total: "70",
            used: "2.52751",
            unit: "credits",
          },
          {
            name: "purchasedCredits",
            scope: "account",
            source: "purchased",
            remaining: "0",
            total: "0",
            unit: "credits",
          },
          {
            name: "freeCredits",
            scope: "account",
            source: "granted",
            remaining: "0",
            total: "0",
            unit: "credits",
          },
        ],
        windows: [
          {
            name: "fiveHour",
            remaining_fraction: 0.819464,
            used_fraction: 0.180536,
            remaining_amount: "11.47249",
            total_amount: "14",
            unit: "credits",
            reset_at: "2026-09-18T12:33:10Z",
          },
          {
            name: "weekly",
            remaining_fraction: 0.927786,
            used_fraction: 0.072214,
            remaining_amount: "32.47249",
            total_amount: "35",
            unit: "credits",
            reset_at: "2026-09-25T12:33:10Z",
          },
          {
            name: "month",
            remaining_fraction: 0.963893,
            used_fraction: 0.036107,
            remaining_amount: "67.47249",
            total_amount: "70",
            unit: "credits",
            reset_at: "2026-10-18T01:54:31Z",
          },
        ],
        details: {
          plan: {
            id: "individual-goat",
            name: "GOAT",
            monthlyCredits: 70,
            monthlyCreditsGranted: 70,
            active: true,
          },
          account: {
            scope: "personal",
            orgId: "",
            orgLogin: "",
            userName: "tester",
          },
          credits: {
            belowThreshold: false,
            creditThreshold: 0,
            monthlyCredits: 67.4724902145,
            purchasedCredits: 0,
            freeCredits: 0,
          },
          subscription: {
            planId: "individual-goat",
            status: "active",
            currentPeriodStart: "2026-09-18T01:54:31Z",
            currentPeriodEnd: "2026-10-18T01:54:31Z",
            quantity: 1,
          },
          usage: {
            totalCount: 490,
            completedCount: 490,
            failedCount: 0,
            totalCredits: 2.4161524415,
            totalCost: 2.4161524415,
            periodBasis: "billing-period",
          },
        },
        warnings: [],
      },
    ],
  },
  config: {
    cache_ttl_seconds: 300,
    request_timeout_seconds: 8,
    warning_percent: 20,
    critical_percent: 10,
  },
};

test.beforeEach(async ({ page }) => {
  await page.addInitScript(() => {
    sessionStorage.setItem("upstream-monitor-management-key", "test-key");
  });
  await page.route(
    "**/v0/management/upstream-monitor/state",
    async (route) => {
      await route.fulfill({
        status: 200,
        contentType: "application/json",
        body: JSON.stringify(uiState),
      });
    },
  );
});

for (const viewport of [
  { name: "desktop", width: 1440, height: 1100 },
  { name: "mobile", width: 390, height: 900 },
]) {
  test(`Command Code layout renders on ${viewport.name}`, async ({ page }) => {
    await page.setViewportSize(viewport);
    await page.goto("http://127.0.0.1:8765/ui.html");
    await expect(page.locator(".hero-label")).toHaveText("5 小时额度");
    await expect(page.locator(".hero-value")).toHaveText("11.47 / 14 credits");
    await expect(page.locator(".hero-percent")).toHaveText("81.9% 剩余");

    const bodyText = await page.locator("body").innerText();
    for (const text of [
      "窗口额度",
      "套餐与账户额度",
      "5 小时",
      "一周",
      "一月",
      "当前仅有套餐月额度，没有额外的已购或赠送 Credits。",
      "本周期用量",
      "组织限制",
      "账户与订阅",
      "个人账户",
      "当前为个人账户，不包含组织限制。",
      "Credits 状态",
    ]) {
      expect(bodyText).toContain(text);
    }
    expect(bodyText.indexOf("窗口额度")).toBeLessThan(
      bodyText.indexOf("套餐与账户额度"),
    );

    const overlap = await page.evaluate(
      () => document.documentElement.scrollWidth - window.innerWidth,
    );
    expect(overlap).toBeLessThanOrEqual(1);
    await page.screenshot({
      path: `/tmp/commandcode-${viewport.name}.png`,
      fullPage: true,
    });
  });
}
