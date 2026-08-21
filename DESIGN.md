# DESIGN.md — Agent Matrix 工作台视觉系统（唯一事实源）

> 方向：C「信号编辑室」· 用户拨盘 视觉冒险度 7 / 动效强度 5 / 信息密度 6
> 用户调整：「颜色清爽一点，不要暗黑，蓝白清爽，毛玻璃质感」
> 本文档取代 2026-08-20 的「暖奶油多巴胺」版（v0.12.0），自 v0.13.0 起生效。
> 功能契约（任何样式不豁免）：这是**工作台**——全幅流体吃满屏宽（禁居中窄容器），
> 任务状态一眼可辨，3 秒内能开始「新建任务 / 看结果 / 追加指令」。
> 编辑室气质来自排版与结构，不来自牺牲效率。

## 1. 视觉主题与氛围

一份蓝白清爽的晨报：衬线刊头、hairline 网格、按时间倒排的任务流。
纸面是冷调白，墨色是冷炭蓝灰，唯一的彩色纪律是「品牌蓝给交互、状态色给语义」。
浮层（sticky 工具行、抽屉）用毛玻璃材质——玻璃只出现在真正浮起的地方。
无卡片、无圆角容器、无投影堆砌；分组靠 hairline 与留白。

## 2. 色板与角色

### 基底（冷调，禁暖灰）
- 纸底 `--paper` `#F7F9FC`；浮层面板 `--surface` `#FFFFFF`
- 墨 `--ink` `#1B2430`；次墨 `--ink-2` `#4A5464`；弱化 `--muted` `#7C8794`
- hairline `--line` `rgba(27,36,48,.10)`；强线 `--line-strong` `rgba(27,36,48,.22)`
- 刊头双线的墨线用 `--ink` 全浓度

### 品牌蓝（唯一交互重音，饱和度受控）
- `--blue` `#1D6FE0`：主按钮、选中 tab 下划线、链接、focus 环、::selection
- hover 亮档 `--blue-hi` `#4A8DF0`；柔底 `--blue-soft` `#E8F0FD`
- 蓝不参与状态语义，禁大面积铺色

### 状态语义色（白底对比 ≥4.5:1，语义固定全站一致）
| 角色 | 墨色 | 点/条色 | 用途 |
|---|---|---|---|
| 进行中 amber | `#9A6206` | `#D4940F` | 进行中 / 执行中 |
| 完成 green | `#0B6E4F` | `#16997B` | 完成 / 在线 |
| 失败 rose | `#B62A44` | `#D44D66` | 失败 / 危险操作 |
| 中性 slate | `#5B6676` | `#98A2B0` | 取消 / 离线 |

### 毛玻璃材质（仅浮层）
- sticky 工具行：`rgba(247,249,252,.78)` + `backdrop-filter: blur(16px) saturate(1.5)`
- 抽屉：`rgba(251,252,254,.88)` + `blur(24px) saturate(1.4)`；遮罩 `rgba(27,36,48,.30)`
- 玻璃边缘不用 border，用 ring：`0 0 0 1px rgba(27,36,48,.08)` + 内高光 `inset 0 1px 0 rgba(255,255,255,.55)`
- `prefers-reduced-transparency` / 不支持 backdrop-filter 时降级为不透明 `#FFFFFF`

### 铁律
- 禁纯黑 #000、禁紫禁粉、禁渐变光晕、禁大面积蓝色铺底
- 状态色只从上表取；蓝色只给交互；文本对比度 ≥ 4.5:1

## 3. 排版规则

- 拉丁展示衬线：`Fraunces`（内嵌可变 woff2，OFL-1.1）——刊头、视图大标题、空态大句
- 中文标题衬线栈：`"Songti SC","STSong","Noto Serif CJK SC",serif`
- 正文 / UI：系统栈 `-apple-system,"PingFang SC","Hiragino Sans GB","Microsoft YaHei","Noto Sans SC",sans-serif`
- 数据 / ID / 时间 / 计数：`JetBrains Mono`（内嵌可变 woff2）+ `tabular-nums`
- 字重只用 400 / 500 / 600；层级靠字重 + 颜色 + 字号三件套，禁斜体
- 字阶：刊头 `clamp(34px,4.2vw,52px)`/600 衬线 · 任务标题 19px/600 衬线 · 正文 15px/行高 1.65 ·
  meta 12.5px · 小节眉 11px 大写字距 +.28em（mono）
- 中文禁负字距；中西文之间加空格；标题 `text-wrap:balance`，正文 `text-wrap:pretty`

## 4. 组件样式

- 全站直角或近直角：hairline 是唯一的"边"。唯一圆角：状态点小圆（50%）
- 任务编辑行：左缘 3px 状态色条 + 衬线标题 + meta 行（@agent · 轮次 · mono 相对时间）
  + 右侧状态（点+字）与 mono 时间；行间 0.5px hairline；整行可点，hover 行底微染 `--blue-soft` 40%
- 小节眉：mono 大写字距标签（今天 / 昨天 / 更早）+ 延伸 hairline + 右端 mono 注释
- 主按钮：`--blue` 白字直角；次按钮：透明底 hairline；危险：rose ghost
- 视图 tab：下划线式（选中 = 蓝色 2px 下划线 + 墨色字），勿用 pill 分段
- 状态徽章：点 + 文字即可，禁柔底 pill 胶囊堆砌
- 抽屉：右侧滑出，毛玻璃，直角，左缘 ring；宽度 520px（新建/详情 720px），移动全宽
- 任务详情：会话线程——指令块左缘 2px 墨线引文式；Agent 结果 `pre` 用 hairline 框 + mono 13px；
  追加 composer 固定抽屉底部，顶边 hairline + 毛玻璃
- 空态：衬线大句 + 一句指导 + 主按钮；骨架屏 hairline 行 shimmer；toast 墨底白字直角

## 5. 布局原则

- 全幅流体（硬规则）：两侧 40px（移动 16px），超宽屏封顶 1760px；1920 下内容利用率 ≥90%
- 刊头 masthead：左 logo + 报名 + kicker；右 mono 日期 + 统计行 + Agent 摘要；下方 1px + 0.5px 双线
- sticky 工具行（毛玻璃）：视图 tab ｜ 状态过滤（mono 计数）｜ 右端同步状态 + 新建任务
- 任务流 + 右栏 rail（≥1200px 显示，232px，左 hairline）：AGENTS 名单 + 版次统计；<1200px 收起
- 任务按时间倒排分「今天 / 昨天 / 更早」；状态过滤在工具行，默认全部
- Agent 视图同为编辑行：状态点 + 名称 + hairline 方标签（技能）+ 最近任务 + mono 心跳 + 下线
- 组内间距 < 组间间距；8px 基准刻度，允许 7px/11px 级光学微调

## 6. 深度层级

z 轴：内容 1 · sticky 工具行 30 · 遮罩 40 · 抽屉 50 · toast 90。
深度语义：平面内容靠 hairline；浮起的东西（工具行/抽屉/toast）才用毛玻璃 + ring。
禁厚重黑影；抽屉只许一层轻影 `0 24px 56px -24px rgba(27,36,48,.25)`。

## 7. Do's / Don'ts

Do：hairline 分组；mono 数字；刊头双线；状态色条语义；毛玻璃只给浮层；行 hover 有反馈
Don't：卡片万能分组；圆角容器；斜体；emoji 图标（inline SVG）；居中窄容器；蓝色当状态色；
柔底 pill 徽章；任何"AI 蓝紫渐变"

## 8. 响应式

断点 768px：rail 在 <1200px 收起；任务行移动端正文截断保持一行标题两行 meta 可读；
抽屉移动全宽；视口高度 `min-h-[100dvh]` 思路；flex 文本子项 `min-width:0`；
工具行移动端横向滚动或换行收纳，不许裁切主按钮。

## 9. Motion 哲学（拨盘 5：有反馈，不表演）

一切 UI 反馈 ≤300ms，只动 transform/opacity；ease-out `cubic-bezier(0.23,1,0.32,1)`，
抽屉 `cubic-bezier(0.32,0.72,0,1)`。
- 任务行入场 stagger 40ms（translateY 4px + opacity）
- 签名交互：SSE 推送到达时对应任务行「更新脉冲」——蓝色 ring 呼吸两次后落定
- 进行中状态点 1.6s 呼吸；按压 `:active` 120ms 反馈
- toast 下弹 220ms；抽屉 260ms；键盘高频操作零动画
- hover 包 `@media (hover:hover) and (pointer:fine)`；`prefers-reduced-motion` 去位移留淡入

---

## DNA 注入记录（craft-loop 要求，逐条溯源）

从 **Linear**（references/design-systems/linear.app/DESIGN.md）偷了 4 个具体值（亮色化适配）：
1. hairline 边框策略：半透明墨色 `rgba(27,36,48,.08~.22)` 做全部结构线（对应其 rgba 白线体系）
2. 字重三轨纪律：400 阅读 / 500 强调 / 600 宣告，不用 700+（对应其 400/510/590）
3. 重音色纪律：品牌蓝只给交互元素，状态色只给指示器（对应其 indigo 只给 CTA）
4. ring 代替 border 表达浮层边缘（对应其 `0 0 0 1px` ring-as-border）

## 工艺密度清单（≥5，交付时逐项打勾）

1. 刊头双线 + mono 大写字距 kicker（编辑细节）
2. 毛玻璃 sticky 工具行与抽屉（材质细节，用户点名）
3. ::selection 蓝底白字
4. 品牌蓝 focus 环 `:focus-visible`，禁浏览器默认蓝
5. 签名交互：SSE 任务行更新脉冲
6. 自定义细滚动条（冷灰 thumb）
7. 页脚 mono 小字实时同步状态（收尾彩蛋）

## 字体与版权

`web/fonts/`：Fraunces（OFL-1.1，fraunces.OFL.txt）、JetBrains Mono（OFL-1.1，jetbrains-mono.OFL.txt），
均允许随软件再分发；中文标题走 Songti SC 系统栈、正文走系统黑体栈，零下载零版权风险。
`web/vendor/`：Vue 3（MIT）。全部内嵌二进制，运行时零外部请求。
