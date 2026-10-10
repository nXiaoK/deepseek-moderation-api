import { expect, test } from "@playwright/test";
import { config, log, mockAPI } from "./fixtures";
import type { Config } from "../src/api";

for (const width of [1440, 390]) {
  test(`keyword blocking settings and decisions at ${width}px`, async ({
    page,
  }, testInfo) => {
    await page.setViewportSize({ width, height: 1000 });
    await mockAPI(page);
    let saved: Config = {
      ...config,
      keyword_ignore_enabled: true,
      ignore_keywords: ["保留忽略词"],
    };
    const policy = () => ({
      id: "policy-1",
      name: "默认内容审核",
      alias: "abuse-audit-v1",
      enabled: true,
      revision: 2,
      config: saved,
    });
    await page.route("**/admin/policies/policy-1", (route) =>
      route.fulfill({ json: policy() }),
    );
    await page.route("**/admin/policies/policy-1/config", (route) => {
      saved = route.request().postDataJSON().config;
      return route.fulfill({ json: policy() });
    });
    const blocked = {
      ...log,
      id: "blocked-1",
      keyword_blocked: true,
      flagged: true,
      confidence: null,
      reason: "关键词阻止，未调用模型",
      model: "",
      channel_id: "",
      attempt_count: 0,
      usage: {
        reported: true,
        prompt_tokens: 0,
        completion_tokens: 0,
        total_tokens: 0,
      },
      cost: {
        status: "zero",
        amount_cny: "0",
        reserved_cny: "0",
      },
    };
    await page.route("**/admin/policies/policy-1/test", (route) => {
      const body = route.request().postDataJSON();
      expect(body.config.keyword_block_enabled).toBe(true);
      expect(body.config.keyword_block_match_mode).toBe("exact");
      expect(body.config.block_keywords).toEqual(["hi", "指定短语"]);
      expect(body.input).toBe("hi");
      return route.fulfill({
        json: {
          ...blocked,
          actual_model: "",
          results: [
            {
              flagged: true,
              category_scores: { illicit: 1 },
              audit: {
                keyword_blocked: true,
                confidence: 1,
                threshold: saved.threshold,
                reason: blocked.reason,
              },
            },
          ],
        },
      });
    });
    await page.route("**/admin/audit-logs?**", (route) =>
      route.fulfill({ json: { items: [blocked], total: 1 } }),
    );
    await page.route("**/admin/audit-logs/blocked-1", (route) =>
      route.fulfill({ json: blocked }),
    );
    await page.goto("/");
    const enabled = page.getByLabel("关键词阻止", { exact: true });
    await enabled.check();
    const modes = page.getByRole("group", { name: "阻止关键词匹配模式" });
    await expect(
      modes.getByRole("button", { name: "模糊匹配" }),
    ).toHaveAttribute("aria-pressed", "true");
    await modes.getByRole("button", { name: "完整匹配" }).click();
    const phrases = page.getByLabel(
      "阻止关键词或短语（每行一条，区分大小写）",
      { exact: true },
    );
    await phrases.fill("  hi  \n\n指定短语\nhi\n");
    await page.getByRole("button", { name: "保存并生效" }).click();
    await expect(
      page.getByRole("button", { name: "保存并生效" }),
    ).toBeDisabled();
    expect(saved.block_keywords).toEqual(["hi", "指定短语"]);
    expect(saved.keyword_block_match_mode).toBe("exact");
    expect(saved.ignore_keywords).toEqual(["保留忽略词"]);
    await expect(phrases).toHaveValue("hi\n指定短语");
    await page.reload();
    await expect(enabled).toBeChecked();
    await expect(
      modes.getByRole("button", { name: "完整匹配" }),
    ).toHaveAttribute("aria-pressed", "true");
    await expect(phrases).toHaveValue("hi\n指定短语");
    await enabled.uncheck();
    await enabled.check();
    await expect(phrases).toHaveValue("hi\n指定短语");
    await modes.getByRole("button", { name: "模糊匹配" }).click();
    await page.getByRole("button", { name: "保存并生效" }).click();
    await expect(
      page.getByRole("button", { name: "保存并生效" }),
    ).toBeDisabled();
    expect(saved.keyword_block_match_mode).toBe("contains");
    await modes.getByRole("button", { name: "完整匹配" }).click();
    await expect(
      page.getByRole("button", { name: "保存并生效" }),
    ).toBeEnabled();
    await page.screenshot({
      path: testInfo.outputPath("keyword-block-policy.png"),
      fullPage: true,
    });
    expect(
      await page.evaluate(
        () => document.documentElement.scrollWidth <= innerWidth,
      ),
    ).toBe(true);

    await page.getByRole("tab", { name: "审核试跑" }).click();
    await page.getByLabel("待审核内容", { exact: true }).fill("hi");
    await page.getByRole("button", { name: "运行审核" }).click();
    await expect(
      page.getByText("关键词阻止 · 命中", { exact: true }),
    ).toBeVisible();
    await expect(page.locator(".score")).toContainText("—");
    await expect(page.locator(".test-result")).toContainText("实际调用 0 次");
    await expect(page.locator(".test-result")).toContainText("¥0");

    page.once("dialog", (dialog) => dialog.accept());
    if (await page.getByRole("button", { name: "打开导航" }).isVisible()) {
      await page.getByRole("button", { name: "打开导航" }).click();
    }
    await page.getByRole("button", { name: "审核记录", exact: true }).click();
    await page
      .getByRole("combobox", { name: "结果", exact: true })
      .selectOption("keyword_blocked");
    const filtered = page.waitForRequest(
      (request) =>
        new URL(request.url()).pathname === "/admin/audit-logs" &&
        new URL(request.url()).searchParams.get("result") === "keyword_blocked",
    );
    await page.getByRole("button", { name: "筛选", exact: true }).click();
    await filtered;
    await expect(page.locator("tbody")).toContainText("关键词阻止");
    await page.getByRole("button", { name: "详情", exact: true }).click();
    const detail = page.getByRole("dialog");
    await expect(detail).toContainText("关键词阻止");
    await expect(detail).toContainText("¥0");
    await expect(
      detail.getByRole("heading", { name: "结构化判定" }),
    ).toHaveCount(0);
    await page.screenshot({
      path: testInfo.outputPath("keyword-block-detail.png"),
      fullPage: true,
    });
  });
}
