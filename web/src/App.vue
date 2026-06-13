<script setup>
// 根组件：仅承载路由出口与 Element Plus 的中文 locale 配置。
import { ElConfigProvider } from 'element-plus'
import zhCn from 'element-plus/es/locale/lang/zh-cn'
</script>

<template>
  <!-- 全局中文化；size 默认 default -->
  <el-config-provider :locale="zhCn">
    <router-view />
  </el-config-provider>
</template>
