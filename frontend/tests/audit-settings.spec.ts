import { expect, test } from "@playwright/test";
import { mockAPI } from "./fixtures";

for (const width of [1440, 390]) {
  test(`reason limit saves, persists and handles conflicts at ${width}px`, async ({
    page,
  }) => {
    await page.setViewportSize({ width, height: 1000 });
    await mockAPI(page);
    let settings = { reason_max_chars: 80, revision: 1 };
    let conflict = false;
    let updates = 0;
    await page.route("**/admin/settings/audit", async (route) => {
      if (route.request().method() === "PUT") {
        const body = route.request().postDataJSON();
        expect(body).toEqual({
          reason_max_chars: 200,
          expected_revision: settings.revision,
        });
        updates++;
        if (conflict) {
          settings = { reason_max_chars: 500, revision: settings.revision + 1 };
          await route.fulfill({
            status: 409,
            json: {
              error: { message: "配置已被其他管理员修改，请刷新后重试" },
            },
          });
          return;
        }
        settings = {
          reason_max_chars: body.reason_max_chars,
          revision: settings.revision + 1,
        };
      }
      await route.fulfill({ json: settings });
    });
    async function openSettings() {
      if (width < 760)
        await page.getByRole("button", { name: "打开导航" }).click();
      await page
        .getByRole("navigation", { name: "主导航" })
        .getByRole("button", { name: "系统设置", exact: true })
        .click();
    }
    await page.goto("/");
    await openSettings();
    await expect(
      page.getByRole("heading", { name: "审核输出设置" }),
    ).toBeVisible();
    const limit = page.getByLabel("reason 字数上限", { exact: true });
    const save = page.getByRole("button", {
      name: "保存审核设置",
      exact: true,
    });
    await expect(limit).toHaveValue("80");
    for (const value of ["0", "4097", "1.5"]) {
      await limit.fill(value);
      await save.click();
      expect(updates).toBe(0);
    }
    await limit.fill("200");
    await save.click();
    await expect(page.getByRole("status")).toHaveText(
      "审核设置已保存，新审核请求立即生效。",
    );
    expect(settings).toEqual({ reason_max_chars: 200, revision: 2 });
    await page.reload();
    await openSettings();
    await expect(limit).toHaveValue("200");
    conflict = true;
    await save.click();
    await expect(page.getByRole("alert")).toContainText(
      "配置已被其他管理员修改",
    );
    await expect(save).toBeEnabled();
    await page
      .getByRole("button", { name: "刷新审核设置", exact: true })
      .click();
    await expect(limit).toHaveValue("500");
    expect(
      await page.evaluate(
        () => document.documentElement.scrollWidth <= window.innerWidth,
      ),
    ).toBe(true);
  });
}
