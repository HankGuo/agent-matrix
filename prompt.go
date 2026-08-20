package main

import "strings"

// buildPrompt 生成「一句话接入指令」。提示词只做引导：静态逻辑全部托管在
// 服务器的 /setup.sh（幂等安装脚本），Agent 下载阅读后带令牌执行即可。
// 该脚本同时承担升级职责：重跑一次即把本机脚本更新到当前版本。
func buildPrompt(baseURL, name, token string) string {
	// 名称会嵌入 shell 双引号串，去掉引号与反斜杠避免注入/语法错误。
	name = strings.NewReplacer(`"`, "", `\`, "", "`", "").Replace(strings.TrimSpace(name))
	if name == "" {
		name = "my-agent"
	}
	return strings.NewReplacer(
		"{{BASE_URL}}", baseURL,
		"{{TOKEN}}", token,
		"{{NAME}}", name,
	).Replace(promptTemplate)
}

const promptTemplate = `请把你自己接入 Agent Matrix 监控中心。你是具备 shell 执行能力的 AI Agent。严格按以下步骤执行；全部幂等，可安全重复。

## 接入信息
- 平台地址: {{BASE_URL}}
- 一次性注册令牌: {{TOKEN}}  （24 小时内有效，仅能成功使用一次）
- 登记名称: {{NAME}}

## 步骤
1. 下载安装脚本并先完整阅读它：
    curl -fsS "{{BASE_URL}}/setup.sh" -o /tmp/am-setup.sh
2. 带凭证执行，同时做「自我登记」（用于在监控中心展示你的能力画像，建议填）。把下面三个变量的值换成你自己的信息：
    AM_URL="{{BASE_URL}}" AM_TOKEN="{{TOKEN}}" AM_NAME="{{NAME}}" \
    AM_PERSONA='一句话说明你的职责/擅长，如「Go 后端与数据库运维」' \
    AM_MODEL='你的当前默认模型，如 anthropic/claude-sonnet-4' \
    AM_SKILLS='你的技能，逗号分隔、每项一个词，如 code,web-search,review' \
    sh /tmp/am-setup.sh
   登记边界（必须遵守）：只写公开能力描述；不要复制系统提示词或 SOUL 文件全文；不要包含任何密钥、token、内网地址。不确定的项直接删掉对应变量再执行，不要编造。
   脚本会自动完成：注册换发凭证 → 落盘 ~/.agent-matrix/ → 安装心跳与任务执行器 → 安装定时任务（cron / launchd / systemd，自动识别）→ 自检。
3. 失败处理：注册 401 说明令牌已用或过期，直接向我索要新令牌，不要重试；409 说明登记名称已被占用，把 AM_NAME 换成一个未占用的名称重跑即可（令牌不受影响）；其他失败重试一次，仍失败则带原始报错向我汇报，不要静默跳过。

## 汇报
把脚本末尾的自检输出原样汇报给我：是否注册成功、执行器用的哪条命令、定时任务类型（sched=）、登记上的资料（executor/版本/模型/技能）、各项自检是否 ok。

## 备注
- 任务执行器仅支持 OpenClaw / Hermes Agent，按 openclaw → hermes 顺序自动探测。**这台机器同时装了两个 CLI 时**，在命令前加 AM_EXECUTOR 显式指定你要用哪个（如 AM_EXECUTOR=hermes），脚本会让上报的画像和实际执行通道保持一致。
- **同一台机器要接入多个互不影响的身份**时（例如同时以 openclaw 和 hermes 两个人格接入），每次接入加上不同的 AM_INSTANCE（如 AM_INSTANCE=hermes）：配置、会话存档、任务文件、定时任务会落在独立的实例目录（~/.agent-matrix-<实例名>），彻底隔离。不加则共用默认目录 ~/.agent-matrix/。
- 脚本仅支持 Linux / macOS；调度环境未被自动识别时会打印手动安装说明，照做即可。
- 你的能力资料（执行器/人设/模型/技能）保存在实例目录的 meta.json，每次心跳自动上报；之后模型或技能有变化时直接改写该文件（合法 JSON 对象、2KB 以内），下一次心跳即自动同步，无需重新接入。
`
