<script setup lang="ts">
import { ref } from "vue";
import { api, APIError, downloadFile, type Policy, type Config } from "./api";
import AppIcon from "./AppIcon.vue";
const props = defineProps<{
  selected: Policy | null;
  archived: Policy[];
  dirty: boolean;
  busy: boolean;
  reload: (id?: string) => Promise<void>;
}>();
const emit = defineEmits<{
  busy: [value: boolean];
  error: [message: string];
  notice: [message: string];
  unauthorized: [];
}>();
const fileInput = ref<HTMLInputElement>();
const importing = ref<{
  name: string;
  alias: string;
  config: Config;
  rulesOnly: boolean;
} | null>(null);
const failure = ref("");
async function work(fn: () => Promise<void>) {
  if (props.busy) return;
  emit("busy", true);
  failure.value = "";
  try {
    await fn();
  } catch (e) {
    failure.value = e instanceof Error ? e.message : "操作失败";
    emit("error", failure.value);
    if (e instanceof APIError && e.status === 401) emit("unauthorized");
  } finally {
    emit("busy", false);
  }
}
async function archive(policy: Policy, archived: boolean) {
  if (
    archived &&
    !window.confirm(
      "归档“" + policy.name + "”？策略将停用，历史记录与通道引用保留。",
    )
  )
    return;
  await work(async () => {
    await api("/admin/policies/" + policy.id + "/archive", "PUT", {
      archived,
      expected_revision: policy.revision,
    });
    await props.reload();
    emit("notice", archived ? "策略已归档。" : "策略已恢复，当前保持停用。");
  });
}
async function remove(policy: Policy) {
  if (
    !window.confirm(
      "永久删除已归档策略“" +
        policy.name +
        "”？历史审核与费用记录保留，策略配置无法恢复。",
    )
  )
    return;
  await work(async () => {
    await api("/admin/policies/" + policy.id, "DELETE");
    await props.reload();
    emit("notice", "策略已删除。");
  });
}
async function readFile(event: Event) {
  const element = event.target as HTMLInputElement,
    file = element.files?.[0];
  element.value = "";
  if (!file) return;
  await work(async () => {
    if (file.size > 1024 * 1024) throw new Error("配置文件最多 1 MiB");
    const data = JSON.parse(await file.text());
    if (
      data.schema_version !== 1 ||
      typeof data.name !== "string" ||
      typeof data.alias !== "string" ||
      typeof data.config?.prompt !== "string"
    )
      throw new Error("请选择导出的版本 1 策略配置");
    importing.value = {
      name:
        Array.from(data.name as string)
          .slice(0, 45)
          .join("") + " 副本",
      alias: data.alias.slice(0, 70) + "-copy",
      config: data.config,
      rulesOnly: true,
    };
  });
}
async function importPolicy() {
  await work(async () => {
    const value = importing.value;
    if (!value) return;
    const imported = await api<Policy>("/admin/policies/import", "POST", {
      schema_version: 1,
      name: value.name,
      alias: value.alias,
      config: {
        ...value.config,
        channels: value.rulesOnly ? [] : value.config.channels,
      },
    });
    importing.value = null;
    await props.reload(imported.id);
    emit("notice", "策略已导入，当前保持停用。");
  });
}
</script>
<template>
  <div class="policy-tools">
    <div class="row">
      <button
        class="icon-button"
        :disabled="busy || dirty"
        title="导入策略配置"
        aria-label="导入策略配置"
        @click="fileInput?.click()"
      >
        <AppIcon name="upload" :size="16" />
      </button>
      <input
        ref="fileInput"
        class="sr-only"
        type="file"
        accept=".json"
        aria-label="选择策略配置文件"
        :disabled="busy || dirty"
        @change="readFile"
      />
      <button
        v-if="selected"
        class="icon-button"
        :disabled="busy || dirty"
        title="导出已保存配置"
        aria-label="导出策略配置"
        @click="
          work(() =>
            downloadFile(
              '/admin/policies/' + selected!.id + '/export',
              selected!.alias + '.json',
            ),
          )
        "
      >
        <AppIcon name="download" :size="16" />
      </button>
      <button
        v-if="selected"
        class="icon-button"
        :disabled="busy || dirty"
        title="归档当前策略"
        aria-label="归档当前策略"
        @click="archive(selected, true)"
      >
        <AppIcon name="archive" :size="16" />
      </button>
    </div>
    <details v-if="archived.length" class="archived-policies">
      <summary>已归档策略 · {{ archived.length }}</summary>
      <div v-for="p in archived" :key="p.id" class="record-row">
        <div>
          <strong>{{ p.name }}</strong>
          <p class="muted small">{{ p.alias }}</p>
        </div>
        <div class="row">
          <button :disabled="busy" @click="archive(p, false)">
            <AppIcon name="refresh" :size="15" />恢复</button
          ><button
            class="icon-button"
            title="导出归档配置"
            :aria-label="'导出 ' + p.name"
            :disabled="busy"
            @click="
              work(() =>
                downloadFile(
                  '/admin/policies/' + p.id + '/export',
                  p.alias + '.json',
                ),
              )
            "
          >
            <AppIcon name="download" :size="15" /></button
          ><button
            class="icon-button danger-button"
            title="永久删除策略"
            :aria-label="'删除策略 ' + p.name"
            :disabled="busy"
            @click="remove(p)"
          >
            <AppIcon name="trash" :size="15" />
          </button>
        </div>
      </div>
    </details>
  </div>
  <div v-if="importing" class="modal-backdrop" @click.self="importing = null">
    <section
      class="modal wide"
      role="dialog"
      aria-modal="true"
      aria-label="导入策略配置"
    >
      <div class="panel-heading">
        <h2>导入策略配置</h2>
        <button aria-label="关闭策略导入" @click="importing = null">
          <AppIcon name="close" />
        </button>
      </div>
      <form class="stack-form" @submit.prevent="importPolicy">
        <div class="form-grid">
          <label
            >策略名称<input
              v-model="importing.name"
              required
              maxlength="200" /></label
          ><label
            >模型别名<input v-model="importing.alias" required maxlength="80"
          /></label>
        </div>
        <label class="check-row"
          ><input
            v-model="importing.rulesOnly"
            type="checkbox"
          />仅导入规则，不绑定模型通道</label
        >
        <p class="muted small">
          阈值 {{ importing.config.threshold }} · 新策略保持停用
        </p>
        <label
          >提示词预览<textarea
            :value="importing.config.prompt"
            readonly
            rows="7"
          />
        </label>
        <p v-if="failure" class="error">{{ failure }}</p>
        <button class="primary" :disabled="busy">
          <AppIcon name="upload" :size="16" />导入策略
        </button>
      </form>
    </section>
  </div>
</template>
<style scoped>
.policy-tools {
  margin-bottom: 20px;
}
.policy-tools > .row {
  justify-content: flex-end;
}
.archived-policies {
  border-top: 1px solid var(--border);
  margin-top: 16px;
  padding-top: 14px;
}
.archived-policies summary {
  font-size: 12px;
  color: var(--muted);
  cursor: pointer;
}
.archived-policies .record-row {
  flex-wrap: wrap;
}
.modal .form-grid {
  padding: 0;
}
</style>
