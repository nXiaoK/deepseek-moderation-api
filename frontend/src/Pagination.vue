<script setup lang="ts">
import { computed, ref, watch } from "vue";
import AppIcon from "./AppIcon.vue";

const props = defineProps<{
  page: number;
  pageSize: number;
  total: number;
  busy: boolean;
}>();
const emit = defineEmits<{ change: [page: number, pageSize: number] }>();
const jumpPage = ref<string | number>(String(props.page));
const totalPages = computed(() =>
  Math.max(1, Math.ceil(props.total / props.pageSize)),
);
const start = computed(() =>
  props.total ? (props.page - 1) * props.pageSize + 1 : 0,
);
const end = computed(() => Math.min(props.page * props.pageSize, props.total));
const format = (n: number) => n.toLocaleString("zh-CN");
const pages = computed(() => {
  const last = totalPages.value;
  const numbers = new Set([1, last]);
  const from = Math.max(1, Math.min(props.page - 1, last - 4));
  const to = Math.min(last, Math.max(props.page + 1, 5));
  for (let n = from; n <= to; n++) numbers.add(n);
  const items: (number | string)[] = [];
  let previous = 0;
  for (const n of [...numbers].sort((a, b) => a - b)) {
    if (n - previous === 2) items.push(previous + 1);
    else if (n - previous > 2) items.push(`gap-${n}`);
    items.push(n);
    previous = n;
  }
  return items;
});
watch(
  () => [props.page, props.busy],
  () => {
    jumpPage.value = String(props.page);
  },
);
function go(page: number) {
  if (props.busy) return;
  const next = Number.isFinite(page)
    ? Math.min(totalPages.value, Math.max(1, Math.trunc(page)))
    : props.page;
  jumpPage.value = String(next);
  if (next !== props.page) emit("change", next, props.pageSize);
}
function resize(event: Event) {
  const select = event.target as HTMLSelectElement;
  const size = Number(select.value);
  // Keep the current selection until its request succeeds, including on errors.
  select.value = String(props.pageSize);
  if (!props.busy && size !== props.pageSize) emit("change", 1, size);
}
</script>

<template>
  <nav class="record-pagination" aria-label="审核记录分页" :aria-busy="busy">
    <div class="pagination-summary" role="status" aria-live="polite">
      <span
        >共 <strong>{{ format(total) }}</strong> 条</span
      >
      <span v-if="total">当前 {{ format(start) }}–{{ format(end) }} 条</span>
      <span v-else>暂无记录</span>
    </div>
    <div class="pagination-controls">
      <label class="page-size"
        >每页
        <select
          aria-label="每页条数"
          :value="pageSize"
          :disabled="busy"
          @change="resize"
        >
          <option v-for="size in [20, 50, 100]" :key="size" :value="size">
            {{ size }} 条
          </option>
        </select>
      </label>
      <div class="page-navigation">
        <button
          type="button"
          class="page-arrow"
          aria-label="上一页"
          title="上一页"
          :disabled="busy || page <= 1"
          @click="go(page - 1)"
        >
          <AppIcon name="back" :size="16" />
        </button>
        <div class="page-numbers">
          <template v-for="item in pages" :key="item">
            <button
              v-if="typeof item === 'number'"
              type="button"
              :aria-label="`第 ${item} 页`"
              :aria-current="item === page ? 'page' : undefined"
              :disabled="busy || !total"
              @click="go(item)"
            >
              {{ item }}
            </button>
            <span v-else class="page-gap" aria-hidden="true">…</span>
          </template>
        </div>
        <button
          type="button"
          class="page-arrow"
          aria-label="下一页"
          title="下一页"
          :disabled="busy || page >= totalPages"
          @click="go(page + 1)"
        >
          <AppIcon name="next" :size="16" />
        </button>
      </div>
      <span class="page-position"
        >第 {{ total ? page : 0 }} / {{ total ? totalPages : 0 }} 页</span
      >
      <form
        class="page-jump"
        novalidate
        @submit.prevent="go(String(jumpPage).trim() ? Number(jumpPage) : page)"
      >
        <label
          >跳至<input
            v-model="jumpPage"
            aria-label="跳转页码"
            type="number"
            inputmode="numeric"
            min="1"
            :max="totalPages"
            step="1"
            :disabled="busy || !total"
          />页</label
        >
        <button type="submit" :disabled="busy || !total">跳转</button>
      </form>
    </div>
  </nav>
</template>

<style scoped>
.record-pagination,
.pagination-summary,
.pagination-controls,
.page-navigation,
.page-numbers,
.page-size,
.page-jump,
.page-jump label {
  display: flex;
  align-items: center;
  gap: 8px;
}
.record-pagination {
  justify-content: space-between;
  flex-wrap: wrap;
  gap: 14px;
  padding: 18px 0 4px;
  color: var(--muted);
  font-size: 12px;
  font-variant-numeric: tabular-nums;
}
.pagination-summary {
  flex-wrap: wrap;
  gap: 12px;
}
.pagination-summary strong {
  color: var(--text);
  font-weight: 600;
}
.pagination-controls {
  flex-wrap: wrap;
  gap: 12px;
}
.page-size,
.page-jump label {
  flex-direction: row;
  color: inherit;
  white-space: nowrap;
  font-size: inherit;
}
.page-numbers,
.page-navigation {
  gap: 4px;
}
.record-pagination button,
.record-pagination select,
.record-pagination input {
  min-height: 34px;
  padding: 6px 9px;
  font-size: 12px;
}
.page-numbers button,
.page-arrow {
  min-width: 34px;
}
.page-numbers button[aria-current="page"] {
  color: var(--accent);
  border-color: var(--accent);
  background: var(--accent-soft);
}
.page-gap {
  padding: 0 4px;
}
.page-position {
  white-space: nowrap;
}
.page-size select {
  width: 82px;
}
.page-jump input {
  width: 64px;
}
@media (max-width: 760px) {
  .pagination-controls {
    width: 100%;
  }
  .page-numbers {
    display: none;
  }
  .record-pagination button,
  .record-pagination select,
  .record-pagination input {
    min-height: 40px;
  }
}
</style>
