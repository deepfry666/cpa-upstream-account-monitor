const { test, expect } = require("@playwright/test");

test.use({ channel: "msedge" });

const uiBaseURL = (process.env.UI_BASE_URL || "http://127.0.0.1:8765").replace(/\/+$/, "");
const uiURL = `${uiBaseURL}/ui.html`;
const iframeHarnessURL = `${uiBaseURL}/__upstream-monitor-iframe-harness`;

const uiState = {
  version: "0.5.3",
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
            remaining: "30",
            total: "35",
            used: "5",
            unit: "credits",
          },
          {
            name: "monthlyCredits",
            scope: "account",
            source: "subscription",
            remaining: "30",
            total: "35",
            used: "5",
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
        ],
        details: {
          plan: {
            id: "individual-goat",
            name: "GOAT",
            monthlyCredits: 35,
            monthlyCreditsGranted: 35,
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
            monthlyCredits: 30,
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

test("state warnings are shown in the status line", async ({ page }) => {
  const state = structuredClone(uiState);
  state.warnings = [
    "快照缓存恢复失败，已保留原文件并停止自动覆盖：decode snapshot cache",
  ];
  await page.unroute("**/v0/management/upstream-monitor/state");
  await page.route(
    "**/v0/management/upstream-monitor/state",
    async (route) => {
      await route.fulfill({
        status: 200,
        contentType: "application/json",
        body: JSON.stringify(state),
      });
    },
  );

  await page.goto(uiURL);

  const statusLine = page.locator("#status-line");
  await expect(statusLine).toContainText("快照缓存恢复失败");
  await expect(statusLine).toHaveClass(/error/);
});

test("Z.ai cash balance limitation renders with console action", async ({ page }) => {
  const state = structuredClone(uiState);
  const account = state.report.accounts[0];
  account.id = "zai-1";
  account.name = "智谱主账户";
  account.provider = "智谱";
  account.adapter = "zai-usage";
  account.kind = "period_quota";
  account.sections = {
    cash_balance: {
      status: "unsupported",
      message: "智谱当前未提供可验证的现金余额查询接口，该账号暂不支持自动查询现金余额。",
      action_url: "https://z.ai/manage-apikey/billing",
      action_label: "前往控制台查看",
    },
  };
  state.providers[0] = {
    ...state.providers[0],
    id: "zai-1",
    name: "智谱主账户",
    default_name: "智谱主账户",
    provider: "智谱",
    adapter: "zai-usage",
    base_url: "https://api.z.ai/api/paas/v4",
  };

  await page.unroute("**/v0/management/upstream-monitor/state");
  await page.route("**/v0/management/upstream-monitor/state", async (route) => {
    await route.fulfill({
      status: 200,
      contentType: "application/json",
      body: JSON.stringify(state),
    });
  });

  await page.setViewportSize({ width: 390, height: 900 });
  await page.goto(uiURL);
  await expect(
    page.getByText("该账号暂不支持自动查询现金余额。"),
  ).toBeVisible();
  await expect(
    page.getByRole("link", { name: "前往控制台查看" }),
  ).toHaveAttribute("href", "https://z.ai/manage-apikey/billing");
});

test("single account refresh sends an explicit asynchronous request", async ({
  page,
}) => {
  const refreshRequests = [];
  await page.route(
    "**/v0/management/upstream-monitor/refresh",
    async (route) => {
      const request = route.request();
      refreshRequests.push({
        method: request.method(),
        body: request.postDataJSON(),
      });
      await route.fulfill({
        status: 202,
        contentType: "application/json",
        body: JSON.stringify({
          job_id: "job_cc_1",
          status: "completed",
          account_ids: ["cc-1"],
          total: 1,
          completed: 1,
          failed: 0,
          skipped: 0,
          results: [{ account_id: "cc-1", status: "success" }],
        }),
      });
    },
  );

  await page.goto(uiURL);
  await page
    .getByRole("button", { name: "刷新 Command Code GOAT" })
    .click();

  await expect.poll(() => refreshRequests.length).toBe(1);
  expect(refreshRequests[0]).toEqual({
    method: "POST",
    body: { account_ids: ["cc-1"], force: true, async: true },
  });
});

test("refresh polls the job and reports its final result", async ({ page }) => {
  let postCount = 0;
  let getCount = 0;
  await page.route(
    "**/v0/management/upstream-monitor/refresh**",
    async (route) => {
      const request = route.request();
      if (request.method() === "POST") {
        postCount += 1;
        await route.fulfill({
          status: 202,
          contentType: "application/json",
          body: JSON.stringify({
            job_id: "job_cc_1",
            status: "queued",
            account_ids: ["cc-1"],
            total: 1,
            completed: 0,
            failed: 0,
            skipped: 0,
          }),
        });
        return;
      }
      getCount += 1;
      await route.fulfill({
        status: 200,
        contentType: "application/json",
        body: JSON.stringify({
          job_id: "job_cc_1",
          status: "completed",
          account_ids: ["cc-1"],
          total: 1,
          completed: 1,
          failed: 0,
          skipped: 0,
          results: [{ account_id: "cc-1", status: "success" }],
        }),
      });
    },
  );

  await page.goto(uiURL);
  await page
    .getByRole("button", { name: "刷新 Command Code GOAT" })
    .click();

  await expect.poll(() => postCount).toBe(1);
  await expect.poll(() => getCount).toBe(1);
  await expect(page.locator("#status-line")).toContainText(
    "刷新完成：1 个成功，0 个失败，0 个跳过",
  );
});

test("refresh all sends the explicit all scope", async ({ page }) => {
  const refreshRequests = [];
  await page.route(
    "**/v0/management/upstream-monitor/refresh",
    async (route) => {
      refreshRequests.push(route.request().postDataJSON());
      await route.fulfill({
        status: 202,
        contentType: "application/json",
        body: JSON.stringify({
          job_id: "job_all",
          status: "completed",
          total: 1,
          completed: 1,
          failed: 0,
          skipped: 0,
          results: [],
        }),
      });
    },
  );
  await page.route(
    "**/v0/management/upstream-monitor/refresh?job_id=*",
    async (route) => {
      await route.fulfill({
        status: 200,
        contentType: "application/json",
        body: JSON.stringify({
          job_id: "job_all",
          status: "completed",
          total: 1,
          completed: 1,
          failed: 0,
          skipped: 0,
          results: [],
        }),
      });
    },
  );

  await page.goto(uiURL);
  await page.getByRole("button", { name: "刷新全部" }).click();

  await expect.poll(() => refreshRequests.length).toBe(1);
  expect(refreshRequests[0]).toEqual({
    scope: "all",
    force: true,
    async: true,
  });
});

test("reload only reads state and never starts a refresh", async ({ page }) => {
  let stateCount = 0;
  let refreshCount = 0;
  await page.unroute("**/v0/management/upstream-monitor/state");
  await page.route(
    "**/v0/management/upstream-monitor/state",
    async (route) => {
      stateCount += 1;
      await route.fulfill({
        status: 200,
        contentType: "application/json",
        body: JSON.stringify(uiState),
      });
    },
  );
  await page.route(
    "**/v0/management/upstream-monitor/refresh**",
    async (route) => {
      refreshCount += 1;
      await route.fulfill({
        status: 500,
        contentType: "application/json",
        body: JSON.stringify({ error: "reload must not refresh" }),
      });
    },
  );

  await page.goto(uiURL);
  await expect.poll(() => stateCount).toBe(1);
  await page.getByRole("button", { name: "重新加载" }).click();

  await expect.poll(() => stateCount).toBe(2);
  expect(refreshCount).toBe(0);
});

test("provider save sends revision and explicit PAT actions", async ({ page }) => {
  const state = structuredClone(uiState);
  state.revision = 12;
  state.providers[0] = {
    ...state.providers[0],
    adapter: "newapi-usage",
    adapter_override: "newapi-usage",
    management_pat_configured: true,
  };
  let savedBody = null;
  await page.unroute("**/v0/management/upstream-monitor/state");
  await page.route(
    "**/v0/management/upstream-monitor/state",
    async (route) => {
      await route.fulfill({
        status: 200,
        contentType: "application/json",
        body: JSON.stringify(state),
      });
    },
  );
  await page.route(
    "**/v0/management/upstream-monitor/providers",
    async (route) => {
      savedBody = route.request().postDataJSON();
      await route.fulfill({
        status: 200,
        contentType: "application/json",
        body: JSON.stringify({ ...state, revision: 13 }),
      });
    },
  );

  await page.goto(uiURL);
  await page.getByRole("tab", { name: "供应商配置" }).click();
  await page.locator('[data-pat-id="cc-1"]').fill("replacement-pat");
  await page.getByRole("button", { name: "保存监控配置" }).click();

  await expect.poll(() => savedBody !== null).toBe(true);
  expect(savedBody.base_revision).toBe(12);
  expect(savedBody.providers[0]).toMatchObject({
    id: "cc-1",
    pat_action: "replace",
    management_pat: "replacement-pat",
  });
  expect(savedBody.providers[0].clear_management_pat).toBeUndefined();
});

test("provider save result remains visible after state refresh", async ({ page }) => {
  const state = structuredClone(uiState);
  state.revision = 12;
  state.providers[0] = {
    ...state.providers[0],
    adapter: "newapi-usage",
    adapter_override: "newapi-usage",
  };
  await page.unroute("**/v0/management/upstream-monitor/state");
  await page.route(
    "**/v0/management/upstream-monitor/state",
    async (route) => {
      await route.fulfill({
        status: 200,
        contentType: "application/json",
        body: JSON.stringify(state),
      });
    },
  );
  await page.route(
    "**/v0/management/upstream-monitor/providers",
    async (route) => {
      state.revision = 13;
      state.providers[0].custom_name = "已提交备注";
      await route.fulfill({
        status: 200,
        contentType: "application/json",
        body: JSON.stringify(state),
      });
    },
  );

  await page.goto(uiURL);
  await page.getByRole("tab", { name: "供应商配置" }).click();
  await page.locator('[data-name-id="cc-1"]').fill("已提交备注");
  await page.getByRole("button", { name: "保存监控配置" }).click();

  await expect(page.locator("#status-line")).toContainText(
    "供应商配置已保存",
  );
  await page.getByRole("button", { name: "重新加载" }).click();
  await expect(page.locator("#status-line")).toContainText(
    "供应商配置已保存",
  );
});

test("provider revision conflict keeps the draft and explains recovery", async ({
  page,
}) => {
  const state = structuredClone(uiState);
  state.revision = 12;
  await page.unroute("**/v0/management/upstream-monitor/state");
  await page.route(
    "**/v0/management/upstream-monitor/state",
    async (route) => {
      await route.fulfill({
        status: 200,
        contentType: "application/json",
        body: JSON.stringify(state),
      });
    },
  );
  await page.route(
    "**/v0/management/upstream-monitor/providers",
    async (route) => {
      await route.fulfill({
        status: 409,
        contentType: "application/json",
        body: JSON.stringify({
          error: "provider configuration changed; reload and try again",
          base_revision: 12,
          revision: 13,
        }),
      });
    },
  );

  await page.goto(uiURL);
  await page.getByRole("tab", { name: "供应商配置" }).click();
  const draft = page.locator('[data-name-id="cc-1"]');
  await draft.fill("冲突草稿");
  await page.getByRole("button", { name: "保存监控配置" }).click();

  await expect(page.locator("#status-line")).toContainText(
    "配置已被其他页面修改，请重新加载后再保存",
  );
  await expect(draft).toHaveValue("冲突草稿");
});

test("provider drafts survive tab changes", async ({ page }) => {
  const state = structuredClone(uiState);
  state.providers[0] = {
    ...state.providers[0],
    adapter: "newapi-usage",
    adapter_override: "newapi-usage",
  };
  await page.unroute("**/v0/management/upstream-monitor/state");
  await page.route(
    "**/v0/management/upstream-monitor/state",
    async (route) => {
      await route.fulfill({
        status: 200,
        contentType: "application/json",
        body: JSON.stringify(state),
      });
    },
  );

  await page.goto(uiURL);
  await page.getByRole("tab", { name: "供应商配置" }).click();
  await page.locator('[data-name-id="cc-1"]').fill("尚未保存的备注");
  await page.locator('[data-pat-id="cc-1"]').fill("尚未保存的PAT");
  await page.getByRole("tab", { name: "账户监控" }).click();
  await page.getByRole("tab", { name: "供应商配置" }).click();

  await expect(page.locator('[data-name-id="cc-1"]')).toHaveValue(
    "尚未保存的备注",
  );
  await expect(page.locator('[data-pat-id="cc-1"]')).toHaveValue(
    "尚未保存的PAT",
  );
});

test("state refresh preserves provider and monitor settings drafts", async ({
  page,
}) => {
  const state = structuredClone(uiState);
  state.revision = 12;
  state.providers[0] = {
    ...state.providers[0],
    adapter: "newapi-usage",
    adapter_override: "newapi-usage",
    management_pat_configured: true,
  };
  state.config.cash_by_account = {
    "cc-1": { warning: "50", critical: "5" },
  };
  await page.unroute("**/v0/management/upstream-monitor/state");
  await page.route(
    "**/v0/management/upstream-monitor/state",
    async (route) => {
      await route.fulfill({
        status: 200,
        contentType: "application/json",
        body: JSON.stringify(state),
      });
    },
  );

  await page.goto(uiURL);
  await page.getByRole("tab", { name: "供应商配置" }).click();
  await page.locator('[data-name-id="cc-1"]').fill("轮询中的备注");
  await page.locator('[data-pat-id="cc-1"]').fill("轮询中的PAT");
  await page.locator('[data-adapter-id="cc-1"]').selectOption("relay-usage");
  await page
    .getByLabel("Command Code GOAT 账户现金注意金额")
    .fill("88");
  await page
    .getByLabel("Command Code GOAT 账户现金严重金额")
    .fill("8");

  await page.getByRole("tab", { name: "监控设置" }).click();
  await page.locator("#settings-warning-percent").fill("35");
  await page.locator("#settings-critical-percent").fill("4");
  await page.getByLabel("CNY 注意金额").fill("70");
  await page.getByLabel("CNY 严重金额").fill("7");

  state.revision = 13;
  state.providers[0] = {
    ...state.providers[0],
    custom_name: "服务端新备注",
    adapter: "sub2api-usage",
    adapter_override: "sub2api-usage",
    management_pat_configured: false,
  };
  state.config.warning_percent = 30;
  state.config.critical_percent = 3;
  state.config.cash_by_account = {
    "cc-1": { warning: "60", critical: "6" },
  };
  state.config.cash_by_currency = {
    CNY: { warning: "60", critical: "6" },
  };
  await page.getByRole("button", { name: "重新加载" }).click();

  await page.getByRole("tab", { name: "供应商配置" }).click();
  await expect(page.locator('[data-name-id="cc-1"]')).toHaveValue(
    "轮询中的备注",
  );
  await expect(page.locator('[data-pat-id="cc-1"]')).toHaveValue("轮询中的PAT");
  await expect(page.locator('[data-adapter-id="cc-1"]')).toHaveValue(
    "relay-usage",
  );
  await expect(
    page.getByLabel("Command Code GOAT 账户现金注意金额"),
  ).toHaveValue("88");
  await expect(
    page.getByLabel("Command Code GOAT 账户现金严重金额"),
  ).toHaveValue("8");

  await page.getByRole("tab", { name: "监控设置" }).click();
  await expect(page.locator("#settings-warning-percent")).toHaveValue("35");
  await expect(page.locator("#settings-critical-percent")).toHaveValue("4");
  await expect(page.getByLabel("CNY 注意金额")).toHaveValue("70");
  await expect(page.getByLabel("CNY 严重金额")).toHaveValue("7");
});

test("changing provider adapter reveals its authentication field immediately", async ({
  page,
}) => {
  await page.goto(uiURL);
  await page.getByRole("tab", { name: "供应商配置" }).click();
  await expect(page.locator('[data-pat-id="cc-1"]')).toHaveCount(0);

  await page.locator('[data-adapter-id="cc-1"]').selectOption("newapi-usage");

  await expect(page.locator('[data-pat-id="cc-1"]')).toBeVisible();
});

test("unload is guarded only while provider drafts are dirty", async ({
  page,
}) => {
  const state = structuredClone(uiState);
  await page.unroute("**/v0/management/upstream-monitor/state");
  await page.route(
    "**/v0/management/upstream-monitor/state",
    async (route) => {
      await route.fulfill({
        status: 200,
        contentType: "application/json",
        body: JSON.stringify(state),
      });
    },
  );

  await page.goto(uiURL);
  await page.getByRole("tab", { name: "供应商配置" }).click();
  const isGuarded = () =>
    page.evaluate(() => {
      const event = new Event("beforeunload", { cancelable: true });
      window.dispatchEvent(event);
      return event.defaultPrevented;
    });

  expect(await isGuarded()).toBe(false);
  await page.locator('[data-name-id="cc-1"]').fill("未保存备注");
  expect(await isGuarded()).toBe(true);
});

test("late history responses never replace the selected account history", async ({
  page,
}) => {
  const state = structuredClone(uiState);
  state.report.accounts = [
    {
      ...state.report.accounts[0],
      id: "account-a",
      name: "账户 A",
      adapter: "newapi-usage",
      windows: [],
      quantities: [],
      kind: "balance",
      balances: [
        { balance_type: "remaining", amount: "12.34", currency: "CNY" },
      ],
    },
    {
      ...state.report.accounts[0],
      id: "account-b",
      name: "账户 B",
      adapter: "newapi-usage",
      windows: [],
      quantities: [],
      kind: "balance",
      balances: [
        { balance_type: "remaining", amount: "56.78", currency: "CNY" },
      ],
    },
  ];
  await page.unroute("**/v0/management/upstream-monitor/state");
  await page.route(
    "**/v0/management/upstream-monitor/state",
    async (route) => {
      await route.fulfill({
        status: 200,
        contentType: "application/json",
        body: JSON.stringify(state),
      });
    },
  );
  await page.route(
    "**/v0/management/upstream-monitor/history?account_id=account-a*",
    async (route) => {
      await new Promise((resolve) => setTimeout(resolve, 350));
      await route.fulfill({
        status: 200,
        contentType: "application/json",
        body: JSON.stringify({
          history: [
            {
              id: "history-a",
              kind: "balance",
              status: "ok",
              checked_at: "2026-09-18T09:00:00Z",
              balances: [
                {
                  balance_type: "remaining",
                  amount: "88.88",
                  currency: "CNY",
                },
              ],
            },
          ],
        }),
      });
    },
  );
  await page.route(
    "**/v0/management/upstream-monitor/history?account_id=account-b*",
    async (route) => {
      await route.fulfill({
        status: 200,
        contentType: "application/json",
        body: JSON.stringify({
          history: [
            {
              id: "history-b",
              kind: "balance",
              status: "ok",
              checked_at: "2026-09-18T10:00:00Z",
              balances: [
                {
                  balance_type: "remaining",
                  amount: "99.99",
                  currency: "CNY",
                },
              ],
            },
          ],
        }),
      });
    },
  );

  await page.goto(uiURL);
  await page.getByRole("tab", { name: "历史" }).click();
  await expect(page.locator(".history-inline-title")).toContainText(
    "账户 A · account-a",
  );
  await expect(page.locator(".history-inline-body")).toContainText(
    "正在加载查询历史…",
  );
  await page.locator('[data-account-id="account-b"] .rail-select').click();

  await expect(page.locator(".history-inline-title")).toContainText(
    "账户 B · account-b",
  );
  await expect(page.locator(".history-inline-body")).toContainText("99.99 CNY");
  await page.waitForTimeout(450);
  await expect(page.locator(".history-inline-body")).not.toContainText(
    "88.88 CNY",
  );
  await expect(page.locator(".history-inline-body")).toContainText("99.99 CNY");
});

test("opening history identifies and focuses the selected account", async ({
  page,
}) => {
  await page.route(
    "**/v0/management/upstream-monitor/history?account_id=cc-1*",
    async (route) => {
      await route.fulfill({
        status: 200,
        contentType: "application/json",
        body: JSON.stringify({ history: [] }),
      });
    },
  );

  await page.goto(uiURL);
  await page.getByRole("button", { name: "最近查询" }).click();

  await expect(page.locator("#history-title")).toContainText(
    "Command Code GOAT · cc-1",
  );
  await expect
    .poll(() => page.evaluate(() => document.activeElement?.id))
    .toBe("history-panel");
});

test("history load failures stay in the selected account view", async ({
  page,
}) => {
  await page.route(
    "**/v0/management/upstream-monitor/history?account_id=cc-1*",
    async (route) => {
      await route.fulfill({
        status: 503,
        contentType: "application/json",
        body: JSON.stringify({ error: "历史服务暂时不可用" }),
      });
    },
  );

  await page.goto(uiURL);
  await page.getByRole("tab", { name: "历史" }).click();

  await expect(page.locator(".history-inline-title")).toContainText(
    "Command Code GOAT · cc-1",
  );
  await expect(page.locator(".history-inline-body")).toContainText(
    "历史服务暂时不可用",
  );
  await expect(page.locator(".history-inline-body")).not.toContainText(
    "暂无查询历史",
  );
});

test("clicking an alert locates its metric and offers filter recovery", async ({
  page,
}) => {
  const state = structuredClone(uiState);
  state.report.accounts = [
    {
      ...state.report.accounts[0],
      id: "account-a",
      name: "账户 A",
      provider: "alpha",
      adapter: "newapi-usage",
      status: "ok",
    },
    {
      ...state.report.accounts[0],
      id: "account-b",
      name: "账户 B",
      provider: "beta",
      adapter: "newapi-usage",
      kind: "period_quota",
      status: "critical",
      windows: [
        {
          name: "weekly",
          remaining_fraction: 0.08,
          used_fraction: 0.92,
          remaining_amount: "8",
          total_amount: "100",
          unit: "credits",
        },
      ],
    },
  ];
  state.report.alerts = [
    {
      id: "alert-b",
      level: "critical",
      account_id: "account-b",
      metric_id: "window",
      window_id: "weekly",
      type: "quota_low",
      message: "一周额度剩余低于阈值",
    },
  ];
  await page.unroute("**/v0/management/upstream-monitor/state");
  await page.route(
    "**/v0/management/upstream-monitor/state",
    async (route) => {
      await route.fulfill({
        status: 200,
        contentType: "application/json",
        body: JSON.stringify(state),
      });
    },
  );

  await page.goto(uiURL);
  await page.getByRole("searchbox", { name: "搜索账户或供应商" }).fill("不存在");
  await page.getByRole("button", { name: "额度", exact: true }).click();
  expect(
    await page
      .getByRole("button", { name: "额度", exact: true })
      .getAttribute("aria-pressed"),
  ).toBe("true");
  await page.getByRole("button", { name: "查看" }).click();

  await expect(page.getByRole("searchbox", { name: "搜索账户或供应商" })).toHaveValue("");
  await expect(page.locator("#detail")).toContainText("账户 B");
  await expect(page.locator("#status-line")).toContainText("已清除筛选");
  await expect(page.getByRole("button", { name: "恢复筛选" })).toBeVisible();
  await expect
    .poll(() =>
      page.evaluate(
        () => document.activeElement?.getAttribute("data-alert-target") || "",
      ),
    )
    .toBe("window:weekly");

  await page.getByRole("button", { name: "恢复筛选" }).click();
  await expect(page.getByRole("searchbox", { name: "搜索账户或供应商" })).toHaveValue(
    "不存在",
  );
  await expect(
    page
      .getByRole("button", { name: "额度", exact: true })
      .getAttribute("aria-pressed"),
  ).resolves.toBe("true");
});

test("clicking an alert for an archived account explains the fallback", async ({
  page,
}) => {
  const state = structuredClone(uiState);
  state.report.alerts = [
    {
      id: "alert-archived",
      level: "warning",
      account_id: "account-archived",
      type: "stale",
      message: "账户快照已过期",
    },
  ];
  await page.unroute("**/v0/management/upstream-monitor/state");
  await page.route(
    "**/v0/management/upstream-monitor/state",
    async (route) => {
      await route.fulfill({
        status: 200,
        contentType: "application/json",
        body: JSON.stringify(state),
      });
    },
  );

  await page.goto(uiURL);
  await page.getByRole("button", { name: "查看" }).click();

  await expect(page.locator("#status-line")).toContainText(
    "该账户已归档或不存在，无法定位告警目标",
  );
  await expect(page.locator("#detail")).not.toContainText("account-archived");
});

test("accounts keep first-seen order until the user chooses another sort", async ({
  page,
}) => {
  const state = structuredClone(uiState);
  state.report.accounts = [
    {
      ...state.report.accounts[0],
      id: "account-first",
      name: "最早发现",
      status: "warning",
    },
    {
      ...state.report.accounts[0],
      id: "account-second",
      name: "后来发现",
      status: "ok",
    },
  ];
  await page.unroute("**/v0/management/upstream-monitor/state");
  await page.route(
    "**/v0/management/upstream-monitor/state",
    async (route) => {
      await route.fulfill({
        status: 200,
        contentType: "application/json",
        body: JSON.stringify(state),
      });
    },
  );

  await page.goto(uiURL);

  await expect(page.locator("#sort")).toHaveValue("stable");
  await expect(page.locator(".rail-name")).toHaveText([
    "最早发现",
    "后来发现",
  ]);
});

test("navigation separates monitor settings from read-only data status", async ({
  page,
}) => {
  await page.goto(uiURL);

  await expect(page.locator('nav[role="tablist"] [role="tab"]')).toHaveText([
    "账户监控",
    "供应商配置",
    "监控设置",
    "数据状态",
  ]);

  await page.getByRole("tab", { name: "监控设置" }).click();
  await expect(
    page.getByRole("heading", { name: "监控设置", exact: true }),
  ).toBeVisible();
  await expect(page.locator("#settings-warning-percent")).toBeEditable();

  await page.getByRole("tab", { name: "数据状态" }).click();
  await expect(
    page.getByRole("heading", { name: "数据状态", exact: true }),
  ).toBeVisible();
  await expect(page.locator("#data-sync-mode")).toContainText("后台轮询");
  await expect(page.locator("#settings-warning-percent")).toHaveCount(0);
});

test("monitor settings save currency cash thresholds", async ({ page }) => {
  let savedBody = null;
  await page.route(
    "**/v0/management/upstream-monitor/config",
    async (route) => {
      if (route.request().method() !== "PUT") return route.fallback();
      savedBody = JSON.parse(route.request().postData() || "{}");
      const state = structuredClone(uiState);
      state.config.cash_by_currency = savedBody.cash_by_currency;
      await route.fulfill({
        status: 200,
        contentType: "application/json",
        body: JSON.stringify(state),
      });
    },
  );

  await page.goto(uiURL);
  await page.getByRole("tab", { name: "监控设置" }).click();
  await page.getByLabel("CNY 注意金额").fill("10");
  await page.getByLabel("CNY 严重金额").fill("1");
  await page.getByRole("button", { name: "保存监控设置" }).click();

  await expect
    .poll(() => savedBody?.cash_by_currency?.CNY?.warning)
    .toBe("10");
  expect(savedBody?.cash_by_currency?.CNY?.critical).toBe("1");
});

test("provider save includes an account cash threshold override", async ({
  page,
}) => {
  const state = structuredClone(uiState);
  state.revision = 12;
  state.config.cash_by_account = {
    "cc-1": { warning: "10", critical: "1" },
  };
  let savedBody = null;
  await page.unroute("**/v0/management/upstream-monitor/state");
  await page.route(
    "**/v0/management/upstream-monitor/state",
    async (route) => {
      await route.fulfill({
        status: 200,
        contentType: "application/json",
        body: JSON.stringify(state),
      });
    },
  );
  await page.route(
    "**/v0/management/upstream-monitor/providers",
    async (route) => {
      savedBody = route.request().postDataJSON();
      state.revision = 13;
      state.config.cash_by_account["cc-1"] = savedBody.providers[0].cash_threshold;
      await route.fulfill({
        status: 200,
        contentType: "application/json",
        body: JSON.stringify(state),
      });
    },
  );

  await page.goto(uiURL);
  await page.getByRole("tab", { name: "供应商配置" }).click();
  await page.getByLabel(/账户现金注意金额/).fill("20");
  await page.getByLabel(/账户现金严重金额/).fill("2");
  await page.getByRole("button", { name: "保存监控配置" }).click();

  await expect.poll(() => savedBody !== null).toBe(true);
  expect(savedBody.providers[0].cash_threshold).toEqual({
    warning: "20",
    critical: "2",
  });
  expect(savedBody.providers[0].clear_cash_threshold).toBeUndefined();
});

test("provider save failure preserves every edited draft field", async ({
  page,
}) => {
  const state = structuredClone(uiState);
  state.revision = 12;
  state.providers[0] = {
    ...state.providers[0],
    adapter: "newapi-usage",
    adapter_override: "newapi-usage",
    management_pat_configured: true,
  };
  state.config.cash_by_account = {
    "cc-1": { warning: "10", critical: "1" },
  };
  await page.unroute("**/v0/management/upstream-monitor/state");
  await page.route(
    "**/v0/management/upstream-monitor/state",
    async (route) => {
      await route.fulfill({
        status: 200,
        contentType: "application/json",
        body: JSON.stringify(state),
      });
    },
  );
  await page.route(
    "**/v0/management/upstream-monitor/providers",
    async (route) => {
      await route.fulfill({
        status: 500,
        contentType: "application/json",
        body: JSON.stringify({ error: "failed to persist provider config" }),
      });
    },
  );

  await page.goto(uiURL);
  await page.getByRole("tab", { name: "供应商配置" }).click();
  const monitored = page.locator('[data-provider-id="cc-1"]');
  const draftName = page.locator('[data-name-id="cc-1"]');
  const draftPat = page.locator('[data-pat-id="cc-1"]');
  const draftAdapter = page.locator('[data-adapter-id="cc-1"]');
  const warning = page.getByLabel("Command Code GOAT 账户现金注意金额");
  const critical = page.getByLabel("Command Code GOAT 账户现金严重金额");

  await monitored.uncheck();
  await draftName.fill("保存失败时保留的备注");
  await draftPat.fill("保存失败时保留的PAT");
  await draftAdapter.selectOption("relay-usage");
  await warning.fill("25");
  await critical.fill("2.5");
  await page.getByRole("button", { name: "保存监控配置" }).click();

  await expect(page.locator("#status-line")).toContainText(
    "failed to persist provider config",
  );
  await expect(monitored).not.toBeChecked();
  await expect(draftName).toHaveValue("保存失败时保留的备注");
  await expect(draftPat).toHaveValue("保存失败时保留的PAT");
  await expect(draftAdapter).toHaveValue("relay-usage");
  await expect(warning).toHaveValue("25");
  await expect(critical).toHaveValue("2.5");
});

test("clearing an existing account cash override sends an explicit clear flag", async ({
  page,
}) => {
  const state = structuredClone(uiState);
  state.revision = 12;
  state.providers[0] = {
    ...state.providers[0],
    adapter: "newapi-usage",
    adapter_override: "newapi-usage",
  };
  state.config.cash_by_account = {
    "cc-1": { warning: "10", critical: "1" },
  };
  let savedBody = null;
  await page.unroute("**/v0/management/upstream-monitor/state");
  await page.route(
    "**/v0/management/upstream-monitor/state",
    async (route) => {
      await route.fulfill({
        status: 200,
        contentType: "application/json",
        body: JSON.stringify(state),
      });
    },
  );
  await page.route(
    "**/v0/management/upstream-monitor/providers",
    async (route) => {
      savedBody = route.request().postDataJSON();
      const savedState = structuredClone(state);
      delete savedState.config.cash_by_account["cc-1"];
      savedState.revision = 13;
      await route.fulfill({
        status: 200,
        contentType: "application/json",
        body: JSON.stringify(savedState),
      });
    },
  );

  await page.goto(uiURL);
  await page.getByRole("tab", { name: "供应商配置" }).click();
  await page
    .getByRole("button", {
      name: "清除 Command Code GOAT 账户现金阈值覆盖",
    })
    .click();
  await expect(
    page.getByLabel("Command Code GOAT 账户现金注意金额"),
  ).toHaveValue("");
  await page.getByRole("button", { name: "保存监控配置" }).click();

  await expect.poll(() => savedBody !== null).toBe(true);
  expect(savedBody.providers[0].clear_cash_threshold).toBe(true);
  expect(savedBody.providers[0].cash_threshold).toBeUndefined();
});

test("account details keep current quota visible while grouped views expose billing usage and diagnostics", async ({
  page,
}) => {
  const state = structuredClone(uiState);
  state.report.accounts = [
    {
      id: "account-grouped",
      name: "分组账户",
      provider: "newapi",
      adapter: "newapi-usage",
      base_url: "https://newapi.example.com",
      kind: "quota",
      status: "warning",
      capabilities: ["quota", "billing", "usage_statistics"],
      checked_at: "2026-09-18T08:00:00Z",
      last_success_at: "2026-09-18T08:00:00Z",
      latency_ms: 125,
      stale: false,
      quantities: [
        {
          name: "accountQuota",
          scope: "account",
          remaining: "70",
          total: "100",
          used: "30",
          unit: "TOKENS",
        },
      ],
      windows: [],
      balances: [],
      details: {
        mode: "quota",
        planName: "团队套餐",
        unit: "TOKENS",
        isValid: true,
        expires_at: "2026-12-31T00:00:00Z",
        rate_limits: [
          { window: "minute", limit: 60, used: 12, remaining: 48, unit: "requests" },
        ],
        subscription: {
          currentPeriodStart: "2026-09-01T00:00:00Z",
          currentPeriodEnd: "2026-10-01T00:00:00Z",
        },
        billing: {
          effective_rate_multiplier: 0.8,
          rpm: 60,
          tpm: 120000,
        },
        usage: {
          total_tokens: 12345,
          cost: "1.2345",
          date_range: "2026-09-01 至 2026-09-18",
        },
        daily_usage: [
          {
            date: "2026-09-18",
            requests: 3,
            total_tokens: 300,
            cost: "0.0042",
          },
        ],
        model_stats: [
          {
            model: "gpt-test",
            requests: 3,
            total_tokens: 300,
            cost: "0.0042",
          },
        ],
        account: {
          userName: "tester",
          scope: "personal",
        },
      },
      sections: {
        billing: { status: "error", message: "计费接口暂时不可用" },
      },
      warnings: ["统计口径按 UTC"],
    },
  ];
  state.report.alerts = [];
  await page.unroute("**/v0/management/upstream-monitor/state");
  await page.route(
    "**/v0/management/upstream-monitor/state",
    async (route) => {
      await route.fulfill({
        status: 200,
        contentType: "application/json",
        body: JSON.stringify(state),
      });
    },
  );

  await page.goto(uiURL);

  await expect(page.locator("#detail [role='tab']")).toHaveText([
    "计费与限制",
    "用量统计",
    "历史",
    "诊断",
  ]);
  await expect(page.locator("#detail")).toContainText("70 / 100 TOKENS");
  await expect(page.locator("#detail")).toContainText("配额明细");
  await expect(page.locator("#detail")).not.toContainText("Token 用量与费用");
  await expect(page.locator("#detail")).not.toContainText("计费倍率");
  await expect(page.locator("#detail")).not.toContainText("查询来源");

  await page.getByRole("tab", { name: "计费与限制" }).click();
  await expect(page.locator("#detail")).toContainText("速率限制");
  await expect(page.locator("#detail")).toContainText("计费倍率");
  await expect(page.locator("#detail")).toContainText("0.8");
  await expect(page.locator("#detail")).not.toContainText("Token 用量与费用");

  await page.getByRole("tab", { name: "用量统计" }).click();
  await expect(page.locator("#detail")).toContainText("Token 用量与费用");
  await expect(page.locator("#detail")).toContainText("每日用量");
  await expect(page.locator("#detail")).toContainText("模型统计");
  await expect(page.locator("#detail")).toContainText("0.0042");
  await expect(page.locator("#detail")).not.toContainText("计费倍率");

  await page.getByRole("tab", { name: "诊断" }).click();
  await expect(page.locator("#detail")).toContainText("查询来源");
  await expect(page.locator("#detail")).toContainText("原始字段");
  await expect(page.locator("#detail")).toContainText("用户：tester");
  await expect(page.locator("#detail")).toContainText("计费接口暂时不可用");
  await expect(page.locator("#detail")).toContainText("统计口径按 UTC");
});

test("clearing an account snapshot requires explicit confirmation", async ({
  page,
}) => {
  const cleanupRequests = [];
  await page.route(
    "**/v0/management/upstream-monitor/cleanup",
    async (route) => {
      cleanupRequests.push(route.request().postDataJSON());
      await route.fulfill({
        status: 200,
        contentType: "application/json",
        body: JSON.stringify(uiState.report),
      });
    },
  );

  await page.goto(uiURL);
  await page.getByRole("tab", { name: "供应商配置" }).click();
  await page.getByRole("button", { name: "清除 Command Code GOAT 记录" }).click();

  const confirmation = page.getByRole("alertdialog", {
    name: "确认清除监控记录",
  });
  await expect(confirmation).toBeVisible();
  await expect(confirmation).toContainText("Command Code GOAT");
  await expect(confirmation).toContainText("已保存快照和查询历史");
  expect(cleanupRequests).toHaveLength(0);

  await page.getByRole("button", { name: "取消清除" }).click();
  await expect(confirmation).toBeHidden();
  expect(cleanupRequests).toHaveLength(0);

  await page.getByRole("button", { name: "清除 Command Code GOAT 记录" }).click();
  await page.getByRole("button", { name: "确认清除记录" }).click();

  await expect.poll(() => cleanupRequests.length).toBe(1);
  expect(cleanupRequests[0]).toEqual({ ids: ["cc-1"] });
});

for (const width of [390, 768]) {
  test(`provider editing stays usable without horizontal scrolling at ${width}px`, async ({
    page,
  }) => {
    await page.setViewportSize({ width, height: 900 });
    await page.goto(uiURL);
    await page.getByRole("tab", { name: "供应商配置" }).click();

    await expect(
      page.getByRole("button", { name: "编辑 Command Code GOAT" }),
    ).toBeVisible();
    await page
      .getByRole("button", { name: "编辑 Command Code GOAT" })
      .click();
    await page.getByLabel("备注名称 Command Code GOAT").fill("窄屏备注");
    await page
      .getByLabel("查询方式 Command Code GOAT")
      .selectOption("newapi-usage");

    await expect(
      page.getByLabel("NewAPI 管理 PAT Command Code GOAT"),
    ).toBeVisible();
    await expect(
      page.getByRole("button", { name: "保存监控配置" }),
    ).toBeVisible();
    const horizontalOverflow = await page.evaluate(
      () => document.documentElement.scrollWidth - window.innerWidth,
    );
    expect(horizontalOverflow).toBeLessThanOrEqual(1);
  });
}

for (const viewport of [
  { name: "desktop", width: 1440, height: 1100 },
  { name: "tablet", width: 768, height: 1024 },
  { name: "mobile", width: 390, height: 900 },
]) {
  test(`Command Code layout renders on ${viewport.name}`, async ({ page }) => {
    await page.setViewportSize(viewport);
    await page.goto(uiURL);
    await expect(page.locator(".hero-label")).toHaveText("5 小时额度");
    await expect(page.locator(".hero-value")).toHaveText("11.47 / 14 credits");
    await expect(page.locator(".hero-percent")).toHaveText("81.9% 剩余");

    let bodyText = await page.locator("body").innerText();
    for (const text of [
      "窗口额度",
      "套餐额度",
      "5 小时",
      "一周",
      "当前仅有套餐月额度，没有额外的已购或赠送 Credits。",
      "Credits 状态",
    ]) {
      expect(bodyText).toContain(text);
    }
    expect(bodyText.indexOf("窗口额度")).toBeLessThan(
      bodyText.indexOf("套餐额度"),
    );
    expect(bodyText).toContain("30 / 35 credits");
    expect(bodyText).not.toContain("一月");
    expect(bodyText).not.toContain("本周期用量");

    await page.getByRole("tab", { name: "用量统计" }).click();
    await expect(page.locator("#detail")).toContainText("本周期用量");

    await page.getByRole("tab", { name: "诊断" }).click();
    bodyText = await page.locator("#detail").innerText();
    for (const text of [
      "组织限制",
      "账户与订阅",
      "个人账户",
      "当前为个人账户，不包含组织限制。",
    ]) {
      expect(bodyText).toContain(text);
    }

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

test("keyboard navigation exposes a visible focus ring in management controls", async ({
  page,
}) => {
  await page.goto(uiURL);

  const seen = new Set();
  for (let index = 0; index < 8; index += 1) {
    await page.keyboard.press("Tab");
    const state = await page.evaluate(() => {
      const active = document.activeElement;
      if (!(active instanceof HTMLElement)) return null;
      const style = getComputedStyle(active);
      return {
        control:
          active.id ||
          active.getAttribute("aria-label") ||
          active.textContent?.trim() ||
          active.tagName,
        focusVisible: active.matches(":focus-visible"),
        outlineStyle: style.outlineStyle,
        outlineWidth: style.outlineWidth,
      };
    });
    expect(state).not.toBeNull();
    expect(state.control).toBeTruthy();
    expect(state.focusVisible).toBe(true);
    expect(state.outlineStyle).not.toBe("none");
    expect(state.outlineWidth).not.toBe("0px");
    seen.add(state.control);
  }
  expect(seen.size).toBeGreaterThanOrEqual(6);
});

for (const theme of ["white", "dark"]) {
  test(`CPAMP iframe embedding uses the ${theme} parent theme`, async ({
    page,
  }) => {
    await page.setViewportSize({ width: 768, height: 1024 });
    await page.route("**/__upstream-monitor-iframe-harness", async (route) => {
      await route.fulfill({
        status: 200,
        contentType: "text/html",
        body: `<!doctype html><html data-theme="${theme}"><head><meta charset="utf-8"><style>html,body{margin:0;height:100%}iframe{display:block;width:100%;height:100%;border:0}</style></head><body><iframe id="monitor" src="/ui.html"></iframe></body></html>`,
      });
    });

    await page.goto(iframeHarnessURL);
    const monitor = page.frameLocator("#monitor");
    await expect(monitor.locator(".brand")).toContainText("上游账户监控");
    await expect(monitor.locator(".hero-value")).toHaveText(
      "11.47 / 14 credits",
    );
    await expect
      .poll(() =>
        monitor.locator("html").evaluate((node) => node.dataset.theme),
      )
      .toBe(theme);

    await monitor.getByRole("tab", { name: "数据状态" }).click();
    await expect(
      monitor.getByRole("heading", { name: "数据状态", exact: true }),
    ).toBeVisible();

    const background = await monitor
      .locator("body")
      .evaluate((node) => getComputedStyle(node).backgroundColor);
    expect(background).toBe(theme === "dark" ? "rgb(21, 23, 27)" : "rgb(245, 246, 248)");
    await page.screenshot({
      path: `/tmp/upstream-monitor-iframe-${theme}.png`,
      fullPage: true,
    });
  });
}
