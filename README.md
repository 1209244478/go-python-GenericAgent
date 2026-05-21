<div align="center">

# GenericAgent (Go + Python)

**Go 核心引擎 + Python 技能层的混合架构 Agent**

*高性能并发引擎 · Python 生态兼容 · SSE 流式调用 · 子进程技能桥接*

</div>

---

## 目录

- [架构概览](#架构概览)
- [环境要求](#环境要求)
- [快速部署](#快速部署)
- [配置说明](#配置说明)
- [编译与运行](#编译与运行)
- [项目结构](#项目结构)
- [工具列表](#工具列表)
- [Python 技能层](#python-技能层)
- [前端系统](#前端系统)
- [测试](#测试)
- [从 Python 版本迁移](#从-python-版本迁移)
- [常见问题](#常见问题)

---

## 架构概览

```
┌─────────────────────────────────────────────────────────┐
│                    Go 核心引擎                           │
│                                                         │
│  ┌──────────┐  ┌──────────┐  ┌──────────┐  ┌────────┐ │
│  │  Agent   │  │   LLM    │  │   Tool   │  │ Memory │ │
│  │  Loop    │→ │  Client  │→ │  Router  │  │Manager │ │
│  │(goroutine)│  │(SSE流式) │  │(调度中心) │  │(分层)  │ │
│  └──────────┘  └──────────┘  └────┬─────┘  └────────┘ │
│                                    │                    │
│  ┌──────────┐  ┌──────────┐       │       ┌────────┐  │
│  │  Config  │  │ Frontend │       │       │  Hub   │  │
│  │(热加载)  │  │  Hub     │←──────┘       │(广播)  │  │
│  └──────────┘  └──────────┘               └────────┘  │
│                                    │                    │
└────────────────────────────────────┼────────────────────┘
                                     │ 子进程 (exec.CommandContext)
                                     ▼
                         ┌───────────────────────┐
                         │   Python 技能层        │
                         │                       │
                         │  skills/bridge.py     │
                         │  skills/test_skill.py │
                         │  skills/your_skill.py │
                         │  memory/*.py          │
                         │  TMWebDriver.py       │
                         └───────────────────────┘
```

**核心设计原则：**

- **Go 负责性能敏感层**：Agent 循环、LLM 通信、工具调度、并发管理
- **Python 负责生态丰富层**：技能脚本、浏览器控制、数据处理、AI/ML 库
- **子进程桥接**：Go 通过 `exec.CommandContext` 调用 Python 脚本，JSON 序列化通信

---

## 环境要求

| 依赖 | 最低版本 | 说明 |
|:---|:---|:---|
| **Go** | 1.23+ | 核心引擎编译 |
| **Python** | 3.11 / 3.12 | 技能层运行（不支持 3.14） |
| **Git** | 任意 | 代码获取 |

**操作系统：** Windows / Linux / macOS

---

## 快速部署

### 1. 克隆仓库

```bash
git clone https://github.com/lsdefine/GenericAgent.git
cd GenericAgent
```

### 2. 安装 Go

**Windows：**

从 [https://go.dev/dl/](https://go.dev/dl/) 下载安装包，或使用包管理器：

```powershell
# 方式一：官方安装包（推荐）
# 下载 go1.23.6.windows-amd64.msi 并安装

# 方式二：Chocolatey
choco install golang

# 方式三：Scoop
scoop install go
```

**Linux：**

```bash
# Ubuntu/Debian
sudo apt install golang-go

# 或从官方下载
wget https://go.dev/dl/go1.23.6.linux-amd64.tar.gz
sudo tar -C /usr/local -xzf go1.23.6.linux-amd64.tar.gz
export PATH=$PATH:/usr/local/go/bin
```

**macOS：**

```bash
brew install go
```

### 3. 安装 Python 依赖

```bash
# 创建虚拟环境（推荐）
python -m venv .venv
# Windows
.venv\Scripts\activate
# Linux/macOS
source .venv/bin/activate

# 安装依赖
pip install -e .
```

### 4. 配置 API 密钥

Go 引擎使用 `mykey.json`（而非 Python 版的 `mykey.py`）：

```bash
# 如果你已有 mykey.py，可自动转换：
python -c "import mykey; import json; json.dump({k:v for k,v in vars(mykey).items() if not k.startswith('_')}, open('mykey.json','w'), indent=2, ensure_ascii=False)"
```

或手动创建 `mykey.json`：

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
  }
}
```

**配置字段说明：**

| 字段 | 必填 | 默认值 | 说明 |
|:---|:---|:---|:---|
| `api_key` | ✅ | — | LLM API 密钥 |
| `api_base` | ✅ | — | API 端点（自动拼接 `/v1/chat/completions`） |
| `model` | ✅ | — | 模型名称 |
| `name` | ❌ | 同 model | 显示名称 |
| `max_tokens` | ❌ | 8192 | 最大输出 token |
| `temperature` | ❌ | 0 | 采样温度 |
| `stream` | ❌ | true | 是否流式输出 |
| `context_win` | ❌ | 128000 | 上下文窗口大小 |
| `connect_timeout` | ❌ | 30 | 连接超时（秒） |
| `read_timeout` | ❌ | 300 | 读取超时（秒） |
| `max_retries` | ❌ | 3 | 重试次数 |

**多模型配置示例：**

```json
{
  "native_claude_config": {
    "name": "claude",
    "api_key": "sk-ant-your-key",
    "api_base": "https://api.anthropic.com",
    "model": "claude-sonnet-4-6",
    "max_tokens": 8192,
    "temperature": 1.0
  },
  "native_oai_config": {
    "name": "gpt",
    "api_key": "sk-your-openai-key",
    "api_base": "https://api.openai.com/v1",
    "model": "gpt-4o",
    "max_tokens": 8192
  }
}
```

### 5. 编译

```bash
go build -o ga.exe ./cmd/ga
```

---

## 编译与运行

### 编译

```bash
# 标准编译
go build -o ga.exe ./cmd/ga

# 减小体积（去除调试信息）
go build -ldflags="-s -w" -o ga.exe ./cmd/ga

# 交叉编译 Linux
GOOS=linux GOARCH=amd64 go build -o ga ./cmd/ga

# 交叉编译 macOS
GOOS=darwin GOARCH=arm64 go build -o ga ./cmd/ga
```

### 运行

```bash
# 交互模式（REPL）
./ga.exe

# 详细输出模式
./ga.exe -verbose

# 指定 LLM 编号（多模型配置时）
./ga.exe -llm_no 1

# 一次性任务模式
./ga.exe -task my_task -input "分析这个项目的代码结构"

# 强制中文系统提示词
GA_LANG=zh ./ga.exe

# 强制英文系统提示词
GA_LANG=en ./ga.exe
```

### 交互模式命令

| 命令 | 说明 |
|:---|:---|
| 直接输入文本 | 发送给 Agent 执行 |
| `/stop` | 中断当前任务 |
| `/exit` 或 `/quit` | 退出程序 |

---

## 项目结构

```
GenericAgent/
├── cmd/ga/main.go              # Go 主入口
├── internal/
│   ├── agent/loop.go           # Agent 核心循环（goroutine + channel）
│   ├── llm/client.go           # LLM 客户端（SSE 流式解析）
│   ├── tool/router.go          # 工具路由 + Python 子进程调度
│   ├── config/config.go        # 配置管理（mykey.json 热加载）
│   ├── memory/manager.go       # 分层记忆管理
│   └── frontend/hub.go         # 多前端并发 Hub
├── skills/                     # Python 技能层
│   ├── bridge.py               # 技能桥接基础
│   └── test_skill.py           # 测试技能
├── assets/                     # 系统资源
│   ├── sys_prompt.txt          # 中文系统提示词
│   ├── sys_prompt_en.txt       # 英文系统提示词
│   ├── tools_schema.json       # 中文工具 Schema
│   └── tools_schema_cn.json    # 工具 Schema（中文版）
├── memory/                     # 记忆存储目录
├── frontends/desktop/          # Tauri 桌面前端
├── reflect/                    # 反射/调度模块（Python 版）
├── integration_test.go         # 集成测试
├── go.mod                      # Go 模块定义
├── mykey.json                  # API 密钥配置（需自行创建）
└── mykey_template.py           # Python 版配置模板（参考用）
```

---

## 工具列表

Go 引擎内置以下工具，通过 `ToolRouter.Dispatch()` 路由：

| 工具 | 说明 | 实现方式 |
|:---|:---|:---|
| `code_run` | 执行代码（Python / PowerShell / Bash） | Go 子进程 |
| `file_read` | 读取文件（支持关键词定位） | Go 原生 |
| `file_write` | 写入文件（自动创建目录） | Go 原生 |
| `file_patch` | 局部修改文件（old→new 替换） | Go 原生 |
| `ask_user` | 询问用户（中断等待人工输入） | Go 原生 |
| `skill_run` | 调用 Python 技能脚本 | Go→Python 子进程 |
| `web_scan` | 网页感知（需 Python TMWebDriver） | Python 桥接 |
| `web_execute_js` | 浏览器 JS 执行（需 Python TMWebDriver） | Python 桥接 |
| `update_working_checkpoint` | 更新短期工作记忆 | Go 原生 |

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
# skills/my_skill.py
import sys
import json

def main():
    # 从命令行参数获取 JSON
    args = json.loads(sys.argv[1])

    # 你的逻辑
    result = {
        "status": "success",
        "data": "处理结果"
    }

    # 输出 JSON 结果
    print(json.dumps(result, ensure_ascii=False))

if __name__ == "__main__":
    main()
```

调用方式（LLM 自动生成或手动触发）：

```
skill_run({"skill": "my_skill", "param1": "value1"})
```

### 技能参数约定

- 参数通过 `sys.argv[1]` 传入，为 JSON 字符串
- 输出通过 `stdout` 返回，必须是合法 JSON
- 错误通过 `sys.exit(1)` + stderr 返回
- 超时默认 120 秒（可在 `runPythonSkill` 中调整）

---

## 前端系统

### Go 内置前端

| 前端 | 说明 |
|:---|:---|
| `CLIFrontend` | 命令行交互（默认） |
| `QueueFrontend` | 队列前端（供程序化消费） |

### Frontend Hub 广播

所有前端通过 `Hub` 注册，Agent 输出自动广播到所有前端：

```go
hub := frontend.NewHub()
hub.Register(frontend.NewCLIFrontend(true))
hub.Register(frontend.NewQueueFrontend("api", 128))

// Agent 输出自动广播
for item := range agent.Run(query, "user") {
    hub.Broadcast(item)
}
```

### Tauri 桌面前端

`frontends/desktop/` 包含基于 Tauri 的桌面 GUI 前端，需要 Rust 工具链编译：

```bash
cd frontends/desktop
npm install
npm run tauri dev
```

---

## 测试

### 单元测试

```bash
# 运行工具路由单元测试
go test -v ./internal/tool/

# 运行所有包的测试
go test -v ./...
```

### 集成测试

集成测试使用 `httptest` 模拟 LLM API 服务器，验证完整链路：

```bash
# 运行全部集成测试
go test -v -timeout 60s -run TestIntegration .

# 运行单个场景
go test -v -run TestIntegration_FileWriteReadPatch .
go test -v -run TestIntegration_MultiToolChain .
```

**集成测试场景：**

| 测试 | 场景 | 验证链路 |
|:---|:---|:---|
| `FileWriteReadPatch` | 文件写入→读取→局部修改 | Agent→LLM→file_write→file_read→file_patch |
| `CodeRun` | Python 代码执行 | Agent→LLM→code_run→Python 子进程 |
| `SkillRun` | Python 技能调用 | Agent→LLM→skill_run→Python 子进程→JSON |
| `ErrorRecovery` | 读取失败→自动创建 | Agent→LLM→file_read(失败)→file_write(恢复) |
| `FrontendHubBroadcast` | 多前端并发广播 | Agent→Hub→2个QueueFrontend |
| `MemoryWithAgent` | 记忆注入系统提示词 | Memory→SystemPrompt→Agent |
| `MultiToolChain` | 三步工具链 | file_write→code_run→file_write |
| `Abort` | 中断长任务 | Agent.Abort()→goroutine 退出 |
| `FileReadWithKeyword` | 关键词定位读取 | file_read(keyword)→精确行号 |
| `MemoryCompress` | 历史标签压缩 | 5237→556 字符，近期保留 |

### 代码检查

```bash
go vet ./...
```

---

## 从 Python 版本迁移

### 配置迁移

```bash
# 自动转换 mykey.py → mykey.json
python -c "import mykey; import json; json.dump({k:v for k,v in vars(mykey).items() if not k.startswith('_')}, open('mykey.json','w'), indent=2, ensure_ascii=False)"
```

### 字段映射

| Python (mykey.py) | Go (mykey.json) | 说明 |
|:---|:---|:---|
| `apikey` | `api_key` | JSON 使用 snake_case |
| `apibase` | `api_base` | 同上 |
| `context_win` | `context_win` | 保持一致 |
| `max_tokens` | `max_tokens` | 保持一致 |
| `temperature` | `temperature` | 保持一致 |
| `stream` | `stream` | 保持一致 |
| `max_retries` | `max_retries` | 保持一致 |

### 功能对应

| Python 模块 | Go 模块 | 状态 |
|:---|:---|:---|
| `agent_loop.py` | `internal/agent/loop.go` | ✅ 已替代并删除 |
| `llmcore.py` | `internal/llm/client.go` | ✅ 已替代并删除 |
| `ga.py` (工具) | `internal/tool/router.go` | ✅ 已替代并删除 |
| `agentmain.py` | `cmd/ga/main.go` | ✅ 已替代并删除 |
| `hub.pyw` | `internal/frontend/hub.go` | ✅ 已替代并删除 |
| `plugins/hooks.py` | `internal/frontend/hub.go` | ✅ 已替代并删除 |
| `frontends/*.py` | `internal/frontend/hub.go` | ✅ 已替代并删除 |
| `ga_cli/` | `cmd/ga/main.go` | ✅ 已替代并删除 |
| `TMWebDriver.py` | `skills/` 桥接 | ⚠️ 需通过 skill_run 调用 |
| `reflect/autonomous.py` | 待实现 | ⏳ 可通过 Python 桥接使用 |

---

## 常见问题

### Q: Go 编译报错 `go: command not found`

确认 Go 已安装并在 PATH 中：

```bash
go version
# 如未找到，添加到 PATH：
# Windows: set PATH=C:\Go\bin;%PATH%
# Linux/macOS: export PATH=$PATH:/usr/local/go/bin
```

### Q: 提示 `mykey.json not found`

Go 引擎不读取 `mykey.py`，需要创建 `mykey.json`。参考 [配置说明](#配置说明)。

### Q: 提示 `mykey.py detected: please convert to mykey.json`

项目根目录同时存在 `mykey.py` 和 `mykey.json` 时，Go 引擎会优先检测 `mykey.py` 并提示转换。删除 `mykey.py` 或完成转换即可。

### Q: Python 技能调用失败

1. 确认 Python 在 PATH 中：`python --version`
2. 确认技能文件在 `skills/` 目录下
3. 确认技能脚本输出合法 JSON 到 stdout
4. 检查 stderr 输出（Go 会捕获并返回）

### Q: 如何使用桌面 GUI？

Go 引擎通过 CLI 交互，桌面 GUI 通过 Tauri 前端运行：

```bash
# CLI 模式
./ga.exe -verbose

# 桌面 GUI 模式
cd frontends/desktop && npm install && npm run tauri dev
```

### Q: 如何添加新工具？

在 `internal/tool/router.go` 的 `Dispatch` 方法中添加新的 case：

```go
func (r *Router) Dispatch(toolName string, args map[string]any, ...) *agent.StepOutcome {
    switch toolName {
    // ... 现有工具
    case "my_new_tool":
        return r.doMyNewTool(args)
    default:
        return &agent.StepOutcome{
            Data:       nil,
            NextPrompt: fmt.Sprintf("未知工具 %s", toolName),
        }
    }
}
```

同时在 `cmd/ga/main.go` 的 `defaultToolsSchema()` 中添加工具描述。

### Q: LLM API 调用超时

调整 `mykey.json` 中的超时参数：

```json
{
  "native_oai_config": {
    "connect_timeout": 60,
    "read_timeout": 600,
    "max_retries": 5
  }
}
```

---

## 许可

MIT License
