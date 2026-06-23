<div align="center">

# GenericAgent (Go + Python)

**Go 核心引擎 + Python 技能层的混合架构 Agent 平台**

*高性能并发引擎 · Python 生态兼容 · SSE 流式调用 · Web 多用户 · 代码沙箱安全*

</div>

---

## 目录

- [架构概览](#架构概览)
- [环境要求](#环境要求)
- [快速部署](#快速部署)
- [配置说明](#配置说明)
- [编译与运行](#编译与运行)
- [项目结构](#项目结构)
- [Agent 高级能力](#agent-高级能力)
- [API 接口](#api-接口)
- [工具列表](#工具列表)
- [Python 技能层](#python-技能层)
- [Reflect 反思模块](#reflect-反思模块)
- [安全防护](#安全防护)
- [前端系统](#前端系统)
- [测试](#测试)
- [常见问题](#常见问题)

---

## 架构概览

```
┌──────────────────────────────────────────────────────────────┐
│                      Go 核心引擎 (Web Server)                │
│                                                              │
│  ┌──────────┐  ┌──────────┐  ┌──────────┐  ┌────────────┐  │
│  │  Agent   │  │   LLM    │  │   Tool   │  │   Memory   │  │
│  │  Loop    │→ │  Client  │→ │  Router  │  │  Manager   │  │
│  │(goroutine)│  │(SSE流式) │  │(调度中心) │  │  (分层)    │  │
│  └────┬─────┘  └──────────┘  └────┬─────┘  └────────────┘  │
│       │                            │                         │
│  ┌────▼──────────────────┐         │       ┌────────────┐   │
│  │ Agent 高级能力         │         │       │    Auth    │   │
│  │ ┌────────┐ ┌────────┐ │         │       │(JWT+Redis) │   │
│  │ │Context │ │  Goal  │ │         │       └────────────┘   │
│  │ │Manager │ │Tracker │ │         │                         │
│  │ └────────┘ └────────┘ │         │                         │
│  │ ┌────────┐ ┌────────┐ │         │                         │
│  │ │  Plan  │ │ Context│ │         │                         │
│  │ │  File  │ │Compact │ │         │                         │
│  │ └────────┘ └────────┘ │         │                         │
│  └───────────────────────┘         │                         │
│       │                            │                         │
│  ┌────▼────────────────────────────▼─────────────────────┐  │
│  │              Task Runtime (任务运行时)                 │  │
│  │  ┌──────┐ ┌──────┐ ┌──────────┐ ┌──────┐ ┌─────────┐ │  │
│  │  │Store │ │Recover│ │Worktree  │ │Timeout│ │Message  │ │  │
│  │  │(磁盘)│ │(恢复) │ │(git隔离) │ │(超时) │ │Router   │ │  │
│  │  └──────┘ └──────┘ └──────────┘ └──────┘ └─────────┘ │  │
│  └───────────────────────────────────────────────────────┘  │
│       │                                                      │
│  ┌────▼─────────┐  ┌──────────┐  ┌──────────┐  ┌────────┐  │
│  │   Web        │  │ Workspace│  │  Session  │  │  Code  │  │
│  │  Handler     │  │(文件管理) │  │ (多会话)  │  │ Sandbox│  │
│  │  (Gin)       │  │(用户隔离) │  │          │  │(安全)  │  │
│  └──────────────┘  └──────────┘  └──────────┘  └────────┘  │
│       │                                                      │
└───────┼──────────────────────────────────────────────────────┘
        │ 子进程 (exec.CommandContext)
        ▼
┌───────────────────────────┐    ┌───────────────────────────┐
│   Python 技能层            │    │   Reflect 反思模块         │
│                           │    │                           │
│  skills/bridge.py         │    │  reflect/scheduler.py     │
│  skills/test_skill.py     │    │  reflect/autonomous.py    │
│  skills/your_skill.py     │    │  reflect/goal_mode.py     │
│  memory/*.py              │    │  reflect/agent_team_*.py  │
└───────────────────────────┘    └───────────────────────────┘
```

**核心设计原则：**

- **Go 负责性能敏感层**：Agent 循环、LLM 通信、工具调度、并发管理、Web 服务
- **Python 负责生态丰富层**：技能脚本、浏览器控制、数据处理、AI/ML 库
- **子进程桥接**：Go 通过 `exec.CommandContext` 调用 Python 脚本，JSON 序列化通信
- **多用户隔离**：JWT 认证 + Redis 会话 + 工作空间路径隔离
- **代码沙箱**：黑名单 + 反混淆归一化，防止恶意代码执行
- **任务持久化**：磁盘状态存储 + 中断恢复 + worktree 隔离
- **多级上下文压缩**：microcompact → session memory → LLM 摘要 → 硬截断降级链
- **目标追踪 + 计划模式**：状态机驱动 + 审批工作流 + 周期提醒

---

## 环境要求

| 依赖 | 最低版本 | 说明 |
|:---|:---|:---|
| **Go** | 1.23+ | 核心引擎编译 |
| **Python** | 3.11 / 3.12 | 技能层运行（不支持 3.14） |
| **Redis** | 6.0+ | 验证码存储、会话管理 |
| **MySQL** / **SQLite** | 8.0+ / 3.x | 用户数据存储（二选一） |
| **Git** | 任意 | 代码获取 |

**操作系统：** Windows / Linux / macOS

---

## 快速部署

### 1. 克隆仓库

```bash
git clone https://github.com/1209244478/go-python-GenericAgent.git
cd GenericAgent
```

### 2. 安装 Go

**Windows：**

从 [https://go.dev/dl/](https://go.dev/dl/) 下载安装包，或使用包管理器：

```powershell
# Chocolatey
choco install golang

# Scoop
scoop install go
```

**Linux：**

```bash
sudo apt install golang-go
# 或从官方下载
wget https://go.dev/dl/go1.23.6.linux-amd64.tar.gz
sudo tar -C /usr/local -xzf go1.23.6.linux-amd64.tar.gz
export PATH=$PATH:/usr/local/go/bin
```

### 3. 安装 Python 依赖

```bash
python -m venv .venv
# Windows
.venv\Scripts\activate
# Linux/macOS
source .venv/bin/activate

pip install -e .
```

### 4. 配置环境变量

```bash
cp .env.example .env
```

编辑 `.env` 填入必要配置：

```ini
# LLM 配置（必填）
LLM_API_BASE=https://api.deepseek.com/v1
LLM_API_KEY=sk-your-api-key-here
LLM_MODEL=deepseek-chat

# 服务器配置
SERVER_PORT=8090

# JWT 密钥（必填，请使用随机字符串）
JWT_SECRET=your-random-secret-string

# Redis
REDIS_ADDR=localhost:6379

# SMTP 邮件（用于注册验证码）
SMTP_HOST=smtp.example.com
SMTP_PORT=465
SMTP_USER=your-email@example.com
SMTP_PASSWORD=your-email-password

# 数据库（默认 SQLite，可选 MySQL）
DB_DRIVER=sqlite
```

### 5. 编译运行

```bash
# 编译
go build -o ga-server ./cmd/server

# 运行
./ga-server

# 或指定配置文件
./ga-server --config /path/to/server.json
```

访问 `http://localhost:8090` 即可使用。

---

## 配置说明

### 环境变量 (.env)

| 变量 | 必填 | 默认值 | 说明 |
|:---|:---|:---|:---|
| `LLM_API_BASE` | ✅ | — | LLM API 端点 |
| `LLM_API_KEY` | ✅ | — | LLM API 密钥 |
| `LLM_MODEL` | ✅ | — | 模型名称 |
| `LLM_MAX_TOKENS` | ❌ | 8192 | 最大输出 token |
| `LLM_TEMPERATURE` | ❌ | 0.7 | 采样温度 |
| `LLM_STREAM` | ❌ | true | 是否流式输出 |
| `SERVER_PORT` | ❌ | 8090 | 服务端口 |
| `SERVER_HOST` | ❌ | 0.0.0.0 | 监听地址 |
| `JWT_SECRET` | ✅ | — | JWT 签名密钥 |
| `JWT_EXPIRE_HOURS` | ❌ | 72 | Token 过期时间（小时） |
| `REDIS_ADDR` | ❌ | localhost:6379 | Redis 地址 |
| `REDIS_PASSWORD` | ❌ | — | Redis 密码 |
| `SMTP_HOST` | ✅ | — | SMTP 服务器 |
| `SMTP_PORT` | ❌ | 465 | SMTP 端口 |
| `DB_DRIVER` | ❌ | sqlite | 数据库驱动（sqlite/mysql） |
| `DB_DSN` | ❌ | — | MySQL 连接串 |
| `DATA_DIR` | ❌ | ./data | 数据目录 |
| `SKILL_DIR` | ❌ | ./skills | 技能目录 |

### 多模型配置 (mykey.json)

如需多模型支持，可在项目根目录创建 `mykey.json`：

```json
{
  "native_oai_config": {
    "name": "gpt",
    "api_key": "sk-your-openai-key",
    "api_base": "https://api.openai.com/v1",
    "model": "gpt-4o",
    "max_tokens": 8192,
    "temperature": 0.7,
    "stream": true
  },
  "native_claude_config": {
    "name": "claude",
    "api_key": "sk-ant-your-key",
    "api_base": "https://api.anthropic.com",
    "model": "claude-sonnet-4-6",
    "max_tokens": 8192
  }
}
```

> 优先级：`.env` > `mykey.json`。当 `.env` 中配置了 LLM 变量时，将忽略 `mykey.json`。

---

## 编译与运行

### 编译

```bash
# 标准编译
go build -o ga-server ./cmd/server

# 减小体积
go build -ldflags="-s -w" -o ga-server ./cmd/server

# 交叉编译 Linux
GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build -o server_linux ./cmd/server

# 交叉编译 macOS
GOOS=darwin GOARCH=arm64 go build -o ga-server ./cmd/server
```

### 运行

```bash
# 默认运行
./ga-server

# 指定配置文件
./ga-server --config /opt/genericagent/server.json

# 详细日志
./ga-server -verbose
```

### systemd 服务 (Linux)

```ini
# /etc/systemd/system/genericagent.service
[Unit]
Description=GenericAgent Server
After=network.target

[Service]
Type=simple
ExecStart=/opt/genericagent/ga-server --config /opt/genericagent/server.json
WorkingDirectory=/opt/genericagent
Restart=on-failure
RestartSec=5

[Install]
WantedBy=multi-user.target
```

```bash
sudo systemctl enable genericagent
sudo systemctl start genericagent
```

---

## 项目结构

```
GenericAgent/
├── cmd/
│   ├── server/main.go          # Web 服务器入口
│   └── ga/main.go              # CLI 模式入口
├── internal/
│   ├── agent/                  # Agent 核心
│   │   ├── loop.go             # Agent 循环（goroutine + channel + 超时检查）
│   │   ├── context.go          # 多级上下文压缩（microcompact/session memory/LLM/硬截断）
│   │   ├── goal.go             # 目标追踪状态机（active/paused/done/failed）
│   │   └── plan.go             # 计划模式（文件持久化 + 审批工作流）
│   ├── llm/client.go           # LLM 客户端（SSE 流式解析）
│   ├── tool/router.go          # 工具路由 + Python 子进程调度 + 代码沙箱
│   ├── config/config.go        # 配置管理（.env / mykey.json 热加载）
│   ├── memory/manager.go       # 分层记忆管理
│   ├── task/                   # 任务持久化与编排
│   │   ├── task.go             # 任务状态/类型/隔离模式/CacheSafeParams 定义
│   │   ├── store.go            # 磁盘持久化（state.json + messages.json）
│   │   ├── runtime.go          # 任务运行时（管理活跃任务 + 订阅 + 审批信号）
│   │   ├── recovery.go         # 中断恢复（孤儿消息过滤 + transcript 重建）
│   │   ├── timeout.go          # 空闲超时监控 + 优雅关闭
│   │   ├── worktree.go         # git worktree 隔离（子任务独立工作树）
│   │   └── message_router.go   # 跨 agent 消息路由（teammate 通信 + shutdown 协议）
│   ├── frontend/hub.go         # 多前端并发 Hub
│   ├── auth/                   # 认证模块
│   │   ├── jwt.go              # JWT 令牌管理
│   │   ├── redis.go            # 验证码存储（尝试次数限制）
│   │   ├── email.go            # SMTP 邮件发送
│   │   └── user.go             # 用户/Session 数据库操作
│   ├── web/                    # Web 服务层
│   │   ├── handler.go          # HTTP 请求处理
│   │   ├── middleware.go       # JWT 认证中间件 + CORS
│   │   └── router.go           # API 路由定义
│   └── workspace/workspace.go  # 工作空间（路径解析 + 用户隔离）
├── web/                        # 前端静态文件
│   ├── app.html                # 主应用页面
│   ├── login.html              # 登录/注册页面
│   ├── css/style.css           # 样式
│   └── js/app.js               # 前端逻辑
├── reflect/                    # 反思模块（定时唤醒 Agent）
│   ├── scheduler.py            # 定时任务调度器
│   ├── autonomous.py           # 自主运行（用户离开 30 分钟触发）
│   ├── goal_mode.py            # 目标模式反思
│   └── agent_team_worker.py    # BBS 接单 worker
├── skills/                     # Python 技能层
│   ├── bridge.py               # 技能桥接基础
│   └── test_skill.py           # 测试技能
├── memory/                     # 记忆存储目录
│   ├── L4_raw_sessions/        # 会话归档压缩
│   ├── autonomous_operation_sop/  # 自主运行 SOP
│   └── skill_search/           # 技能搜索引擎
├── docs/                       # 文档
│   ├── GETTING_STARTED.md      # 入门指南
│   ├── SETUP_FEISHU.md         # 飞书集成
│   ├── installation.md         # 安装文档（英）
│   └── installation_zh.md      # 安装文档（中）
├── frontends/desktop/          # Tauri 桌面前端
├── assets/                     # 系统资源
│   ├── sys_prompt.txt          # 中文系统提示词
│   ├── sys_prompt_en.txt       # 英文系统提示词
│   └── tools_schema.json       # 工具 Schema
├── test_malicious_code.py      # 安全测试用恶意代码样本
├── integration_test.go         # 集成测试
├── deploy.py                   # 服务器部署脚本
├── .env.example                # 环境变量模板
├── go.mod                      # Go 模块定义
└── pyproject.toml              # Python 依赖定义
```

---

## Agent 高级能力

GenericAgent 在基础 Agent 循环之上，实现了八项工程化能力，覆盖长会话、并发、中断、计划、目标、超时等场景。

### 上下文管理（多级降级压缩）

[internal/agent/context.go](file:///c:/Users/wangrongzhou/Documents/Git/GenericAgent/internal/agent/context.go) 实现四级降级链，避免单点失败导致上下文溢出：

| 级别 | 策略 | 是否调 LLM | 说明 |
|:---|:---|:---|:---|
| L0 | microcompact | 否 | 裁剪超长 tool 结果，保留头部+尾部 |
| L1 | session memory | 否 | 本地提取关键信息（文件路径、代码引用、决策） |
| L2 | LLM 摘要 | 是 | 调用 LLM 生成结构化摘要 |
| L3 | 硬截断 | 否 | 保留 system + 最近 N 轮原文 |

- **递归守卫**：`recursionGuard` 防止 compact LLM 调用再次触发 compact
- **分级警告**：warning(70%) / error(85%) / hard(95%) 三级阈值
- **Token 估算**：中英文混合 + 工具调用 JSON + 图片附件精确估算

### 任务持久化

[internal/task/store.go](file:///c:/Users/wangrongzhou/Documents/Git/GenericAgent/internal/task/store.go) 将任务状态序列化到磁盘：

```
data/tasks/<taskID>/
├── state.json        # 任务元数据（状态/类型/时间戳/目标）
├── messages.json     # 消息历史
├── output.log        # 输出日志
└── plans/plan-<id>.md # 计划文件
```

- **原子写**：tmp + rename 保证状态文件一致性
- **内容替换**：`ContentReplacementState` 外置存储大 tool 结果
- **启动恢复**：`Restore()` 扫描磁盘，将 running 任务标记为 failed

### 并发能力

[internal/task/runtime.go](file:///c:/Users/wangrongzhou/Documents/Git/GenericAgent/internal/task/runtime.go) 管理多任务并发：

- **任务类型**：main / subagent（同步阻塞）/ teammate（异步协作）/ remote / monitor
- **隔离模式**：none（共享目录）/ worktree（git 独立工作树）
- **Abort 控制**：`context.Context` + `CombinedAbortSignal`（signal + timeout 组合）
- **订阅机制**：每个任务通过 channel 广播 `DisplayItem` 给前端

### 子任务编排

[internal/task/message_router.go](file:///c:/Users/wangrongzhou/Documents/Git/GenericAgent/internal/task/message_router.go) 实现跨 agent 通信：

- **寻址**：`to="all"` 广播 / `to="<name>"` 定向
- **Team 管理**：teamName → name → task 三层映射
- **shutdown 协议**：`[shutdown_request]` / `[shutdown_response]` 优雅关闭
- **消息 UI cap**：`TeammateMessagesUICap=50` 防止 inbox 内存爆炸
- **CacheSafeParams**：fork 子任务时对齐 model/systemPrompt/temperature/maxTokens，共享 LLM 缓存前缀

### 中断恢复

[internal/task/recovery.go](file:///c:/Users/wangrongzhou/Documents/Git/GenericAgent/internal/task/recovery.go) 处理进程崩溃后的状态重建：

- **孤儿消息过滤**：清理无 tool_result 的 tool_use、纯 thinking 消息、空白消息
- **内容还原**：从 `ContentReplacementState` 还原被压缩的 tool 结果
- **Transcript 重建**：从历史消息恢复目标追踪器和计划文件状态
- **Worktree 清理**：检测并清理 stale worktree

### 计划模式

[internal/agent/plan.go](file:///c:/Users/wangrongzhou/Documents/Git/GenericAgent/internal/agent/plan.go) 实现计划提交-审批工作流：

1. Agent 调用 `PlanSubmit` 提交计划文本
2. loop.go 阻塞等待 `waitForPlanApproval()` 信号
3. 用户审批通过 → 继续执行；拒绝 → 退出并返回 `PLAN_REJECTED`
4. 计划持久化到 `plans/plan-<taskID>.md`，供后续引用
5. `AllowedPrompts` 定义计划允许执行的命令前缀

### 目标追踪

[internal/agent/goal.go](file:///c:/Users/wangrongzhou/Documents/Git/GenericAgent/internal/agent/goal.go) 实现目标状态机：

| 状态 | 行为 |
|:---|:---|
| active | 每 N 轮注入目标提醒（默认 5 轮） |
| paused | 暂停提醒，可恢复 |
| done | 注入完成确认 |
| failed | 注入失败原因 |

- **LLM 完成判定**：`EvaluateCompletionWithLLM` 调用 LLM 判断目标是否达成（45s 超时）
- **周期提醒**：`ShouldRemind` 检查轮次间隔，避免每轮都注入
- **Transcript 恢复**：`RestoreFromTranscript` 从历史消息重建目标状态

### 超时控制

[internal/task/timeout.go](file:///c:/Users/wangrongzhou/Documents/Git/GenericAgent/internal/task/timeout.go) 实现多层超时：

- **空闲超时**：`IdleTimeoutMonitor` 长时间无活动自动暂停任务
- **组合信号**：`CombinedAbortSignal` 合并用户取消 + 超时信号
- **优雅关闭**：grace period 等待清理 + shutdown timeout 强制退出
- **任务时长**：`MaxDuration` + `time.AfterFunc` 限制单任务最大运行时间

---

## API 接口

### 认证

| 方法 | 路径 | 说明 |
|:---|:---|:---|
| POST | `/api/auth/send-code` | 发送邮箱验证码 |
| POST | `/api/auth/register` | 注册（邮箱+验证码+密码） |
| POST | `/api/auth/login` | 登录（邮箱+密码） |

### Agent

| 方法 | 路径 | 说明 |
|:---|:---|:---|
| POST | `/api/agent/run` | 同步运行 Agent |
| POST | `/api/agent/stream` | SSE 流式运行 Agent |
| GET | `/api/agent/ws` | WebSocket 运行 Agent |

### 会话管理

| 方法 | 路径 | 说明 |
|:---|:---|:---|
| GET | `/api/sessions` | 获取会话列表 |
| POST | `/api/sessions` | 创建新会话 |
| DELETE | `/api/sessions` | 删除会话（含历史文件） |

### 聊天记录

| 方法 | 路径 | 说明 |
|:---|:---|:---|
| GET | `/api/chat/history` | 获取当前会话聊天记录 |
| DELETE | `/api/chat/history` | 清空聊天记录 |

### 工作空间

| 方法 | 路径 | 说明 |
|:---|:---|:---|
| GET | `/api/workspace/files` | 列出文件 |
| GET | `/api/workspace/file` | 读取文件内容 |
| GET | `/api/workspace/preview` | 预览文件（HTML 安全渲染） |
| POST | `/api/workspace/upload` | 上传文件（50MB 限制） |
| GET | `/api/workspace/download` | 下载文件 |
| DELETE | `/api/workspace/file` | 删除文件 |

### 其他

| 方法 | 路径 | 说明 |
|:---|:---|:---|
| GET | `/api/user/profile` | 获取用户信息 |
| GET | `/api/templates` | 获取模板列表 |
| GET | `/api/skills` | 获取技能列表 |

> 所有 `/api/` 路径（除认证接口外）均需在 Header 中携带 `Authorization: Bearer <token>`。

---

## 工具列表

Go 引擎内置以下工具，通过 `ToolRouter.Dispatch()` 路由：

| 工具 | 说明 | 实现方式 |
|:---|:---|:---|
| `code_run` | 执行代码（Python / PowerShell / Bash） | Go 子进程 + 沙箱检测 |
| `file_read` | 读取文件（支持关键词定位） | Go 原生 |
| `file_write` | 写入文件（自动创建目录） | Go 原生 |
| `file_patch` | 局部修改文件（old→new 替换） | Go 原生 |
| `ask_user` | 询问用户（中断等待人工输入） | Go 原生 |
| `skill_run` | 调用 Python 技能脚本 | Go→Python 子进程 |
| `web_scan` | 网页感知（需 Python TMWebDriver） | Python 桥接 |
| `web_execute_js` | 浏览器 JS 执行（需 Python TMWebDriver） | Python 桥接 |
| `update_working_checkpoint` | 更新短期工作记忆 | Go 原生 |
| `goal_set` | 设置目标（启动目标追踪状态机） | Go 原生 |
| `goal_update` | 更新目标状态（pause/resume/complete/fail） | Go 原生 |
| `plan_submit` | 提交执行计划（触发审批工作流） | Go 原生 |
| `task_spawn` | 创建子任务（subagent/teammate，支持 worktree 隔离） | Go 原生 |
| `task_message` | 跨 agent 通信（teammate 消息路由） | Go 原生 |

---

## Python 技能层

### 技能调用机制

Go 引擎通过 `skill_run` 工具调用 `skills/` 目录下的 Python 脚本：

```
Go Agent → skill_run(args) → exec.CommandContext("python", "skills/xxx.py", argsJSON)
                                              ↓
                              Python 脚本读取 sys.argv[1]，解析 JSON
                                              ↓
                              Python 脚本执行逻辑，print(json.dumps(result))
                                              ↓
                              Go 解析 stdout JSON → StepOutcome
```

### 编写自定义技能

在 `skills/` 目录下创建 `.py` 文件：

```python
import sys
import json

def main():
    args = json.loads(sys.argv[1])
    result = {"status": "success", "data": "处理结果"}
    print(json.dumps(result, ensure_ascii=False))

if __name__ == "__main__":
    main()
```

调用方式：

```
skill_run({"skill": "my_skill", "param1": "value1"})
```

---

## Reflect 反思模块

`reflect/` 目录包含定时唤醒 Agent 的反思脚本，由调度器周期性触发：

| 脚本 | 触发周期 | 说明 |
|:---|:---|:---|
| [scheduler.py](file:///c:/Users/wangrongzhou/Documents/Git/GenericAgent/reflect/scheduler.py) | 120s | 定时任务调度器，扫描 `sche_tasks/` 目录执行 cron 任务 |
| [autonomous.py](file:///c:/Users/wangrongzhou/Documents/Git/GenericAgent/reflect/autonomous.py) | 1800s | 自主运行：用户离开 30 分钟后触发自动任务 |
| [goal_mode.py](file:///c:/Users/wangrongzhou/Documents/Git/GenericAgent/reflect/goal_mode.py) | — | 目标模式反思：检查目标进度 |
| [agent_team_worker.py](file:///c:/Users/wangrongzhou/Documents/Git/GenericAgent/reflect/agent_team_worker.py) | 60s | BBS 接单 worker：轮询新帖并唤醒 Agent |

### 反思脚本协议

每个反思脚本需定义：

```python
INTERVAL = 60      # 触发周期（秒）
ONCE = False       # 是否只执行一次

def check():
    """返回字符串则唤醒 Agent 并注入该文本；返回 None 不唤醒；返回 '/exit' 退出"""
    return "[REFLECT] 检测到新任务，请处理"

def init(args):
    """可选：初始化配置（接收 agent_team_setting.json）"""
    pass
```

### 定时任务调度

`scheduler.py` 扫描 `sche_tasks/` 目录下的 JSON 任务文件，支持：

- **repeat 模式**：once / daily / weekday / weekly / monthly / every_N
- **冷却防漂移**：冷却时间略短于实际周期
- **最大延迟窗口**：超过 `DEFAULT_MAX_DELAY=6` 小时不触发
- **端口锁**：bind 45762 端口防止重复启动

---

## 安全防护

### 代码执行沙箱

`code_run` 和 `skill_run` 工具内置代码安全检测，在执行前拦截恶意代码：

**Python 拦截规则：**

| 类别 | 拦截项 |
|:---|:---|
| 系统命令 | `os.system`, `os.popen`, `subprocess.*`, `__import__`, `exec()`, `eval()` |
| 文件操作 | `open()`, `pathlib.Path`, `shutil.rmtree`, `tempfile` |
| 网络通信 | `socket`, `requests`, `http.server`, `webbrowser`, `smtplib`, `telnetlib` |
| 反序列化 | `pickle.loads`, `base64.b64decode`, `ctypes` |
| 内省绕过 | `__builtins__`, `getattr()`, `globals()`, `locals()` |
| 数据库 | `sqlite3`, `mysql.connector`, `pymongo` |

**Shell 拦截规则：**

| 类别 | 拦截项 |
|:---|:---|
| 破坏命令 | `rm -rf`, `mkfs`, `dd`, `chmod`, `chown` |
| 网络工具 | `curl`, `wget`, `nc`, `ssh`, `scp`, `socat` |
| 系统管理 | `systemctl`, `shutdown`, `reboot`, `iptables`, `crontab` |
| 信息窃取 | `env`, `printenv`, `cat /etc/passwd`, `base64 -d` |
| 反向 Shell | `bash -i`, `dev/tcp`, `python -c`, `perl -e` |

**反混淆归一化：**

- 移除反斜杠转义：`r\m` → `rm`
- 移除引号混淆：`w'get` → `wget`
- 移除字符串拼接：`'op' + 'en'` → `open`
- 移除变量替换：`$()` → 空
- 正则精确匹配短命令（如 `env`）

### XSS 防护

- HTML 文件预览返回 `text/plain`，前端使用 `iframe.srcdoc` + `sandbox="allow-scripts"` 渲染
- iframe 仅允许脚本执行，禁止访问父页面 Cookie/Token

### 其他安全措施

| 措施 | 说明 |
|:---|:---|
| 路径穿越防护 | `skill_run` 验证 skillName 不含 `../`、`\`、空格 |
| 文件上传限制 | `MaxBytesReader` 限制 50MB |
| 验证码防爆破 | 10 次失败后锁定 5 分钟 |
| 用户工作空间隔离 | 文件操作基于用户 ID 隔离目录 |
| JWT 认证 | 所有 API（除登录/注册）需携带 Token |

---

## 前端系统

### Web 前端

内置响应式 Web 前端，支持桌面和移动端：

| 页面 | 路径 | 说明 |
|:---|:---|:---|
| 登录/注册 | `/login` | 邮箱验证码注册 + 密码登录 |
| 主应用 | `/` | Agent 对话 + 文件管理 + 技能列表 |

**主应用功能：**

- Agent 对话（SSE 流式输出）
- 多会话管理（侧边栏创建/切换/删除）
- 文件列表与预览（HTML 安全渲染、代码高亮）
- 文件上传/下载
- 技能列表

### CLI 前端

```bash
# 交互模式
./ga -verbose

# 一次性任务
./ga -task my_task -input "分析代码结构"
```

### Tauri 桌面前端

`frontends/desktop/` 包含基于 Tauri 的桌面 GUI 前端：

```bash
cd frontends/desktop
npm install
npm run tauri dev
```

---

## 测试

### 单元测试

```bash
# 运行工具路由测试
go test -v ./internal/tool/

# 运行安全测试
go test -v -run "TestSecurity" ./internal/tool/

# 运行所有测试
go test -v ./...
```

### 安全测试

安全测试覆盖 100+ 攻击模式：

| 测试函数 | 覆盖范围 |
|:---|:---|
| `TestSecurity_PythonBlockedPatterns` | 28 种 Python 恶意代码 |
| `TestSecurity_ShellBlockedPatterns` | 27 种 Shell 恶意命令 |
| `TestSecurity_SkillRunPathTraversal` | 5 种路径穿越攻击 |
| `TestSecurity_PythonNormalCodeAllowed` | 9 种正常 Python 代码（无误杀） |
| `TestSecurity_ShellNormalCodeAllowed` | 14 种正常 Shell 命令（无误杀） |
| `TestSecurity_NormalizationBypass` | 11 种反混淆绕过尝试 |
| `TestSecurity_MaliciousPythonFile` | 完整恶意文件 30 种攻击验证 |

### 集成测试

```bash
go test -v -timeout 60s -run TestIntegration .
```

---

## 常见问题

### Q: Go 编译报错 `go: command not found`

确认 Go 已安装并在 PATH 中：

```bash
go version
```

### Q: 如何切换数据库？

默认使用 SQLite（零配置），切换 MySQL 需在 `.env` 中设置：

```ini
DB_DRIVER=mysql
DB_DSN=user:password@tcp(127.0.0.1:3306)/dbname
```

### Q: Redis 连接失败

确认 Redis 已启动：

```bash
redis-cli ping
# 应返回 PONG
```

### Q: 邮件验证码发送失败

1. 检查 SMTP 配置是否正确
2. 确认 SMTP 端口（465 SSL / 587 TLS）
3. 部分邮箱需要开启"应用专用密码"

### Q: Python 技能调用失败

1. 确认 Python 在 PATH 中：`python --version`
2. 确认技能文件在 `skills/` 目录下
3. 确认技能脚本输出合法 JSON 到 stdout

### Q: 如何添加新工具？

在 `internal/tool/router.go` 的 `Dispatch` 方法中添加新的 case，同时在 `assets/tools_schema.json` 中添加工具描述。

### Q: LLM API 调用超时

调整 `.env` 中的超时参数：

```ini
LLM_CONNECT_TIMEOUT=60
LLM_READ_TIMEOUT=600
LLM_MAX_RETRIES=5
```

---

## 许可

MIT License
