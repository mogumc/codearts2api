# codearts2api

把华为云 CodeArts [<https://codearts.huaweicloud.com/>] 的模型接口（盘古开源大模型 2.0、GLM、DeepSeek 等）包装成**标准 OpenAI Chat Completions API**。

底层走华为云 `SDK-HMAC-SHA256` 请求签名，AK/SK 存放在 `.env`。Go 实现，零第三方依赖（仅标准库）。

> [!TIP]  
> 免费 CodeArts 同时只支持 5 个并发，请注意不要多开会话使用。

## 使用方法

### 1. 获取程序

**方式一：下载预编译版本**（推荐）

到 [Releases](https://github.com/moguqaq/codearts2api/releases) 下载对应平台：

| 平台 | 文件 |
|---|---|
| Windows (x64) | `codearts2api_windows_amd64.zip` |
| Linux (x64) | `codearts2api_linux_amd64.tar.gz` |
| Linux (ARM64) | `codearts2api_linux_arm64.tar.gz` |
| macOS (Apple Silicon) | `codearts2api_darwin_arm64.tar.gz` |

每个包内含可执行文件与 `.env.example`，`checksums.txt` 提供 SHA256 校验。

**方式二：从源码编译**

```cmd
go build -o codearts2api.exe .
```

**方式三：Docker**

```cmd
docker run -d --name codearts2api ^
  -p 8787:8787 ^
  -e CODEARTS_AK=你的AccessKey ^
  -e CODEARTS_SK=你的SecretKey ^
  -v codearts2api-data:/data ^
  ghcr.io/moguqaq/codearts2api:latest
```

镜像已内置 `CODEARTS_PROXY_HOST=0.0.0.0` 与 `/data` 数据目录，挂载 volume 即可持久化模型缓存与签到状态。也支持 `linux/amd64` 与 `linux/arm64`。

查看版本：`codearts2api -version`

### 2. 配置 AK/SK

复制 `.env.example` 为 `.env`：

```ini
CODEARTS_AK=你的AccessKey
CODEARTS_SK=你的SecretKey
```

如何获取 AK/SK：进入 <https://console.huaweicloud.com/iam/#/mine/accessKey> ，登录后「新增访问密钥」即可。

**请勿泄露你的 AK/SK，这具有华为云账号的最高访问权限。**

### 3. 启动代理

```cmd
codearts2api.exe
```

默认监听 `http://127.0.0.1:8787`。

### 4. 在各类工具中接入

把 OpenAI 兼容 Base URL 指向：

```text
http://127.0.0.1:8787/v1
```

- API Key：任意非空字符串（例如 `codearts`）
- 模型：`openpangu-2.0-pro`

支持 OpenCode、Cline、Continue、LobeChat、Cherry Studio 等所有支持自定义 OpenAI 兼容端点的工具。

## 免费额度签到

```cmd
codearts2api.exe benefit          # 状态 + 签到 + 余额
codearts2api.exe benefit status   # 只查询
codearts2api.exe benefit claim    # 只签到
codearts2api.exe benefit models   # 查询免费模型列表
```

### 关于「自动领取」

**代理默认会在每天首个请求时顺带签到一次**（`CODEARTS_AUTO_CLAIM=1`，默认开启）。

这个设计是为了避免常驻定时器的开销：代理不跑后台定时任务，而是在收到请求时做一次**极轻量的内存判断**（比对内存里的日期字符串，不触网、不读盘）。只有当天还没签到过，才会在**后台 goroutine** 里发起签到，因此不会拖慢用户请求。

签到结果写入 `benefit-state.json`（可用 `CODEARTS_BENEFIT_STATE` 改路径）：

```json
{
  "last_success_date": "2026-10-16",
  "last_attempt_at": 1792131513598,
  "last_claim_time": 1789539411000,
  "last_error": ""
}
```

- `last_success_date`：最后签到成功的日期，决定今天是否还需要签到
- `last_attempt_at`：最后一次尝试时间（含失败），用于失败退避
- `last_claim_time`：上游返回的 `create_time`
- `last_error`：最后一次失败原因，成功后清空

**幂等与退避**：同一天只签到一次，重启进程也不会重复签（状态持久化在磁盘）。签到失败后 10 分钟内不会重试，避免上游异常时每个请求都去打签到接口。上游若返回「今日已领取」类错误，按成功处理，当天不再重试。

启动日志会显示当前签到状态：

```text
自动签到: 今日尚未签到，将在首个请求时自动签到
自动签到: 今日已签到（2026-10-16 09:05:12）
```

**其他方式**：

- 关闭自动签到：设 `CODEARTS_AUTO_CLAIM=0`，改用下面的手动/计划任务方式
- 手动执行：`codearts2api.exe benefit claim`
- 计划任务（代理没启动也能签到，更可靠）：

```cmd
schtasks /create /tn CodeArtsBenefit /tr "C:\path\to\codearts2api.exe benefit claim" /sc daily /st 09:05
schtasks /query /tn CodeArtsBenefit
schtasks /delete /tn CodeArtsBenefit /f
```

## 接口说明

| 端点                          | 说明                           |
| --------------------------- | ---------------------------- |
| `POST /v1/chat/completions` | OpenAI 格式聊天补全，支持 `stream` 流式 |
| `GET /v1/models`            | 模型列表                         |
| `GET /v1/models/{model_id}` | 单模型详情                        |

## 环境变量

| 变量                          | 默认值                    | 说明                              |
| --------------------------- | ---------------------- | ------------------------------- |
| `CODEARTS_AK`               | 必填                     | 华为云 Access Key                   |
| `CODEARTS_SK`               | 必填                     | 华为云 Secret Key                   |
| `CODEARTS_API_KEYS`         | 空（不鉴权）                 | 允许访问本代理的密钥，逗号分隔                 |
| `CODEARTS_PROXY_HOST`       | `127.0.0.1`            | 监听地址                            |
| `CODEARTS_PROXY_PORT`       | `8787`                 | 监听端口                            |
| `CODEARTS_MODEL_CACHE`      | `models-cache.json`    | 模型缓存路径                          |
| `CODEARTS_BENEFIT_STATE`    | `benefit-state.json`   | 自动签到状态路径                        |
| `CODEARTS_AUTO_CLAIM`       | `1`（开启）                | 是否每天首个请求顺带签到                    |
| `CODEARTS_BASE_URL`         | 华为云 CodeArts 地址        | CodeArts 上游基址（可覆盖）              |
| `CODEARTS_OPENGW_BASE_URL`  | 华为云 opengw 地址          | 免费额度网关基址（可覆盖）                   |
| `CODEARTS_BASE_HOST`        | 空（取 BASE_URL 域名）       | 签名用的 host（中转场景必填）               |
| `CODEARTS_OPENGW_BASE_HOST` | 空（取 BASE_URL 域名）       | opengw 签名用的 host（中转场景必填）         |

`.env` 文件会在当前目录与可执行文件所在目录中查找；已存在的环境变量优先于 `.env`。

## 下游鉴权

**本代理会消耗你华为云账号的额度。** 默认只监听 `127.0.0.1`，但如果你把它暴露到网络（或部署在服务器上），**必须**设置密钥：

```ini
CODEARTS_API_KEYS=sk-你的密钥1,sk-你的密钥2
```

设置后客户端需在请求头携带密钥（OpenAI 兼容工具的「API Key」字段即填这里）：

```text
Authorization: Bearer sk-你的密钥1
```

- 未设置 `CODEARTS_API_KEYS` 时不鉴权（保持本机使用的便利性）
- 支持多个密钥，便于轮换或分发给不同使用者
- 校验使用常量时间比较，避免通过响应耗时逐字节猜测密钥
- `OPTIONS` 预检不校验——浏览器预检不带自定义头，拦截会导致跨域调用整体失败
- 启动日志会提示鉴权状态：`下游鉴权: 已开启（2 个密钥）`

## 覆盖上游端点

`CODEARTS_BASE_URL` 与 `CODEARTS_OPENGW_BASE_URL` 用于替换上游基址，适用于私有化部署、指向本地测试替身，或上游域名变更时无需重新编译。

基址会在启动时校验：必须是带主机名的 `http`/`https` 地址，且不含查询串或锚点。末尾斜杠会被自动去掉，带路径前缀的基址（如 `https://gw.example.com/codearts`）会被保留。配置非法时进程直接退出并给出具体原因。

**签名 host 自动跟随基址**：改 `BASE_URL` 即可，无需额外配置——签名用的 host 就是该基址的域名。

### 通过代理 / 反向代理访问

华为云网关按它**收到的 Host 头**验签。若连接地址与上游真实域名不同，签名里的 host 必须写上游真实域名，否则验签失败，表现为：

```text
401 {"error_code":"APIG.0301","error_msg":"Incorrect IAM authentication information: x-auth-token not found"}
```

此时用 `CODEARTS_BASE_HOST` / `CODEARTS_OPENGW_BASE_HOST` 显式指定签名 host：

```ini
# 实际连接的地址（代理）
CODEARTS_BASE_URL=https://proxy.example.com/codearts
CODEARTS_OPENGW_BASE_URL=https://proxy.example.com/opengw

# 签名用的上游真实域名
CODEARTS_BASE_HOST=snap-access.cn-north-4.myhuaweicloud.com
CODEARTS_OPENGW_BASE_HOST=opengw.developer.huaweicloud.com
```

两个通道的域名不同，因此**分别配置**。不配这两项时，签名 host 自动取对应 `BASE_URL` 的域名。

> [!NOTE]
> 两个通道各自持有独立的客户端，签名 host 在启动时就已确定，不依赖请求 URL 反查。

`CODEARTS_BASE_HOST` 只接受主机名（可带端口），不接受协议前缀或路径——写错会让签名静默失效，所以这里宁可报错。

## 本地测试

先启动代理，然后：

```cmd
go run ./cmd/testclient
go run ./cmd/testclient glm-5.3-flash
go run ./cmd/testclient tools
go run ./cmd/testclient tools glm-5.3-flash
```

`tools` 模式会分别用非流式和流式各发一轮带 `tools` 的请求（`tool_choice` 用对象形态），
并检查响应里有没有 `tool_calls`——排查工具调用问题时先跑这个。

单元测试：

```cmd
go test ./...
go test -race ./...
go vet ./...
gofmt -l .
```

## CI / CD

| 工作流 | 触发条件 | 内容 |
|---|---|---|
| `ci.yml` | push 到 main/master、PR、手动 | gofmt 检查、`go vet`、`go mod tidy` 校验、`go test -race`、覆盖率、四平台交叉编译、Docker 构建冒烟 |
| `release.yml` | 推送 `v*` tag、手动 | 测试门禁 → 交叉编译打包 → 创建 GitHub Release（含 SHA256 校验和）→ 推送多架构镜像到 GHCR |

发布新版本：

```cmd
git tag v1.0.0
git push origin v1.0.0
```

推送 tag 后自动完成编译、打包、创建 Release 与推送镜像。版本号、commit、构建时间会通过 `-ldflags` 注入二进制，`codearts2api -version` 可查看。

Docker 镜像推送到 `ghcr.io/<owner>/<repo>`，标签包含完整版本、主次版本、主版本与 `latest`（预发布版本不打 `latest`）。

## 代码结构

```text
codearts2api/
├── main.go                        # 入口：启动代理 / benefit 子命令 / -version
├── Dockerfile                     # 多阶段构建（静态二进制 + alpine 运行）
├── .github/workflows/
│   ├── ci.yml                     # 规范检查、测试、交叉编译、镜像冒烟
│   └── release.yml                # tag 触发：打包发布 + 推送镜像
├── cmd/testclient/main.go         # 本地联调脚本
├── internal/
│   ├── config/config.go           # 配置加载（.env + 环境变量）、端点定义与校验
│   ├── version/version.go         # 构建版本信息（-ldflags 注入）
│   ├── signer/signer.go           # 华为云 SDK-HMAC-SHA256 签名
│   ├── upstream/client.go         # 带签名的 HTTP 客户端
│   ├── catalog/catalog.go         # 模型目录：双通道同步 + 本地缓存
│   ├── openai/protocol.go         # OpenAI 协议：SSE 聚合、finish_reason 归一化
│   ├── openai/request.go          # 请求整形：工具调用所需的 tool_stream / tool_choice / 工具历史清洗
│   ├── server/server.go           # HTTP 路由与流式转发
│   ├── server/http.go             # SSE 读写与 JSON 响应辅助
│   ├── benefit/benefit.go         # 免费额度签到与查询
│   ├── benefit/state.go           # 签到状态持久化
│   └── benefit/autoclaim.go       # 每天首个请求顺带签到
└── .env                           # AK/SK
```

## 实现要点

- **双通道签名**：`agent` 通道（AgentCenter 老套餐模型）走基础签名；`benefit` 通道（免费额度模型）必须额外携带并签名 `maas_type` / `model-id` / `model-name` 三个头，否则上游报 `model is not registered`。不在已知列表中的模型一律按 `benefit` 处理，上游新增免费模型无需改代码。
- **工具调用（function call）**：上游对 `tools` 有三处与标准 OpenAI 不同的要求，代理在转发前统一整形，否则表现为「模型只把调用写成一段文字」：① 必须带 `tool_stream=true`，否则不产出结构化 `tool_calls`；② `tool_stream` 只在流式下生效，因此带工具的请求强制走上游流式，非流式客户端由代理聚合成单个 JSON；③ `tool_choice` 只接受字符串，对象形态会被上游以 `InferHub.001001005.400` 拒绝——指定具体函数时改为「只下发该函数 + `required`」。残破的工具历史（孤儿 `tool_calls`、没有对应调用的工具结果）会被剥掉，否则上游整轮 400。
- **模型列表**：启动时从 AgentCenter 与 opengw 网关同步，写入 `models-cache.json`；300 秒短缓存避免客户端轮询造成请求风暴；同步失败自动回退缓存，再回退内置兜底列表。
- **自动签到**：不跑常驻定时器。请求路径上只做一次内存日期比对（不触网、不读盘），需要签到时交给后台 goroutine，签到状态落盘保证跨重启幂等；失败按 10 分钟退避。
- **流式转发**：`benefit` 通道会补齐缺失的 `finish_reason` 与 `[DONE]`，并把 `content: null` 规范化为空字符串（部分 Agent Runtime 只接受字符串 content）；`agent` 通道保持即时转发，不改变既有客户端状态机。
- **非流式兜底**：CodeArts 在排队或重试时可能即使 `stream=false` 仍返回 SSE，代理统一聚合成单个 JSON 再返回。
- **超时策略**：不使用 `http.Client.Timeout`（它是整个请求的总超时，会切断长时间流式输出），改用 Transport 级响应头超时 + 非流式请求的 context 超时。
- **端点可配置**：所有上游地址收敛到 `config.Endpoints`，基址与签名 host 均可由环境变量覆盖；启动时校验合法性，非法配置立即退出而非运行到一半才报错。
- **连接与签名解耦**：每个上游通道持有独立的签名客户端，签名 host 在启动时确定。替换 `BASE_URL` 时自动跟随新域名；用代理时由 `*_BASE_HOST` 显式覆盖。
- **下游鉴权**：可选的 Bearer 密钥校验，常量时间比较；未配置时不鉴权以保持本机便利。

## 说明

- 代理只做签名和转发，不修改模型行为。
- 每次调用会消耗华为云 CodeArts 套餐额度（token）。
- 默认仅绑定 `127.0.0.1`，不会对外网开放（Docker 镜像中改为 `0.0.0.0` 以便端口映射）。**暴露到网络时请务必设置 `CODEARTS_API_KEYS`。**
- 本项目及本项目作者与华为云平台及其附属公司无任何关系，且华为云平台中并未对本项目中调用方式明确提出允许，若造成封号/限流等任何因使用本工具造成的情况与项目作者无关。如果您使用该项目，则默认视为同意本 Readme 中所有说明。
