# Ongrid 基础组件

页面从本目录导入公共组件；设计约定集中维护在 [前端设计语言与开发规范](../../../../docs/design/frontend-design-language.md)。新增或修改组件前先阅读该文档，避免复制多套布局、主题和交互规则。

- 导出入口：[index.ts](index.ts)。
- 公共外观与交互状态：[styles/index.css](../../styles/index.css) 中的 `.og-*` 规则。
- 主题映射与字体：[tailwind.config.ts](../../../tailwind.config.ts)。
- shadcn 派生代码许可：[shadcn.LICENSE](shadcn.LICENSE)。

组件 API 以相应 TypeScript 类型为准；修改公共契约时同步更新规范和相关交互测试。
