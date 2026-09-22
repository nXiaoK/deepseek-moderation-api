import { expect, test, type Page } from "@playwright/test";
import { log, mockAPI } from "./fixtures";

async function openLogs(page: Page) {
  await page.goto("/");
  await expect(page.locator(".app-shell")).toBeVisible();
  const menu = page.getByRole("button", { name: "打开导航" });
  if (await menu.isVisible()) await menu.click();
  await page
    .getByRole("navigation", { name: "主导航" })
    .getByRole("button", { name: "审核记录", exact: true })
    .click();
  await expect(
    page.getByRole("navigation", { name: "审核记录分页" }),
  ).toHaveAttribute("aria-busy", "false");
}

test.beforeEach(async ({ page }) => {
  await mockAPI(page);
});

test("audit pagination jumps, changes size, preserves filters and handles bounds", async ({
  page,
}, testInfo) => {
  const queries: URLSearchParams[] = [];
  await page.route("**/admin/audit-logs?*", async (route) => {
    queries.push(new URL(route.request().url()).searchParams);
    await route.fulfill({ json: { items: [], total: 15263 } });
  });
  await openLogs(page);
  const nav = page.getByRole("navigation", { name: "审核记录分页" });
  const position = nav.locator(".page-position");
  await expect(nav).toContainText("共 15,263 条");
  await expect(nav).toContainText("当前 1–20 条");
  await expect(position).toHaveText("第 1 / 764 页");
  await expect(
    nav.getByRole("button", { name: "上一页", exact: true }),
  ).toBeDisabled();
  await nav.getByRole("button", { name: "第 764 页", exact: true }).click();
  await expect(position).toHaveText("第 764 / 764 页");
  await expect(nav).toContainText("当前 15,261–15,263 条");
  await expect(
    nav.getByRole("button", { name: "下一页", exact: true }),
  ).toBeDisabled();
  expect(queries.at(-1)?.get("page")).toBe("764");
  await nav.getByLabel("跳转页码").fill("385");
  await nav.getByLabel("跳转页码").press("Enter");
  await expect(position).toHaveText("第 385 / 764 页");
  await expect(
    nav.getByRole("button", { name: "第 385 页", exact: true }),
  ).toHaveAttribute("aria-current", "page");
  await nav.getByLabel("跳转页码").fill("99999");
  await nav.getByRole("button", { name: "跳转", exact: true }).click();
  await expect(position).toHaveText("第 764 / 764 页");
  await nav.getByLabel("跳转页码").fill("0");
  await nav.getByLabel("跳转页码").press("Enter");
  await expect(position).toHaveText("第 1 / 764 页");
  await nav.getByLabel("跳转页码").fill("");
  await nav.getByLabel("跳转页码").press("Enter");
  await expect(nav.getByLabel("跳转页码")).toHaveValue("1");
  await nav.getByLabel("每页条数").selectOption("100");
  await expect(position).toHaveText("第 1 / 153 页");
  expect(queries.at(-1)?.get("page_size")).toBe("100");
  await nav.getByRole("button", { name: "下一页", exact: true }).click();
  await expect(position).toHaveText("第 2 / 153 页");
  await page
    .getByRole("combobox", { name: "关键词忽略", exact: true })
    .selectOption("include");
  await page.getByRole("button", { name: "筛选", exact: true }).click();
  await expect(position).toHaveText("第 1 / 153 页");
  await nav.getByLabel("每页条数").selectOption("50");
  await expect(position).toHaveText("第 1 / 306 页");
  expect(queries.at(-1)?.get("keyword_ignore")).toBe("include");
  expect(queries.at(-1)?.get("page_size")).toBe("50");
  await nav.screenshot({ path: testInfo.outputPath("pagination-desktop.png") });
});

test("audit pagination preserves displayed data on failure and recovers from shrinking totals", async ({
  page,
}) => {
  let total = 45,
    fail = false;
  let release: (() => void) | undefined;
  let pending: Promise<void> | undefined;
  await page.route("**/admin/audit-logs?*", async (route) => {
    if (pending) await pending;
    if (fail)
      return route.fulfill({
        status: 503,
        json: { error: { message: "分页加载失败", code: "unavailable" } },
      });
    const q = new URL(route.request().url()).searchParams;
    await route.fulfill({
      json: {
        total,
        items: total
          ? [
              {
                ...log,
                id: "page-" + q.get("page"),
                created_at: "2026-09-22T12:00:00Z",
                kind: "test",
                model: "page-" + q.get("page"),
                reason: "test",
                latency_ms: 1,
              },
            ]
          : [],
      },
    });
  });
  await openLogs(page);
  const nav = page.getByRole("navigation", { name: "审核记录分页" });
  const position = nav.locator(".page-position");
  fail = true;
  pending = new Promise<void>((resolve) => {
    release = resolve;
  });
  await nav.getByRole("button", { name: "下一页", exact: true }).click();
  await expect(nav.getByLabel("每页条数")).toBeDisabled();
  await expect(nav.getByLabel("跳转页码")).toBeDisabled();
  release!();
  pending = undefined;
  await expect(nav).toHaveAttribute("aria-busy", "false");
  await expect(page.getByRole("alert")).toContainText("分页加载失败");
  await expect(position).toHaveText("第 1 / 3 页");
  await expect(page.locator("tbody")).toContainText("page-1");
  await nav.getByLabel("每页条数").selectOption("100");
  await expect(nav).toHaveAttribute("aria-busy", "false");
  await expect(nav.getByLabel("每页条数")).toHaveValue("20");
  fail = false;
  await nav.getByRole("button", { name: "第 3 页", exact: true }).click();
  await expect(position).toHaveText("第 3 / 3 页");
  total = 5;
  await nav.getByRole("button", { name: "上一页", exact: true }).click();
  await expect(position).toHaveText("第 1 / 1 页");
  await expect(page.locator("tbody")).toContainText("page-1");
  total = 0;
  await page.getByRole("button", { name: "筛选", exact: true }).click();
  await expect(position).toHaveText("第 0 / 0 页");
  await expect(nav).toContainText("暂无记录");
  await expect(
    nav.getByRole("button", { name: "下一页", exact: true }),
  ).toBeDisabled();
  await expect(nav.getByLabel("跳转页码")).toBeDisabled();
});

test("audit pagination fits mobile and supports direct jumping", async ({
  page,
}, testInfo) => {
  await page.setViewportSize({ width: 390, height: 844 });
  await page.route("**/admin/audit-logs?*", (route) =>
    route.fulfill({ json: { items: [], total: 15263 } }),
  );
  await openLogs(page);
  const nav = page.getByRole("navigation", { name: "审核记录分页" });
  await nav.getByLabel("跳转页码").fill("500");
  await nav.getByRole("button", { name: "跳转", exact: true }).click();
  await expect(nav.locator(".page-position")).toHaveText("第 500 / 764 页");
  expect(await nav.evaluate((el) => el.scrollWidth <= el.clientWidth)).toBe(
    true,
  );
  const bounds = await nav.boundingBox();
  expect(bounds!.x + bounds!.width).toBeLessThanOrEqual(390);
  await nav.screenshot({ path: testInfo.outputPath("pagination-mobile.png") });
});
