import { expect, test } from "@playwright/test";
import { config, mockAPI } from "./fixtures";

test("creating a policy requires consent before discarding unsaved edits", async ({
  page,
}) => {
  await mockAPI(page);
  const created = {
    id: "policy-2",
    name: "新审核策略",
    alias: "new-audit",
    config,
    enabled: false,
    revision: 1,
  };
  let creations = 0;
  await page.route("**/admin/policies", async (route) => {
    if (route.request().method() === "POST") {
      creations++;
      return route.fulfill({ json: created });
    }
    if (creations) return route.fulfill({ json: [created] });
    return route.fallback();
  });
  await page.route("**/admin/policies/policy-2", (route) =>
    route.fulfill({ json: created }),
  );
  await page.goto("/");
  const prompt = page.getByLabel("审核提示词", { exact: true });
  await prompt.fill("尚未保存的审核规则");
  await page.getByRole("button", { name: "新建策略", exact: true }).click();
  const dialog = page.getByRole("dialog");
  await dialog.getByLabel("策略名称", { exact: true }).fill(created.name);
  await dialog.getByLabel("模型别名", { exact: true }).fill(created.alias);
  page.once("dialog", async (confirmation) => {
    expect(confirmation.message()).toContain("未保存的编辑");
    await confirmation.dismiss();
  });
  await dialog.getByRole("button", { name: "创建策略", exact: true }).click();
  await expect(dialog).toBeVisible();
  await expect(prompt).toHaveValue("尚未保存的审核规则");
  expect(creations).toBe(0);

  page.once("dialog", (confirmation) => confirmation.accept());
  await dialog.getByRole("button", { name: "创建策略", exact: true }).click();
  await expect(dialog).toBeHidden();
  await expect(
    page.getByRole("combobox", { name: "当前策略", exact: true }),
  ).toHaveValue(created.id);
  await expect(prompt).toHaveValue(config.prompt);
  expect(creations).toBe(1);
});
