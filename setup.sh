#!/bin/sh
# Agent Matrix 一键接入脚本（幂等，可安全重复执行）
#
# 用法：
#   新接入：AM_URL="https://matrix.example.com" AM_TOKEN="ame_…" AM_NAME="my-agent" sh setup.sh
#   升级/换装：sh setup.sh            （已有 ~/.agent-matrix/config 时跳过注册，直接更新脚本与定时任务）
#   指定执行器：AM_RUN_TASK='openclaw agent --session-key "matrix-$2" --message "$1"' sh setup.sh
#
# 自动完成：注册换发凭证 → 落盘 ~/.agent-matrix/ → 安装心跳与任务执行器 →
#           安装每分钟定时任务（cron / launchd / systemd user timer）→ 自检。
set -u

PATH="$HOME/.npm-global/bin:$HOME/.local/bin:/usr/local/bin:/opt/homebrew/bin:/usr/bin:/bin:$PATH"
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

# ---- 1) 能力画像采集（随注册上报、随升级刷新，全部可选） ----
# 执行器与版本自动探测；人设/模型/技能由 Agent 通过 AM_PERSONA / AM_MODEL / AM_SKILLS 提供。
# JSON 交给 python3 组装，避免 shell 拼串的引号注入；无 python3 则跳过画像（不影响接入）。
EXECUTOR=""
if   command -v openclaw >/dev/null 2>&1; then EXECUTOR=openclaw
elif command -v hermes   >/dev/null 2>&1; then EXECUTOR=hermes
fi
EXECUTOR_VER=""
[ -n "$EXECUTOR" ] && EXECUTOR_VER=$("$EXECUTOR" --version 2>/dev/null | head -1 | sed 's/^[^0-9]*//; s/[^0-9.].*$//')
META=""
if command -v python3 >/dev/null 2>&1; then
  META=$(AM_PERSONA="${AM_PERSONA:-}" AM_MODEL="${AM_MODEL:-}" AM_SKILLS="${AM_SKILLS:-}" \
    AM_EXECUTOR="$EXECUTOR" AM_EXECUTOR_VER="$EXECUTOR_VER" python3 - <<'PYMETA'
import json, os
def clip(v, n): return (v or "").strip()[:n]
m = {}
if clip(os.environ.get("AM_EXECUTOR"), 32):     m["executor"] = clip(os.environ["AM_EXECUTOR"], 32)
if clip(os.environ.get("AM_EXECUTOR_VER"), 32): m["executor_version"] = clip(os.environ["AM_EXECUTOR_VER"], 32)
if clip(os.environ.get("AM_PERSONA"), 200):     m["persona"] = clip(os.environ["AM_PERSONA"], 200)
if clip(os.environ.get("AM_MODEL"), 64):        m["model"] = clip(os.environ["AM_MODEL"], 64)
skills = [s for s in (clip(x, 32) for x in os.environ.get("AM_SKILLS", "").split(",")) if s][:20]
if skills: m["skills"] = skills
print(json.dumps(m, ensure_ascii=False))
PYMETA
  )
fi

# ---- 2) 注册（已有配置则跳过） ----
if [ -s "$CFG" ]; then
  . "$CFG"
  say "== 已存在配置，跳过注册"
else
  [ -n "$AM_TOKEN" ] || die "缺少 AM_TOKEN（一次性注册令牌）"
  META_FIELD=""
  if [ -n "$META" ] && [ "$META" != "{}" ]; then
    META_FIELD=$(python3 -c 'import json,sys; print(",\"meta\":" + json.dumps(sys.argv[1], ensure_ascii=False))' "$META")
  fi
  resp=$(curl -fsS -m 20 -X POST "$AM_URL/api/register" -H 'Content-Type: application/json' \
    -d "{\"token\":\"$AM_TOKEN\",\"name\":\"$AM_NAME\",\"hostname\":\"$(hostname)\",\"os\":\"$(uname -s)\",\"arch\":\"$(uname -m)\"$META_FIELD}") \
    || die "注册失败（令牌无效、已用或过期）"
  HB=$(printf '%s' "$resp" | sed -n 's/.*"heartbeat_token":"\([^"]*\)".*/\1/p')
  [ -n "$HB" ] || die "注册响应缺少 heartbeat_token"
  AM_HB_TOKEN="$HB"
  say "== 注册成功"
fi

# ---- 3) 选定任务执行命令（写入 config，之后改命令只需编辑 config） ----
# 官方支持 OpenClaw 与 Hermes Agent 两家执行器，按顺序探测；两者都按任务 ID 绑定会话，
# 同一任务的追加轮自动携带上文：
#   openclaw: 走常驻 Gateway 的一个 agent turn，--session-key 即任务 ID
#   hermes:   经 hermes-round.sh 包装——首轮 chat -q 建会话并记录 session_id，后续轮 --resume 续上
RUN_TASK="${AM_RUN_TASK:-}"
if [ -z "$RUN_TASK" ]; then
  if   command -v openclaw >/dev/null 2>&1; then RUN_TASK='openclaw agent --session-key "matrix-$2" --message "$1" --timeout 1800'
  elif command -v hermes   >/dev/null 2>&1; then RUN_TASK="sh $DIR/hermes-round.sh \"\$1\" \"\$2\""
  else RUN_TASK='echo "未探测到 openclaw / hermes CLI：请先安装其一，或编辑 config 把 AM_RUN_TASK 改成你的一次性非交互执行命令（$1=任务内容 $2=任务ID）" >&2; exit 99'
  fi
fi
case "$RUN_TASK" in *"'"*) die "AM_RUN_TASK 不能包含单引号";; esac
printf 'AM_URL=%s\nAM_HB_TOKEN=%s\nAM_RUN_TASK=%s\n' "$AM_URL" "$AM_HB_TOKEN" "'$RUN_TASK'" > "$CFG"
chmod 600 "$CFG"
say "== 执行器: $RUN_TASK"

# ---- 4) 心跳脚本 ----
# 临时文件 + mv 原子替换：本脚本可能被「自升级任务」触发，此时 heartbeat.sh / task-runner.sh
# 可能正在运行；直接 cat > 截断改写会让运行中的 sh 实例读到错乱内容，mv 换 inode 则安全。
# 心跳收到 410（已下线）时自卸载：runner 忙则本轮推迟；卸定时任务、删整个 ~/.agent-matrix。
cat > "$DIR/.heartbeat.sh.tmp" <<'EOF'
#!/bin/sh
. "$HOME/.agent-matrix/config"
code=$(curl -sS -m 15 -o /dev/null -w '%{http_code}' -X POST "$AM_URL/api/heartbeat" \
  -H "Authorization: Bearer $AM_HB_TOKEN" 2>/dev/null) || exit 0
[ "$code" = "410" ] || exit 0

# 已被平台下线：runner 正在执行任务则本轮跳过，下一分钟心跳仍会 410，届时再卸
mkdir "$HOME/.agent-matrix/runner.lock" 2>/dev/null || exit 0

if [ "$(uname -s)" = "Darwin" ]; then
  for unit in heartbeat task-runner; do
    plist="$HOME/Library/LaunchAgents/com.agent-matrix.$unit.plist"
    launchctl unload "$plist" 2>/dev/null
    rm -f "$plist"
  done
elif command -v crontab >/dev/null 2>&1; then
  crontab -l 2>/dev/null | grep -v 'agent-matrix/' | crontab - 2>/dev/null
elif command -v systemctl >/dev/null 2>&1; then
  for unit in heartbeat task-runner; do
    systemctl --user disable --now "agent-matrix-$unit.timer" 2>/dev/null
    rm -f "$HOME/.config/systemd/user/agent-matrix-$unit.service" \
          "$HOME/.config/systemd/user/agent-matrix-$unit.timer"
  done
  systemctl --user daemon-reload 2>/dev/null
fi
cd / && rm -rf "$HOME/.agent-matrix"
EOF
mv -f "$DIR/.heartbeat.sh.tmp" "$DIR/heartbeat.sh"

# ---- 5) 任务执行器 ----
# shell 外壳负责目录锁/PATH/配置；主体逻辑在 python3 内嵌脚本里：
# 拉取 → 下载附件并生成编号清单 → eval AM_RUN_TASK 执行 → 回收产出文件上传 → 按退出码机械回写。
cat > "$DIR/.task-runner.sh.tmp" <<'EOF'
#!/bin/sh
# task-runner.sh：拉取平台任务并执行，机械回写结果（由 Agent Matrix setup.sh 生成）
lockdir="$HOME/.agent-matrix/runner.lock"
mkdir "$lockdir" 2>/dev/null || exit 0   # 一个任务没跑完，后续轮次直接跳过（串行执行）
trap 'rmdir "$lockdir"' EXIT

# 调度环境（cron/launchd/systemd）的 PATH 极窄，显式补全 CLI 常见目录
PATH="$HOME/.npm-global/bin:$HOME/.local/bin:/usr/local/bin:/opt/homebrew/bin:/usr/bin:/bin:$PATH"
export PATH

. "$HOME/.agent-matrix/config"
export AM_URL AM_HB_TOKEN AM_RUN_TASK
AM_FILES_DIR="${AM_FILES_DIR:-$HOME/.agent-matrix/files}"
export AM_FILES_DIR

python3 - <<'PYEOF'
import json, os, re, shutil, subprocess, sys, urllib.request

AM_URL = os.environ["AM_URL"].rstrip("/")
TOK = os.environ["AM_HB_TOKEN"]
FILES = os.environ.get("AM_FILES_DIR") or os.path.expanduser("~/.agent-matrix/files")
HDR = {"Authorization": "Bearer " + TOK}

def api(method, path, obj=None, timeout=40):
    data, h = None, dict(HDR)
    if obj is not None:
        data = json.dumps(obj).encode()
        h["Content-Type"] = "application/json"
    req = urllib.request.Request(AM_URL + path, data=data, headers=h, method=method)
    with urllib.request.urlopen(req, timeout=timeout) as r:
        return json.loads(r.read().decode() or "{}")

def sanitize(name):
    # 落盘文件名清洗：去路径分隔符与控制字符，限长，防空名
    name = re.sub(r"[\x00-\x1f/\\]", "_", (name or "").strip())
    return name[:80] or "file"

def human(n):
    return "%.1f MB" % (n / 1048576) if n >= 1048576 else "%.0f KB" % (n / 1024)

try:
    resp = api("GET", "/api/agent/tasks", timeout=20)
except Exception:
    sys.exit(0)  # 拉取失败（网络抖动等）本轮放弃，下一分钟再来

for t in resp.get("tasks", []):
    aid, tid = t["assignment_id"], t["task_id"]
    indir = os.path.join(FILES, tid, "in")
    outdir = os.path.join(FILES, tid, "out")
    os.makedirs(outdir, exist_ok=True)

    # 附件：下载为「序号-文件名」，清单编号/路径/描述三重锚定，注入给执行器的提示词
    manifest = ""
    atts = t.get("attachments") or []
    if atts:
        os.makedirs(indir, exist_ok=True)
        lines = ["", "", "## 附件清单（共 %d 个，已下载到本机）" % len(atts)]
        for i, a in enumerate(atts, 1):
            fpath = os.path.join(indir, "%02d-%s" % (i, sanitize(a.get("name"))))
            try:
                req = urllib.request.Request(AM_URL + a["url"], headers=HDR)
                with urllib.request.urlopen(req, timeout=300) as r, open(fpath, "wb") as f:
                    shutil.copyfileobj(r, f)
            except Exception as e:
                fpath = "（下载失败: %s）" % e
            lines.append("[附件%d] %s（%s）" % (i, a.get("name", ""), human(a.get("size") or 0)))
            lines.append("    路径: " + fpath)
            if a.get("description"):
                lines.append("    说明: " + a["description"])
        lines.append("请按编号引用附件（如「附件1」），用你可用的工具读取/提取内容；路径不可读或下载失败要如实回报，不要编造内容。")
        manifest = "\n".join(lines)

    prompt = t["content"] + manifest
    prompt += "\n\n## 产出\n如需产出文件，请写入目录 %s （执行结束后会被自动回收上传，已回收的文件移入该目录的 sent/ 子目录，后续轮次仍可读取），并在结果文本中按文件名引用说明。" % outdir

    # AM_RUN_TASK 是 config 里的一次性非交互执行命令：$1=任务内容 $2=任务ID（tsk_…）
    p = subprocess.run(["/bin/sh", "-c", 'eval "$AM_RUN_TASK"', "run_task", prompt, tid],
                       env=dict(os.environ), stdout=subprocess.PIPE,
                       stderr=subprocess.STDOUT, text=True, errors="replace")
    output = p.stdout or ""
    st = "done" if p.returncode == 0 else "failed"

    # 回收产出目录：逐个上传，成功后移入 sent/ 子目录（防后续轮次重复上传，文件仍可被读取）；
    # 上传失败不阻断结果回写，文件留在原地，下轮再试。
    uploaded = []
    sentdir = os.path.join(outdir, "sent")
    for fn in sorted(os.listdir(outdir)):
        fp = os.path.join(outdir, fn)
        if not os.path.isfile(fp):
            continue
        r = subprocess.run(["curl", "-fsS", "-m", "300", "-X", "POST",
                            AM_URL + "/api/agent/tasks/" + aid + "/outputs",
                            "-H", "Authorization: Bearer " + TOK,
                            "-F", "file=@" + fp],
                           stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
        if r.returncode == 0:
            os.makedirs(sentdir, exist_ok=True)
            os.replace(fp, os.path.join(sentdir, aid + "-" + fn))
            uploaded.append(fn)
    if uploaded:
        output += "\n\n[已回收产出文件: " + ", ".join(uploaded) + "]"

    try:
        api("POST", "/api/agent/tasks/" + aid + "/result",
            {"status": st, "result": output[-30000:]})
    except Exception:
        pass
PYEOF
EOF
mv -f "$DIR/.task-runner.sh.tmp" "$DIR/task-runner.sh"

# Hermes 会话包装：同一任务 ID 绑定同一 Hermes 会话，追加轮自动续上文。
# 首轮 hermes chat -q --quiet 建会话，从输出的 session_id 行取精确 ID 存档；
# 后续轮 --resume 续上；resume 失败（会话被清理等）自动降级为新会话，
# 取不到 session_id 时退化为每轮新会话，都不影响任务执行本身。
cat > "$DIR/.hermes-round.sh.tmp" <<'EOF'
#!/bin/sh
# hermes-round.sh "<指令>" "<任务ID>"（由 Agent Matrix setup.sh 生成）
MAP="$HOME/.agent-matrix/sessions"
mkdir -p "$MAP"
sidf="$MAP/$2"
outf="$MAP/.$2.out"
trap 'rm -f "$outf"' EXIT

sid=""
[ -s "$sidf" ] && sid=$(cat "$sidf")

if [ -n "$sid" ]; then
  hermes chat -q "$1" --quiet --source agent-matrix --resume "$sid" >"$outf" 2>&1
  if [ $? -eq 0 ]; then
    cat "$outf"
    exit 0
  fi
  rm -f "$sidf"   # 会话已失效：按新会话重跑
fi

hermes chat -q "$1" --quiet --source agent-matrix >"$outf" 2>&1
rc=$?
cat "$outf"
sid=$(sed -n 's/^session_id: //p' "$outf" | head -1)
if [ -n "$sid" ]; then
  printf '%s' "$sid" > "$sidf"
  hermes sessions rename "$sid" "matrix-$2" >/dev/null 2>&1 || true
fi
exit "$rc"
EOF
mv -f "$DIR/.hermes-round.sh.tmp" "$DIR/hermes-round.sh"

chmod +x "$DIR/heartbeat.sh" "$DIR/task-runner.sh" "$DIR/hermes-round.sh"
command -v python3 >/dev/null 2>&1 || say "== 警告: 未找到 python3，task-runner 整体依赖它"

# ---- 6) 每分钟定时任务（heartbeat.sh 与 task-runner.sh 各一条） ----
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

# ---- 7) 自检 ----
say "== 自检"
curl -fsS -m 15 -X POST "$AM_URL/api/heartbeat" -H "Authorization: Bearer $AM_HB_TOKEN" >/dev/null 2>&1 \
  && say "  心跳: ok" || say "  心跳: FAIL"
# 能力画像刷新：注册时上报过一次；升级重跑（跳过注册）时借带 meta 的心跳更新一次。
# 每分钟常规心跳不携带 meta，避免无谓流量。
if [ -n "$META" ] && [ "$META" != "{}" ]; then
  MBODY=$(python3 -c 'import json,sys; print(json.dumps({"meta": sys.argv[1]}, ensure_ascii=False))' "$META")
  curl -fsS -m 15 -X POST "$AM_URL/api/heartbeat" -H "Authorization: Bearer $AM_HB_TOKEN" \
    -H 'Content-Type: application/json' -d "$MBODY" >/dev/null 2>&1 \
    && say "  资料上报: ok" || say "  资料上报: FAIL（不影响使用，重跑本脚本可再试）"
fi
say "== 资料: executor=${EXECUTOR:-未知} version=${EXECUTOR_VER:-未知} model=${AM_MODEL:-未填} skills=${AM_SKILLS:-未填}"
curl -fsS -m 15 "$AM_URL/api/agent/tasks" -H "Authorization: Bearer $AM_HB_TOKEN" >/dev/null 2>&1 \
  && say "  任务拉取: ok" || say "  任务拉取: FAIL"
# 模拟调度器的窄 PATH 环境（launchd 默认 PATH 只有 /usr/bin:/bin:/usr/sbin:/sbin）
env -i PATH=/usr/bin:/bin:/usr/sbin:/sbin HOME="$HOME" /bin/sh "$DIR/task-runner.sh" >/dev/null 2>&1 \
  && say "  窄环境 runner: ok" || say "  窄环境 runner: FAIL（检查 PATH 与 AM_RUN_TASK）"

say "AM_SETUP_DONE name=$AM_NAME sched=$SCHED"
