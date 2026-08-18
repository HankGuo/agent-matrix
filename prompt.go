package main

import "strings"

// buildPrompt 生成「一句话接入指令」。把这段指令交给任意具备 shell 执行能力的
// Agent（Claude Code / Kimi CLI / OpenClaw / Hermes 等），它会自行完成注册、
// 落盘配置并安装定时任务。指令全部幂等，可安全重复执行。
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

// taskRunnerScript 是任务执行器参考实现：目录锁防重入、拉取任务、调用 Agent 的
// 一次性 CLI 命令执行、按退出码机械回写。接入指令与补充指令共用这一段，避免漂移。
// 占位 {{BASE_URL}} 由调用方替换。
const taskRunnerScript = `#!/bin/sh
# task-runner.sh：拉取平台任务并执行，机械回写结果
lockdir="$HOME/.agent-matrix/runner.lock"
mkdir "$lockdir" 2>/dev/null || exit 0   # 一个任务没跑完，后续轮次直接跳过（串行执行）
trap 'rmdir "$lockdir"' EXIT

# 调度环境（cron/launchd/systemd）的 PATH 极窄，必须显式补全你 CLI 所在的目录
PATH="$HOME/.kimi-code/bin:$HOME/.npm-global/bin:$HOME/.local/bin:/usr/local/bin:/opt/homebrew/bin:$PATH"
export PATH

. "$HOME/.agent-matrix/config"

run_task() {
  kimi -p "$1"    # ← 换成你自己的一次性非交互执行命令，例如 claude -p "$1"
}

resp=$(curl -fsS -m 15 "$AM_URL/api/agent/tasks" -H "Authorization: Bearer $AM_HB_TOKEN" 2>/dev/null) || exit 0
printf '%s' "$resp" | python3 -c '
import sys, json, base64
for t in json.load(sys.stdin).get("tasks", []):
    print(t["assignment_id"] + " " + base64.b64encode(t["content"].encode()).decode())
' | while read -r aid b64; do
  content=$(printf '%s' "$b64" | base64 -d)
  output=$(run_task "$content" 2>&1) && st=done || st=failed
  payload=$(python3 -c 'import sys,json;print(json.dumps({"status":sys.argv[1],"result":sys.argv[2][-30000:]}))' "$st" "$output")
  curl -fsS -m 30 -X POST "$AM_URL/api/agent/tasks/$aid/result" \
    -H "Authorization: Bearer $AM_HB_TOKEN" -H 'Content-Type: application/json' \
    -d "$payload" || true
done`

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

## 步骤 4：任务执行器脚本
平台管理员会向你派发任务。写入 $HOME/.agent-matrix/task-runner.sh 并 chmod +x。拉取、执行、回写全部机械化，不依赖你记住任何约定：

` + "```sh\n" + taskRunnerScript + "\n```" + `

规则：
- run_task 必须换成你自己的一次性非交互执行命令（不许等待人工输入）；退出码非 0 视为失败，输出尾部 30KB 作为结果写回
- 任务一经拉取即锁定给你，重复拉取不会重复返回；每个 assignment_id 只能成功写回一次
- 没有 python3 就用 jq 或你可用的 JSON 工具改写解析部分，语义不变
- Windows：用 PowerShell 写等价逻辑（Invoke-RestMethod 拉取与回写）

## 步骤 5：安装每分钟定时任务（按操作系统选一种，已安装则跳过；heartbeat.sh 与 task-runner.sh 各装一条）
- Linux 有 cron：执行
    ( crontab -l 2>/dev/null | grep -v 'agent-matrix/' ; printf '* * * * * %s\n* * * * * %s\n' "$HOME/.agent-matrix/heartbeat.sh" "$HOME/.agent-matrix/task-runner.sh" ) | crontab -
- Linux 无 cron 但有 systemd：为 heartbeat.sh 和 task-runner.sh 各创建一对 ~/.config/systemd/user/agent-matrix-*.service + .timer（OnBootSec=60, OnUnitActiveSec=60），然后 systemctl --user daemon-reload 并 enable --now 两个 timer
- macOS：为两个脚本各创建一个 ~/Library/LaunchAgents/com.agent-matrix.*.plist（StartInterval 为 60），分别 launchctl load
- Windows：heartbeat.ps1 与 task-runner 的 PowerShell 等价物，各建一个每分钟触发的 schtasks

## 步骤 6：验证
1. 手动执行一次 $HOME/.agent-matrix/heartbeat.sh（无输出即正常）。
2. 再手动调一次心跳接口确认返回包含 "ok":true：
    . "$HOME/.agent-matrix/config" && curl -fsS -X POST "$AM_URL/api/heartbeat" -H "Authorization: Bearer $AM_HB_TOKEN"
3. 手动执行一次 $HOME/.agent-matrix/task-runner.sh（没有任务时应静默退出）。
4. 模拟调度器的窄 PATH 环境再跑一次——交互 shell 里发现不了的环境问题靠这步抓：
    env -i HOME="$HOME" /bin/sh "$HOME/.agent-matrix/task-runner.sh"
5. 手动调一次任务拉取接口确认返回 JSON（没有任务时 tasks 为空数组）：
    . "$HOME/.agent-matrix/config" && curl -fsS "$AM_URL/api/agent/tasks" -H "Authorization: Bearer $AM_HB_TOKEN"
6. 确认两条定时任务都已存在（crontab -l / launchctl list / systemctl --user list-timers / schtasks /query 之一，按你的平台）。

## 步骤 7：向我汇报
告诉我：注册是否成功、使用了哪种定时任务、run_task 用的是哪条命令、验证结果。若任何一步失败，重试一次；仍失败则报告失败步骤和原始报错，不要静默跳过。
`

// buildTaskLoopPrompt 生成给已接入 Agent 的「任务执行器安装指令」。
// 不含任何密钥：Agent 从它本地的 ~/.agent-matrix/config 读取凭证。
func buildTaskLoopPrompt(baseURL string) string {
	return strings.ReplaceAll(taskLoopTemplate, "{{BASE_URL}}", baseURL)
}

const taskLoopTemplate = `你已接入 Agent Matrix 监控中心（凭证在本机 $HOME/.agent-matrix/config）。现在为它安装「任务执行器」。全部步骤幂等：

## 步骤 1：写入 $HOME/.agent-matrix/task-runner.sh 并 chmod +x

` + "```sh\n" + taskRunnerScript + "\n```" + `

run_task 必须换成你自己的一次性非交互执行命令（不许等待人工输入）。

## 步骤 2：安装每分钟定时任务
与 heartbeat 相同的机制追加一条：cron 追加 ` + "`* * * * * $HOME/.agent-matrix/task-runner.sh`" + `；launchd / systemd / schtasks 复制对应单元改名指向 task-runner.sh。

## 步骤 3：清理旧机制（如果存在）
若你的 heartbeat.sh 里曾有向 inbox.jsonl 追加任务的段落（v0.4 旧机制），删掉那段并移除 inbox.jsonl——任务执行统一走 task-runner.sh。

## 步骤 4：验证并汇报
1. 手动执行一次 task-runner.sh（没有任务时应静默退出）。
2. 手动调一次拉取接口确认返回 JSON：
    . "$HOME/.agent-matrix/config" && curl -fsS "{{BASE_URL}}/api/agent/tasks" -H "Authorization: Bearer $AM_HB_TOKEN"
3. 向我汇报安装结果与 run_task 使用的命令。
`
