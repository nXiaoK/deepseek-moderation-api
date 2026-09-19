import { expect, test } from "@playwright/test";
import { mockAPI } from "./fixtures";

test("channel refresh failures are visible and retry clears the error", async ({
  page,
}) => {
  await mockAPI(page);
  const errors: string[] = [];
  page.on("pageerror", (error) => errors.push(error.message));
  await page.goto("/");
  await page
    .getByRole("navigation", { name: "主导航" })
    .getByRole("button", { name: "审核模型", exact: true })
    .click();
  await expect(
    page.getByRole("button", { name: "编辑 DeepSeek 主用", exact: true }),
  ).toBeVisible();
  let fail = true;
  await page.route("**/admin/model-channels", (route) => {
    if (route.request().method() === "POST")
      return route.fulfill({ json: { id: "channel-2" } });
    if (fail && route.request().method() === "GET")
      return route.fulfill({
        status: 503,
        json: { error: { message: "通道状态暂时不可用，请重试" } },
      });
    return route.fallback();
  });
  await page.getByRole("button", { name: "刷新状态", exact: true }).click();
  await expect(page.getByRole("alert")).toContainText("通道状态暂时不可用");
  await expect(page.getByRole("button", { name: "刷新状态" })).toBeEnabled();
  expect(errors).toEqual([]);
  fail = false;
  await page.getByRole("button", { name: "刷新状态", exact: true }).click();
  await expect(page.getByRole("alert")).toHaveCount(0);
  await expect(page.getByRole("button", { name: "刷新状态" })).toBeEnabled();

  fail = true;
  await page.getByLabel("通道名称", { exact: true }).fill("新审核模型");
  await page
    .getByRole("combobox", { name: "连接密钥", exact: true })
    .selectOption("credential-1");
  await page.getByRole("button", { name: "保存通道", exact: true }).click();
  await expect(page.getByRole("status")).toContainText("模型通道已保存");
  await expect(page.getByRole("alert")).toContainText("通道状态暂时不可用");
  await expect(page.getByRole("button", { name: "刷新状态" })).toBeEnabled();
  expect(errors).toEqual([]);
});
