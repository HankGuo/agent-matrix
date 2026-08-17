# Agent Matrix

[English](README.en.md)

轻量的 **Agent 注册与在线状态监控中心**。核心思路：不需要在每台机器上安装 daemon——在 WebUI 里**一键生成一段接入指令（提示词）**，把它发给任意具备 shell 执行能力的 Agent（Claude Code / Kimi CLI / Codex / OpenClaw / Hermes……），Agent 会自己完成注册、落盘配置、安装定时心跳任务。你在 WebUI 里实时看到所有 Agent 的在线状态。

单二进制 + 嵌入式 SQLite + 内嵌 WebUI，**零外部运行时依赖**。

## 工作原理

```
浏览器 ──► Agent Matrix Server（任意一台服务器）
              │  ① 生成一次性注册令牌 + 接入指令
              ▼
        你把指令发给目标 Agent
              ▼
  Agent 自己执行: ② POST /api/register（核销令牌，换回心跳令牌）
                  ③ 写 ~/.agent-matrix/{config,heartbeat.sh}
                  ④ 安装 cron / launchd / systemd / schtasks 定时任务
                  ⑤ 每分钟 POST /api/heartbeat
              ▼
   WebUI 按「最后心跳时间」判定在线/离线（默认超时 3 分钟）
```

## 特性

- **提示词即安装器**：接入指令自带幂等校验与自检步骤，Agent 执行完会主动汇报结果
- **一次性注册令牌**：24 小时有效、只用一次；注册后换发独立心跳令牌，数据库只存哈希
- **纯 WebUI**：管理端只需要浏览器；15 秒自动刷新状态灯
- **跨平台心跳**：指令覆盖 Linux（cron / systemd user timer）、macOS（launchd）、Windows（schtasks）
- **可靠部署**：一个静态二进制 + 一个 SQLite 文件，systemd 拉起即可；内建优雅退出、限流、安全响应头

## 快速开始

### 构建

需要 Go ≥ 1.22（开发使用 1.26）：

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

打开 `http://localhost:8080`（或你的域名）。**首次访问会强制进入初始化页**：设置管理员账号和密码（PBKDF2-SHA256 加盐存储），作为后续登录凭证。初始化完成后，管理接口和仪表盘都只接受该账号登录。

### 环境变量

| 变量 | 默认值 | 说明 |
|---|---|---|
| `AGENT_MATRIX_ADDR` | `:8080` | HTTP 监听地址 |
| `AGENT_MATRIX_DB` | `./agent-matrix.db` | SQLite 数据库路径 |
| `AGENT_MATRIX_BASE_URL` | `http://localhost:8080` | 对外访问地址，用于生成接入指令 |
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

- **HTTPS 反代（Caddy）**：`matrix.example.com { reverse_proxy 127.0.0.1:8080 }`，防火墙只放行 443
- **备份**：备份 `AGENT_MATRIX_DB` 指向的单个文件即可

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
| `DELETE` | `/api/agents/{id}` | 会话 Cookie | 删除 Agent |
| `GET` | `/healthz` | 无 | 健康检查 |

## 定位与边界

Agent Matrix 只做**注册表 + 在线状态监控**，不做任务派发。需要派发/编排时可以与任务平台（如 Multica）共存：Matrix 回答「谁活着」，派单平台回答「谁在干活」。

## License

[MIT](LICENSE)
