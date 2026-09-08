# Ongrid 基础组件

页面从本目录导入组件，沿用 shadcn/ui 的克制外观与 Ongrid 的明暗主题变量。

- 表单使用 `Input`、`Textarea`、`Label`、`Select`、`Checkbox`、`Radio`、`Switch`、`Slider`。标签用 `htmlFor` 对应字段 `id`，无可见标签时提供 `aria-label`；错误字段用 `aria-invalid` 和 `aria-describedby`。
- `Input` / `Textarea` 保留原生属性、事件和 ref；组合搜索栏、聊天输入区使用 `variant="inset"`，由外层容器提供边框，避免双重边框。
- 输入框内的绝对定位图标使用 `top-1/2 -translate-y-1/2` 垂直居中，不用固定顶部偏移；装饰图标加 `pointer-events-none`。
- 列表搜索使用 `Input type="search"`，背景透明以融入所在页面，保留边框和聚焦提示。
- `Select variant="ghost"` 用于聊天模型选择等低强调入口：透明背景、无可见边框，悬停轻微高亮，保留键盘焦点提示；普通表单沿用默认样式。
- 横向筛选栏用 `FilterField label={...}` 包裹 `Select` 或 `Input`，标签与控件共用外框；宽度设在 `FilterField` 上，字段保留明确的可访问名称。创建、编辑和工具执行参数表单仍用普通 `Label`，同一组字段统一使用上方标签，不混用两种方向。
- `Select` 默认按内容宽度排列，避免在横向工具栏独占一行；表单需要撑满列时显式加 `className="w-full"`。
- 页头、筛选栏和表单操作统一为 36px，同组按钮用 `items-center` 对齐；不要用独立的 `h-*` / `py-*` / 字号覆盖制造第三种尺寸。密集行操作使用 `Button size="sm"`（28px），独立图标按钮使用 `size="icon"`。表单提交必须显式指定 `type="submit"`。
- 按钮使用 `primary`、`outline`、`subtle`、`link`、`danger`、`dangerGhost`；旧 `ghost` 与 `outline` 一致。主操作用 indigo，删除操作用 red。
- 已有的淡色语义按钮或动态选中态可用 `plain`：只复用尺寸和形状，由调用方保留成对的背景、文字和悬停色。不要仅凭类名包含 `indigo` / `red` 或 hover 色就改成实心按钮。
- 元数据用 `Chip`（也导出为 `Badge`），仅异常或确有必要的状态使用语义色；正常状态优先灰字与小圆点。
- 弹层使用 `Dialog` / `Modal`、`Popover`、`DropdownMenu`、`Hint`；标签页使用 `Tabs`，分页复用 `PaginationFooter`。
- 页面 `className` 只补布局、宽度、图标让位和代码字体；公共外观及状态维护在 `styles/index.css` 的 `.og-*` 规则中，避免每页复制边框、背景、圆角、字号和聚焦样式。

- 横向信息卡片将标题、描述和元数据放入左侧信息块，右侧操作组与信息块垂直居中；窄屏允许操作组换到下一行，避免挤压正文。
