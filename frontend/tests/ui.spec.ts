import { expect, test, type Page } from "@playwright/test";
import { analytics, mockAPI, config } from "./fixtures";

async function navigate(page: Page, name: string) {
  await expect(page.locator(".app-shell")).toBeVisible();
  if (await page.getByRole("button", { name: "打开导航" }).isVisible()) {
    await page.getByRole("button", { name: "打开导航" }).click();
  }
  await page
    .getByRole("navigation", { name: "主导航" })
    .getByRole("button", { name, exact: true })
    .click();
  await expect(page.getByRole("heading", { name, level: 1 })).toBeVisible();
  await expect(
    page
      .getByRole("navigation", { name: "主导航", includeHidden: true })
      .getByRole("button", { name, exact: true, includeHidden: true }),
  ).toBeEnabled();
}
async function checkCanvas(page: Page) {
  await expect(page.locator(".trend-chart canvas")).toBeVisible();
  await expect
    .poll(() =>
      page
        .locator(".trend-chart canvas")
        .evaluate((canvas: HTMLCanvasElement) => {
          const data = canvas
            .getContext("2d")!
            .getImageData(0, 0, canvas.width, canvas.height).data;
          let count = 0;
          for (let i = 0; i < data.length; i += 4) {
            if (
              data[i + 3] > 40 &&
              Math.max(data[i], data[i + 1], data[i + 2]) -
                Math.min(data[i], data[i + 1], data[i + 2]) >
                45
            )
              count++;
          }
          return count;
        }),
    )
    .toBeGreaterThan(100);
}
test.beforeEach(async ({ page }) => {
  await mockAPI(page);
});

test("analytics filters, chart modes, hover, zoom and image download", async ({
  page,
}, testInfo) => {
  const errors: string[] = [];
  page.on("pageerror", (e) => errors.push(e.message));
  await page.goto("/");
  await navigate(page, "数据分析");
  await checkCanvas(page);
  await page.screenshot({
    path: testInfo.outputPath("analytics-desktop.png"),
    fullPage: true,
  });
  const canvas = page.locator(".trend-chart canvas");
  const beforeHover = await canvas.screenshot();
  await canvas.hover({ position: { x: 250, y: 130 } });
  expect((await canvas.screenshot()).equals(beforeHover)).toBe(false);
  const bounds = await canvas.boundingBox();
  await page.mouse.move(bounds!.x + 15, bounds!.y + bounds!.height - 8);
  await page.mouse.down();
  await page.mouse.move(
    bounds!.x + bounds!.width * 0.3,
    bounds!.y + bounds!.height - 8,
    { steps: 8 },
  );
  await page.mouse.up();
  await checkCanvas(page);
  await page.getByRole("button", { name: "柱状图", exact: true }).click();
  await expect(
    page.getByRole("button", { name: "柱状图", exact: true }),
  ).toHaveAttribute("aria-pressed", "true");
  await checkCanvas(page);
  const downloadEvent = page.waitForEvent("download");
  await page.getByRole("button", { name: "下载趋势图" }).click();
  expect((await downloadEvent).suggestedFilename()).toMatch(/\.png$/);
  for (const metric of ["平均耗时", "输出速度", "已知费用", "Token 用量"]) {
    await page
      .locator(".analytics-metrics")
      .getByRole("button", { name: new RegExp(metric) })
      .click();
    await expect(
      page.getByRole("heading", { name: metric + "趋势" }),
    ).toBeVisible();
    await checkCanvas(page);
  }
  const request = page.waitForRequest(
    (req) =>
      req.url().includes("/admin/analytics?") &&
      new URL(req.url()).searchParams.get("range") === "7d",
  );
  await page.getByRole("button", { name: "7 天", exact: true }).click();
  await request;
  await checkCanvas(page);
  const source = page.waitForRequest(
    (req) =>
      req.url().includes("/admin/analytics?") &&
      new URL(req.url()).searchParams.get("kind") === "test",
  );
  await page.getByLabel("来源", { exact: true }).selectOption("test");
  await source;
  await checkCanvas(page);
  await page
    .locator(".model-comparison")
    .getByRole("button", { name: "grok-4-fast", exact: true })
    .click();
  await expect(page.locator(".model-comparison tbody tr")).toHaveCount(1);
  await expect(page.getByLabel("模型", { exact: true })).toHaveValue(
    "grok-4-fast",
  );
  await page.getByRole("button", { name: "全部模型", exact: true }).click();
  await expect(page.locator(".model-comparison tbody tr")).toHaveCount(4);
  await page.locator(".time-details summary").click();
  await expect(page.locator(".time-details tbody tr")).toHaveCount(24);
  expect(errors).toEqual([]);
});

test("custom ranges use Beijing time and reject invalid spans", async ({
  page,
}) => {
  await page.goto("/");
  await navigate(page, "数据分析");
  await checkCanvas(page);
  await page.getByRole("button", { name: "自定义", exact: true }).click();
  await page.getByLabel("开始时间（北京时间）").fill("2026-09-12T10:00");
  await page.getByLabel("结束时间（北京时间）").fill("2026-09-12T09:00");
  await page.getByRole("button", { name: "应用范围" }).click();
  await expect(page.getByRole("alert")).toContainText("结束须晚于开始");
  await page.getByLabel("结束时间（北京时间）").fill("2026-09-13T10:00");
  const request = page.waitForRequest(
    (req) =>
      req.url().includes("/admin/analytics?") &&
      new URL(req.url()).searchParams.get("range") === "custom",
  );
  await page.getByRole("button", { name: "应用范围" }).click();
  const url = new URL((await request).url());
  expect(url.searchParams.get("from")).toBe("2026-09-12T02:00:00.000Z");
  expect(url.searchParams.get("to")).toBe("2026-09-13T02:00:00.000Z");
  await expect(page.getByRole("alert")).toHaveCount(0);
});

test("empty, missing latency, failed reload and retry stay truthful", async ({
  page,
}) => {
  let state = "empty";
  await page.route("**/admin/analytics?*", (route) =>
    state === "error"
      ? route.fulfill({
          status: 500,
          json: { error: { message: "分析数据暂时不可用" } },
        })
      : route.fulfill({
          json: analytics(new URL(route.request().url()), state),
        }),
  );
  await page.goto("/");
  await navigate(page, "数据分析");
  await expect(page.getByText("暂无调用数据", { exact: true })).toBeVisible();
  await expect(page.getByRole("button", { name: "下载趋势图" })).toBeDisabled();
  state = "no-latency";
  await page.getByRole("button", { name: "刷新数据" }).click();
  await checkCanvas(page);
  await page
    .locator(".analytics-metrics")
    .getByRole("button", { name: /平均耗时/ })
    .click();
  await expect(page.getByText("暂无耗时样本", { exact: true })).toBeVisible();
  await expect(
    page.locator(".model-comparison tbody tr").first(),
  ).toContainText("—");
  state = "error";
  await page.getByRole("button", { name: "刷新数据" }).click();
  await expect(page.getByRole("alert")).toContainText("分析数据暂时不可用");
  await expect(page.locator(".analytics-metrics")).toHaveCount(0);
  state = "populated";
  await page.getByRole("button", { name: "重试" }).click();
  await checkCanvas(page);
});

for (const width of [1440, 768, 390, 320]) {
  test("all workspaces fit viewport " + width, async ({ page }, testInfo) => {
    const errors: string[] = [];
    page.on("pageerror", (e) => errors.push(e.message));
    await page.setViewportSize({ width, height: width < 768 ? 844 : 1000 });
    await page.goto("/");
    for (const name of [
      "运行概览",
      "审核策略",
      "审核模型",
      "连接密钥",
      "访问密钥",
      "审核记录",
      "成本与预算",
      "系统设置",
      "审核评测",
      "数据分析",
    ]) {
      await navigate(page, name);
      if (name === "数据分析") await checkCanvas(page);
      const overflow = await page.evaluate(
        () => document.documentElement.scrollWidth - window.innerWidth,
      );
      expect(overflow, name).toBeLessThanOrEqual(1);
      await page.screenshot({
        path: testInfo.outputPath(name + ".png"),
        fullPage: true,
      });
    }
    if (width < 768) {
      await page.getByRole("button", { name: "打开导航" }).click();
      await expect(
        page.getByRole("navigation", { name: "主导航" }),
      ).toBeVisible();
      await page
        .getByRole("navigation")
        .getByRole("button", { name: "数据分析", exact: true })
        .focus();
      await page.keyboard.press("Escape");
      await expect(
        page.getByRole("navigation", { name: "主导航" }),
      ).toBeHidden();
      await expect(
        page.getByRole("button", { name: "打开导航" }),
      ).toBeFocused();
    }
    expect(errors).toEqual([]);
  });
}

for (const width of [1440, 390]) {
  test(`keyword ignore editing, trial and records at ${width}px`, async ({
    page,
  }, testInfo) => {
    await page.setViewportSize({ width, height: 1000 });
    const zeroCost = {
      status: "zero",
      amount_cny: "0",
      reserved_cny: "0",
      period: "",
      note: "关键词忽略，未调用模型",
    };
    const usage = {
      reported: true,
      prompt_tokens: 0,
      completion_tokens: 0,
      total_tokens: 0,
    };
    const ignored = {
      id: "ignored-1",
      kind: "production",
      policy_id: "policy-1",
      client_id: "client-1",
      model: "",
      channel_id: "",
      keyword_ignored: true,
      flagged: false,
      confidence: null,
      threshold: 0,
      reason: "关键词忽略，未调用模型",
      error_code: "",
      latency_ms: 1,
      attempt_count: 0,
      attempts: [],
      usage,
      cost: zeroCost,
      input_stored: false,
      created_at: "2026-09-13T04:30:00Z",
    };
    let saved: Record<string, unknown> | undefined;
    await page.route("**/admin/policies/policy-1/config", async (route) => {
      saved = route.request().postDataJSON().config;
      await route.fulfill({
        json: {
          id: "policy-1",
          name: "默认内容审核",
          alias: "abuse-audit-v1",
          enabled: true,
          revision: 2,
          config: saved,
        },
      });
    });
    await page.route("**/admin/policies/policy-1/test", async (route) => {
      expect(route.request().postDataJSON().config.ignore_keywords).toEqual([
        "指定短语",
        "Allow",
      ]);
      await route.fulfill({
        json: {
          id: "ignored-1",
          model: "abuse-audit-v1",
          actual_model: "",
          channel_id: "",
          attempt_count: 0,
          latency_ms: 1,
          usage,
          cost: zeroCost,
          results: [
            {
              flagged: false,
              category_scores: { illicit: 0 },
              audit: {
                keyword_ignored: true,
                confidence: 0,
                threshold: 0,
                reason: ignored.reason,
              },
            },
          ],
        },
      });
    });
    await page.route("**/admin/audit-logs?**", (route) =>
      route.fulfill({ json: { items: [ignored], total: 1 } }),
    );
    await page.route("**/admin/audit-logs/ignored-1", (route) =>
      route.fulfill({ json: ignored }),
    );
    await page.goto("/");
    await page.getByLabel("关键词忽略", { exact: true }).check();
    const phrases = page.getByLabel(
      "忽略关键词或短语（每行一条，区分大小写）",
      { exact: true },
    );
    await phrases.fill("  指定短语  \n\nAllow\n指定短语\n");
    await expect(
      page.getByRole("button", { name: "保存并生效" }),
    ).toBeEnabled();
    await page.getByRole("button", { name: "保存并生效" }).click();
    await expect(
      page.getByRole("button", { name: "保存并生效" }),
    ).toBeDisabled();
    expect(saved?.keyword_ignore_enabled).toBe(true);
    expect(saved?.ignore_keywords).toEqual(["指定短语", "Allow"]);
    await expect(phrases).toHaveValue("指定短语\nAllow");
    await page.screenshot({
      path: testInfo.outputPath("keyword-policy.png"),
      fullPage: true,
    });
    expect(
      await page.evaluate(
        () => document.documentElement.scrollWidth <= innerWidth,
      ),
    ).toBe(true);
    await page.getByRole("tab", { name: "审核试跑" }).click();
    await page
      .getByLabel("待审核内容", { exact: true })
      .fill("前文指定短语后文");
    await page.getByRole("button", { name: "运行审核", exact: true }).click();
    await expect(
      page.getByText("关键词忽略 · 未命中", { exact: true }),
    ).toBeVisible();
    await expect(page.locator(".test-result")).toContainText("实际调用 0 次");
    await navigate(page, "审核记录");
    const filterRequest = page.waitForRequest(
      (req) =>
        req.url().includes("/admin/audit-logs?") &&
        new URL(req.url()).searchParams.get("keyword_ignore") === "only",
    );
    await page
      .getByRole("combobox", { name: "关键词忽略", exact: true })
      .selectOption("only");
    await page.getByRole("button", { name: "筛选", exact: true }).click();
    await filterRequest;
    await expect(page.locator("tbody")).toContainText("关键词忽略");
    await page.getByRole("button", { name: "详情", exact: true }).click();
    await expect(page.getByRole("dialog")).toContainText("关键词忽略");
    await expect(page.getByRole("dialog")).toContainText("未调用模型");
    await expect(page.getByRole("dialog")).toContainText("¥0");
    await expect(page.getByRole("dialog")).not.toContainText(
      "模型原始输出未保存",
    );
    await page.screenshot({
      path: testInfo.outputPath("keyword-record.png"),
      fullPage: true,
    });
  });
}

test("model channel RPM can be created and edited", async ({ page }) => {
  let created: Record<string, unknown> | undefined;
  let updated: Record<string, unknown> | undefined;
  await page.route("**/admin/model-channels", async (route) => {
    if (route.request().method() !== "POST") return route.fallback();
    created = route.request().postDataJSON();
    await route.fulfill({ json: { id: "new-channel", ...created } });
  });
  await page.route("**/admin/model-channels/channel-1", async (route) => {
    updated = route.request().postDataJSON();
    await route.fulfill({ json: { id: "channel-1", ...updated } });
  });
  await page.goto("/");
  await navigate(page, "审核模型");
  const rpm = page.getByRole("spinbutton", { name: "RPM（每分钟调用上限）" });
  await expect(rpm).toHaveValue("0");
  await page.getByLabel("通道名称", { exact: true }).fill("RPM 测试通道");
  await page
    .getByRole("combobox", { name: "连接密钥", exact: true })
    .selectOption("credential-1");
  await rpm.fill("120");
  await page.getByRole("button", { name: "保存通道", exact: true }).click();
  await expect(rpm).toHaveValue("0");
  expect(created?.rpm).toBe(120);
  await page
    .getByRole("button", { name: "编辑 DeepSeek 主用", exact: true })
    .click();
  await expect(rpm).toHaveValue("0");
  await rpm.fill("30");
  await page.getByRole("button", { name: "保存通道", exact: true }).click();
  await expect(
    page.getByRole("heading", { name: "新增模型通道", exact: true }),
  ).toBeVisible();
  expect(updated?.rpm).toBe(30);
  expect(updated?.expected_revision).toBe(1);
});

test("model channel test shows endpoint, detailed failures, recovery and request errors", async ({
  page,
}) => {
  let calls = 0;
  let release: (() => void) | undefined;
  const gate = new Promise<void>((resolve) => {
    release = resolve;
  });
  await page.route("**/admin/model-channels/channel-1/test", async (route) => {
    expect(route.request().method()).toBe("POST");
    expect(route.request().postDataJSON()).toEqual({ expected_revision: 1 });
    calls++;
    if (calls === 1) await gate;
    if (calls === 3)
      return route.fulfill({
        status: 409,
        json: { error: { message: "配置已被更新，请重新加载后保存" } },
      });
    return route.fulfill({
      json: {
        ok: calls === 2,
        attempted: true,
        channel_id: "channel-1",
        channel_name: "DeepSeek 主用",
        endpoint: "https://api.cline.bot/api/v1/chat/completions",
        api_format: "chat_completions",
        model: "~deepseek/deepseek-v4-flash-latest",
        timeout_ms: 4000,
        max_tokens: 512,
        latency_ms: 830,
        http_status: calls === 1 ? 404 : 200,
        error_code: calls === 1 ? "upstream_config_invalid" : undefined,
        error_message:
          calls === 1
            ? "模型名称或参数不受支持（HTTP 404：Model not found）"
            : undefined,
        hint: calls === 1 ? "检查实际请求地址和接口类型" : undefined,
        assessment:
          calls === 2 ? { confidence: 0.1, reason: "正常测试文本" } : undefined,
        model_output:
          calls === 2
            ? '{"confidence":0.1,"reason":"正常测试文本"}'
            : undefined,
        usage: { reported: true, total_tokens: 30 },
      },
    });
  });
  await page.goto("/");
  await navigate(page, "审核模型");
  const button = page.getByRole("button", {
    name: "测试 DeepSeek 主用",
    exact: true,
  });
  await button.click();
  await expect(button).toBeDisabled();
  const result = page.getByRole("region", { name: "模型测试结果" });
  await expect(result).toContainText("测试中");
  release!();
  await expect(result).toContainText("测试失败");
  await expect(result).toContainText(
    "https://api.cline.bot/api/v1/chat/completions",
  );
  await expect(result).toContainText("~deepseek/deepseek-v4-flash-latest");
  await expect(result).toContainText("HTTP 404");
  await expect(result).toContainText("Model not found");
  await expect(result).toContainText("检查实际请求地址和接口类型");
  await button.click();
  await expect(result).toContainText("测试成功");
  await expect(result).toContainText("正常测试文本");
  await expect(result).not.toContainText("Model not found");
  await result.getByText("查看模型输出", { exact: true }).click();
  await expect(result.locator("pre")).toContainText('"confidence":0.1');
  await button.click();
  await expect(result).toContainText("配置已被更新");
  await expect(result).not.toContainText("测试成功");
  expect(calls).toBe(3);
});

test("policy cooldown settings show legacy defaults and persist edits", async ({
  page,
}) => {
  let saved: Record<string, unknown> | undefined;
  await page.route("**/admin/policies/policy-1/config", async (route) => {
    saved = route.request().postDataJSON().config;
    await route.fulfill({
      json: {
        id: "policy-1",
        name: "默认内容审核",
        alias: "abuse-audit-v1",
        enabled: true,
        revision: 2,
        config: saved,
      },
    });
  });
  await page.goto("/");
  await page.getByRole("tab", { name: "模型调度" }).click();
  const threshold = page.getByRole("spinbutton", { name: "连续失败次数" });
  const cooldown = page.getByRole("spinbutton", {
    name: "失败冷却时间（分钟）",
  });
  await expect(threshold).toHaveValue("3");
  await expect(cooldown).toHaveValue("30");
  await expect(page.getByRole("button", { name: "保存并生效" })).toBeDisabled();
  await threshold.fill("5");
  await cooldown.fill("45");
  await page.getByRole("button", { name: "保存并生效" }).click();
  await expect(page.getByRole("button", { name: "保存并生效" })).toBeDisabled();
  expect(saved?.failure_threshold).toBe(5);
  expect(saved?.failure_cooldown_minutes).toBe(45);
  await expect(threshold).toHaveValue("5");
  await expect(cooldown).toHaveValue("45");
});

test("policy edits and billing forms preserve actions", async ({
  page,
}, testInfo) => {
  await page.goto("/");
  await expect(page.getByRole("button", { name: "保存并生效" })).toBeDisabled();
  await page.getByLabel("策略名称", { exact: true }).fill("审核策略新名称");
  await expect(page.getByRole("button", { name: "保存并生效" })).toBeEnabled();
  await expect(page.getByRole("switch")).toBeDisabled();
  await page.getByRole("tab", { name: "模型调度" }).click();
  await expect(page.getByRole("spinbutton", { name: "优先级" })).toHaveValue(
    "1",
  );
  await page.getByRole("tab", { name: "审核试跑" }).click();
  await expect(page.getByRole("button", { name: "运行审核" })).toBeDisabled();
  await page.getByLabel("待审核内容").fill("待审核样本");
  await expect(page.getByRole("button", { name: "运行审核" })).toBeEnabled();
  await navigate(page, "成本与预算");
  await page.getByRole("tab", { name: "调用方预算" }).click();
  await expect(page.getByRole("progressbar")).toHaveAttribute(
    "aria-valuenow",
    "24.406",
  );
  await page.screenshot({
    path: testInfo.outputPath("budgets.png"),
    fullPage: true,
  });
  await page.getByRole("tab", { name: "模型单价" }).click();
  await page.getByRole("button", { name: "添加模型单价" }).click();
  await expect(
    page.getByRole("dialog", { name: "设置模型单价" }),
  ).toBeVisible();
  await page.screenshot({
    path: testInfo.outputPath("price-dialog.png"),
    fullPage: true,
  });
  await page.getByRole("button", { name: "关闭", exact: true }).click();
  await expect(page.getByRole("dialog")).toHaveCount(0);
});

test("small costs retain decimals and isolated latency samples remain visible", async ({
  page,
}) => {
  await page.addInitScript(() => {
    const state = window as typeof window & { chartText: string[] };
    state.chartText = [];
    const fillText = CanvasRenderingContext2D.prototype.fillText;
    const clearRect = CanvasRenderingContext2D.prototype.clearRect;
    CanvasRenderingContext2D.prototype.clearRect = function (
      x,
      y,
      width,
      height,
    ) {
      if (this.canvas.closest(".trend-chart")) state.chartText = [];
      clearRect.call(this, x, y, width, height);
    };
    CanvasRenderingContext2D.prototype.fillText = function (
      text,
      x,
      y,
      maxWidth,
    ) {
      if (this.canvas.closest(".trend-chart")) state.chartText.push(text);
      if (maxWidth === undefined) fillText.call(this, text, x, y);
      else fillText.call(this, text, x, y, maxWidth);
    };
  });
  await page.route("**/admin/analytics?*", (route) => {
    const data = analytics(new URL(route.request().url()), "no-latency");
    data.series.forEach((point, i) => {
      point.time = new Date(
        Date.parse(point.time) + 12 * 3600000,
      ).toISOString();
      point.known_cost_cny = i === 12 ? "0.00000314159" : "0";
    });
    data.series[12].avg_latency_ms = 2345;
    data.series[12].latency_samples = 1;
    data.summary.avg_latency_ms = 2345;
    data.summary.latency_samples = 1;
    return route.fulfill({ json: data });
  });
  await page.goto("/");
  await navigate(page, "数据分析");
  await checkCanvas(page);
  await expect
    .poll(() =>
      page.evaluate(() =>
        (window as typeof window & { chartText: string[] }).chartText.includes(
          "12:00",
        ),
      ),
    )
    .toBe(true);
  await page.locator(".trend-chart canvas").scrollIntoViewIfNeeded();
  const chartBounds = await page.locator(".trend-chart canvas").boundingBox();
  await page.mouse.move(
    chartBounds!.x + 14,
    chartBounds!.y + chartBounds!.height - 8,
  );
  await page.mouse.down();
  await page.mouse.move(
    chartBounds!.x + chartBounds!.width * 0.35,
    chartBounds!.y + chartBounds!.height - 8,
    { steps: 8 },
  );
  await page.mouse.up();
  await expect
    .poll(() =>
      page.evaluate(() => {
        const labels = (
          window as typeof window & { chartText: string[] }
        ).chartText.filter((text) => /^\d{2}:\d{2}$/.test(text));
        return labels.join(",");
      }),
    )
    .toMatch(/^(?!.*12:00).+/);
  await page
    .locator(".analytics-metrics")
    .getByRole("button", { name: /已知费用/ })
    .click();
  await expect
    .poll(() =>
      page.evaluate(() =>
        (window as typeof window & { chartText: string[] }).chartText.some(
          (text) => /^¥0\.0+[1-9]/.test(text),
        ),
      ),
    )
    .toBe(true);
  await page
    .locator(".analytics-metrics")
    .getByRole("button", { name: /平均耗时/ })
    .click();
  await expect
    .poll(() =>
      page
        .locator(".trend-chart canvas")
        .evaluate((canvas: HTMLCanvasElement) => {
          const pixels = canvas
            .getContext("2d")!
            .getImageData(0, 0, canvas.width, canvas.height).data;
          let orange = 0;
          for (let i = 0; i < pixels.length; i += 4) {
            if (
              pixels[i] > 150 &&
              pixels[i + 1] > 90 &&
              pixels[i + 2] < 90 &&
              pixels[i + 3] > 150
            )
              orange++;
          }
          return orange;
        }),
    )
    .toBeGreaterThan(15);
});

test("connection API format can be selected on creation and changed on edit", async ({
  page,
}) => {
  let credential: Record<string, unknown> | undefined;
  const saves: Record<string, unknown>[] = [];
  await page.route("**/admin/credentials", async (route) => {
    if (route.request().method() === "POST") {
      const body = route.request().postDataJSON();
      saves.push(body);
      credential = { ...body, id: "cline", masked: "••••test" };
      return route.fulfill({ json: { id: "cline" } });
    }
    return route.fulfill({ json: credential ? [credential] : [] });
  });
  await page.route("**/admin/credentials/cline", async (route) => {
    expect(route.request().method()).toBe("PUT");
    const body = route.request().postDataJSON();
    saves.push(body);
    credential = { ...credential, ...body };
    return route.fulfill({ json: { id: "cline" } });
  });
  await page.goto("/");
  await navigate(page, "连接密钥");
  const format = page.getByRole("combobox", { name: "API 接口类型" });
  await expect(format).toHaveValue("chat_completions");
  await page.locator('input[type="url"]').fill("https://api.cline.bot/api/v1");
  await page.getByPlaceholder("例如：DeepSeek 主账户").fill("Cline");
  await page
    .getByLabel("DeepSeek API Key", { exact: true })
    .fill("test-api-key");
  await format.selectOption("responses");
  await page.getByRole("button", { name: "加密保存" }).click();
  await expect(page.locator(".credential-row")).toContainText("/responses");
  expect(saves[0]).toMatchObject({
    base_url: "https://api.cline.bot/api/v1",
    api_format: "responses",
    api_key: "test-api-key",
  });
  await page.getByRole("button", { name: "编辑", exact: true }).click();
  await expect(format).toHaveValue("responses");
  await format.selectOption("chat_completions");
  await page.getByRole("button", { name: "加密保存" }).click();
  await expect(page.locator(".credential-row")).toContainText(
    "/chat/completions",
  );
  expect(saves[1]).toMatchObject({
    base_url: "https://api.cline.bot/api/v1",
    api_format: "chat_completions",
    api_key: "",
  });
  await page.getByRole("button", { name: "编辑", exact: true }).click();
  await expect(format).toHaveValue("chat_completions");
  await page.getByRole("button", { name: "取消编辑" }).click();
  await page
    .getByRole("combobox", { name: "连接类型" })
    .selectOption("grok_via_sub2api");
  await expect(format).toHaveValue("responses");
  await format.selectOption("chat_completions");
  await expect(format).toHaveValue("chat_completions");
});

for (const provider of ["deepseek", "grok_via_sub2api"]) {
  test(`connection credential API address can be edited without replacing the key: ${provider}`, async ({
    page,
  }) => {
    const credential = {
      id: "editable",
      name: "可编辑连接",
      provider,
      base_url: "https://old.example.com",
      masked: "••••test",
      active: false,
    };
    await page.route("**/admin/credentials", (route) =>
      route.fulfill({ json: [credential] }),
    );
    let saves = 0;
    await page.route("**/admin/credentials/editable", (route) => {
      expect(route.request().method()).toBe("PUT");
      const body = route.request().postDataJSON();
      expect(body).toEqual({
        provider,
        name: credential.name,
        base_url: "https://new.example.com/proxy/v1/responses/",
        api_format: "",
        api_key: saves === 0 ? "" : "replacement-test-key",
        active: false,
      });
      credential.base_url = body.base_url;
      saves++;
      return route.fulfill({ json: { id: credential.id } });
    });
    await page.goto("/");
    await navigate(page, "连接密钥");
    const label = provider === "deepseek" ? "DeepSeek" : "sub2api";
    for (const key of ["", "replacement-test-key"]) {
      await page.getByRole("button", { name: "编辑", exact: true }).click();
      await expect(
        page.getByRole("heading", { name: "编辑连接密钥" }),
      ).toBeVisible();
      const address = page
        .locator("label")
        .filter({ hasText: `${label} API 地址` })
        .locator('input[type="url"]');
      await expect(address).toBeEditable();
      await address.fill("https://new.example.com/proxy/v1/responses/");
      await page.getByLabel(`${label} API Key`, { exact: true }).fill(key);
      await page.getByRole("button", { name: "加密保存" }).click();
      await expect(
        page.getByRole("heading", { name: "添加模型密钥" }),
      ).toBeVisible();
      await expect(page.locator(".credential-row")).toContainText(
        credential.base_url,
      );
      await expect(page.locator(".credential-row")).toContainText("已停用");
    }
    expect(saves).toBe(2);
  });
}

test("connection credential deletion confirms, cancels and clears the edited entry", async ({
  page,
}) => {
  await page.route("**/admin/credentials", (route) =>
    route.fulfill({
      json: [
        {
          id: "unused",
          name: "待删除连接",
          provider: "deepseek",
          base_url: "https://api.deepseek.com",
          masked: "••••test",
          active: true,
        },
      ],
    }),
  );
  let deletes = 0;
  await page.route("**/admin/credentials/unused", (route) => {
    expect(route.request().method()).toBe("DELETE");
    deletes++;
    return route.fulfill({ json: { ok: true } });
  });
  await page.goto("/");
  await navigate(page, "连接密钥");
  const remove = page.getByRole("button", {
    name: "删除连接密钥 待删除连接",
    exact: true,
  });
  page.once("dialog", (dialog) => dialog.dismiss());
  await remove.click();
  await expect(remove).toBeVisible();
  expect(deletes).toBe(0);
  await page.getByRole("button", { name: "编辑", exact: true }).click();
  await expect(
    page.getByRole("heading", { name: "编辑连接密钥" }),
  ).toBeVisible();
  await page
    .getByLabel("DeepSeek API Key", { exact: true })
    .fill("test-unsaved-secret");
  page.once("dialog", async (dialog) => {
    expect(dialog.message()).toContain("待删除连接");
    await dialog.accept();
  });
  await remove.click();
  await expect(remove).toHaveCount(0);
  await expect(
    page.getByRole("heading", { name: "添加模型密钥" }),
  ).toBeVisible();
  await expect(
    page.getByLabel("DeepSeek API Key", { exact: true }),
  ).toHaveValue("");
  await expect(page.getByRole("status")).toContainText("连接密钥已删除");
  expect(deletes).toBe(1);
});

test("referenced connection credential deletion reports the conflict and keeps the entry", async ({
  page,
}) => {
  await page.route("**/admin/credentials/credential-1", (route) =>
    route.fulfill({
      status: 409,
      json: {
        error: {
          code: "credential_in_use",
          message: "连接密钥仍被模型通道引用，请先在审核模型更换连接密钥",
        },
      },
    }),
  );
  await page.goto("/");
  await navigate(page, "连接密钥");
  page.once("dialog", (dialog) => dialog.accept());
  const remove = page.getByRole("button", {
    name: "删除连接密钥 DeepSeek 主账户",
    exact: true,
  });
  await remove.click();
  await expect(page.getByRole("alert")).toContainText("仍被模型通道引用");
  await expect(remove).toBeVisible();
  await expect(remove).toBeEnabled();
});

test("audit log search sends request and channel filters", async ({ page }) => {
  await page.goto("/");
  await navigate(page, "审核记录");
  await page.getByLabel("请求 ID", { exact: true }).fill("audit-1");
  await page.getByLabel("审核模型", { exact: true }).fill("deepseek-v3.2");
  await page
    .getByRole("combobox", { name: "调用通道", exact: true })
    .selectOption("channel-1");
  const request = page.waitForRequest(
    (req) =>
      req.url().includes("/admin/audit-logs?") &&
      new URL(req.url()).searchParams.get("request_id") === "audit-1",
  );
  await page.getByRole("button", { name: "筛选", exact: true }).click();
  const query = new URL((await request).url()).searchParams;
  expect(query.get("model")).toBe("deepseek-v3.2");
  expect(query.get("channel_id")).toBe("channel-1");
  expect(query.get("page")).toBe("1");
});

test("connection-specific generic model prices can be saved and reset", async ({
  page,
}) => {
  let saved: Record<string, unknown> | undefined;
  await page.route("**/admin/billing/prices", async (route) => {
    if (route.request().method() !== "POST") return route.fallback();
    saved = route.request().postDataJSON();
    await route.fulfill({
      json: [
        {
          id: 3,
          model: "deepseek",
          credential_id: "credential-1",
          rates: {
            off_hit: 0,
            off_miss: 0,
            off_output: 0,
            peak_hit: 0,
            peak_miss: 0,
            peak_output: 0,
          },
          source: "test tariff",
          effective_at: "2026-09-13T00:00:00Z",
        },
      ],
    });
  });
  let reset = false;
  await page.route("**/admin/billing/prices/3", (route) => {
    reset = route.request().method() === "DELETE";
    return route.fulfill({ json: [] });
  });
  await page.goto("/");
  await navigate(page, "成本与预算");
  await page.getByRole("tab", { name: "模型单价" }).click();
  await page.getByRole("button", { name: "添加模型单价" }).click();
  await page
    .getByRole("combobox", { name: "计价范围", exact: true })
    .selectOption("credential-1");
  await page.getByLabel("模型名称", { exact: true }).fill("deepseek");
  await page
    .getByLabel("价格来源 / 变更依据", { exact: true })
    .fill("test tariff");
  await page.getByRole("button", { name: "保存单价", exact: true }).click();
  await expect(page.getByRole("dialog")).toHaveCount(0);
  expect(saved?.credential_id).toBe("credential-1");
  expect(saved?.model).toBe("deepseek");
  await expect(
    page.getByRole("heading", { name: "deepseek", exact: true }),
  ).toBeVisible();
  page.once("dialog", (dialog) => dialog.accept());
  await page
    .getByRole("button", { name: "恢复模型默认价格", exact: true })
    .click();
  await expect(
    page.getByRole("heading", { name: "deepseek", exact: true }),
  ).toHaveCount(0);
  expect(reset).toBe(true);
});

test("image trials support uploads, URL blocks and removal", async ({
  page,
}, testInfo) => {
  let submitted: Record<string, unknown> | undefined;
  await page.route("**/admin/policies/policy-1/test", (route) => {
    submitted = route.request().postDataJSON();
    return route.fulfill({
      json: {
        id: "trial",
        actual_model: "deepseek-v3.2",
        latency_ms: 300,
        attempt_count: 1,
        usage: { reported: true, total_tokens: 20 },
        results: [
          {
            flagged: false,
            audit: { confidence: 0.1, threshold: 0.8, reason: "ok" },
          },
        ],
        attempts: [],
      },
    });
  });
  await page.goto("/");
  await page.getByRole("tab", { name: "审核试跑" }).click();
  await page.getByLabel("选择本地图片", { exact: true }).setInputFiles({
    name: "sample.png",
    mimeType: "image/png",
    buffer: Buffer.from(
      "iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mP8/x8AAwMCAO+aF9sAAAAASUVORK5CYII=",
      "base64",
    ),
  });
  await expect(page.getByAltText("审核图片 1")).toBeVisible();
  await page
    .getByLabel("图片 URL", { exact: true })
    .fill("https://example.com/test.png");
  await page.getByRole("button", { name: "添加图片 URL", exact: true }).click();
  await page.getByLabel("待审核内容", { exact: true }).fill("图文样本");
  await page.getByRole("button", { name: "运行审核", exact: true }).click();
  await expect(page.getByText("ok", { exact: true })).toBeVisible();
  const blocks = submitted?.input as {
    type: string;
    image_url?: { url: string };
  }[];
  expect(blocks.map((block) => block.type)).toEqual([
    "text",
    "image_url",
    "image_url",
  ]);
  expect(blocks[1].image_url?.url).toMatch(/^data:image\/png;base64,/);
  await page.screenshot({
    path: testInfo.outputPath("image-trial.png"),
    fullPage: true,
  });
  await page.getByRole("button", { name: "移除图片 2", exact: true }).click();
  await expect(page.locator(".trial-image")).toHaveCount(1);
});

test("analytics dimensions carry into audit log drilldown", async ({
  page,
}) => {
  await page.goto("/");
  await navigate(page, "数据分析");
  await checkCanvas(page);
  await page
    .getByRole("combobox", { name: "策略", exact: true })
    .selectOption("policy-1");
  await checkCanvas(page);
  await page
    .getByRole("combobox", { name: "调用方", exact: true })
    .selectOption("client-1");
  await checkCanvas(page);
  await page
    .getByRole("combobox", { name: "模型通道", exact: true })
    .selectOption("channel-1");
  await checkCanvas(page);
  const request = page.waitForRequest((req) =>
    req.url().includes("/admin/audit-logs?"),
  );
  await page.getByRole("button", { name: "查看审核记录", exact: true }).click();
  const q = new URL((await request).url()).searchParams;
  expect(q.get("policy_id")).toBe("policy-1");
  expect(q.get("client_id")).toBe("client-1");
  expect(q.get("channel_id")).toBe("channel-1");
  expect(q.get("keyword_ignore")).toBe("include");
  expect(q.get("from")).toBe("2026-09-12T16:00:00.000Z");
  expect(q.get("to")).toBe("2026-09-13T16:00:00.000Z");
  await expect(
    page.getByRole("heading", { name: "审核记录", level: 1 }),
  ).toBeVisible();
  await expect(
    page.getByRole("combobox", { name: "关键词忽略", exact: true }),
  ).toHaveValue("include");
});

test("evaluation workbench starts a bounded comparison and exports results", async ({
  page,
}, testInfo) => {
  let started = false;
  const run = {
    id: "eval-1",
    name: "回归对照",
    policy_name: "默认内容审核",
    status: "completed",
    total: 1,
    completed: 1,
    max_cost_cny: "5",
    message: "",
    created_at: "2026-09-14T00:00:00Z",
  };
  const detail = {
    run,
    config,
    policy_revision: 1,
    targets: { "": "按策略调度" },
    scores: [
      {
        target: "",
        total: 1,
        processed: 1,
        valid: 1,
        errors: 0,
        labelled: 1,
        correct: 1,
        allow_samples: 1,
        flagged_samples: 0,
        false_positives: 0,
        false_negatives: 0,
        unstable_samples: 0,
        average_latency_ms: 500,
        known_cost_cny: "0.00012",
        pending_costs: 0,
      },
    ],
    results: [
      {
        sequence: 0,
        sample_id: "sample-1",
        sample_name: "正常问候",
        target: "",
        iteration: 1,
        expected: "allow",
        status: "completed",
        request_id: "audit-1",
        model: "deepseek-v3.2",
        confidence: 0.1,
        flagged: false,
        threshold: 0.8,
        reason: "ok",
        error_code: "",
        error_message: "",
        latency_ms: 500,
      },
    ],
  };
  await page.route("**/admin/evaluation/runs", (route) => {
    if (route.request().method() === "POST") {
      const body = route.request().postDataJSON();
      expect(body.sample_ids).toEqual(["sample-1"]);
      expect(body.max_cost_cny).toBe("5");
      expect(body.retries).toBe(3);
      expect(body.config.prompt).toBe(config.prompt);
      started = true;
      return route.fulfill({ status: 201, json: { id: "eval-1" } });
    }
    return route.fulfill({ json: started ? [run] : [] });
  });
  await page.route("**/admin/evaluation/runs/eval-1", (route) =>
    route.fulfill({ json: detail }),
  );
  await page.route(
    "**/admin/evaluation/runs/eval-1/samples/sample-1",
    (route) =>
      route.fulfill({
        json: {
          id: "sample-1",
          name: "正常问候",
          input: "评测时的原始样本",
          expected: "allow",
          note: "正常文本",
          revision: 1,
        },
      }),
  );
  await page.route("**/admin/evaluation/runs/eval-1/export", (route) =>
    route.fulfill({
      contentType: "text/csv",
      body: "sample,score\nhello,0.1\n",
    }),
  );
  await page.goto("/");
  await navigate(page, "审核评测");
  await page
    .getByRole("checkbox", { name: "选择 正常问候", exact: true })
    .check();
  await page.getByRole("button", { name: "运行评测", exact: true }).click();
  await expect(
    page.getByRole("dialog", { name: "运行评测", exact: true }),
  ).toContainText("RPM 为 10 时至少间隔 8 秒");
  await page.getByLabel("评测名称", { exact: true }).fill("回归对照");
  await expect(page.getByLabel("失败重试次数", { exact: true })).toHaveValue(
    "2",
  );
  await page.getByLabel("失败重试次数", { exact: true }).fill("3");
  await page.getByRole("button", { name: "开始评测", exact: true }).click();
  await expect(
    page.getByRole("heading", { name: "通道对比", exact: true }),
  ).toBeVisible();
  await expect(
    page.getByRole("heading", { name: "回归对照", exact: true }),
  ).toBeVisible();
  await page.screenshot({
    path: testInfo.outputPath("evaluation-desktop.png"),
    fullPage: true,
  });
  await page.getByRole("button", { name: "正常问候", exact: true }).click();
  await expect(
    page.getByRole("dialog", { name: "评测样本快照" }),
  ).toContainText("评测时的原始样本");
  await page.getByRole("button", { name: "关闭样本快照" }).click();
  const download = page.waitForEvent("download");
  await page.getByRole("button", { name: "导出评测 CSV", exact: true }).click();
  expect((await download).suggestedFilename()).toBe("evaluation-eval-1.csv");
  await page.setViewportSize({ width: 390, height: 844 });
  await expect(page.locator(".sidebar")).toBeHidden();
  await page.screenshot({
    path: testInfo.outputPath("evaluation-mobile.png"),
    fullPage: true,
  });
  expect(
    await page.evaluate(
      () => document.documentElement.scrollWidth - innerWidth,
    ),
  ).toBeLessThanOrEqual(1);
});

test("evaluation samples can be edited and imported without starting a run", async ({
  page,
}) => {
  let saved = false,
    imported = false;
  await page.route("**/admin/evaluation/samples/sample-1", (route) => {
    if (route.request().method() !== "PUT") return route.fallback();
    const body = route.request().postDataJSON();
    expect(body.expected_revision).toBe(1);
    expect(body.input).toBe("更新后的样本");
    saved = true;
    return route.fulfill({ json: { id: "sample-1" } });
  });
  await page.route("**/admin/evaluation/samples/import", (route) => {
    expect(route.request().postDataJSON().builtin).toBe(true);
    imported = true;
    return route.fulfill({ json: { imported: 48 } });
  });
  await page.goto("/");
  await navigate(page, "审核评测");
  await page
    .getByRole("button", { name: "编辑 正常问候", exact: true })
    .click();
  await page.getByLabel("样本内容", { exact: true }).fill("更新后的样本");
  await page.getByRole("button", { name: "保存样本", exact: true }).click();
  await expect(page.getByRole("dialog")).toHaveCount(0);
  expect(saved).toBe(true);
  await page.getByRole("button", { name: "导入内置样本", exact: true }).click();
  await expect(page.getByRole("status")).toContainText("已导入 48 条");
  expect(imported).toBe(true);
});

test("access keys support editing expiry and identity-preserving rotation", async ({
  page,
}) => {
  let settings: Record<string, unknown> | undefined;
  await page.route("**/admin/api-keys/client-1", (route) => {
    expect(route.request().method()).toBe("PUT");
    settings = route.request().postDataJSON();
    return route.fulfill({ json: { ok: true } });
  });
  await page.route("**/admin/api-keys/client-1/rotate", (route) => {
    expect(route.request().postDataJSON().expected_revision).toBe(1);
    return route.fulfill({ json: { token: "dsa_test_rotated_only" } });
  });
  await page.goto("/");
  await navigate(page, "访问密钥");
  await page
    .getByRole("button", { name: "编辑访问密钥 生产应用", exact: true })
    .click();
  const modal = page.getByRole("dialog", { name: "编辑访问密钥", exact: true });
  await modal.getByLabel("调用方名称", { exact: true }).fill("更新应用");
  await modal.getByLabel("每分钟请求上限", { exact: true }).fill("120");
  await modal.getByLabel("到期时间", { exact: true }).fill("2028-01-01T00:00");
  await modal
    .getByRole("button", { name: "保存访问密钥", exact: true })
    .click();
  await expect(modal).toHaveCount(0);
  expect(settings?.name).toBe("更新应用");
  expect(settings?.rpm).toBe(120);
  expect(settings?.expected_revision).toBe(1);
  expect(settings?.expires_at).toBeTruthy();
  page.once("dialog", (dialog) => dialog.accept());
  await page
    .getByRole("button", { name: "轮换访问密钥 生产应用", exact: true })
    .click();
  await expect(page.locator(".token-reveal")).toContainText(
    "dsa_test_rotated_only",
  );
});

test("policies can be archived restored and imported as disabled", async ({
  page,
}) => {
  const policy = {
    id: "policy-1",
    name: "默认内容审核",
    alias: "abuse-audit-v1",
    enabled: true,
    archived: false,
    revision: 1,
    updated_at: "2026-09-14T00:00:00Z",
    config,
  };
  let imported: typeof policy | undefined;
  await page.route("**/admin/policies?*", (route) =>
    route.fulfill({ json: imported ? [policy, imported] : [policy] }),
  );
  await page.route("**/admin/policies/policy-1", (route) =>
    route.fulfill({ json: policy }),
  );
  await page.route("**/admin/policies/policy-1/archive", (route) => {
    const body = route.request().postDataJSON();
    expect(body.expected_revision).toBe(policy.revision);
    policy.archived = body.archived;
    policy.enabled = false;
    policy.revision++;
    return route.fulfill({ json: policy });
  });
  await page.route("**/admin/policies/import", (route) => {
    const body = route.request().postDataJSON();
    expect(body.config.channels).toEqual([]);
    imported = {
      ...policy,
      id: "imported",
      name: body.name,
      alias: body.alias,
      archived: false,
      enabled: false,
      revision: 1,
      config: body.config,
    };
    return route.fulfill({ status: 201, json: imported });
  });
  await page.route("**/admin/policies/imported", (route) =>
    route.fulfill({ json: imported }),
  );
  await page.goto("/");
  await expect(
    page.getByRole("button", { name: "归档当前策略", exact: true }),
  ).toBeEnabled();
  page.once("dialog", (dialog) => dialog.accept());
  await page.getByRole("button", { name: "归档当前策略", exact: true }).click();
  await expect(page.locator(".archived-policies")).toContainText(
    "默认内容审核",
  );
  await page.locator(".archived-policies summary").click();
  await page.getByRole("button", { name: "恢复", exact: true }).click();
  await expect(page.getByRole("switch")).toHaveAttribute(
    "aria-checked",
    "false",
  );
  const file = {
    schema_version: 1,
    name: "导出配置",
    alias: "portable-policy",
    config,
  };
  await page.getByLabel("选择策略配置文件", { exact: true }).setInputFiles({
    name: "policy.json",
    mimeType: "application/json",
    buffer: Buffer.from(JSON.stringify(file)),
  });
  const modal = page.getByRole("dialog", { name: "导入策略配置", exact: true });
  await modal.getByLabel("策略名称", { exact: true }).fill("导入结果");
  await modal.getByLabel("模型别名", { exact: true }).fill("imported-policy");
  await modal.getByRole("button", { name: "导入策略", exact: true }).click();
  await expect(modal).toHaveCount(0);
  await expect(page.locator(".policy-picker select")).toHaveValue("imported");
  await expect(page.getByRole("switch")).toHaveAttribute(
    "aria-checked",
    "false",
  );
});

test("operations alerts link to the affected workspace", async ({ page }) => {
  await page.route("**/admin/operations", (route) =>
    route.fulfill({
      json: {
        uptime_seconds: 7200,
        runtime: {
          model_concurrency: 16,
          request_concurrency: 32,
          request_body_mib: 128,
          trial_concurrency: 2,
          max_images: 16,
        },
        active_requests: 2,
        request_body_bytes: 1048576,
        model_in_flight: 16,
        trial_in_flight: 1,
        migration_version: 3,
        pending_costs: 2,
        estimated_costs: 1,
        running_evaluations: 0,
        database: {
          open: 5,
          in_use: 2,
          idle: 3,
          limit: 20,
          wait_count: 1,
          wait_ms: 12,
        },
        alerts: [
          {
            id: "model-capacity",
            severity: "warning",
            message: "上游模型并发已满",
            page: "channels",
          },
        ],
      },
    }),
  );
  await page.goto("/");
  await navigate(page, "系统设置");
  await expect(
    page.getByRole("heading", { name: "运行状态", exact: true }),
  ).toBeVisible();
  await expect(
    page.getByText("上游模型并发已满", { exact: true }),
  ).toBeVisible();
  await page
    .getByRole("button", { name: "查看上游模型并发已满", exact: true })
    .click();
  await expect(
    page.getByRole("heading", { name: "审核模型", level: 1 }),
  ).toBeVisible();
});

test("session expiry closes sensitive editors", async ({ page }) => {
  await page.route("**/admin/api-keys/client-1", (route) =>
    route.fulfill({ status: 401, json: { error: { message: "登录已过期" } } }),
  );
  await page.goto("/");
  await navigate(page, "访问密钥");
  await page
    .getByRole("button", { name: "编辑访问密钥 生产应用", exact: true })
    .click();
  await page.getByRole("button", { name: "保存访问密钥", exact: true }).click();
  await expect(
    page.getByRole("heading", { name: "登录控制台", exact: true }),
  ).toBeVisible();
  await expect(page.getByRole("dialog")).toHaveCount(0);
});

test("a late old-session response cannot sign out a new session", async ({
  page,
}) => {
  let release!: () => void;
  const pending = new Promise<void>((resolve) => {
    release = resolve;
  });
  let analyticsRequests = 0;
  await page.route("**/admin/analytics?*", async (route) => {
    if (++analyticsRequests > 1)
      return route.fulfill({ json: analytics(new URL(route.request().url())) });
    await pending;
    await route.fulfill({
      status: 401,
      json: { error: { message: "旧会话已过期" } },
    });
  });
  await page.route("**/admin/auth/logout", (route) =>
    route.fulfill({ json: { ok: true } }),
  );
  await page.route("**/admin/auth/login", (route) =>
    route.fulfill({ json: { username: "admin", csrf: "new-session-csrf" } }),
  );
  await page.goto("/");
  await navigate(page, "数据分析");
  await expect(page.locator(".analytics-skeleton")).toBeVisible();
  await page.getByRole("button", { name: "退出登录", exact: true }).click();
  await page.getByLabel("密码", { exact: true }).fill("test-login-only");
  await page.getByRole("button", { name: "登录", exact: true }).click();
  await expect(page.locator(".app-shell")).toBeVisible();
  const old = page.waitForResponse(
    (response) =>
      response.url().includes("/admin/analytics?") && response.status() === 401,
  );
  release();
  await (await old).finished();
  await page.evaluate(
    () =>
      new Promise((resolve) =>
        requestAnimationFrame(() => requestAnimationFrame(resolve)),
      ),
  );
  await expect(page.locator(".app-shell")).toBeVisible();
  await expect(page.locator(".login-shell")).toHaveCount(0);
});

test("invalid JSON is reported without treating it as application data", async ({
  page,
}) => {
  await page.route("**/admin/analytics?*", (route) =>
    route.fulfill({
      contentType: "text/html",
      body: "<html>invalid response</html>",
    }),
  );
  await page.goto("/");
  await navigate(page, "数据分析");
  await expect(page.getByRole("alert")).toContainText("服务返回了无效响应");
  await expect(page.locator(".app-shell")).toBeVisible();
});

test("audit log keyword visibility and latency filters persist through paging and export", async ({
  page,
}) => {
  await page.route("**/admin/audit-logs?*", async (route) => {
    await route.fulfill({ json: { items: [], total: 45 } });
  });
  await page.route("**/admin/audit-logs/export?*", (route) =>
    route.fulfill({ contentType: "text/csv", body: "request_id\n" }),
  );
  await page.goto("/");
  const initialRequest = page.waitForRequest((req) =>
    req.url().includes("/admin/audit-logs?"),
  );
  await navigate(page, "审核记录");
  expect(
    new URL((await initialRequest).url()).searchParams.get("keyword_ignore"),
  ).toBe("exclude");
  const visibility = page.getByRole("combobox", {
    name: "关键词忽略",
    exact: true,
  });
  const latency = page.getByRole("combobox", { name: "耗时", exact: true });
  await expect(visibility).toHaveValue("exclude");
  await expect(latency).toHaveValue("");
  for (const ms of ["1000", "2000", "3000", "5000", "10000"]) {
    await latency.selectOption(ms);
    const request = page.waitForRequest((req) =>
      req.url().includes("/admin/audit-logs?"),
    );
    await page.getByRole("button", { name: "筛选", exact: true }).click();
    const q = new URL((await request).url()).searchParams;
    expect(q.get("latency_gt_ms")).toBe(ms);
    expect(q.get("keyword_ignore")).toBe("exclude");
    expect(q.get("page")).toBe("1");
  }
  await visibility.selectOption("include");
  await latency.selectOption("2000");
  const applied = page.waitForRequest((req) =>
    req.url().includes("/admin/audit-logs?"),
  );
  await page.getByRole("button", { name: "筛选", exact: true }).click();
  await applied;
  const next = page.waitForRequest((req) =>
    req.url().includes("/admin/audit-logs?"),
  );
  await page.getByRole("button", { name: "下一页", exact: true }).click();
  const nextQuery = new URL((await next).url()).searchParams;
  expect(nextQuery.get("page")).toBe("2");
  expect(nextQuery.get("keyword_ignore")).toBe("include");
  expect(nextQuery.get("latency_gt_ms")).toBe("2000");
  const exportRequest = page.waitForRequest((req) =>
    req.url().includes("/admin/audit-logs/export?"),
  );
  const download = page.waitForEvent("download");
  await page
    .getByRole("button", { name: "导出审核摘要 CSV", exact: true })
    .click();
  const exportQuery = new URL((await exportRequest).url()).searchParams;
  await download;
  expect(exportQuery.get("keyword_ignore")).toBe("include");
  expect(exportQuery.get("latency_gt_ms")).toBe("2000");
  await visibility.selectOption("only");
  await latency.selectOption("");
  const reset = page.waitForRequest((req) =>
    req.url().includes("/admin/audit-logs?"),
  );
  await page.getByRole("button", { name: "筛选", exact: true }).click();
  const resetQuery = new URL((await reset).url()).searchParams;
  expect(resetQuery.get("page")).toBe("1");
  expect(resetQuery.get("keyword_ignore")).toBe("only");
  expect(resetQuery.get("latency_gt_ms")).toBe("");
});

test("audit summaries export active filters and stored text seeds manual evaluation", async ({
  page,
}) => {
  let exported = false;
  await page.route("**/admin/audit-logs/export?*", (route) => {
    expect(new URL(route.request().url()).searchParams.get("request_id")).toBe(
      "audit-1",
    );
    exported = true;
    return route.fulfill({
      contentType: "text/csv",
      body: "request_id\naudit-1\n",
    });
  });
  await page.route("**/admin/audit-logs/audit-1", (route) =>
    route.fulfill({
      json: {
        id: "audit-1",
        policy_id: "policy-1",
        kind: "production",
        client_id: "client-1",
        model: "deepseek-v3.2",
        attempt_count: 1,
        flagged: false,
        confidence: 0.1,
        threshold: 0.8,
        reason: "ok",
        error_code: "",
        latency_ms: 100,
        usage: {
          reported: true,
          total_tokens: 15,
          prompt_tokens: 10,
          completion_tokens: 5,
        },
        attempts: [],
        input_stored: true,
        input: "保存的审核文本",
        created_at: "2026-09-14T00:00:00Z",
      },
    }),
  );
  await page.goto("/");
  await navigate(page, "审核记录");
  await page.getByLabel("请求 ID", { exact: true }).fill("audit-1");
  const download = page.waitForEvent("download");
  await page
    .getByRole("button", { name: "导出审核摘要 CSV", exact: true })
    .click();
  expect((await download).suggestedFilename()).toBe("audit-records.csv");
  expect(exported).toBe(true);
  await page.getByRole("button", { name: "详情", exact: true }).click();
  await page.getByRole("button", { name: "加入评测样本", exact: true }).click();
  const modal = page.getByRole("dialog", { name: "标注样本", exact: true });
  await expect(modal.getByLabel("样本内容", { exact: true })).toHaveValue(
    "保存的审核文本",
  );
  await expect(
    modal.getByRole("combobox", { name: "预期判定", exact: true }),
  ).toHaveValue("manual");
});

test("login screen fits mobile and desktop", async ({ page }, testInfo) => {
  await page.route("**/admin/session", (route) =>
    route.fulfill({ status: 401, json: { error: { message: "请先登录" } } }),
  );
  await page.goto("/");
  await expect(page.getByRole("heading", { name: "登录控制台" })).toBeVisible();
  for (const width of [1440, 390]) {
    await page.setViewportSize({ width, height: 900 });
    await expect(page.getByLabel("密码", { exact: true })).toBeVisible();
    expect(
      await page.evaluate(
        () => document.documentElement.scrollWidth <= innerWidth,
      ),
    ).toBe(true);
    await page.screenshot({
      path: testInfo.outputPath("login-" + width + ".png"),
      fullPage: true,
    });
  }
});
