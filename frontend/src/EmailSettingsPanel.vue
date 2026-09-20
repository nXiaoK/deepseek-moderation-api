<script setup lang="ts">
import { onBeforeUnmount, onMounted, ref } from "vue";
import { api, ignoreAPIError } from "./api";

interface Settings {
  enabled: boolean;
  host: string;
  port: number;
  security: "tls" | "starttls";
  username: string;
  from: string;
  to: string;
  revision: number;
  password_set: boolean;
}
interface Status {
  pending: number;
  failed: number;
  sent: number;
  last_sent_at: string | null;
}
const emit = defineEmits<{ saved: [] }>();
const settings = ref<Settings | null>(null);
const status = ref<Status | null>(null);
const password = ref(""),
  clearPassword = ref(false);
const busy = ref(false),
  error = ref(""),
  notice = ref("");
const abort = new AbortController();
let timer: ReturnType<typeof setInterval> | undefined;
const path = "/admin/settings/email";

async function refreshStatus() {
  status.value = await api<Status>(
    path + "/status",
    "GET",
    undefined,
    abort.signal,
  );
}
async function run(action: () => Promise<void>) {
  if (busy.value) return;
  busy.value = true;
  error.value = "";
  notice.value = "";
  try {
    await action();
  } catch (e) {
    if (!ignoreAPIError(e))
      error.value = e instanceof Error ? e.message : "邮件设置操作失败";
  } finally {
    busy.value = false;
  }
}
async function load() {
  await run(async () => {
    settings.value = await api<Settings>(path, "GET", undefined, abort.signal);
    password.value = "";
    clearPassword.value = false;
    await refreshStatus();
  });
}
async function save() {
  await run(async () => {
    if (!settings.value) return;
    const { revision, password_set, ...config } = settings.value;
    settings.value = await api<Settings>(
      path,
      "PUT",
      {
        ...config,
        expected_revision: revision,
        password: password.value,
        clear_password: clearPassword.value,
      },
      abort.signal,
    );
    password.value = "";
    clearPassword.value = false;
    notice.value = "邮件设置已保存。";
    emit("saved");
    await refreshStatus();
  });
}
async function test() {
  await run(async () => {
    await api(path + "/test", "POST", {}, abort.signal);
    notice.value =
      "测试邮件已提交给邮件服务器，请检查站长收件箱和垃圾邮件文件夹。";
  });
}
onMounted(() => {
  load();
  timer = setInterval(() => {
    if (!document.hidden) refreshStatus().catch(() => {});
  }, 15000);
});
onBeforeUnmount(() => {
  abort.abort();
  if (timer) clearInterval(timer);
  password.value = "";
});
</script>

<template>
  <section class="panel settings-panel">
    <div class="panel-heading">
      <div>
        <h2>命中邮件提醒</h2>
        <p class="muted small">
          正式审核命中时通知站长，包含缓存命中；试跑、评测、关键词忽略及审核失败不发送。
        </p>
      </div>
      <button type="button" :disabled="busy" @click="load">
        重新加载邮件设置
      </button>
    </div>
    <p v-if="error" class="error" role="alert">{{ error }}</p>
    <p v-if="notice" role="status">{{ notice }}</p>
    <form v-if="settings" class="stack-form" @submit.prevent="save">
      <fieldset :disabled="busy" class="email-fields">
        <label class="email-toggle"
          ><input
            v-model="settings.enabled"
            type="checkbox"
          />启用命中邮件提醒</label
        >
        <div class="email-grid">
          <label
            >SMTP 主机<input
              v-model="settings.host"
              placeholder="smtp.example.com"
              :required="settings.enabled"
              maxlength="253"
          /></label>
          <label
            >SMTP 端口<input
              v-model.number="settings.port"
              type="number"
              min="1"
              max="65535"
              required
          /></label>
          <label
            >连接加密<select v-model="settings.security">
              <option value="tls">TLS（通常为 465 端口）</option>
              <option value="starttls">STARTTLS（通常为 587 端口）</option>
            </select></label
          >
          <label
            >SMTP 用户名<input
              v-model="settings.username"
              placeholder="通常为完整发件邮箱；免认证中继可留空"
              autocomplete="off"
              maxlength="320"
          /></label>
          <label
            >SMTP 密码 / 授权码<input
              v-model="password"
              type="password"
              autocomplete="new-password"
              maxlength="1024"
              :disabled="clearPassword"
              :placeholder="
                settings.password_set
                  ? '已保存，留空保留原密码'
                  : '填写邮箱授权码或 SMTP 密码'
              "
          /></label>
          <label class="email-toggle"
            ><input
              v-model="clearPassword"
              type="checkbox"
              @change="password = ''"
            />清除已保存的 SMTP 密码</label
          >
          <label
            >发件邮箱<input
              v-model="settings.from"
              type="email"
              :required="settings.enabled"
              maxlength="320"
              placeholder="notify@example.com"
          /></label>
          <label
            >站长收件邮箱<input
              v-model="settings.to"
              type="email"
              :required="settings.enabled"
              maxlength="320"
              placeholder="admin@example.com"
          /></label>
        </div>
        <p class="muted small">
          密码加密保存。邮件包含请求
          ID、策略、评分和脱敏原因，不包含审核原文、图片或密钥。更换主机或用户名时需重新填写密码。
        </p>
        <p class="muted small">
          请先保存再发送测试邮件。每个命中请求发送一封，失败最多尝试 5
          次；关闭提醒会清除待发送任务，已开始发送的邮件可能仍会送达。
        </p>
        <div class="email-actions">
          <button class="primary" type="submit">保存邮件设置</button
          ><button type="button" @click="test">
            发送测试邮件（已保存配置）
          </button>
        </div>
      </fieldset>
    </form>
    <p v-if="status" class="muted small">
      保留期内：待发送 {{ status.pending }} · 已提交邮件服务器
      {{ status.sent }} · 重试耗尽 {{ status.failed
      }}<span v-if="status.last_sent_at">
        · 最近发送
        {{
          new Date(status.last_sent_at).toLocaleString("zh-CN", {
            timeZone: "Asia/Shanghai",
          })
        }}</span
      >
    </p>
    <p v-if="status?.failed" class="error">
      部分提醒重试后仍未发送，请检查配置并发送测试邮件。失败任务不会自动重新发送。
    </p>
  </section>
</template>

<style scoped>
.email-fields {
  border: 0;
  padding: 0;
  margin: 0;
  min-width: 0;
}
.email-grid {
  display: grid;
  grid-template-columns: repeat(2, minmax(0, 1fr));
  gap: 16px;
  margin: 18px 0;
}
.email-toggle {
  display: flex;
  align-items: center;
  gap: 8px;
}
.email-toggle input {
  width: auto;
}
.email-actions {
  display: flex;
  flex-wrap: wrap;
  gap: 12px;
}
@media (max-width: 760px) {
  .email-grid {
    grid-template-columns: 1fr;
  }
}
</style>
