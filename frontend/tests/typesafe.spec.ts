import { expect, test } from "@playwright/test";
import { config, mockAPI } from "./fixtures";
import { DEFAULT_TYPESAFE_QUESTION, type Config } from "../src/api";

for (const width of [1440, 390]) {
  test(`Jev connection, text channel and editable question at ${width}px`, async ({
    page,
  }, testInfo) => {
    await page.setViewportSize({ width, height: 1000 });
    await mockAPI(page);
    let saved: Config = { ...config };
    const policy = () => ({
      id: "policy-1",
      name: "默认内容审核",
      alias: "abuse-audit-v1",
      enabled: true,
      revision: 2,
      config: saved,
    });
    let credentials: unknown[] = [];
    await page.route("**/admin/credentials", async (route) => {
      if (route.request().method() === "POST") {
        const body = route.request().postDataJSON();
        expect(body).toMatchObject({
          provider: "typesafe",
          base_url: "https://api.typesafe.ai",
          api_format: "systemone",
          api_key: "fake-typesafe-key",
        });
        credentials = [
          {
            ...body,
            api_key: undefined,
            id: "jev-key",
            masked: "••••-key",
            active: true,
          },
        ];
        return route.fulfill({ json: { id: "jev-key" } });
      }
      return route.fulfill({ json: credentials });
    });
    await page.route("**/admin/policies/policy-1", (route) =>
      route.fulfill({ json: policy() }),
    );
    await page.route("**/admin/policies/policy-1/config", (route) => {
      saved = route.request().postDataJSON().config;
      return route.fulfill({ json: policy() });
    });
    let channelSaved = false;
    await page.route("**/admin/model-channels", (route) => {
      if (route.request().method() !== "POST") return route.fallback();
      expect(route.request().postDataJSON()).toMatchObject({
        credential_id: "jev-key",
        model: "jev-latest",
        text_only: true,
      });
      channelSaved = true;
      return route.fulfill({ json: { id: "jev-channel" } });
    });
    await page.goto("/");
    const question = page.getByLabel("Jev 审核问题（是 = 可以安全提交）", {
      exact: true,
    });
    await expect(question).toHaveAttribute(
      "placeholder",
      DEFAULT_TYPESAFE_QUESTION,
    );
    await question.fill("这段话可以安全提交给 LLM 吗？");
    await page.getByRole("button", { name: "保存并生效", exact: true }).click();
    await expect(
      page.getByRole("button", { name: "保存并生效", exact: true }),
    ).toBeDisabled();
    expect(saved.typesafe_question).toBe("这段话可以安全提交给 LLM 吗？");
    expect(saved.prompt).toBe(config.prompt);
    await page.reload();
    await expect(question).toHaveValue(saved.typesafe_question!);

    async function navigate(name: string) {
      if (width < 760)
        await page.getByRole("button", { name: "打开导航" }).click();
      await page
        .getByRole("navigation", { name: "主导航" })
        .getByRole("button", { name, exact: true })
        .click();
    }
    await navigate("连接密钥");
    await page
      .getByRole("combobox", { name: "连接类型", exact: true })
      .selectOption("typesafe");
    await expect(
      page.getByRole("textbox", { name: /^TypeSafe API 地址/ }),
    ).toHaveValue("https://api.typesafe.ai");
    await expect(
      page.getByRole("combobox", { name: "API 接口类型", exact: true }),
    ).toHaveValue("systemone");
    await expect(
      page.getByRole("combobox", { name: "API 接口类型", exact: true }),
    ).toBeDisabled();
    await page.getByLabel("名称", { exact: true }).fill("Jev 测试连接");
    await page
      .getByLabel("TypeSafe API Key", { exact: true })
      .fill("fake-typesafe-key");
    await page.getByRole("button", { name: "加密保存", exact: true }).click();
    await expect(page.getByRole("status")).toContainText("模型密钥已加密保存");
    await expect(page.locator(".credential-row")).toContainText(
      "TypeSafe / Jev",
    );
    await expect(page.locator(".credential-row")).toContainText("/systemone");
    await page.screenshot({
      path: testInfo.outputPath("jev-credentials.png"),
      fullPage: true,
    });

    await navigate("审核模型");
    await page.getByLabel("通道名称", { exact: true }).fill("Jev 审核");
    await page
      .getByRole("combobox", { name: "连接密钥", exact: true })
      .selectOption("jev-key");
    await expect(page.getByRole("textbox", { name: /^模型名/ })).toHaveValue(
      "jev-latest",
    );
    await expect(
      page.getByLabel("此模型为文本模型", { exact: true }),
    ).toBeChecked();
    await expect(
      page.getByLabel("此模型为文本模型", { exact: true }),
    ).toBeDisabled();
    await expect(
      page.getByLabel("最大输出 tokens", { exact: true }),
    ).toHaveCount(0);
    await page.screenshot({
      path: testInfo.outputPath("jev-channel.png"),
      fullPage: true,
    });
    await page.getByRole("button", { name: "保存通道", exact: true }).click();
    await expect(page.getByRole("status")).toContainText("模型通道已保存");
    expect(channelSaved).toBe(true);
    expect(
      await page.evaluate(
        () => document.documentElement.scrollWidth <= innerWidth,
      ),
    ).toBe(true);
  });
}
