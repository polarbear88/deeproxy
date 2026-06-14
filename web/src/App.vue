<script setup>
// 根组件：承载路由出口与 Element Plus 的 locale 配置。
// Element Plus 内置组件（分页、日期选择器、上传等）的文案由 el-config-provider 的
// locale 决定。这里让它跟随 lang store 响应式切换：zh→zhCn、en→默认英文，
// 与全局 vue-i18n 语言保持一致（修复切换语言后 EP 内置文案不变的问题）。
import { computed } from 'vue'
import { ElConfigProvider } from 'element-plus'
import zhCn from 'element-plus/es/locale/lang/zh-cn'
import en from 'element-plus/es/locale/lang/en'
import { useLangStore } from '@/stores/lang'

const langStore = useLangStore()
// 当前 EP locale：随 lang store 的 locale 响应式重算。
const elLocale = computed(() => (langStore.locale === 'en' ? en : zhCn))
</script>

<template>
  <!-- 全局 locale 跟随语言切换；size 默认 default -->
  <el-config-provider :locale="elLocale">
    <router-view />
  </el-config-provider>
</template>
