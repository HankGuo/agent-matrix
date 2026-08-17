# Agent Matrix

[English](README.en.md)

轻量的 **Agent 注册与在线状态监控中心**。核心思路：不需要在每台机器上安装 daemon——在 WebUI 里**一键生成一段接入指令（提示词）**，把它发给任意具备 shell 执行能力的 Agent（Claude Code / Kimi CLI / Codex / OpenClaw / Hermes……），Agent 会自己完成注册、落盘配置、安装定时心跳任务。你在 WebUI 里实时看到所有 Agent 的在线状态。

单二进制 + 嵌入式 SQLite + 内嵌 WebUI，**零外部运行时依赖**。

## 整体架构

```mermaid
flowchart LR
    subgraph 控制端["控制端（无需安装任何软件）"]
        B["浏览器 · 纯 WebUI"]
    end

    subgraph 中心服务器
        S["agent-matrix 单二进制<br/>HTTP API + 内嵌 WebUI"]
        DB[("嵌入式 SQLite<br/>WAL · 单文件")]
        S <--> DB
    end

    subgraph 被纳管的 Agent 机器
        G1["云服务器 A<br/>cron → heartbeat.sh"]
        G2["云服务器 B<br/>systemd timer → heartbeat.sh"]
        G3["本地 macOS<br/>launchd → heartbeat.sh"]
    end

    B -->|"HTTPS 管理"| S
    G1 -->|"POST /api/heartbeat（每分钟）"| S
    G2 -->|"POST /api/heartbeat（每分钟）"| S
    G3 -->|"POST /api/heartbeat（每分钟）"| S
    B -.->|"① 复制接入指令（带一次性令牌）"| P[" "]
    P -.->|"② 粘贴给目标 Agent 执行"| G1
```

关键点：Server 与 Agent 机器之间**只有 Agent 主动外拨的心跳**（POST），没有任何入站连接要求；接入指令经管理员剪贴板带外传递，不依赖 Agent 侧预装任何组件。

## 接入流程

```mermaid
flowchart TD
    A["WebUI 点击「接入新 Agent」"] --> B["生成一次性令牌 ame_…<br/>+ 完整接入指令"]
    B --> C["管理员复制指令，粘贴给目标 Agent"]
    C --> D["① POST /api/register<br/>核销令牌，换发心跳令牌 amh_…"]
    D --> E["② 落盘 ~/.agent-matrix/config(600) + heartbeat.sh"]
    E --> F["③ 安装定时任务<br/>cron / launchd / systemd / schtasks"]
    F --> G["④ 手动触发一次心跳，验证返回 ok:true"]
    G --> H{"验证通过？"}
    H -- 是 --> I["Agent 汇报成功 · WebUI 亮绿灯"]
    H -- 否 --> J["重试一次；仍失败则带原始报错汇报"]
```

## 注册与心跳时序

```mermaid
sequenceDiagram
    autonumber
    participant A as 管理员（浏览器）
    participant S as Agent Matrix Server
    participant G as 目标 Agent

    A->>S: POST /api/setup（仅首次访问：初始化管理员账号）
    A->>S: POST /api/login（账号 + 密码）
    A->>S: POST /api/enrollments（生成接入指令）
    S-->>A: 一次性令牌 ame_… + 接入指令文本
    A->>G: 粘贴指令（带外通道：IM / SSH / 控制台）
    G->>S: POST /api/register（核销一次性令牌）
    S-->>G: agent_id + 心跳令牌 amh_… + 建议间隔 60s
    G->>G: 写 config 与 heartbeat.sh，安装定时任务
    loop 每分钟
        G->>S: POST /api/heartbeat（Bearer amh_…）
        S-->>G: {"ok": true, "server_time": …}
    end
    A->>S: GET /api/agents（WebUI 每 15s 轮询）
    S-->>A: 在线状态（最后心跳 ≤ 3 分钟判定在线）
```

## 接入指令示例

WebUI 生成的指令长这样（真实输出，一字未改）。复制后原样发给目标 Agent 即可：

<details>
<summary>点开查看完整接入指令</summary>

```text
请把你自己接入 Agent Matrix 监控中心。你是运行在某台机器上的 AI Agent，具备执行 shell 命令和创建定时任务的能力。严格按以下步骤执行，每步都要验证结果；所有步骤幂等，可安全重复执行。

## 接入信息
- 平台地址: https://matrix.example.com
- 一次性注册令牌: ame_NkKcSim-6v60yDcTs2E1IqQGKNcE9Ogg  （24 小时内有效，仅能成功使用一次）
- 登记名称: cloud-a

## 步骤 1：注册
先设置变量，再调用注册接口（没有 curl 就换 wget 或你可用的 HTTP 客户端）：

    AM_URL="https://matrix.example.com"
    AM_TOKEN="ame_NkKcSim-6v60yDcTs2E1IqQGKNcE9Ogg"
    curl -fsS -X POST "$AM_URL/api/register" -H 'Content-Type: application/json' \
      -d "{\"token\":\"$AM_TOKEN\",\"name\":\"cloud-a\",\"hostname\":\"$(hostname)\",\"os\":\"$(uname -s)\",\"arch\":\"$(uname -m)\"}"

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

## 步骤 5：验证
1. 手动执行一次 $HOME/.agent-matrix/heartbeat.sh（无输出即正常）。
2. 再手动调一次心跳接口确认返回包含 "ok":true：
    . "$HOME/.agent-matrix/config" && curl -fsS -X POST "$AM_URL/api/heartbeat" -H "Authorization: Bearer $AM_HB_TOKEN"
3. 确认定时任务已存在（crontab -l / launchctl list / systemctl --user list-timers / schtasks /query 之一，按你的平台）。

## 步骤 6：向我汇报
告诉我：注册是否成功、使用了哪种定时任务、验证结果。若任何一步失败，重试一次；仍失败则报告失败步骤和原始报错，不要静默跳过。
```

</details>

## 特性

- **提示词即安装器**：接入指令自带幂等校验与自检步骤，Agent 执行完会主动汇报结果
- **首次访问强制初始化**：无任何账号时 WebUI 只开放初始化页，密码 PBKDF2-SHA256 加盐存储
- **一次性注册令牌**：24 小时有效、只用一次；注册后换发独立心跳令牌，数据库只存哈希
- **纯 WebUI**：管理端只需要浏览器；15 秒自动刷新状态灯
- **跨平台心跳**：指令覆盖 Linux（cron / systemd user timer）、macOS（launchd）、Windows（schtasks）
- **可靠部署**：一个静态二进制 + 一个 SQLite 文件，systemd 拉起即可；内建优雅退出、限流、安全响应头

## 快速开始

### 构建

需要 Go ≥ 1.24（开发使用 1.26）：

```bash
git clone https://github.com/HankGuo/agent-matrix.git
cd agent-matrix
go build -o agent-matrix .
```

也可直接 `go install github.com/HankGuo/agent-matrix@latest`。

### 运行

```bash
export AGENT_MATRIX_BASE_URL='https://matrix.example.com'  # 对外地址，写入接入指令
./agent-matrix
```

打开 `http://localhost:26817`（或你的域名）。**首次访问会强制进入初始化页**：设置管理员账号和密码（PBKDF2-SHA256 加盐存储），并可直接填写平台地址。初始化完成后，管理接口和仪表盘都只接受该账号登录；平台地址之后可在右上角「设置」中随时修改。

### 环境变量

| 变量 | 默认值 | 说明 |
|---|---|---|
| `AGENT_MATRIX_ADDR` | `:26817` | HTTP 监听地址 |
| `AGENT_MATRIX_DB` | `./agent-matrix.db` | SQLite 数据库路径 |
| `AGENT_MATRIX_BASE_URL` | `http://localhost:26817` | 对外访问地址，用于生成接入指令。部署后也可在 WebUI「设置」里修改，**WebUI 设置优先于环境变量** |
| `AGENT_MATRIX_ONLINE_TIMEOUT` | `3m` | 超过该时长无心跳判定离线 |
| `AGENT_MATRIX_ADMIN_TOKEN` | 空（可选） | 应急登录令牌。设置后登录页可用它替代账号密码；用于忘记密码等场景，不设置则只有账号密码一条路 |

### 生产部署建议

- **systemd**（`Restart=always`）：

```ini
[Unit]
Description=Agent Matrix
After=network.target

[Service]
Environment=AGENT_MATRIX_BASE_URL=https://matrix.example.com
Environment=AGENT_MATRIX_DB=/var/lib/agent-matrix/agent-matrix.db
ExecStart=/usr/local/bin/agent-matrix
Restart=always
RestartSec=3

[Install]
WantedBy=multi-user.target
```

- **HTTPS 反代（Caddy）**：`matrix.example.com { reverse_proxy 127.0.0.1:26817 }`，防火墙只放行 443
- **备份**：备份 `AGENT_MATRIX_DB` 指向的单个文件即可（含账号、Agent 列表与会话密钥）

## 使用流程

1. WebUI 右上角「接入新 Agent」→ 起名（可选）→ 生成接入指令 → 复制
2. 把指令**原样发给目标 Agent**（在它的对话窗口里粘贴即可）
3. Agent 执行完会汇报「注册成功、定时任务类型、验证结果」
4. 回到 WebUI，状态灯变绿即接入完成

> 注意：目标 Agent 必须具备**执行 shell 命令和创建定时任务**的能力。只能对话、无法执行命令的 Agent（例如某些厂商托管的 IM 机器人）无法自行接入——这是机制决定的，不是配置问题。

## API 速览

| 方法 | 路径 | 鉴权 | 说明 |
|---|---|---|---|
| `POST` | `/api/register` | 一次性注册令牌 | Agent 注册，换发心跳令牌 |
| `POST` | `/api/heartbeat` | `Bearer amh_…` | 心跳上报（可选携带 `meta` JSON） |
| `GET` | `/api/auth/status` | 无 | 查询是否需要初始化、是否启用应急令牌 |
| `POST` | `/api/setup` | 仅未初始化时可用 | 首次访问创建管理员账号 |
| `POST` | `/api/login` | 账号密码 / 应急令牌 | WebUI 登录，种会话 Cookie |
| `GET` | `/api/agents` | 会话 Cookie | Agent 列表（含在线状态） |
| `POST` | `/api/enrollments` | 会话 Cookie | 生成一次性令牌 + 接入指令 |
| `GET` / `POST` | `/api/settings` | 会话 Cookie | 读取 / 修改平台地址 |
| `DELETE` | `/api/agents/{id}` | 会话 Cookie | 删除 Agent |
| `GET` | `/healthz` | 无 | 健康检查 |

## 定位与边界

Agent Matrix 只做**注册表 + 在线状态监控**，不做任务派发。需要派发/编排时可以与任务平台（如 Multica）共存：Matrix 回答「谁活着」，派单平台回答「谁在干活」。

## License

[MIT](LICENSE)
