# CLIProxy Antigravity ACP

通过官方 Antigravity CLI 的 ACP 协议，将 Gemini 接入 CLIProxyAPI（CPA），让聊天客户端和 Agent 沿用 CPA 的 API 地址与 API Key。

**当前版本：0.11.1** · [版本记录](CHANGELOG.md) · [Pi 配置](clients/pi/README.md) · [Codex 配置](clients/codex/README.md) · [CPA 主页面集成](management-center/README.md)

这是独立社区项目，不属于 Google 或 CPA 官方。使用官方 ACP 协议不等于获得 Google 背书，也不保证任意用途均符合账号套餐条款；部署者应自行确认账号及服务的使用权限。

## 功能概览

| 能力 | 当前支持 |
| --- | --- |
| 统一入口 | CPA API Key、模型前缀 `antigravity-acp/`，与其他 CPA provider 并存 |
| 聊天 | `/v1/chat/completions`，流式及非流式文本输出 |
| Agent | Chat Completions 函数工具；`/v1/responses` 函数及 custom 客户端工具 |
| 图片输入 | PNG、JPEG、WebP、GIF；Base64 或符合校验规则的公共 HTTPS URL |
| 会话复用 | 同一调用方、账号、模型、工具和历史前缀匹配时续接 ACP 会话 |
| 进程复用 | 独立请求可以复用已完成且空闲的 ACP 进程；新建隔离会话 |
| 并发控制 | 默认 4 个活动请求、16 个排队位置、30 秒排队超时 |
| 账号管理 | 官方 OAuth、账号名称与备注、手动切换、删除本地账号凭证、配额展示 |
| 请求可视化 | 实际模型、账号、来源 IP、耗时、命中状态、未复用原因和自动刷新 |
| 统计 | 请求总量、会话命中总量、命中率、逐请求会话与进程复用状态 |

模型目录来自当前账号的官方 ACP 会话，只暴露 Gemini。不同账号或 CLI 版本返回的模型可能不同，README 和客户端示例中的模型 ID 不代表可用性承诺。

## 架构

~~~mermaid
flowchart LR
    Client["聊天客户端 / Pi / Codex"] -->|"CPA API Key"| CPA["CLIProxyAPI"]
    CPA --> Plugin["ACP 原生插件"]
    Plugin -->|"私有 HTTP + 独立令牌"| Bridge["ACP Bridge"]
    Bridge -->|"JSON-RPC / stdio"| CLI["官方 agy acp"]
    CLI --> Gemini["Gemini"]
    Bridge -. "工具调用与结果转接" .-> Client
~~~

插件负责 CPA 注册、请求转换和管理页面；Bridge 管理并发、会话、账号及 CLI 子进程。Google 登录和模型请求由官方 CLI 完成，没有直接反代私有 Google 接口的后备通道。

客户端工具在客户端执行，沿用客户端自身的权限与审批。Bridge 拒绝原生服务端文件/命令工具权限，只转接已注册的客户端工具。仍应保留容器隔离。

## 已验证的组件版本

| 组件 | 版本或平台 |
| --- | --- |
| 插件 / Bridge | 0.11.1 |
| CLIProxyAPI | `eceasy/cli-proxy-api:v8.0.69` |
| Antigravity CLI | `agy 1.5.7`，ACP v1 |
| 构建环境 | Linux amd64、Go 1.26、CGO C 编译器 |
| 可选主面板 | Management Center v1.27.4，提交 `d06a110` |
| 可选前端构建 | Bun 1.4.3 |

这些是已验证的基线，并非对更新版本的兼容性承诺。官方安装脚本可能下载比基线更新的 CLI，应记录实际版本并在升级前验证。当前没有提供预编译发布包；下面从源码构建。

## 部署流程

以下命令在 **Linux amd64** 主机的 Bash 中执行，示例项目路径为 `/opt/cpa-acp`。需要 Docker Engine、Docker Compose v2、Git、curl、OpenSSL，以及可访问官方登录和模型服务的网络。

假设你已有正常运行的 CPA，并知道它的 Compose 文件、服务名、配置文件和管理密钥。未部署 CPA 时，先按 [CPA 官方文档](https://github.com/router-for-me/CLIProxyAPI) 完成基础部署，再接入本插件。

### 1. 获取源码与官方 CLI

~~~bash
sudo mkdir -p /opt/cpa-acp
sudo chown "$(id -u):$(id -g)" /opt/cpa-acp
git clone https://github.com/AndyYJF/cliproxy-antigravity-acp.git /opt/cpa-acp
cd /opt/cpa-acp
mkdir -p bin
~~~

按 [Antigravity 官方安装说明](https://antigravity.google/docs/getting-started) 安装 Linux CLI。官方默认安装目录为 `~/.local/bin`：

~~~bash
curl -fsSL https://antigravity.google/cli/install.sh -o /tmp/antigravity-install.sh
# 阅读安装脚本后执行
bash /tmp/antigravity-install.sh
"$HOME/.local/bin/agy" --version
install -m 755 "$HOME/.local/bin/agy" bin/agy
~~~

必须复制与目标主机架构匹配的 Linux 可执行文件，不能使用 Windows 或 macOS 版本。CLI 不包含在本仓库中。

### 2. 构建插件和 Bridge

使用容器提供 Go 与 C 编译环境，宿主机无需安装 Go：

~~~bash
cd /opt/cpa-acp
docker run --rm \
  -v "$PWD:/src" -w /src \
  golang:1.26-bookworm bash -lc '
    set -e
    go mod download
    CGO_ENABLED=1 go build -buildmode=c-shared -o bin/antigravity-acp.so ./cmd/plugin
    CGO_ENABLED=0 go build -o bin/acp-bridge ./cmd/bridge
  '
docker build -t cliproxy-antigravity-acp:0.11.1 .
docker run --rm --entrypoint /usr/local/bin/agy \
  cliproxy-antigravity-acp:0.11.1 --version
~~~

`bin/antigravity-acp.h` 是 CGO 生成的头文件，部署时不需要。插件是原生共享库，升级 CPA 或迁移架构后应重新检查 ABI 兼容性。

### 3. 初始化状态与内部令牌

内部 Bridge 令牌、CPA 管理密钥和客户端 API Key 是三种不同的凭证：

| 凭证 | 用途 | 放置位置 |
| --- | --- | --- |
| Bridge 令牌 | CPA 插件访问私有 Bridge | 同一个文件分别只读挂载到两个容器 |
| CPA 管理密钥 | 管理页面及管理 API | CPA 原有管理配置 |
| CPA API Key | 聊天客户端调用 `/v1/*` | 客户端环境变量或其密钥管理功能 |

仅在首次安装、令牌文件不存在时生成：

~~~bash
cd /opt/cpa-acp
mkdir -p state/home state/plugins/linux/amd64
if [ ! -f state/bridge-token ]; then
  (umask 077; openssl rand -hex 32 > state/bridge-token)
fi
sudo chown -R 10001:10001 state/home
sudo chmod 700 state/home
sudo chown 10001:10001 state/bridge-token
sudo chmod 400 state/bridge-token
install -m 755 bin/antigravity-acp.so \
  state/plugins/linux/amd64/antigravity-acp-v0.11.1.so
~~~

Bridge 默认以 UID/GID `10001:10001` 运行。CPA 容器也必须能读取令牌文件；如 CPA 使用非 root 用户，请按实际 UID/GID 设置共享只读访问权限，不要将令牌改为所有人可读。

`state/home` 保存官方 CLI 登录状态和账号数据，必须持久化。备份时将它与令牌视为敏感数据，不能提交到 Git。

### 4. 启动私有 Bridge

仓库提供的 [compose.yaml](compose.yaml) 不发布 Bridge 宿主机端口，只加入 CPA 共用的 Docker 网络：

~~~bash
cd /opt/cpa-acp
docker network inspect cpanet >/dev/null 2>&1 || docker network create cpanet
docker compose config --quiet
docker compose up -d bridge
docker compose ps
docker compose logs --tail=50 bridge
~~~

若已有其他网络名，在项目 `.env` 中设置：

~~~dotenv
ACP_NETWORK=your-existing-network
ACP_IMAGE=cliproxy-antigravity-acp:0.11.1
~~~

该网络必须允许 Bridge 访问官方服务，不要设置为禁止外网的 `internal: true`。Bridge 健康检查路径为 `/healthz`，容器内默认监听 `:8328`，包括健康检查在内的请求都需要内部 Bearer 令牌；可通过插件管理页检查状态。

### 5. 将插件接入 CPA

在 **CPA 自己的 Compose 文件**中，为现有服务追加以下挂载和网络；保留原来的配置、认证目录和其他挂载：

~~~yaml
services:
  cpa: # 替换为现有 CPA 服务名
    volumes:
      - /opt/cpa-acp/state/plugins:/CLIProxyAPI/plugins
      - /opt/cpa-acp/state/bridge-token:/run/antigravity-acp-token:ro
    networks:
      - cpanet

networks:
  cpanet:
    external: true
    name: cpanet
~~~

如果 CPA 已有插件目录挂载，将构建产物命名为 `antigravity-acp-v0.11.1.so` 并放进该目录的 `linux/amd64/` 子目录；不要再用一个空目录覆盖其他插件。

将 [deploy/cpa-plugin.example.yaml](deploy/cpa-plugin.example.yaml) 合并到 **CPA 配置文件**中：

~~~yaml
plugins:
  enabled: true
  dir: /CLIProxyAPI/plugins
  configs:
    antigravity-acp:
      enabled: true
      bridge_url: http://antigravity-acp-bridge:8328
      token_file: /run/antigravity-acp-token
      store:
        version: 0.11.1
~~~

`bridge_url` 和 `token_file` 位于插件配置根层级。`store.version` 选择同版本文件名（如 `antigravity-acp-v0.11.1.so`），必须与部署文件版本一致，不要将连接参数放进 `store`。

在 CPA Compose 所在目录执行 `docker compose up -d <CPA服务名>`，使新增挂载和网络生效；只重载插件不能新增容器挂载。通过 CPA 管理页面确认插件已加载。首次尚未登录 Google 时，模型目录为空是正常现象。

如果 CPA 配置了全局代理，确保 `antigravity-acp-bridge` 经 Docker 网络直连，并由该网络的 DNS 解析；不能让外部代理解析这个容器名。具体绕过规则取决于你的代理部署。

### 6. 完成官方 OAuth 并选择账号

独立管理页地址：

~~~text
https://your-cpa.example/v0/resource/plugins/antigravity-acp/overview
~~~

使用 CPA 管理密钥连接后：

1. 在凭证管理中新增账号，启动 OAuth。
2. 在浏览器打开页面提供的官方授权链接，完成登录。
3. 按页面提示提交授权码或完整回调 URL。授权任务约五分钟过期，可取消后重试。
4. 手动选择刚添加的账号；新增账号不会自动替换当前账号。
5. 刷新账号信息、配额和模型目录。
6. 在 CPA 插件管理中重新加载插件，必要时关闭再开启，使 CPA 模型注册同步。

也可在容器中执行官方登录流程：

~~~bash
cd /opt/cpa-acp
docker compose exec bridge agy acp --login
~~~

网页登录通常更便于管理多个账号。不要从其他部署复制未知凭证文件，或将 Google token 填入客户端 API Key。

OAuth 使用官方 `agy acp --login`；配额优先采用 ACP 提供的信息，必要时调用官方 CLI 的 `/usage` 命令。仓库没有自行请求私有 Google 配额接口。协议说明见 [官方 ACP 文档](https://antigravity.google/docs/ide/extensions/acp/)。

### 7. 可选：集成 CPA 主页面

原生插件已提供独立管理页。若希望在 CPA 主面板中复用登录、同步主题并直接执行账号与配额管理，需要构建本仓库提供的面板覆盖代码。

完整命令见 [management-center/README.md](management-center/README.md)。构建后将单文件产物部署为 CPA 的 `management.html`，并设置：

~~~yaml
remote-management:
  disable-auto-update-panel: true
~~~

否则 CPA 自动更新可能覆盖定制面板。面板仅代办固定白名单内的管理请求，不把 CPA 管理密钥传入插件 iframe。

### 8. 验证客户端请求

先查询模型目录；将地址和密钥替换为自己的值：

~~~bash
export CPA_BASE_URL='https://your-cpa.example'
read -rsp 'CPA API Key: ' CPA_API_KEY
echo
export CPA_API_KEY

curl --fail-with-body -sS "$CPA_BASE_URL/v1/models" \
  -H "Authorization: Bearer $CPA_API_KEY"
~~~

从返回目录选择真实存在的 `antigravity-acp/` 模型。下面的 `<MODEL_ID>` 必须替换：

~~~bash
curl --fail-with-body -sS "$CPA_BASE_URL/v1/chat/completions" \
  -H "Authorization: Bearer $CPA_API_KEY" \
  -H 'Content-Type: application/json' \
  -d '{
    "model": "antigravity-acp/<MODEL_ID>",
    "messages": [{"role": "user", "content": "请回复：连接正常"}],
    "stream": false
  }'
~~~

此步骤会发送一次真实模型请求。状态页和模型目录刷新不发送聊天请求。不要用真实账号进行高并发压测；本地测试使用模拟 ACP 子进程。

## 客户端接入

Base URL 使用原来的 CPA 地址并以 `/v1` 结尾，API Key 不变，模型选择 `antigravity-acp/` 前缀。

- 普通聊天客户端：选择 OpenAI 兼容接口，使用 `/v1/chat/completions`。
- [Pi Agent](clients/pi/README.md)：`openai-completions` 适配器实际调用 Chat Completions，并不是旧版 `/v1/completions`。
- [Codex CLI](clients/codex/README.md)：使用 Responses，携带完整历史和 `store=false`；本仓库提供能力目录与配置示例。

没有适配 Claude Code。客户端配置中预置的模型名称仅是示例，需按实际目录调整。

## 并发、会话和统计

Bridge Compose 中可修改以下环境变量，修改后执行 `docker compose up -d bridge`：

| 变量 | 默认值 | 含义 |
| --- | --- | --- |
| `ACP_MAX_CONCURRENCY` | `4` | 活动请求上限及会话池容量 |
| `ACP_QUEUE_CAPACITY` | `16` | 等待队列容量，`0` 表示不排队 |
| `ACP_QUEUE_TIMEOUT` | `30s` | 等待执行位置的最长时间 |
| `ACP_SESSION_REUSE` | `1` | 设为 `0` 关闭会话/进程复用路径 |
| `AGY_BINARY` | `/usr/local/bin/agy` | 官方 CLI 路径 |
| `BRIDGE_TOKEN_FILE` | `/run/acp-token` | 内部令牌文件 |
| `WORK_ROOT` | `/work` | 临时会话工作目录 |
| `HOME` | `/home/acp` | 持久化 CLI 状态目录 |

随仓库提供的 Compose 限制容器内存为 1536 MiB、进程数为 128。增加并发会增加 CLI 进程和内存需求，也可能触及上游账号限制；不能仅通过调大队列获得更多上游容量。队列满或超时会返回错误，客户端应按错误信息处理重试。

**会话命中**要求可信调用方作用域、账号、模型、接口、工具和指令一致，且完整历史能续接缓存。普通聊天及 Agent 工具回传均可命中。请求仍必须发送完整历史，缓存不是持久对话存储；历史编辑、分叉、超时、账号切换或请求取消可能导致重建。

**进程复用**适用于没有可续接历史的独立请求：当 CLI 声明支持 `session/close` 时，关闭已完成的旧会话并释放工作目录，再用同一进程创建新会话。忙碌或等待工具结果的会话不会被借用。它节省启动成本，但不计作会话命中。

空闲会话约五分钟回收。统计在 Bridge 重启后重置，请求总数包含被拒绝或失败的模型请求；管理查询不计入。会话命中率不是 Google 的 token 缓存命中率。适配器不提供精确 token 用量，客户端显示 0 token/费用不代表实际消耗为零。

路由页面显示的来源 IP 用于观察；代理头应由你信任的入口代理设置，它不是认证或缓存隔离依据。账号当前采用手动选择，不提供自动轮询或账号故障转移。

## 图片与其他兼容性边界

- 图片数量没有插件侧固定上限；单张解码后最多 **8 MiB**，实际发送的图片合计最多 **16 MiB**，HTTP 请求体最多 **24 MiB**。Base64 与历史消息也占请求体空间。
- 支持 Base64 和公共 HTTPS 图片 URL；远程 URL 需符合访问校验，包括 HTTPS/443，不能用于访问本机或内网资源。
- 图片可保留在消息和工具结果历史中。没有音视频、文件内容上传或图片输出支持。
- Responses 需要完整历史和 `store=false`；不支持 `previous_response_id`、后台响应及服务端存储响应。
- 一轮返回一个客户端工具调用，即使客户端传入 `parallel_tool_calls=true`。
- ACP 无法映射的生成控制参数会静默忽略，包括 token 上限、采样、reasoning effort、输出格式、stop、logprobs、penalties、n；仅返回一个输出。不提供这些参数的等效保证。
- 无效消息、错误工具数据、旧版/托管工具和不支持的协议模式仍会返回错误，不会一律吞掉。
- system/developer 指令以适配后的上下文送入 ACP，不具有原接口的原生特权角色语义。适配器包含中性助手身份描述规范化，不修改用户消息或工具数据；不保证隐藏客户端身份或改变平台识别结果。

## 升级与回滚

1. 记录当前源码提交、CLI 版本、镜像标签和插件版本。将 CPA 配置、当前插件及定制 `management.html` 备份到插件扫描目录之外；敏感状态应单独安全备份。
2. 拉取明确版本的源码，按构建步骤重新生成 Bridge 镜像与插件。使用新的镜像标签，保留旧镜像供回滚。
3. 等待路由页面中的活动请求结束，再更新 Bridge。重建 Bridge 会清空内存会话和统计，可能中断尚未结束的请求。
4. 部署与新版本号匹配的共享库文件。以下以 0.11.1 为例，后续升级请同步替换文件名中的版本号；使用临时文件再重命名，避免直接覆盖已加载库的内容：

~~~bash
cd /opt/cpa-acp
install -m 755 bin/antigravity-acp.so state/plugins/linux/amd64/antigravity-acp-v0.11.1.so.new
mv state/plugins/linux/amd64/antigravity-acp-v0.11.1.so.new state/plugins/linux/amd64/antigravity-acp-v0.11.1.so
~~~

5. 更新 `ACP_IMAGE`、CPA 插件 `store.version`，重新创建 Bridge 并在 CPA 中重新加载插件。若当前 CPA 未完成热重载，在维护窗口重新创建/重启 CPA。
6. 检查 Bridge 状态、账号、模型注册、版本及一次小请求。面板覆盖代码也变更时，按面板文档重新构建。

回滚时恢复旧镜像标签、插件、相应版本配置和面板文件；保留当前有效的凭证状态及其他 provider 配置。不要用旧的整份 CPA 配置覆盖升级后无关的修改。

仅停用本通道时，将 `plugins.configs.antigravity-acp.enabled` 设为 `false` 并重载 CPA，再停止 Bridge。无需删除其他 provider 或客户端 API Key。

## 常见问题

| 现象 | 排查方向 |
| --- | --- |
| 插件找不到或加载失败 | 检查 CPA 版本、架构、`plugins.dir`、`linux/amd64/antigravity-acp-v0.11.1.so` 和共享库权限 |
| Bridge 未连接 / 401 | 检查两个容器是否同网、DNS、内部令牌是否一致且可读；不要混用 CPA API Key |
| 登录成功但没有模型 | 手动选择账号、刷新目录，再重新加载 CPA 插件；以账号实际返回目录为准 |
| 主面板内管理请求失败 | 确认部署了匹配版本的面板覆盖代码；独立页可用于区分面板和 Bridge 问题 |
| 超出并发或等待超时 | 查看活动请求、排队和内存；降低客户端并发，避免无间隔重试 |
| 图片仍被拒绝 | 数量无限制不等于大小无限制；检查解码大小、累计大小、请求体与 URL 规则 |
| 会话命中为零 | 查看逐条未复用原因；独立单请求通常只可能命中进程复用 |
| token 用量为零 | 该协议路径不提供精确 token 计费数据，不能把零视为免费 |
| 升级后主面板恢复原版 | 检查 `remote-management.disable-auto-update-panel` 和静态页面挂载 |

## 开发与本地验证

主要目录：

~~~text
cmd/plugin/           CPA 原生插件、协议转换、嵌入式管理页面
cmd/bridge/           ACP 进程、会话、账号、并发和请求统计
clients/              Pi / Codex 接入配置
deploy/               可合并的 CPA 配置示例
management-center/    CPA 主面板覆盖代码和共享 UI 辅助函数
~~~

现有 Go 测试使用模拟 ACP 子进程，不需要 Google 登录；修改会话、图片和并发逻辑时可用它们验证，避免向真实账号施加高并发：

~~~bash
docker run --rm -v "$PWD:/src" -w /src \
  golang:1.26-bookworm go test ./cmd/bridge ./cmd/plugin
~~~

管理页面覆盖代码的测试与构建步骤见其 [README](management-center/README.md)。真实 CLI 与 Google 服务的行为仍需单独低频验证，模拟测试不能证明账号配额或上游模型可用性。

## 隐私、第三方组件与许可

仓库不包含登录凭证、API Key、运行状态、数据库、日志、个人服务器配置、截图或私有验证记录；这些内容应放在被忽略的本地目录。发布自己的分支前仍应检查新增文件和历史，不能仅依赖 `.gitignore`。

官方 CLI 可能在其 HOME 中持久化对话和运行数据，本项目不承诺零留存。管理页会显示账号名称、配额、来源 IP 和请求信息，对外分享截图或导出数据时请自行去敏。

CPA ABI 参考代码及管理页面共享辅助函数的许可分别保留于 [LICENSE-CPA](LICENSE-CPA)、[management-center/shared/LICENSE](management-center/shared/LICENSE)，详见 [NOTICE](NOTICE)。Antigravity CLI 由官方分发，适用其自身条款。本仓库原创部分尚未单独声明许可；公开可见不代表授予额外使用许可。
