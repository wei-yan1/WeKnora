<template>
  <div class="plugin-service">
    <h2 class="section-title">{{ t('settings.pluginService') }}</h2>
    <div class="desc-row">
      <p class="section-desc">{{ t('settings.pluginServiceDesc') }}</p>
      <t-button
        :loading="rescanning"
        @click="onRescan"
        class="rescan-btn"
      >
        <template #icon><t-icon name="refresh" /></template>
        {{ t('settings.refreshPlugins') }}
      </t-button>
    </div>
  </div>
</template>

<script setup lang="ts">
import { ref } from 'vue'
import { MessagePlugin } from 'tdesign-vue-next'
import { useI18n } from 'vue-i18n'
import { rescanPlugins, type RescanReport } from '@/api/plugin'

const { t } = useI18n()
const rescanning = ref(false)

function summarize(report: RescanReport): string {
  const lines: string[] = []
  if (report.added?.length) lines.push(t('settings.rescanSummaryAdded', { count: report.added.length }) + `: ${report.added.join(', ')}`)
  if (report.skipped?.length) lines.push(t('settings.rescanSummarySkipped', { count: report.skipped.length }) + `: ${report.skipped.join(', ')}`)
  if (report.changed?.length) lines.push(t('settings.rescanSummaryChanged', { count: report.changed.length }) + `: ${report.changed.join(', ')}`)
  if (report.errors?.length) lines.push(t('settings.rescanSummaryErrors', { count: report.errors.length }) + `:\n${report.errors.join('\n')}`)
  if (!lines.length) return t('settings.rescanSummaryEmpty')
  return lines.join('\n')
}

async function onRescan() {
  if (rescanning.value) return
  rescanning.value = true
  try {
    const report = await rescanPlugins()
    MessagePlugin.success(summarize(report))
  } catch (e: any) {
    MessagePlugin.error(e?.message || t('settings.refreshPluginsFailed'))
  } finally {
    rescanning.value = false
  }
}
</script>

<style lang="less" scoped>
.plugin-service {
  .section-title {
    font-size: 18px;
    font-weight: 600;
    color: var(--td-text-color-primary);
    margin: 0 0 8px 0;
  }
  .desc-row {
    display: flex;
    align-items: flex-start;
    justify-content: space-between;
    gap: 24px;
  }
  .section-desc {
    font-size: 13px;
    color: var(--td-text-color-secondary);
    line-height: 1.6;
    margin: 0;
    max-width: 520px;
  }
  .rescan-btn {
    min-width: 160px;
    flex-shrink: 0;
  }
}
</style>
