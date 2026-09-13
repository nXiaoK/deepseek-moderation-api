import { expect, test, type Page } from "@playwright/test";
import { analytics, mockAPI } from "./fixtures";

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
      .getByRole("navigation", { name: "主导航" })
      .getByRole("button", { name, exact: true }),
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
  await page.getByRole("button", { name: "替换", exact: true }).click();
  await expect(
    page.getByRole("heading", { name: "替换模型密钥" }),
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
