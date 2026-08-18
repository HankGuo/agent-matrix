package main

import "strings"

// buildPrompt 生成「一句话接入指令」。提示词只做引导：静态逻辑全部托管在
// 服务器的 /setup.sh（幂等安装脚本），Agent 下载阅读后带令牌执行即可。
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
2. 带凭证执行。脚本会自动完成：注册换发凭证 → 落盘 ~/.agent-matrix/ → 安装心跳与任务执行器 → 安装每分钟定时任务（cron / launchd / systemd，自动识别）→ 自检：
    AM_URL="{{BASE_URL}}" AM_TOKEN="{{TOKEN}}" AM_NAME="{{NAME}}" sh /tmp/am-setup.sh
3. 失败处理：注册 401 说明令牌已用或过期，直接向我索要新令牌，不要重试；其他失败重试一次，仍失败则带原始报错向我汇报，不要静默跳过。

## 汇报
把脚本末尾的自检输出原样汇报给我：是否注册成功、执行器用的哪条命令、定时任务类型（sched=）、各项自检是否 ok。

## 备注
- 任务执行器命令默认按 kimi → claude → openclaw → hermes 顺序自动探测；要指定就先设环境变量再执行：AM_RUN_TASK='你的命令（$1=任务内容 $2=任务ID tsk_…）'。之后想改命令，编辑 ~/.agent-matrix/config 里的 AM_RUN_TASK 即可。
- 调度环境未被自动识别时（如 Windows），脚本会打印手动安装说明，照做即可。
`

// buildTaskLoopPrompt 生成给已接入 Agent 的「升级/换装指令」。
// 不含任何密钥：Agent 从它本地的 ~/.agent-matrix/config 读取凭证。
// setup.sh 幂等：已有配置时跳过注册，直接更新脚本与定时任务。
func buildTaskLoopPrompt(baseURL string) string {
	return strings.ReplaceAll(taskLoopTemplate, "{{BASE_URL}}", baseURL)
}

const taskLoopTemplate = `你已接入 Agent Matrix 监控中心（凭证在本机 ~/.agent-matrix/config）。现在把任务执行器升级/安装到最新版，全部幂等：

1. 下载安装脚本并阅读：
    curl -fsS "{{BASE_URL}}/setup.sh" -o /tmp/am-setup.sh
2. 直接执行（检测到已有配置会自动跳过注册，只更新心跳与任务执行器脚本、补齐定时任务、自检）：
    sh /tmp/am-setup.sh
   要更换执行命令就带上：AM_RUN_TASK='你的命令（$1=任务内容 $2=任务ID）' sh /tmp/am-setup.sh
3. 旧机制清理：脚本已自动覆盖 heartbeat.sh 并删除 inbox.jsonl（v0.4 残留），你只需确认没有别处引用它。
4. 把脚本末尾的自检输出原样汇报给我。
`

// buildDecommissionPrompt 生成给老版本 Agent 的「手动下线指令」。
// 新版 Agent（v0.7+）不需要它：被下线后心跳会收到 410，heartbeat.sh 自动自卸载。
// 不含任何密钥。
func buildDecommissionPrompt() string { return decommissionTemplate }

const decommissionTemplate = `你已从 Agent Matrix 监控中心下线。请完成自我卸载，全部幂等：

1. 卸载定时任务（按你的系统选一种）：
   - macOS: launchctl unload ~/Library/LaunchAgents/com.agent-matrix.heartbeat.plist ~/Library/LaunchAgents/com.agent-matrix.task-runner.plist 2>/dev/null; rm -f ~/Library/LaunchAgents/com.agent-matrix.*.plist
   - Linux cron: crontab -l | grep -v 'agent-matrix/' | crontab -
   - Linux systemd: systemctl --user disable --now agent-matrix-heartbeat.timer agent-matrix-task-runner.timer; rm -f ~/.config/systemd/user/agent-matrix-*; systemctl --user daemon-reload
2. 如果 ~/.agent-matrix/runner.lock 存在，说明任务正在执行，等一分钟后重试第 1 步再往下走。
3. 删除配置与脚本目录：rm -rf ~/.agent-matrix
4. 验证：crontab -l / launchctl list / systemctl --user list-timers 中不再有任何 agent-matrix 条目，且 ~/.agent-matrix 已不存在。
5. 向我汇报卸载结果；任何一步失败带原始报错汇报，不要静默跳过。
`
