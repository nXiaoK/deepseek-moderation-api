import { expect, test, type Page } from "@playwright/test";
import { log, mockAPI } from "./fixtures";
import type { InputRankingItem } from "../src/api";

const from = "2026-10-09T00:00:00Z";
const to = "2026-10-10T00:00:00Z";
const items: InputRankingItem[] = Array.from({ length: 22 }, (_, i) => ({
  fingerprint: (i + 1).toString(16).padStart(64, "0"),
  preview: i === 2 ? null : i === 0 ? "hi" : "重复输入 " + i,
  text_chars: i === 0 ? 2 : 6,
  occurrences: i === 0 ? 40 : 2,
  flagged: i === 0 ? 40 : 0,
  keyword_blocked: i === 0 ? 40 : 0,
  keyword_ignored: i === 1 ? 2 : 0,
  model_flagged: 0,
  model_allowed: i > 1 ? 2 : 0,
  errors: 0,
  cache_hits: 0,
  clients: 2,
  first_seen: from,
  last_seen: "2026-10-09T23:00:00Z",
}));

async function navigate(page: Page, name: string) {
  await expect(page.locator(".app-shell")).toBeVisible();
  if (await page.getByRole("button", { name: "打开导航" }).isVisible())
    await page.getByRole("button", { name: "打开导航" }).click();
  await page
    .getByRole("navigation", { name: "主导航" })
    .getByRole("button", { name, exact: true })
    .click();
}

for (const width of [1440, 390]) {
  test(`input ranking filters, details, export and drilldown at ${width}px`, async ({
    page,
  }, testInfo) => {
    await page.setViewportSize({ width, height: 1000 });
    await page
      .context()
      .grantPermissions(["clipboard-read", "clipboard-write"]);
    await mockAPI(page);
    let lastQuery = new URLSearchParams();
    await page.route("**/admin/analytics/input-ranking?**", (route) => {
      const q = new URL(route.request().url()).searchParams;
      lastQuery = q;
      const minCount = Number(q.get("min_count") || 2);
      const matching = items.filter((item) => item.occurrences >= minCount);
      const pageSize = Number(q.get("page_size") || 20);
      const currentPage = Math.min(
        Number(q.get("page") || 1),
        Math.max(1, Math.ceil(matching.length / pageSize)),
      );
      return route.fulfill({
        json: {
          from,
          to,
          policy_id: q.get("policy_id") || "",
          client_id: q.get("client_id") || "",
          page: currentPage,
          page_size: pageSize,
          min_count: minCount,
          total: matching.length,
          summary: {
            indexed_requests: 82,
            unique_inputs: 22,
            repeated_inputs: 22,
            repeat_requests: 60,
            unindexed_requests: 8,
            backfill_pending: 3,
            oldest_retained_at: from,
          },
          items: matching.slice(
            (currentPage - 1) * pageSize,
            currentPage * pageSize,
          ),
        },
      });
    });
    await page.route("**/admin/analytics/input-ranking/export?**", (route) =>
      route.fulfill({
        body: "输入摘要,出现次数\nhi,40\n",
        headers: { "Content-Type": "text/csv" },
      }),
    );
    await page.route("**/admin/analytics/input-ranking/*?**", (route) => {
      const fingerprint = new URL(route.request().url()).pathname
        .split("/")
        .pop();
      if (fingerprint === "export") return route.fallback();
      const item = items.find((item) => item.fingerprint === fingerprint)!;
      return route.fulfill({
        json: {
          ...item,
          ...(item.preview != null ? { input: item.preview } : {}),
        },
      });
    });
    await page.goto("/");
    await navigate(page, "数据分析");
    await page.getByRole("tab", { name: "输入重复排行", exact: true }).click();
    const ranking = page.locator(".input-ranking");
    await expect(ranking.locator("tbody tr").first()).toContainText("hi");
    await expect(ranking).toContainText("历史补算待处理 3 条");
    await expect(ranking).toContainText("未建立文本指纹 8 条");
    await expect(ranking).toContainText("原文未保存");
    expect(lastQuery.get("min_count")).toBe("2");
    expect(lastQuery.has("model")).toBe(false);
    await ranking.getByRole("button", { name: "下一页", exact: true }).click();
    await expect(ranking.locator("tbody tr").first()).toContainText(
      "重复输入 20",
    );
    expect(lastQuery.get("page")).toBe("2");
    expect(lastQuery.get("from")).toBe(from);
    expect(lastQuery.get("to")).toBe(to);
    await ranking
      .getByRole("combobox", { name: "每页条数", exact: true })
      .selectOption("50");
    await expect(ranking.locator("tbody tr")).toHaveCount(22);
    await ranking.getByRole("button", { name: "7 天", exact: true }).click();
    await expect.poll(() => lastQuery.get("range")).toBe("7d");
    await ranking
      .getByRole("combobox", { name: "策略", exact: true })
      .selectOption("policy-1");
    await expect.poll(() => lastQuery.get("policy_id")).toBe("policy-1");
    await ranking
      .getByRole("combobox", { name: "调用方", exact: true })
      .selectOption("client-1");
    await expect.poll(() => lastQuery.get("client_id")).toBe("client-1");
    await ranking.getByLabel("最低出现次数", { exact: true }).fill("3");
    await ranking.getByRole("button", { name: "筛选", exact: true }).click();
    await expect(ranking.locator("tbody tr")).toHaveCount(1);
    expect(lastQuery.get("min_count")).toBe("3");
    const exportRequest = page.waitForRequest((request) =>
      request.url().includes("/input-ranking/export?"),
    );
    const download = page.waitForEvent("download");
    await ranking
      .getByRole("button", { name: "导出输入排行 CSV", exact: true })
      .click();
    const exportQuery = new URL((await exportRequest).url()).searchParams;
    expect(exportQuery.get("from")).toBe(from);
    expect(exportQuery.get("policy_id")).toBe("policy-1");
    expect((await download).suggestedFilename()).toBe("input-ranking.csv");
    await page.screenshot({
      path: testInfo.outputPath("input-ranking.png"),
      fullPage: true,
    });
    expect(
      await page.evaluate(
        () => document.documentElement.scrollWidth <= innerWidth,
      ),
    ).toBe(true);
    await ranking
      .getByRole("button", { name: "查看输入详情", exact: true })
      .click();
    const dialog = page.getByRole("dialog", { name: "重复输入详情" });
    await expect(dialog.locator("pre")).toHaveText("hi");
    await expect(dialog).toContainText("关键词阻止");
    await dialog
      .getByRole("button", { name: "复制完整输入", exact: true })
      .click();
    await expect(
      dialog.getByRole("button", { name: "已复制", exact: true }),
    ).toBeVisible();
    expect(await page.evaluate(() => navigator.clipboard.readText())).toBe(
      "hi",
    );
    await page.screenshot({
      path: testInfo.outputPath("input-ranking-detail.png"),
      fullPage: true,
    });
    const logsRequest = page.waitForRequest(
      (request) => new URL(request.url()).pathname === "/admin/audit-logs",
    );
    await dialog
      .getByRole("button", { name: "查看审核记录", exact: true })
      .click();
    const logQuery = new URL((await logsRequest).url()).searchParams;
    expect(logQuery.get("input_fingerprint")).toBe(items[0].fingerprint);
    expect(logQuery.get("keyword_ignore")).toBe("include");
    expect(logQuery.get("kind")).toBe("production");
    expect(logQuery.get("policy_id")).toBe("policy-1");
    await expect(page.getByText("相同输入", { exact: false })).toBeVisible();
    await page
      .getByRole("button", { name: "清除相同输入筛选", exact: true })
      .click();
    await expect(
      page.getByRole("button", { name: "清除相同输入筛选", exact: true }),
    ).toBeHidden();
  });
}

test("input ranking keeps previous results on errors and hides unsaved input", async ({
  page,
}) => {
  await mockAPI(page);
  let fail = false;
  await page.route("**/admin/analytics/input-ranking?**", (route) => {
    if (fail)
      return route.fulfill({
        status: 503,
        json: { error: { message: "统计暂时不可用" } },
      });
    const q = new URL(route.request().url()).searchParams;
    return route.fulfill({
      json: {
        from,
        to,
        policy_id: "",
        client_id: "",
        page: 1,
        page_size: 20,
        min_count: 2,
        total: 1,
        summary: {
          indexed_requests: 2,
          unique_inputs: 1,
          repeated_inputs: 1,
          repeat_requests: 1,
          unindexed_requests: 0,
          backfill_pending: 0,
          oldest_retained_at: from,
        },
        items: Number(q.get("min_count")) > 2 ? [] : [items[2]],
      },
    });
  });
  await page.route("**/admin/analytics/input-ranking/*?**", (route) =>
    route.fulfill({ json: items[2] }),
  );
  await page.goto("/");
  await navigate(page, "数据分析");
  await page.getByRole("tab", { name: "输入重复排行", exact: true }).click();
  const ranking = page.locator(".input-ranking");
  await expect(ranking).toContainText("原文未保存");
  await ranking
    .getByRole("button", { name: "查看输入详情", exact: true })
    .click();
  const dialog = page.getByRole("dialog");
  await expect(dialog).toContainText("原文未保存或已过期");
  await expect(
    dialog.getByRole("button", { name: "复制完整输入", exact: true }),
  ).toBeDisabled();
  await expect(dialog.locator("pre")).toHaveCount(0);
  await dialog
    .getByRole("button", { name: "关闭输入详情", exact: true })
    .click();
  fail = true;
  await ranking
    .getByRole("button", { name: "刷新输入排行", exact: true })
    .click();
  await expect(ranking.getByRole("alert")).toContainText("统计暂时不可用");
  await expect(ranking).toContainText("当前显示上次成功加载的结果");
  await expect(ranking.locator("tbody")).toContainText("原文未保存");
  fail = false;
  await ranking.getByRole("button", { name: "重试", exact: true }).click();
  await expect(ranking.getByRole("alert")).toHaveCount(0);
  await ranking.getByLabel("最低出现次数", { exact: true }).fill("3");
  await ranking.getByRole("button", { name: "筛选", exact: true }).click();
  await expect(ranking).toContainText("当前范围没有达到最低次数的输入");
  await page.getByRole("tab", { name: "调用统计", exact: true }).click();
  await expect(page.locator(".trend-chart canvas")).toBeVisible();
});
