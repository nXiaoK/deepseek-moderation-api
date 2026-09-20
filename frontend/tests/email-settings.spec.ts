import { expect, test } from "@playwright/test";
import { mockAPI } from "./fixtures";

for (const width of [1440, 390]) {
  test(`email settings save, password retention and test mail at ${width}px`, async ({
    page,
  }) => {
    await page.setViewportSize({ width, height: 1000 });
    await mockAPI(page);
    let settings = {
      enabled: false,
      host: "",
      port: 465,
      security: "tls",
      username: "",
      from: "",
      to: "",
      revision: 1,
      password_set: false,
    };
    let lastBody: Record<string, unknown> = {};
    let failTest = false;
    await page.route("**/admin/settings/email", async (route) => {
      if (route.request().method() === "PUT") {
        lastBody = route.request().postDataJSON();
        expect(lastBody.expected_revision).toBe(settings.revision);
        expect(lastBody).not.toHaveProperty("password_set");
        const { expected_revision, password, clear_password, ...config } =
          lastBody;
        settings = {
          ...settings,
          ...config,
          revision: settings.revision + 1,
          password_set: clear_password
            ? false
            : !!password || settings.password_set,
        };
      }
      await route.fulfill({ json: settings });
    });
    await page.route("**/admin/settings/email/test", (route) =>
      route.fulfill(
        failTest
          ? { status: 502, json: { error: { message: "测试邮件发送失败" } } }
          : { json: { ok: true } },
      ),
    );
    await page.goto("/");
    if (width < 760)
      await page.getByRole("button", { name: "打开导航" }).click();
    await page
      .getByRole("navigation", { name: "主导航" })
      .getByRole("button", { name: "系统设置", exact: true })
      .click();
    await expect(
      page.getByRole("heading", { name: "命中邮件提醒" }),
    ).toBeVisible();
    await page.getByLabel("启用命中邮件提醒").check();
    await page
      .getByLabel("SMTP 主机", { exact: true })
      .fill("smtp.example.com");
    await page
      .getByLabel("SMTP 用户名", { exact: true })
      .fill("sender@example.com");
    await page
      .getByLabel("SMTP 密码 / 授权码", { exact: true })
      .fill("test-only-secret");
    await page
      .getByLabel("发件邮箱", { exact: true })
      .fill("sender@example.com");
    await page
      .getByLabel("站长收件邮箱", { exact: true })
      .fill("admin@example.com");
    await page
      .getByRole("button", { name: "保存邮件设置", exact: true })
      .click();
    await expect(page.getByRole("status")).toHaveText("邮件设置已保存。");
    expect(lastBody.password).toBe("test-only-secret");
    const password = page.getByLabel("SMTP 密码 / 授权码", { exact: true });
    await expect(password).toHaveValue("");
    await expect(password).toHaveAttribute(
      "placeholder",
      "已保存，留空保留原密码",
    );
    await page
      .getByRole("button", { name: "保存邮件设置", exact: true })
      .click();
    await expect(page.getByRole("status")).toHaveText("邮件设置已保存。");
    expect(lastBody.password).toBe("");
    await page
      .getByRole("button", { name: "发送测试邮件（已保存配置）", exact: true })
      .click();
    await expect(page.getByRole("status")).toContainText("测试邮件已提交");
    failTest = true;
    await page
      .getByRole("button", { name: "发送测试邮件（已保存配置）", exact: true })
      .click();
    await expect(page.getByRole("alert")).toContainText("测试邮件发送失败");
    await page.getByLabel("启用命中邮件提醒").uncheck();
    await page.getByLabel("清除已保存的 SMTP 密码").check();
    await page
      .getByRole("button", { name: "保存邮件设置", exact: true })
      .click();
    await expect(page.getByRole("status")).toHaveText("邮件设置已保存。");
    expect(lastBody.clear_password).toBe(true);
    expect(settings.enabled).toBe(false);
    await expect(password).toHaveAttribute(
      "placeholder",
      "填写邮箱授权码或 SMTP 密码",
    );
    expect(
      await page.evaluate(
        () => document.documentElement.scrollWidth <= window.innerWidth,
      ),
    ).toBe(true);
  });
}
