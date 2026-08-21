# DESIGN.md — Agent Matrix 工作台视觉系统（唯一事实源）

> 方向：D「暖灰柔角工坊」进化版 · 用户拨盘 视觉冒险度 8 / 动效强度 8 / 信息密度 5 · 调整：「颜色多巴胺，卡哇伊一点」
> 功能契约（任何样式不豁免）：这是**工作台**——全幅流体吃满屏宽（禁居中窄容器），任务状态一眼可辨，
> 3 秒内能开始「新建任务 / 看结果 / 追加指令」。可爱来自色彩与圆角与动效，不来自牺牲效率。

## 1. 视觉主题与氛围

暖奶油纸面上的多巴胺工作台。像一间采光好的工作室：柔角卡片、糖果色语义信号、圆润字体，
但骨子里是工程工具——数据用等宽体，状态用颜色编码，操作路径最短。
氛围层：极轻薄暖纸噪点（透明度 <4%），破纯平。

## 2. 色板与角色

### 基底（暖调，禁冷灰）
- 页面底 `--bg` `#FAF7F2`；容器卡 `--surface-deep` `#F3EFE7`；卡片 `--surface` `#FFFFFF`
- 墨 `--ink` `#26221C`；次墨 `--ink-2` `#57503F`；弱化 `--muted` `#8A8172`
- 线 `--line` `rgba(80,66,45,.10)`；强线 `--line-strong` `rgba(80,66,45,.18)`

### 多巴胺语义色（每色一对：文字墨 / 柔底；饱和度受控，禁荧光）
| 角色 | 墨色 | 柔底 | 点色 | 用途 |
|---|---|---|---|---|
| 品牌珊瑚 coral | `#C2410C` | `#FBE9E2` | `#F4673B` | 主按钮、链接、focus 环、::selection |
| 琥珀 amber | `#B45309` | `#FCF0DC` | `#F0A02E` | 进行中 / 执行中 |
| 叶绿 green | `#047857` | `#DDF3E9` | `#1DA97C` | 完成 / 在线 |
| 玫红 rose | `#BE123C` | `#FBE4E8` | `#EE4A6E` | 失败 / 危险操作 |
| 石灰石灰 gray | `#6B6459` | `#EFEBE4` | `#A8A094` | 取消 / 离线 / 中性 |

### Agent 策展色盘（替代旧的随机 hsl 哈希——从 6 对设计好的 pastel 对里确定性取，禁全色轮随机）
coral `#C2410C/#FBE9E2` · teal `#0F766E/#DDF0EE` · honey `#B45309/#FCF0DC` ·
leaf `#047857/#DDF3E9` · sky `#0369A1/#DEEEF8` · cocoa `#7C4A21/#F0E6DC`
（Agent 卡头像字与技能 chip 用；按 agent id 哈希取模 6，同一 Agent 永远同色）

### 铁律
- 禁紫色系、禁粉色系大面积使用（玫红仅限失败语义，小面积）
- 状态色只从上面表里取；功能色语义固定，全站一致（Color Consistency Lock）
- 文本对比度 ≥ 4.5:1；点色/柔底不做正文底色

## 3. 排版规则

- 展示/正文拉丁：`Nunito`（内嵌可变字重 woff2，400-1000；圆润字腔 = 卡哇伊来源）
- 中文：系统栈 `"PingFang SC","Hiragino Sans GB","Microsoft YaHei","Noto Sans SC"`（零下载零版权风险）
- 数据/ID/时间/计数：`JetBrains Mono`（内嵌可变 woff2）+ `font-variant-numeric: tabular-nums`
- 组合：`font-family:"Nunito",-apple-system,"PingFang SC","Microsoft YaHei","Noto Sans SC",sans-serif`
- 字阶：大标题 28-30px/700 收紧字距 -0.02em · 视图小标 19px/700 · 卡题 15.5px/700 · 正文 14-15px/行高 1.6-1.7 · 辅助 12-13px
- 层级靠字重+颜色+字号三件套，禁斜体，禁无脑放大；标题 `text-wrap:balance`
- 中西文之间加空格；中文正文 ≥14px

## 4. 组件样式

- 圆角刻度：卡 14px · 控件 10px · 小标签 8px · 徽章/状态点 pill 999（一套规矩全站一致）
- 卡片：白底 + ring 边框 `0 0 0 1px var(--line)`（偷 Miro 的 ring-as-border）+ 双层暖调染色阴影
  `0 1px 2px rgba(90,70,40,.05), 0 10px 28px -14px rgba(90,70,40,.22)`；hover 位移 -2px + 阴影加深
- 主按钮：珊瑚 `#C2410C` 白字；次按钮：白底 hairline；危险：玫红 ghost
- 状态徽章：柔底 + 同色墨文字 pill；状态点小圆点（在线带 6px 柔光晕，全站唯一的光）
- 看板列：容器卡 `--surface-deep`，列头 = 语义色小点 + 名称 + mono 计数
- 任务卡：白卡，标题 + 摘要（2 行截断）+ 指派 chips + mono 时间/轮次；列色体现在列头不染卡身
- 滑出面板：右侧 drawer，圆角只给左上一处 16px；遮罩 `rgba(38,34,28,.32)`
- 空态：居中插画位（SVG 点阵 logo 放大版）+ 一句指导 + 主按钮
- 骨架屏：柔底 shimmer 匹配布局；toast：墨底白字 pill

## 5. 布局原则

- 全幅流体：两侧 40px（移动 16px），超宽屏封顶 1760px；1920 下内容利用率 ≥90%
- 桌面三列看板等宽 grid gap 20px；移动塌缩为 seg + 单列
- 顶栏融入页面材质（同色底 + blur），不是后贴的工具条
- 组内间距 < 组间间距；4px 基准刻度

## 6. 深度层级

z 轴刻度：内容 1 · 顶栏 20 · 遮罩 40 · 面板 50 · toast 90。
深度靠「ring + 染色阴影」表达，禁厚重黑影、禁毛玻璃滥用（仅顶栏一处 blur）。

## 7. Do's / Don'ts

Do：状态语义色编码；数字一律 mono；hover 反馈必须有；空态要有下一步指引；多巴胺用在语义分工上
Don't：随机染色；斜体；emoji 当图标（用 inline SVG）；紫/粉大面积；居中窄容器；卡片当万能分组（hairline 能解决的不用卡）

## 8. 响应式

断点 768px：看板 ↔ seg+单列；面板移动全宽；视口高度用 `min-h-[100dvh]` 思路（禁 100vh 抖动）；
flex 文本子项 `min-width:0`；移动 seg 控件三段必须完整可读不裁切

## 9. Motion 哲学（拨盘 8，但工作台纪律优先）

活跃但不碍事：一切 UI 反馈 ≤300ms，只动 transform/opacity，ease-out `cubic-bezier(0.23,1,0.32,1)`，
drawer `cubic-bezier(0.32,0.72,0,1)`。
- 入场编排：首屏卡片 stagger 40ms 依次浮现（scale(.97)+opacity 起步）
- 签名交互：SSE 推送到达时，对应任务卡「更新脉冲」——珊瑚色 ring 呼吸两下后落定为新状态
- 按压反馈 `:active{transform:scale(.97)}` 120ms；进行中状态点 1.6s 呼吸
- toast 从下弹入 220ms；面板滑出 260ms drawer 曲线
- 键盘高频操作（Tab 切换、输入）零动画；hover 包 `@media (hover:hover) and (pointer:fine)`；
  `prefers-reduced-motion` 下移除位移保留淡入

---

## DNA 注入记录（craft-loop 要求，逐条溯源）

从 **Miro**（references/design-systems/miro/DESIGN.md）偷了 5 个具体值：
1. pastel 柔底/深墨成对的语义色用法 → 我们的多巴胺色对表
2. ring-as-border：`0 0 0 1px` 阴影环代替生硬 border → 卡片边框策略
3. 展示标题负字距（-0.02em 级）
4. 圆角分层刻度（小控件/卡/面板/pill 各有归属）
5. 单层重阴影禁令 → 双层染色轻阴影栈

## 工艺密度清单（≥5，交付时逐项打勾）

1. 氛围层：暖纸噪点 body::before（<4%）
2. ::selection：珊瑚柔底 `#FBE9E2` + 珊瑚墨字
3. 品牌 focus 环：珊瑚 `:focus-visible` outline，禁浏览器默认蓝
4. 签名交互：SSE 实时更新脉冲（同时是 Q2 工程答案）
5. 双层暖调染色阴影
6. 自定义细滚动条（暖灰 thumb）
7. 收尾彩蛋：页脚 mono 小字实时显示同步状态（「实时同步已连接 · 刚刚更新」）

## 字体与版权

`web/fonts/`：Nunito（OFL-1.1，nunito.OFL.txt）、JetBrains Mono（OFL-1.1，jetbrains-mono.OFL.txt），
均为 SIL Open Font License，允许随软件再分发；中文走系统栈无版权问题。
`web/vendor/`：Vue 3（MIT，vue.LICENSE.txt）。全部内嵌二进制，运行时零外部请求。
