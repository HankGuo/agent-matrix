package main

import "strings"

// buildPrompt 生成「一句话接入指令」。把这段指令交给任意具备 shell 执行能力的
// Agent（Claude Code / Kimi CLI / OpenClaw / Hermes 等），它会自行完成注册、
// 落盘配置并安装定时心跳任务。指令全部幂等，可重复执行。
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

const promptTemplate = `请把你自己接入 Agent Matrix 监控中心。你是运行在某台机器上的 AI Agent，具备执行 shell 命令和创建定时任务的能力。严格按以下步骤执行，每步都要验证结果；所有步骤幂等，可安全重复执行。

## 接入信息
- 平台地址: {{BASE_URL}}
- 一次性注册令牌: {{TOKEN}}  （24 小时内有效，仅能成功使用一次）
- 登记名称: {{NAME}}

## 步骤 1：注册
先设置变量，再调用注册接口（没有 curl 就换 wget 或你可用的 HTTP 客户端）：

    AM_URL="{{BASE_URL}}"
    AM_TOKEN="{{TOKEN}}"
    curl -fsS -X POST "$AM_URL/api/register" -H 'Content-Type: application/json' \
      -d "{\"token\":\"$AM_TOKEN\",\"name\":\"{{NAME}}\",\"hostname\":\"$(hostname)\",\"os\":\"$(uname -s)\",\"arch\":\"$(uname -m)\"}"

从返回的 JSON 中取出 heartbeat_token（形如 amh_...）。若返回 401，说明令牌已用或过期，直接向我索要新令牌，不要重试。

## 步骤 2：落盘配置
    mkdir -p "$HOME/.agent-matrix" && chmod 700 "$HOME/.agent-matrix"
    printf 'AM_URL=%s\nAM_HB_TOKEN=%s\n' "$AM_URL" "<heartbeat_token>" > "$HOME/.agent-matrix/config"
    chmod 600 "$HOME/.agent-matrix/config"

把 <heartbeat_token> 替换为步骤 1 的真实返回值。该文件包含凭证，不要泄露、不要提交到任何仓库。

## 步骤 3：心跳脚本
写入 $HOME/.agent-matrix/heartbeat.sh 并 chmod +x：

    #!/bin/sh
    . "$HOME/.agent-matrix/config"
    curl -fsS -m 15 -X POST "$AM_URL/api/heartbeat" -H "Authorization: Bearer $AM_HB_TOKEN" >/dev/null 2>&1 || true

## 步骤 4：安装每分钟定时任务（按操作系统选一种，已安装则跳过）
- Linux 有 cron：执行
    ( crontab -l 2>/dev/null | grep -v 'agent-matrix/heartbeat' ; echo "* * * * * $HOME/.agent-matrix/heartbeat.sh" ) | crontab -
- Linux 无 cron 但有 systemd：创建 ~/.config/systemd/user/agent-matrix-heartbeat.service（ExecStart 指向上面的脚本）和同名 .timer（OnBootSec=60, OnUnitActiveSec=60），然后 systemctl --user daemon-reload && systemctl --user enable --now agent-matrix-heartbeat.timer
- macOS：创建 ~/Library/LaunchAgents/com.agent-matrix.heartbeat.plist（ProgramArguments 指向脚本，StartInterval 为 60），然后 launchctl load 该 plist
- Windows：写 heartbeat.ps1（用 Invoke-RestMethod 做同样的 POST），再 schtasks /create /tn "AgentMatrixHeartbeat" /sc minute /mo 1 /tr 指向它

## 步骤 5：任务接收循环（接收平台派发的任务）
平台管理员会向你派发任务。把「拉取」并入每分钟执行的心跳脚本，把「执行」留给你自己的主循环——任务耗时不可预估，绝不要在定时脚本里同步执行。

在 heartbeat.sh 的心跳命令之后追加：

    resp=$(curl -fsS -m 15 "$AM_URL/api/agent/tasks" -H "Authorization: Bearer $AM_HB_TOKEN" 2>/dev/null || true)
    case "$resp" in *'"assignment_id"'*) printf '%s\n' "$resp" >> "$HOME/.agent-matrix/inbox.jsonl";; esac

在你的主循环里消费 $HOME/.agent-matrix/inbox.jsonl：每行 JSON 的 tasks 数组含若干 {"assignment_id":"tsa_...","title":"...","content":"..."}。content 就是平台管理员发给你的指令本身，按你的能力执行它（可以调用 OpenClaw / Hermes / 本地工具，也可以直接处理），完成后写回结果并把这行从 inbox 移除：

    curl -fsS -m 30 -X POST "$AM_URL/api/agent/tasks/<assignment_id>/result" \
      -H "Authorization: Bearer $AM_HB_TOKEN" -H 'Content-Type: application/json' \
      -d '{"status":"done","result":"<执行结果摘要，纯文本，≤32KB>"}'

失败则 status 用 "failed" 并在 result 写明原因。规则：每个 assignment_id 只能成功写回一次；任务一经拉取即锁定给你，重复拉取不会重复返回；写回前必须已拉取。

## 步骤 6：验证
1. 手动执行一次 $HOME/.agent-matrix/heartbeat.sh（无输出即正常）。
2. 再手动调一次心跳接口确认返回包含 "ok":true：
    . "$HOME/.agent-matrix/config" && curl -fsS -X POST "$AM_URL/api/heartbeat" -H "Authorization: Bearer $AM_HB_TOKEN"
3. 手动调一次任务拉取接口确认返回 JSON（没有任务时 tasks 为空数组）：
    . "$HOME/.agent-matrix/config" && curl -fsS "$AM_URL/api/agent/tasks" -H "Authorization: Bearer $AM_HB_TOKEN"
4. 确认定时任务已存在（crontab -l / launchctl list / systemctl --user list-timers / schtasks /query 之一，按你的平台）。

## 步骤 7：向我汇报
告诉我：注册是否成功、使用了哪种定时任务、验证结果。若任何一步失败，重试一次；仍失败则报告失败步骤和原始报错，不要静默跳过。
`

// buildTaskLoopPrompt 生成给已接入 Agent 的「补充任务能力」指令。
// 不含任何密钥：Agent 从它本地的 ~/.agent-matrix/config 读取凭证。
func buildTaskLoopPrompt(baseURL string) string {
	return strings.ReplaceAll(taskLoopTemplate, "{{BASE_URL}}", baseURL)
}

const taskLoopTemplate = `你已接入 Agent Matrix 监控中心（凭证在本机 $HOME/.agent-matrix/config）。现在为它补充「任务接收」能力，全部步骤幂等：

## 步骤 1：在心跳脚本里追加任务拉取
编辑 $HOME/.agent-matrix/heartbeat.sh，在心跳命令之后追加：

    resp=$(curl -fsS -m 15 "{{BASE_URL}}/api/agent/tasks" -H "Authorization: Bearer $AM_HB_TOKEN" 2>/dev/null || true)
    case "$resp" in *'"assignment_id"'*) printf '%s\n' "$resp" >> "$HOME/.agent-matrix/inbox.jsonl";; esac

## 步骤 2：在主循环里消费任务
$HOME/.agent-matrix/inbox.jsonl 每行 JSON 的 tasks 数组含若干 {"assignment_id":"tsa_...","title":"...","content":"..."}。content 是平台管理员发给你的指令本身，按你的能力执行它，完成后写回结果并把这行从 inbox 移除：

    . "$HOME/.agent-matrix/config"
    curl -fsS -m 30 -X POST "$AM_URL/api/agent/tasks/<assignment_id>/result" \
      -H "Authorization: Bearer $AM_HB_TOKEN" -H 'Content-Type: application/json' \
      -d '{"status":"done","result":"<执行结果摘要，纯文本，≤32KB>"}'

失败则 status 用 "failed" 并写明原因。规则：每个 assignment_id 只能成功写回一次；任务一经拉取即锁定给你；不要在定时脚本里同步执行任务。

## 步骤 3：验证并汇报
手动执行一次 heartbeat.sh，再手动调一次拉取接口确认返回 JSON（无任务时 tasks 为空数组），然后向我汇报结果。
`
