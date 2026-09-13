<script setup lang="ts">
import { onBeforeUnmount, onMounted, ref, watch } from "vue";
import {
  init,
  use,
  type EChartsCoreOption,
  type EChartsType,
} from "echarts/core";
import { BarChart, LineChart, PieChart } from "echarts/charts";
import {
  AriaComponent,
  DataZoomComponent,
  GridComponent,
  LegendComponent,
  TooltipComponent,
} from "echarts/components";
import { CanvasRenderer } from "echarts/renderers";

use([
  BarChart,
  LineChart,
  PieChart,
  AriaComponent,
  DataZoomComponent,
  GridComponent,
  LegendComponent,
  TooltipComponent,
  CanvasRenderer,
]);
const props = defineProps<{ option: EChartsCoreOption; label: string }>();
const emit = defineEmits<{ select: [name: string] }>();
const element = ref<HTMLDivElement>();
let chart: EChartsType | undefined;
let observer: ResizeObserver | undefined;
function render() {
  chart?.setOption(
    {
      animationDuration: window.matchMedia("(prefers-reduced-motion: reduce)")
        .matches
        ? 0
        : 400,
      animationDurationUpdate: window.matchMedia(
        "(prefers-reduced-motion: reduce)",
      ).matches
        ? 0
        : 300,
      textStyle: { fontFamily: 'Inter, "PingFang SC", sans-serif' },
      aria: { enabled: true, description: props.label },
      ...props.option,
    },
    { notMerge: true },
  );
}
onMounted(() => {
  chart = init(element.value!);
  chart.on("click", (event) => emit("select", event.name));
  observer = new ResizeObserver(() => chart?.resize());
  observer.observe(element.value!);
  render();
});
watch(() => props.option, render);
onBeforeUnmount(() => {
  observer?.disconnect();
  chart?.dispose();
});
defineExpose({
  download(filename: string) {
    if (!chart) return;
    const link = document.createElement("a");
    link.download = `${filename}.png`;
    link.href = chart.getDataURL({
      type: "png",
      pixelRatio: 2,
      backgroundColor: "#ffffff",
    });
    link.click();
  },
});
</script>

<template>
  <div ref="element" class="data-chart" role="img" :aria-label="label" />
</template>

<style scoped>
.data-chart {
  width: 100%;
  height: 100%;
  min-width: 0;
}
</style>
