# Ongrid 前端设计语言与开发规范

本规范用于新增页面、调整布局和维护公共组件，记录仓库已采用的设计约定。以克制、清晰、适合持续运维的控制台为目标；未写明的页面结构先参考现有多数页面，不另创一套视觉风格。历史页面仍有不一致处，不能把这些差异当作新规范。

## 开发入口与维护边界

- 页面骨架复用 [PageHeader](../../web/src/components/ui/PageHeader.tsx)、[Card](../../web/src/components/ui/Card.tsx)、[EmptyState](../../web/src/components/ui/EmptyState.tsx) 和 [PaginationFooter](../../web/src/components/ui/PaginationFooter.tsx)。参考 [设备](../../web/src/pages/Edges.tsx)、[日志](../../web/src/pages/Logs.tsx)、[监控](../../web/src/pages/Monitor.tsx) 的对应区域。
- 控件从 [components/ui](../../web/src/components/ui/) 导入。复杂交互由 Base UI 支撑，原生输入继续保留浏览器验证和事件；外观借鉴 shadcn/ui，并适配本项目 Tailwind 3 和主题变量，不直接复制另一套主题或升级框架。
- 公共外观和状态维护在 [styles/index.css](../../web/src/styles/index.css) 的 `.og-*` 规则中；Tailwind 映射和字体见 [tailwind.config.ts](../../web/tailwind.config.ts)。页面 `className` 补充布局、宽度、图标让位和代码字体，避免重复覆盖边框、背景、圆角、字号与聚焦样式。
- 本文是设计约定的集中维护入口。公共组件契约或视觉规则改变时，在同一个 PR 更新本文与相关测试；引用 shadcn 派生代码时保留 [MIT 许可](../../web/src/components/ui/shadcn.LICENSE)。

## 页面与布局

1. 列表页使用统一页头：标题、简短说明、右侧操作；筛选可放在 `PageHeader.extra` 或页头下方。操作组同一行用 `items-center`，需要时换行。
2. 数据列表按信息用途选择表格或分行列表。一组相关信息优先用一个 `Card` 加 `divide-y`，不为每行重复套卡片；信息层级通过间距、文字与分隔表达。
3. 横向信息卡片左侧放标题、描述和元数据，右侧操作组与信息块垂直居中。窄屏允许操作组换到下一行，正文容器使用 `min-w-0`，避免按钮挤压正文。
4. 保留密集表格的横向滚动，不要把整个页面撑出视口；长字段可截断并提供查看完整内容的入口。下拉选项可换行，不能只显示无法区分的前缀。
5. 表单弹窗用 `Modal` 的 `sm` / `md` / `lg` / `xl` 预设；宽度受视口约束，长内容在正文区滚动，标题与底部操作保持可达。不要额外叠加互相冲突的 `max-w-*`。确实需要用户调宽时用现有 `resizable`。

## 配色、文字与图标

| 用途 | 约定 |
| --- | --- |
| 页面、表面、分隔 | 中性 zinc 骨架；公共控件使用 `bg-bg` / `bg-card` / `border-border` 等语义类 |
| 正文、次要文字、辅助信息 | `text-text` / `text-text-muted` / `text-text-faint`，按信息层级使用，不能靠降低透明度隐藏必要说明 |
| 主要操作 | `Button variant="primary"`，使用 indigo |
| 品牌 | `--accent` 用于 logo 等品牌位置，不铺成大面积操作区 |
| 成功、降级、异常、信息 | emerald / amber / red / sky；状态优先通过 `Chip` 的 `tone` 表达 |
| 正常状态 | 小圆点加灰字，状态点用 `-500`，不为每个 OK 铺彩色背景 |

- 沿用 Inter / 系统无衬线字体；ID、查询语句、命令等使用 `font-mono`。页头沿用 `PageHeader` 的层级，表单和密集控件沿用公共组件字号，避免各页微调形成多套尺寸。
- `text-[10px]` 等紧凑字只适合非关键元数据，不用于主要操作和必须读懂的表单提示。必填、错误和危险操作说明不能只靠颜色区分。
- 通用图标使用 `lucide-react`；通知渠道、模型等品牌图标复用现有 [icons](../../web/src/components/icons/) 组件，例如 `CommunicationProviderIcon`，不以相似的通用图标代替。
- 输入框内图标用 `top-1/2 -translate-y-1/2` 居中，装饰图标加 `pointer-events-none` 和 `aria-hidden`。独立图标按钮提供可访问名称。
- 不使用发光阴影、`hover:scale`、`animate-pulse` 或花哨的“刷新中”徽章；动效服务于交互反馈，不抢占业务信息。

### 明暗主题

语义变量由 `html.dark` / `html.light` 切换；新公共样式优先使用现有变量，不在页面写死一套深色值。旧页面 zinc 类依赖 CSS 的 light 覆盖，带透明度的变体必须确认存在对应覆盖，不能认为 `.bg-zinc-900` 会覆盖 `.bg-zinc-900/20`。

检查默认、悬停、聚焦、禁用、选中和错误状态在两种主题下的可读性。深色弹窗内的弱提示已有统一对比度规则；不要在局部重新叠加透明度使说明变淡。浮层通过 Portal 渲染，也必须检查主题与内容对比度。

## 控件选择与尺寸

| 场景 | 使用方式 |
| --- | --- |
| 页头、筛选栏、普通表单操作 | 默认控件高度 36px；`Button` 默认尺寸 |
| 密集行内操作 | `Button size="sm"`，28px |
| 独立图标操作 | `Button size="icon"`，36px |
| 文本、数字、日期、文件输入 | `Input`，保留原生 `type`、`required`、`min` 等属性 |
| 多行输入 | `Textarea`；组合搜索栏、聊天输入使用 `variant="inset"`，边框由外层提供 |
| 列表搜索 | `Input type="search"`，透明表面融入页面，保留边框和聚焦反馈 |
| 单选列表 | `Select`；通过 `options` 或已有 `<option>` 子元素提供选项 |
| 复选、开关、互斥选择、范围 | `Checkbox` / `Switch` / `Radio` / `Slider` |
| 元数据与状态 | `Chip`（亦导出为 `Badge`） |

不要用独立的 `h-*`、`py-*` 和字号覆盖制造第三套常规控件尺寸。

### 时间范围

监控类页面复用 [TimeRangePicker](../../web/src/components/ui/TimeRangePicker.tsx)：快捷范围与自定义起止时间放在同一个弹出面板，避免切换自定义时在筛选栏新增输入行。快捷范围一键应用；自定义在本地时区编辑，点击“应用”才更新查询，取消或 Escape 丢弃草稿。触发按钮显示已选范围，完整日期可通过悬浮提示查看。日期输入复用原生 `datetime-local`，校验起止顺序、页面最小跨度及 7 天上限。关联跳转携带的固定时间窗保持固定。

### 按钮

- `primary`：当前区域的主要提交或创建操作。
- `outline`：次要操作；旧 `ghost` 保持相同的带边框语义。
- `subtle`：低强调操作；`link`：文本链接样式的按钮。
- `danger` / `dangerGhost`：删除等危险操作，分别为实心与低强调。
- `plain`：已有的语义色或动态选中态需要由页面提供时使用；页面必须同时保证背景、文字与悬停色搭配。

`Button` 默认 `type="button"`。提交表单必须显式写 `type="submit"`，不要依赖原生按钮的隐式提交行为。

### 标签与筛选栏

创建、编辑和工具执行参数用上方标签，`Label htmlFor` 对应字段 `id`；同一组字段保持一致。横向筛选用 `FilterField`，标签与控件共用外框，宽度设在外层；字段仍需要明确的可访问名称。

```tsx
import { FilterField, Input, Label, Select } from '@/components/ui';

// 横向列表筛选；role、setRole 和 roleOptions 来自页面状态。
<FilterField label={tr('角色', 'Role')} className="w-56">
  <Select label={tr('角色', 'Role')} value={role}
    onValueChange={setRole} options={roleOptions} />
</FilterField>

// 普通表单；name、setName 来自页面状态。
<div>
  <Label htmlFor="device-name">{tr('设备名称', 'Device name')}</Label>
  <Input id="device-name" required value={name}
    onChange={(event) => setName(event.target.value)} />
</div>
```

`Select` 默认按内容宽度排列，表单需要撑满列时加 `className="w-full"`。`onValueChange` 返回字符串，数字字段在调用方转换。选项超过 10 个时默认启用搜索，也可显式设置 `searchable`；聊天模型等低强调入口使用 `variant="ghost"`。不要给隐藏输入派发 `change` 模拟用户选择，测试应实际打开菜单并选择选项。

## 弹层、导航与交互

- 菜单、提示、轻量编辑、标签页分别复用 `DropdownMenu`、`Hint`、`Popover`、`Tabs`；不在页面重写定位、视口翻转、Escape 或焦点管理。
- 常规表单用 `Modal`；需要组合结构时用 `Dialog`。确认、提示和输入请求复用 [useDialogs](../../web/src/components/ui/useDialogs.tsx)，渲染返回的 `dialog` 并 `await` 结果；取消与卸载不能批准动作。异步等待后继续使用连接或选中对象时，检查它是否仍有效。
- 提交中禁用重复动作，失败保留用户输入并展示错误。加载、空数据、筛选无结果和请求失败要区分，空态复用 `EmptyState`，错误给出可执行的重试或修正入口。
- 导航用链接，动作使用按钮。可点击卡片必须有键盘可达的入口，不能只在 `div` 上写 `onClick`；不要移除唯一的焦点提示。
- 无可见标签的字段使用 `aria-label`；错误字段用 `aria-invalid` 和 `aria-describedby` 关联说明。Tooltip 补充解释，不能替代必要的可见操作名。
- 所有用户文案走 `tr('中文', 'English')`，包含占位符、空态、错误、Tooltip 和可访问名称。避免同一字符串中英并排，检查较长英文是否挤压布局。

## 提交前检查

- [ ] 已复用公共组件、语义色与页面结构；新规则同步更新本文。
- [ ] 标签、键盘操作、Escape、焦点返回、取消和重复提交行为正确。
- [ ] 长名称、长选项、空态、错误态及窄窗口可用；弹窗和表格没有意外撑开页面。
- [ ] 中文与英文、light 与 dark 均检查；视觉变更按根目录 `AGENTS.md` 的截图要求实看，纯文档变更无需截图。
- [ ] 运行相关交互测试和 `npm run build`；需要全量前端回归时运行 `npm test`，资源受限可用 `npm test -- --maxWorkers=2 --minWorkers=1`。
- [ ] 运行 `npm run lint`，区分新增与基线问题；测试或构建通过不能替代真实视觉验证，PR 如实记录未验证部分。其他交付检查遵循 [贡献指南](../../CONTRIBUTING.md)。
