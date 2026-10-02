<script setup lang="ts">
import { onBeforeUnmount, onMounted, ref } from "vue";
import { api, ignoreAPIError } from "./api";

interface Settings {
  reason_max_chars: number;
  revision: number;
}
const emit = defineEmits<{ saved: [] }>();
const settings = ref<Settings | null>(null);
const busy = ref(false),
  error = ref(""),
  notice = ref("");
const abort = new AbortController();
const path = "/admin/settings/audit";
async function run(action: () => Promise<void>) {
  if (busy.value) return;
  busy.value = true;
  error.value = "";
  notice.value = "";
  try {
    await action();
  } catch (e) {
    if (!ignoreAPIError(e))
      error.value = e instanceof Error ? e.message : "审核设置操作失败";
  } finally {
    busy.value = false;
  }
}
async function load() {
  await run(async () => {
    settings.value = await api<Settings>(path, "GET", undefined, abort.signal);
  });
}
async function save() {
  await run(async () => {
    if (!settings.value) return;
    const { reason_max_chars, revision } = settings.value;
    if (
      !Number.isInteger(reason_max_chars) ||
      reason_max_chars < 1 ||
      reason_max_chars > 4096
    )
      throw new Error("reason 字数上限必须为 1～4096 的整数");
    settings.value = await api<Settings>(
      path,
      "PUT",
      {
        reason_max_chars,
        expected_revision: revision,
      },
      abort.signal,
    );
    notice.value = "审核设置已保存，新审核请求立即生效。";
    emit("saved");
  });
}
onMounted(load);
onBeforeUnmount(() => abort.abort());
</script>

<template>
  <section class="panel settings-panel">
    <div class="panel-heading">
      <h2>审核输出设置</h2>
      <button type="button" :disabled="busy" @click="load">刷新审核设置</button>
    </div>
    <p v-if="error" class="error" role="alert">{{ error }}</p>
    <p v-if="notice" class="notice" role="status">{{ notice }}</p>
    <form v-if="settings" class="stack-form narrow" @submit.prevent="save">
      <label
        >reason 字数上限<input
          v-model.number="settings.reason_max_chars"
          type="number"
          min="1"
          max="4096"
          step="1"
          required
          :disabled="busy"
      /></label>
      <p class="muted small">
        默认 80，支持 1～4096 个 Unicode
        字符（中文、英文、标点均计入）。适用于所有策略的正式审核、试跑、评测和通道测试；超过设定上限仍会审核失败。保存后无需重启，进行中的请求沿用原设置。
      </p>
      <p class="muted small">
        如经常收到较长原因，可调整为 200 或
        500。策略提示词中若写有更小的字数限制，建议一并修改；字数上限不替代通道的最大输出
        tokens 设置。
      </p>
      <button class="primary" :disabled="busy">保存审核设置</button>
    </form>
  </section>
</template>
