#!/bin/sh
# Agent Matrix 一键接入脚本（幂等，可安全重复执行）
#
# 用法：
#   新接入：AM_URL="https://matrix.example.com" AM_TOKEN="ame_…" AM_NAME="my-agent" sh setup.sh
#   升级/换装：sh setup.sh            （已有 ~/.agent-matrix/config 时跳过注册，直接更新脚本与定时任务）
#   指定执行器：AM_RUN_TASK='kimi -p "$1"' sh setup.sh
#
# 自动完成：注册换发凭证 → 落盘 ~/.agent-matrix/ → 安装心跳与任务执行器 →
#           安装每分钟定时任务（cron / launchd / systemd user timer）→ 自检。
set -u

PATH="$HOME/.kimi-code/bin:$HOME/.npm-global/bin:$HOME/.local/bin:/usr/local/bin:/opt/homebrew/bin:/usr/bin:/bin:$PATH"
export PATH

AM_URL="${AM_URL:-{{BASE_URL}}}"
AM_TOKEN="${AM_TOKEN:-}"
AM_NAME="${AM_NAME:-agent-$(hostname 2>/dev/null || echo unknown)}"
DIR="$HOME/.agent-matrix"
CFG="$DIR/config"

say() { printf '%s\n' "$*"; }
die() { say "FAIL: $*"; exit 1; }

# 直接运行仓库里的原始脚本（未经过服务器替换占位符）时给出明确提示
case "$AM_URL" in *"{{"*) die "AM_URL 未设置：请通过 GET /setup.sh 下载本脚本，或用 AM_URL=... 显式指定平台地址";; esac

command -v curl >/dev/null 2>&1 || die "需要 curl"
mkdir -p "$DIR" && chmod 700 "$DIR"
rm -f "$DIR/inbox.jsonl"   # 清理 v0.4 旧机制残留（如存在）

# ---- 1) 注册（已有配置则跳过） ----
if [ -s "$CFG" ]; then
  . "$CFG"
  say "== 已存在配置，跳过注册"
else
  [ -n "$AM_TOKEN" ] || die "缺少 AM_TOKEN（一次性注册令牌）"
  resp=$(curl -fsS -m 20 -X POST "$AM_URL/api/register" -H 'Content-Type: application/json' \
    -d "{\"token\":\"$AM_TOKEN\",\"name\":\"$AM_NAME\",\"hostname\":\"$(hostname)\",\"os\":\"$(uname -s)\",\"arch\":\"$(uname -m)\"}") \
    || die "注册失败（令牌无效、已用或过期）"
  HB=$(printf '%s' "$resp" | sed -n 's/.*"heartbeat_token":"\([^"]*\)".*/\1/p')
  [ -n "$HB" ] || die "注册响应缺少 heartbeat_token"
  AM_HB_TOKEN="$HB"
  say "== 注册成功"
fi

# ---- 2) 选定任务执行命令（写入 config，之后改命令只需编辑 config） ----
RUN_TASK="${AM_RUN_TASK:-}"
if [ -z "$RUN_TASK" ]; then
  if   command -v kimi     >/dev/null 2>&1; then RUN_TASK='kimi -p "$1"'
  elif command -v claude   >/dev/null 2>&1; then RUN_TASK='claude -p "$1"'
  elif command -v openclaw >/dev/null 2>&1; then RUN_TASK='openclaw agent --session-key "matrix-$2" --message "$1" --timeout 1800'
  elif command -v hermes   >/dev/null 2>&1; then RUN_TASK='hermes chat -q "$1" --quiet'
  else RUN_TASK='echo "config 里的 AM_RUN_TASK 是占位值，请填入你的一次性非交互执行命令（$1=任务内容 $2=任务ID）" >&2; exit 99'
  fi
fi
case "$RUN_TASK" in *"'"*) die "AM_RUN_TASK 不能包含单引号";; esac
printf 'AM_URL=%s\nAM_HB_TOKEN=%s\nAM_RUN_TASK=%s\n' "$AM_URL" "$AM_HB_TOKEN" "'$RUN_TASK'" > "$CFG"
chmod 600 "$CFG"
say "== 执行器: $RUN_TASK"

# ---- 3) 心跳脚本 ----
# 临时文件 + mv 原子替换：本脚本可能被「自升级任务」触发，此时 heartbeat.sh / task-runner.sh
# 可能正在运行；直接 cat > 截断改写会让运行中的 sh 实例读到错乱内容，mv 换 inode 则安全。
cat > "$DIR/.heartbeat.sh.tmp" <<'EOF'
#!/bin/sh
. "$HOME/.agent-matrix/config"
curl -fsS -m 15 -X POST "$AM_URL/api/heartbeat" -H "Authorization: Bearer $AM_HB_TOKEN" >/dev/null 2>&1 || true
EOF
mv -f "$DIR/.heartbeat.sh.tmp" "$DIR/heartbeat.sh"

# ---- 4) 任务执行器 ----
# 目录锁串行防重入；拉取 → eval AM_RUN_TASK 执行 → 按退出码机械回写。
cat > "$DIR/.task-runner.sh.tmp" <<'EOF'
#!/bin/sh
# task-runner.sh：拉取平台任务并执行，机械回写结果（由 Agent Matrix setup.sh 生成）
lockdir="$HOME/.agent-matrix/runner.lock"
mkdir "$lockdir" 2>/dev/null || exit 0   # 一个任务没跑完，后续轮次直接跳过（串行执行）
trap 'rmdir "$lockdir"' EXIT

# 调度环境（cron/launchd/systemd）的 PATH 极窄，显式补全 CLI 常见目录
PATH="$HOME/.kimi-code/bin:$HOME/.npm-global/bin:$HOME/.local/bin:/usr/local/bin:/opt/homebrew/bin:/usr/bin:/bin:$PATH"
export PATH

. "$HOME/.agent-matrix/config"

run_task() {
  # AM_RUN_TASK 在 config 里配置，$1=任务内容，$2=任务ID（tsk_…，按任务维持会话时用）
  eval "$AM_RUN_TASK"
}

resp=$(curl -fsS -m 15 "$AM_URL/api/agent/tasks" -H "Authorization: Bearer $AM_HB_TOKEN" 2>/dev/null) || exit 0
printf '%s' "$resp" | python3 -c '
import sys, json, base64
for t in json.load(sys.stdin).get("tasks", []):
    print(t["assignment_id"] + " " + t["task_id"] + " " + base64.b64encode(t["content"].encode()).decode())
' | while read -r aid tid b64; do
  content=$(printf '%s' "$b64" | base64 -d)
  output=$(run_task "$content" "$tid" 2>&1) && st=done || st=failed
  payload=$(python3 -c 'import sys,json;print(json.dumps({"status":sys.argv[1],"result":sys.argv[2][-30000:]}))' "$st" "$output")
  curl -fsS -m 30 -X POST "$AM_URL/api/agent/tasks/$aid/result" \
    -H "Authorization: Bearer $AM_HB_TOKEN" -H 'Content-Type: application/json' \
    -d "$payload" || true
done
EOF
mv -f "$DIR/.task-runner.sh.tmp" "$DIR/task-runner.sh"

chmod +x "$DIR/heartbeat.sh" "$DIR/task-runner.sh"
command -v python3 >/dev/null 2>&1 || say "== 警告: 未找到 python3，task-runner 的 JSON 解析依赖它（或自行改用 jq 重写）"

# ---- 5) 每分钟定时任务（heartbeat.sh 与 task-runner.sh 各一条） ----
SCHED="manual"
if [ "$(uname -s)" = "Darwin" ] && command -v launchctl >/dev/null 2>&1; then
  for unit in heartbeat task-runner; do
    plist="$HOME/Library/LaunchAgents/com.agent-matrix.$unit.plist"
    cat > "$plist" <<EOF
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
  <key>Label</key><string>com.agent-matrix.$unit</string>
  <key>ProgramArguments</key>
  <array><string>$DIR/$unit.sh</string></array>
  <key>StartInterval</key><integer>60</integer>
  <key>RunAtLoad</key><true/>
</dict>
</plist>
EOF
    launchctl unload "$plist" 2>/dev/null || true
    launchctl load "$plist" 2>/dev/null || true
  done
  SCHED="launchd"
elif command -v crontab >/dev/null 2>&1; then
  ( crontab -l 2>/dev/null | grep -v 'agent-matrix/'
    printf '* * * * * %s\n* * * * * %s\n' "$DIR/heartbeat.sh" "$DIR/task-runner.sh" ) | crontab -
  SCHED="cron"
elif command -v systemctl >/dev/null 2>&1; then
  udir="$HOME/.config/systemd/user"
  mkdir -p "$udir"
  for unit in heartbeat task-runner; do
    cat > "$udir/agent-matrix-$unit.service" <<EOF
[Service]
Type=oneshot
ExecStart=$DIR/$unit.sh
EOF
    cat > "$udir/agent-matrix-$unit.timer" <<EOF
[Timer]
OnBootSec=60
OnUnitActiveSec=60
[Install]
WantedBy=timers.target
EOF
  done
  systemctl --user daemon-reload 2>/dev/null \
    && systemctl --user enable --now agent-matrix-heartbeat.timer agent-matrix-task-runner.timer 2>/dev/null \
    && SCHED="systemd" || say "== 警告: systemd --user 操作失败，请手动 enable 两个 timer"
fi
[ "$SCHED" = "manual" ] && say "== 未识别的调度环境（Windows 或其它）：请手动为 heartbeat.sh 与 task-runner.sh 安装每分钟触发"
say "== 定时任务: ${SCHED}（每分钟 × 2）"

# ---- 6) 自检 ----
say "== 自检"
curl -fsS -m 15 -X POST "$AM_URL/api/heartbeat" -H "Authorization: Bearer $AM_HB_TOKEN" >/dev/null 2>&1 \
  && say "  心跳: ok" || say "  心跳: FAIL"
curl -fsS -m 15 "$AM_URL/api/agent/tasks" -H "Authorization: Bearer $AM_HB_TOKEN" >/dev/null 2>&1 \
  && say "  任务拉取: ok" || say "  任务拉取: FAIL"
# 模拟调度器的窄 PATH 环境（launchd 默认 PATH 只有 /usr/bin:/bin:/usr/sbin:/sbin）
env -i PATH=/usr/bin:/bin:/usr/sbin:/sbin HOME="$HOME" /bin/sh "$DIR/task-runner.sh" >/dev/null 2>&1 \
  && say "  窄环境 runner: ok" || say "  窄环境 runner: FAIL（检查 PATH 与 AM_RUN_TASK）"

say "AM_SETUP_DONE name=$AM_NAME sched=$SCHED"
