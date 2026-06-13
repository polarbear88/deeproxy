<script setup>
// 统计指标卡片：仪表盘实时/今日指标复用（DRY）。
defineProps({
  title: { type: String, default: '' },
  value: { type: [String, Number], default: '-' },
  // 可选副标题/单位
  suffix: { type: String, default: '' },
  // Element Plus 图标组件名
  icon: { type: String, default: '' },
  // 图标主题色
  color: { type: String, default: 'var(--dp-brand)' },
})
</script>

<template>
  <el-card shadow="hover" class="stat-card">
    <div class="stat-inner">
      <div v-if="icon" class="stat-icon" :style="{ backgroundColor: color }">
        <el-icon :size="22"><component :is="icon" /></el-icon>
      </div>
      <div class="stat-body">
        <div class="stat-title text-muted">{{ title }}</div>
        <div class="stat-value">
          {{ value }}<span v-if="suffix" class="stat-suffix">{{ suffix }}</span>
        </div>
      </div>
    </div>
  </el-card>
</template>

<style scoped lang="scss">
.stat-card {
  :deep(.el-card__body) {
    padding: 18px 20px;
  }
}
.stat-inner {
  display: flex;
  align-items: center;
  gap: 14px;
}
.stat-icon {
  width: 46px;
  height: 46px;
  border-radius: 10px;
  display: flex;
  align-items: center;
  justify-content: center;
  color: #fff;
  flex-shrink: 0;
}
.stat-title {
  font-size: 13px;
  margin-bottom: 6px;
}
.stat-value {
  font-size: 22px;
  font-weight: 700;
  line-height: 1;
}
.stat-suffix {
  font-size: 13px;
  font-weight: 400;
  margin-left: 4px;
  color: var(--el-text-color-secondary);
}
</style>
