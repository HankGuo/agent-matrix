# AGENTS.md

个人单用户的 Agent 工作台（私有化部署），不是多租户平台。改动时守住这些边界：

## 硬性约定

- **仅支持 Linux / macOS**（服务端与 Agent 机器同）；不为 Windows 兼容增加任何代码或分支
- **Agent 执行器仅支持 OpenClaw 与 Hermes Agent**；不做通用执行器扩展点、不留"未来支持更多"的口径
- 登记名称全局唯一，重名注册一律 409（禁止为重名共存做任何兼容逻辑）
- 单管理员单会话；不做多用户、RBAC、组织等概念

## 构建 / 测试 / 运行

```bash
go build -o agent-matrix .   # 构建单二进制（内嵌 WebUI 与 setup.sh）
go vet ./... && go test ./... # 提交前必跑
./agent-matrix                # 默认监听 :26817，首访强制初始化管理员
```

## 结构

- 根目录扁平单包（`package main`）：`main.go` 入口、`api.go` 路由与中间件、`store.go` SQLite、`tasks.go` / `attachments.go` / `auth.go` 按域分文件
- `web/` 是无构建原生前端（app.js / index.html / style.css），嵌进二进制
- `setup.sh` 是 Agent 侧幂等安装脚本，由 `/setup.sh` 原样下发；改它必须过 `sh -n setup.sh`
- `prompt.go` 的接入提示词模板与 README 的「接入指令示例」块保持逐字同步
