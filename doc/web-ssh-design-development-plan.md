# Web SSH
## 设计与开发实施方案

**产品名称：** Web SSH  
**产品定位：** Personal Web SSH Client（个人 Web SSH 客户端）  
**项目目录：** `web-ssh`  
**Docker 容器：** `web-ssh`  
**数据库：** `web-ssh.db`  
**后端：** Go `1.26.5`
**前端：** Vue 3 + TypeScript  
**终端：** xterm.js  
**数据库：** SQLite  
**部署：** Docker 单容器  
**UI 参考：** OpenCode 官方网站 Web UI（https://opencode.ai），正式布局以已确认的 [v2 原型](web-ssh-prototype-v2.html) 为准。

更新日期：2026-11-20。本轮定向补充第六阶段新增的 SSH 私钥认证及 K02～K06 已确认契约：密码与私钥认证互斥；私钥原始字节存入独立持久化目录；Passphrase 独立加密入库；上传、解析、回收、备份和故障边界按本文件定义。本文定义目标契约；6-K 本地实现及验证状态见 [交付报告](测试报告/stage-6-k-report.md)，部署/真机未验项不能由文档替代。第五阶段 Session、Binding、关闭及 WebSocket 协议保持冻结。

| 权威文档 | 唯一负责的内容 |
|---|---|
| 本文 | 产品需求、HTTP API/DTO、错误码与关闭原因、数据库、配置、产品验收 |
| [架构设计](web-ssh-architecture-implementation-plan.md) | 模块、生命周期、同步边界、缓存算法、Host Key 内部流程、资源回收 |
| [WebSocket 协议](web-ssh-websocket-protocol.md) | 消息字段、传输顺序、客户端输出与重连算法 |
| [实施阶段](web-ssh-implementation-stages.md) | 七阶段依赖、实施任务、测试与阶段验收 |

其他文档通过章节链接引用权威定义，不能另写一套字段、默认值或流程。

---

# 1. 项目定位与命名

## 1.1 产品定位

本项目是一个运行在浏览器中的 SSH 连接和终端管理工具。

用户通过浏览器登录，管理自己的 SSH 服务器，选择服务器后即可打开 SSH 终端，并支持多个终端会话和跨设备接管。

本项目正式定义为：

**Personal Web SSH Client（个人 Web SSH 客户端）**

不再使用 `Personal Web SSH Gateway` 作为产品名称。

## 1.2 为什么不叫 SSH Gateway

SSH Gateway 通常更强调统一 SSH 访问入口、集中身份认证、访问策略、审计和 SSH 流量代理等网关能力。

本项目的主要用途是：

- 管理自己的 SSH 服务器连接。
- 在浏览器中打开 SSH 终端。
- 管理多个终端会话。
- 浏览器关闭后保持远程 SSH 会话。
- 在其他设备上接管已有会话。

虽然技术实现上由 Go 后端代理浏览器与目标 SSH 服务器之间的通信，但这只是内部架构，不代表产品定位是 SSH Gateway。

## 1.3 命名统一

| 项目 | 名称 |
|---|---|
| 产品名称 | Web SSH |
| 英文描述 | Personal Web SSH Client |
| 项目目录 | `web-ssh` |
| Docker 容器 | `web-ssh` |
| 数据库文件 | `web-ssh.db` |
| 页面标题 | Web SSH |
| 项目描述 | A lightweight personal SSH client in your browser. |

如果早期原型或文件中出现 `Termgate`、`SSH Gateway`、`ssh-gateway` 等名称，正式实现时统一改为 `Web SSH` / `web-ssh`。

不要因为名称调整而改变已经确定的技术架构。

---

# 2. 项目目标

开发一个轻量、稳定、跨设备使用的 Web SSH 客户端。

核心目标：

1. 不需要在本地电脑安装 SSH 客户端。
2. 支持 Windows、macOS、Linux、Android、iOS 等设备通过浏览器访问。
3. 支持管理多台服务器。
4. 支持多个终端标签页。
5. 浏览器关闭或网络断开后，SSH Session 继续运行。
6. 更换设备登录后，可以重新接管之前的 SSH Session。
7. 终端输出在浏览器离线期间继续由服务端接收并缓存。
8. 提供简单直观的服务器管理和会话管理界面。

## 2.1 核心设计原则

- 简单、稳定、易维护。
- 尽量减少第三方依赖和外部组件。
- 不引入 Redis、消息队列等不必要的基础设施。
- SSH 会话由 Go 后端直接管理。
- 浏览器 WebSocket 断开不能影响远程 SSH 会话。
- 所有重要会话参数通过配置文件管理，不在代码中写死。
- 第一版优先完成 SSH 终端和会话接管，不扩展其他功能。

---

# 3. 技术架构

## 3.1 技术选型

| 模块 | 技术 |
|---|---|
| 后端 | Go |
| 前端 | Vue 3 |
| 前端语言 | TypeScript |
| 构建工具 | Vite |
| 终端组件 | xterm.js |
| SSH 客户端 | golang.org/x/crypto/ssh |
| WebSocket | github.com/coder/websocket `v1.8.15` |
| 数据库 | SQLite |
| HTTP 服务 | Go 标准库 net/http |
| 配置文件 | YAML |
| 密码哈希 | Argon2id |
| 敏感数据加密 | AES-256-GCM |
| 部署 | Docker |

WebSocket 固定使用 `github.com/coder/websocket v1.8.15`，读取、心跳和强制关闭依据见[架构 §7.5](web-ssh-architecture-implementation-plan.md#ws-read-close)。其余依赖版本由 Coding Agent 选择当前稳定版本，并在项目中固定版本。

## 3.2 系统架构

```text
                  用户浏览器
               Vue 3 + xterm.js
                      |
                      | HTTPS / WebSocket
                      |
              Nginx Proxy Manager
                      |
                      |
               Go Web SSH
                      |
          +-----------+-----------+
          |           |           |
       HTTP API    Session      SQLite
                   Manager
                      |
              SSH Connection
                      |
                SSH Channel
                      |
                    PTY
                      |
          +-----------+-----------+
          |           |           |
        Server A   Server B   Server C
```

前端与后端可以分别开发，最终构建时将前端静态文件嵌入 Go 程序，由 Go 服务统一提供访问。

最终只需要运行一个 Docker 容器。

## 3.3 关键架构原则

浏览器与 SSH 连接必须彻底解耦。

浏览器通过 WebSocket 与 Go 服务通信。

Go 服务通过 SSH 协议连接目标服务器。

两条连接的生命周期独立：

- 浏览器 WebSocket 断开：只断开浏览器连接。
- SSH 连接：继续保持。
- SSH 输出：继续读取并缓存。
- 浏览器重新连接：重新绑定已有 Session，获取缓存并继续交互。

**禁止将 WebSocket 连接的关闭事件直接关联到 SSH 连接的关闭事件。**

---

# 4. 第一版功能范围

## 4.1 第一版必须实现

| 功能 | 说明 |
|---|---|
| Web 登录 | 独立 Web 管理密码 |
| 服务器管理 | 增删改查服务器 |
| 服务器分组 | 支持简单分组 |
| SSH 连接 | 密码认证或上传私钥认证（二者互斥） |
| SSH Host Key | 首次记录，后续校验 |
| SSH PTY | 支持交互式终端 |
| 多标签终端 | 同时打开多个 SSH 会话 |
| 会话列表 | 独立管理所有 SSH Session |
| 会话接管 | 更换浏览器或设备后重新连接 |
| 输出缓存 | 浏览器离线期间继续缓存输出 |
| 离线自动关闭 | 按[离线保留配置](#configuration)到期回收 |
| 手动关闭 | 立即关闭 SSH 会话 |
| 主题切换 | 默认白色主题，可切换深色主题 |
| Docker 部署 | 单容器部署 |

## 4.2 第一版明确不做

- SFTP
- FTP
- 文件管理
- RDP
- VNC
- 端口转发
- 批量执行命令
- 通用 SSH 密钥资产管理平台、密钥生成/导入库、公钥分发、SSH Agent 和证书管理
- 多用户角色和权限体系
- 网关重启后的 SSH Session 恢复
- tmux / screen 集成
- 多网关集群
- Redis 等外部依赖

不要因为这些功能可能有用，就提前设计复杂的扩展体系。

---

<a id="authentication"></a>
# 5. 用户认证与安全设计

## 5.1 单管理员与登录会话

第一版只有一个管理员身份，允许该管理员在多个浏览器分别登录。每次登录产生独立 Web 登录会话；管理员身份和一次登录会话不是同一个标识。

- 首次启动且 `admin_credentials` 无记录时，从 `APP_ADMIN_PASSWORD` 读取初始化密码。不能为空；要求 8～1024 UTF-8 字节，不裁剪密码中的空白。未提供时停止启动。
- 使用 Argon2id，首次哈希参数为内存 65536 KiB、迭代 3、并行度 1、随机 salt 16 字节、输出 32 字节，按 PHC 字符串保存算法、参数、salt 与哈希。验证读取记录中的参数；本版不提供密码管理界面或自动密码轮换。
- 已有管理员记录后，环境变量不覆盖现有哈希；初始化成功后部署方可以移除该变量。不保存或记录明文密码。
- 登录令牌由密码学安全随机源生成 32 字节、无填充 Base64URL 编码。数据库仅保存 SHA-256 哈希；登录响应只通过 Cookie 交付令牌。
- Cookie 名称 `web_ssh_auth`，`HttpOnly`、`Path=/`、`SameSite=Strict`，不设置 `Domain`；`Secure` 由 `auth.cookie_secure` 控制，默认开启。到期时间与服务端记录一致，不做滑动续期。
- 登录有效期由 `auth.session_ttl` 指定，默认值见[配置表](#configuration)。退出登录成功时只撤销当前登录会话及其 probe、绑定凭证和 BrowserBinding；其他浏览器的独立登录不受影响。持久化撤销成功是退出成功的前提，数据库失败规则见 [§11.2](#api-auth)。
- 登录失效后停止对应浏览器操作权限，SSH Session 仍按离线规则保留。无输入、无输出的浏览器也必须在登录到期时解除绑定，机制见[架构生命周期](web-ssh-architecture-implementation-plan.md#session-lifecycle)。

登录采用进程级固定时间窗口限流，所有登录尝试共用计数，避免按任意来源建立无界索引。窗口和次数由 `auth.login_rate_window`、`auth.login_rate_limit` 指定，超过返回 `LOGIN_RATE_LIMIT` 和 `Retry-After`。同时限制有效登录会话总数；先删除过期记录，仍满则返回 `LOGIN_SESSION_LIMIT`，不撤销其他设备的有效登录。清理和容量检查必须与新会话插入保持原子性。

## 5.2 同源与反向代理

- 除登录、退出登录和健康检查外，所有管理 API 要求有效 Cookie；退出登录使用专用幂等处理，允许过期、冻结或缺失 Cookie，仍必须验证 Origin。所有写 API（含登录、退出登录）及 WebSocket 升级要求 `Origin` 与 `server.public_origin` 精确匹配，缺失或不匹配拒绝。
- `server.public_origin` 是确定的外部访问来源，由配置声明，不从请求头猜测。代理转发的 `Host`、`X-Forwarded-Host`、`X-Forwarded-Proto` 不能改变允许来源或 Cookie 策略。
- `server.trusted_proxies` 仅允许声明的代理 CIDR 提供客户端地址，用于日志。来源校验不依赖该地址，也不因代理受信任而允许任意 Origin。
- 本机 HTTP 开发必须显式设置环回监听地址、HTTP 环回 `public_origin` 和 `auth.cookie_secure: false`。除此之外禁止关闭 Secure；正式环境由 HTTPS/WSS 入口访问。

## 5.3 SSH 认证凭据保存

服务器 SSH 用户认证仅支持两种互斥模式：`password` 与 `private_key`，每台服务器同一时刻只能使用一种；不顺序尝试、自动降级或在远端认证失败后切换另一种方式。

**密码模式**保持既有行为：SSH 密码使用 AES-256-GCM 加密，格式为版本字节 `1` + 12 字节随机 nonce + 密文/16 字节认证标签。`APP_ENCRYPTION_KEY` 为标准 Base64 编码的 32 字节密钥；也可设置 `APP_ENCRYPTION_KEY_FILE`，两者必须且只能设置一个。新增认证不改变既有密码密文，不做全量重加密。新增密码服务器要求非空 SSH 密码；编辑时省略 `password` 或传空字符串表示保留原密文，非空字符串替换并重新加密，密码不裁剪空白。

**私钥模式**通过服务器新增/编辑表单上传单个私钥文件，不允许手工粘贴或客户端指定存储路径。首批支持 OpenSSH、PEM PKCS#1 RSA、PEM SEC1 EC，以及固定 `golang.org/x/crypto v0.59.0` 实际可解析并成功构造 SSH Signer 的 PEM PKCS#8 类型；不承诺 DSA、库不支持的加密 PKCS#8 或未验证格式。必须完成私钥解析和 Signer 构造；仅能解析容器不代表可作为 SSH 签名密钥。文件最大 65,536 字节。上传内容先在有界内存中验证格式和 Passphrase，成功后才保存；落盘、读取和备份均保持上传原始字节，不应用层加密/解密、不重编码、不规范化换行、不裁剪、不转换格式。

私钥对象存放于专用目录 `/var/lib/web-ssh/ssh-keys/`，与 SQLite 数据目录分离，由后端生成不可预测对象标识和存储名。目录不进入静态资源或普通下载路由；不得把原始文件、文件名路径、私钥内容或物理路径返回给前端。目标目录建议权限 0700，文件 0600，运行 UID/GID 为 10001:10001；部署 bind mount 必须由宿主预先配置正确属主和权限，不能 chmod 777、切换 root 或在启动时递归 chown。

**Passphrase** 是私钥原生加密口令，与文件字节分开存储。用户输入允许为空：空值表示尝试按无口令私钥解析；若文件实际加密则解析失败，不能借用旧口令。Passphrase 使用现有 APP 主密钥体系和 AES-256-GCM 加密保存到独立数据库字段，不明文入库，不回传查询 DTO、不写日志或异常。新增 Passphrase 密文使用独立版本格式和随机 nonce；AAD 包含格式版本、固定用途标识及服务器 ID，采用无歧义编码。继续使用同一 APP 主密钥；旧 SSH 密码维持 v1，不全量重加密；Passphrase 不允许降级按密码 v1 解密。AAD 能阻止跨用途/跨服务器直接搬用密文，但 GCM 认证失败不能单独区分主密钥错误、密文损坏或上下文不匹配。

**口令更新语义**：创建密钥配置、密码切换为密钥及替换私钥时，用户必须为新文件重新提供匹配 Passphrase；不自动重用旧保存口令。普通服务器资料编辑不改变既有凭据；不提供单独更新已保存 Passphrase 的操作。替换文件时空 Passphrase 表示新文件应能无口令解析。新文件和新口令验证、文件保存及数据库配置提交全部成功后，才清理旧凭据；失败保留旧配置和旧凭据。密码切换为密钥、密钥切换为密码、密钥替换的旧文件/旧密文清理必须遵循引用、短期租约、事务不确定状态及回收保护规则，详见[架构私钥存储](web-ssh-architecture-implementation-plan.md#private-key-storage)。

服务器编辑或认证方式/认证材料/Username 实际变化使旧 Host Key probe 失效；仅改变认证不清除同一 Host/Port 的 Host Key 信任。已建立 SSH Session 继续使用创建时的连接，不因服务器认证变更重连，不改变其 Binding 代次。认证材料仅由上传校验、Test 或 Creation 短期任务持有，不进入长期 Session、Terminal、probe、API 或日志。

数据库启动校验：现有 SSH 密码密文及新增 Passphrase 密文均需解密校验；任意一条已保存 Passphrase 无法解密，应用启动失败，沿用现有 SSH 密码策略。若所有密钥均无 Passphrase 且数据库无其他加密记录，只能确认主密钥格式正确，必须标记为“无法验证历史匹配性”，不能声称匹配验证通过；首版不增加数据库主密钥 sentinel。单个私钥文件缺失/损坏不阻止全站启动，只使关联服务器凭据不可用，保留引用并允许重新上传修复；不自动生成空文件、覆盖引用或回退密码。

## 5.4 SSH Host Key 产品规则

Host Key 信任同时绑定目标 Host、Port 和完整公钥。前端只展示目标、算法及 SHA256 指纹；完整公钥由后端保存和比对，不能由前端提交。

1. 首次连接没有当前目标信任时，临时探测实际 Host Key，在 SSH 用户认证前停止并关闭连接；返回一次性 `probe_id` 及展示信息。
2. 用户明确确认后调用[确认 API](#api-servers)，后端保存探测到的公钥；再重新测试或创建 Session，并再次校验公钥后才认证。
3. 同一 Host/Port 公钥变化时，展示原、新指纹；拒绝自动覆盖。Host 或 Port 变化时清除原信任，即使新目标公钥相同也须重新确认。
4. 仅修改 Username、SSH 密码、认证方式、私钥对象/内容或 Passphrase 不清除已有 Host Key 信任，但使旧 probe 失效。其他连接参数变化同样使旧 probe 失效。
5. 用户取消确认不修改信任。已建立 Session 使用原连接，服务器编辑或信任更新不引起自动重连。
6. 测试连接复用同一校验规则，通过后只验证认证，不申请 Channel/PTY、不启动 Shell、不产生可接管 Session。

探测记录的版本、容量、唯一消费、并发确认、失败重试及编辑删除竞争的唯一内部定义见[架构 Host Key](web-ssh-architecture-implementation-plan.md#host-key)。接口形状见 [§11.3](#api-servers)，数据持久化见 [§12](#database)。

## 5.5 其他安全要求

- Session ID、服务器 ID 使用密码学安全随机源生成的 16 字节，API 编码为 32 位小写十六进制；不可预测 ID 不能替代登录和权限检查。
- 单管理员可管理该应用的全部 Session，但凭证始终绑定签发时的独立登录会话。
- 同一 Session 同时只有一个有效浏览器操作端。旧浏览器无权通过延迟输入、resize 或断开回调影响新绑定。
- 绑定凭证和 `probe_id` 不放 URL，不写日志；控制消息、HTTP 请求、登录状态和网络资源均有明确上限。

---

# 6. SSH Session 产品行为

## 6.1 所有权与状态

Go 后端独立托管每个 Session 的 SSH Connection、Shell Channel、PTY 和有界输出缓存，不要求目标服务器安装 tmux、screen 或其他软件。浏览器只提供输入输出通道，关闭页面或网络断开不能直接关闭 SSH。

状态 `CONNECTING`、`CONNECTED`、`DISCONNECTED`、`CLOSING`、`CLOSED` 的转换、名额预占、绑定代次和关闭顺序统一见[架构生命周期](web-ssh-architecture-implementation-plan.md#session-lifecycle)。`CONNECTED` 表示当前有浏览器绑定，不代替 SSH 健康状态。

## 6.2 创建与打开

用户点击服务器“连接”创建新的 SSH Session。创建成功返回就绪的 Session，浏览器通过同一套一次性凭证机制接入。初始建连、PTY 或 Shell 失败必须释放资源与名额。创建响应丢失或浏览器未接入时，Session 仍受离线期限约束，不能永久占用资源。

PTY 终端类型固定为 `xterm-256color`，初始列数、行数由创建请求提供；不接受客户端自定义终端类型或环境变量。

同一浏览器已打开的 Session 优先定位到已有标签；创建新 Session 和打开已有 Session 是不同操作。Session 详情的连接目标取自创建时固定的非敏感快照，不随服务器资料修改而改变。

## 6.3 断线与接管

- 普通 WebSocket 断开立即使该 BrowserBinding 失效，SSH、Shell、PTY、输出读取及缓存继续工作。网络失联的判定时机由心跳与超时决定。
- `CONNECTED` 和 `DISCONNECTED` 均允许用户主动接管。接管准备 API 只签发凭证，新 WebSocket 原子提交绑定时才替换旧端。提交前失败保留原绑定；同一预期代次并发提交只有一个成功。
- 自动恢复只使用 `attach`，不能抢占已有浏览器。被接管、代次冲突或登录失效后停止自动恢复，需用户主动操作。
- 已经获准并开始执行的 SSH 操作不能撤回；排队但尚未获准的旧输入与 resize 必须丢弃。旧端回调不得解除新端绑定。
- 新端通知发送失败不撤销已经提交的接管，也不恢复旧端。相关重试依[WebSocket 重连规则](web-ssh-websocket-protocol.md#reconnect)执行。

API 见 [§11.4](#api-sessions)；唯一接管和并发流程见[架构绑定](web-ssh-architecture-implementation-plan.md#binding)与[操作顺序](web-ssh-architecture-implementation-plan.md#operation-order)。

## 6.4 输出缓存与恢复边界

SSH 输出在浏览器在线和离线时都持续读取。缓存同时受字节数和历史行数上限约束；任一超限即淘汰最早数据。行数按原始 LF 统计，算法见[OutputBuffer](web-ssh-architecture-implementation-plan.md#output-buffer)，上限及默认值见[配置](#configuration)。

缓存及传输保存原始字节：不解码 UTF-8、不解析 ANSI、不对齐或修改字节边界，淘汰仅推进绝对偏移。缓存不是永久日志，也不是终端屏幕快照。

每条 WebSocket 只执行一次历史回放。当前通道无法与后续输出连续衔接时终止浏览器通道，SSH 继续运行；新通道按终端实际处理完成的偏移重新绑定。不能在同一 WebSocket 上反复发送新快照。

连续恢复保留 xterm.js 实例和解析器状态；已丢失字节时，客户端在新连接收到截断通知后重建终端，再从声明的可用起点处理原始后缀。重建会清除旧画面和本地滚动历史；提示显示在终端外，不混入 SSH 字节。后缀可能从 UTF-8 或 ANSI 序列中间开始，第一版不能保证恢复 `vim`、`top` 等完整画面或消除所有残片。该限制通过明确提示告知用户，不增加服务端终端模拟器。

完整消息与前端处理规则仅定义在[协议](web-ssh-websocket-protocol.md#client-tracker)。

## 6.5 尺寸、关闭和离线期限

浏览器尺寸变化及接管成功后同步当前终端尺寸，不能因此创建新 SSH Session。输入在回放已由 xterm.js 处理完成后启用，避免历史终端查询产生自动应答并写回当前 Shell；详细门禁见[协议](web-ssh-websocket-protocol.md#output-order)。

关闭本地标签仅解除浏览器绑定。真正关闭 Session 必须使用独立操作并二次确认，提示正在运行的前台命令可能中断。手动关闭立即进入关闭流程，不等待离线期限。

离线保留时长由 `session.idle_timeout` 指定，从服务端确认无有效绑定时计算；成功重新绑定取消期限，再次断开重新计算。不使用“没有键盘输入”作为离线判断。离线超时与接管、手动关闭、SSH 退出竞争的实现见架构生命周期。

## 6.6 进程退出限制

Go 进程退出、容器重启或重建会终止 SSH 连接，内存缓存丢失，远程 Shell 通常终止。第一版不恢复重启前的 SSH Session；启动时清理遗留活动元数据，旧 Session 不再显示为可接管。

---

<a id="configuration"></a>
# 7. 配置文件与运行参数

本章是所有配置键、默认值和单位的唯一权威。配置启动时读取，修改后重启生效，不支持运行时修改。缺项使用下列默认值；未知键、重复键、类型错误、越界或关联校验失败必须输出不含秘密的字段级错误并停止启动，不能自动改成另一值。

## 7.1 完整 YAML 默认配置

```yaml
server:
  host: "0.0.0.0"
  port: 8080
  public_origin: "https://localhost:8080"
  trusted_proxies: []
  # HTTP 首部等待、请求总预算及退出预算，单位秒
  read_header_timeout: 5
  request_timeout: 30
  shutdown_timeout: 25
  # 管理 API 请求体上限，单位字节
  max_request_body_size: 65536

http:
  # 浏览器申请 Session 绑定凭证的 HTTP POST 本地等待期限，单位秒
  credential_timeout: 5

auth:
  # 登录有效期，单位天；Cookie 安全属性默认开启
  session_ttl: 7
  cookie_secure: true
  # 全局登录尝试固定窗口：秒、次数
  login_rate_window: 60
  login_rate_limit: 10
  # 同时有效的 Web 登录会话总数
  max_sessions: 64

session:
  # 浏览器离线保留，单位分钟
  idle_timeout: 120
  max_sessions_per_server: 20
  # 原始输出缓存，单位字节；历史行数按 LF 统计
  output_buffer_size: 1048576
  history_limit: 2000
  # 一次性绑定凭证：有效期秒数及全局记录上限
  binding_credential_ttl: 30
  binding_credential_limit: 256
  # 等待输入队列空位或远端操作串行许可的上限，单位秒
  operation_wait_timeout: 5

ssh:
  # TCP、握手、认证、PTY 和 Shell 建立的共同总预算，单位秒
  connect_timeout: 15
  # 已建立连接的单次输入/resize 操作上限，单位秒
  operation_timeout: 10
  keepalive_interval: 30
  keepalive_timeout: 10
  # 同一在途 Keepalive 请求连续未应答的超时窗口数
  keepalive_max_failures: 3
  # Host Key 探测凭证：有效期秒数及全局记录上限
  host_key_probe_ttl: 300
  host_key_probe_limit: 256
  # 私钥孤儿文件回收安全宽限期，单位秒；从登记回收候选时刻计算
  private_key_gc_grace_period: 86400

websocket:
  # 全部已升级连接，包括未绑定及正在关闭的连接
  max_connections: 128
  control_queue_limit: 32
  # 单条完整消息或输出块的字节上限
  max_control_message_size: 16384
  max_input_message_size: 65536
  output_chunk_size: 65536
  # 以下时间均为秒；bind_timeout 仅用于升级后首条完整 bind，不用于 HTTP 凭证申请
  bind_timeout: 5
  message_read_timeout: 10
  write_timeout: 10
  close_timeout: 10
  ping_interval: 30
  pong_timeout: 10

terminal:
  max_cols: 500
  max_rows: 300
  # 单终端待提交与在途 write 数据合计上限，单位字节
  write_queue_bytes: 1048576
  # 等待在途 write 完成的上限，单位秒
  write_drain_timeout: 10
  reconnect_max_attempts: 10
  # 恢复间隔与重置失败计数所需稳定时长，单位秒
  reconnect_delay: 5
  reconnect_stable_time: 30

database:
  path: "/data/web-ssh.db"
  # SQLite 锁等待上限，单位毫秒
  busy_timeout: 5000

logging:
  level: "info"
```

## 7.2 类型、合法值与关联校验

所有未单列的数值均为正整数，不接受浮点数、字符串数字、`null`、零或负数；转换为 Go 容量或 duration 前须检测溢出。默认值只有本章 YAML 一份定义。所有下发客户端的数值还必须在 JavaScript 安全整数范围内；涉及客户端的秒数（包括 `http.credential_timeout`）乘 1000，以及 `(websocket.bind_timeout + session.operation_wait_timeout + websocket.write_timeout) × 1000`，均不得超过 2147483647 毫秒，防止浏览器计时器溢出立即触发。

| 键或范围 | 单位与合法性 |
|---|---|
| `server.host` | 非空监听 IP；允许 IPv4/IPv6，不能包含端口 |
| `server.port` | TCP 端口，1～65535 |
| `server.public_origin` | 单个规范化 `scheme://host[:port]`，无用户信息、路径、查询或片段；部署时改为真实外部来源 |
| `server.trusted_proxies` | CIDR 字符串数组，默认不信任转发客户端地址；非法 CIDR 拒绝 |
| `server.read_header_timeout`、`server.request_timeout`、`server.shutdown_timeout` | 秒；请求预算不得小于 `ssh.connect_timeout`；退出预算必须小于容器停止宽限期 |
| `server.max_request_body_size` | 字节，至少 1024；流式限额，不能先无界读取 |
| `http.credential_timeout` | 正整数，秒；浏览器申请 Session 绑定凭证的 HTTP POST 本地等待期限，通过 AbortController 定时取消；受上述 JS 安全整数及毫秒计时器范围校验 |
| `auth.session_ttl` | 天，1～365 |
| `auth.cookie_secure` | 布尔值；false 仅允许监听环回 IP 且 public_origin 为 HTTP 的 localhost/环回 IP；其余必须 HTTPS 且 true |
| `auth.login_rate_window`、`auth.login_rate_limit`、`auth.max_sessions` | 秒、每窗口尝试次数、有效登录会话数；均为正整数 |
| `session.idle_timeout` | 分钟；从服务端解除当前绑定时计时 |
| `session.max_sessions_per_server` | 个；包含正在创建和正在关闭且尚未释放名额的 Session |
| `session.output_buffer_size`、`session.history_limit` | 字节、原始 LF 行数，两个上限同时生效 |
| `session.binding_credential_ttl`、`session.binding_credential_limit` | 秒、全进程凭证记录数；绑定凭证有效期不得小于 `websocket.bind_timeout` |
| `session.operation_wait_timeout` | 秒；输入队列等待超时为 INPUT_BACKPRESSURE；接管等待串行许可超时为 BIND_BUSY，不提交接管 |
| `ssh.connect_timeout`、`ssh.operation_timeout` | 秒；后者超时需通过关闭 SSH 底层连接解除不可取消的在途操作，关闭原因 `ssh_error` |
| `ssh.keepalive_interval`、`ssh.keepalive_timeout`、`ssh.keepalive_max_failures` | 秒、秒、连续超时窗口数；不是并行探测次数 |
| `ssh.host_key_probe_ttl`、`ssh.host_key_probe_limit` | 秒、全进程待确认记录数；到期清理和容量检查/插入原子完成 |
| `ssh.private_key_gc_grace_period` | 秒，正整数，默认 86,400（24 小时）；从持久化登记回收候选的时间计算，重启不得重置或绕过；届满后仍须通过全部引用、租约、事务和备份保护检查 |
| `websocket.max_connections`、`websocket.control_queue_limit` | 个、条；连接直至 Reader/Writer/关闭任务回收后才释放容量 |
| `websocket.max_control_message_size` | 字节，至少 1024；应用 JSON 控制消息上限 |
| `websocket.max_input_message_size`、`websocket.output_chunk_size` | 字节；输出块不得大于 `session.output_buffer_size` 或 `terminal.write_queue_bytes` |
| `websocket.bind_timeout` | 正整数，秒；从 WebSocket Upgrade 成功起等待首条完整 bind，不用于 HTTP 凭证请求；具体计时规则见[协议限制](web-ssh-websocket-protocol.md#limits) |
| `websocket.message_read_timeout`、`write_timeout`、`close_timeout`、`ping_interval`、`pong_timeout` | 秒，均为 `websocket` 下的键；具体计时起点见[协议限制](web-ssh-websocket-protocol.md#limits) |
| `terminal.max_cols`、`terminal.max_rows` | 列、行，正整数且不大于 65535 |
| `terminal.write_queue_bytes` | 字节；包含未提交队列及唯一在途 xterm.js write |
| `terminal.write_drain_timeout` | 秒；超时销毁旧实例，新的终端从偏移 0 请求恢复 |
| `terminal.reconnect_max_attempts` | 首次用户发起的尝试之外，最多自动恢复次数，1～10；不允许无限重试 |
| `terminal.reconnect_delay`、`terminal.reconnect_stable_time` | 秒；稳定时长不得小于恢复间隔 |
| `database.path` | 非空文件路径，父目录可写；必须是可持久化本地文件系统 |
| `database.busy_timeout` | 毫秒；只限制 SQLite 锁等待，不替代请求总预算 |
| `logging.level` | `debug`、`info`、`warn`、`error` 之一 |

默认自动恢复等待跨度覆盖常见的心跳失联判定，避免旧通道尚未被服务端判离线时过早用尽尝试。持续输出、调度或网络故障仍可能超过预算，次数耗尽后显示状态并由用户主动重试，不承诺无限等待一定恢复。

`server.request_timeout` 继续作为服务端 HTTP 请求预算，仅覆盖 HTTP 阶段。已经创建的 Session、已升级 WebSocket 不能继承该请求截止时间；WebSocket 使用本章独立超时。服务器保存接口的 multipart 私钥上传另有固定 128 KiB 总请求体上限和 48 KiB metadata part 上限，不提高全局 JSON 请求体限制；详细限制见 §11.3。私钥上传接收并发、解析/KDF 并发为进程级固定预算，不新增 YAML 配置。

经确认，`http.credential_timeout` 与 `websocket.bind_timeout` 各自从本章默认配置加载，职责独立，即使默认值相同也不得互相取值或回退。前者在浏览器发起绑定凭证 POST 前建立 AbortController 定时器，请求及响应读取完成或失败后清除；到期只表示浏览器停止等待并尝试取消请求，不能证明服务端已停止处理或未签发凭证。它不是服务端请求处理、Session 操作锁等待或 WS 首条 bind 的期限。后者仍从 Upgrade 成功后计时；`session.operation_wait_timeout`、WS 写入、Ping/Pong 和关闭预算均不变，不增加它们与新字段的大小关系或求和约束。

Session 详情 GET 不设置浏览器本地自动超时，也不复用上述两个字段；支持用户主动取消和组件生命周期取消。`server.request_timeout` 不能作为浏览器本地等待完成的保证。

以下为固定实现约束，不新增可配置开关：SQLite WAL、外键开启、最大打开/空闲连接均为 1；WebSocket 不启用压缩；输出通知容量 1；原始字节不做 UTF-8/ANSI 解析；同一 WebSocket 单 Reader、单应用 Writer。这样避免第一版出现不可测试的组合。

## 7.3 必须提供的秘密与客户端配置

`APP_ADMIN_PASSWORD` 仅首次初始化必须提供；加密密钥变量二选一必须始终提供，格式见 [§5](#authentication)。秘密不写入 YAML 默认值、镜像、API 或日志。

`GET /api/auth/me` 返回客户端必要配置，字段名与 YAML 叶子名一致，时间仍为秒，客户端转换为毫秒后使用。服务端和客户端均使用同一加载结果，不在前端另写默认值。完整 DTO 见 [§11.2](#api-auth)。

---

# 8. 服务器管理

## 8.1 管理信息

服务器包含名称、Host、Port（默认 22）、Username、认证方式（密码/上传私钥，互斥）、简单分组、备注、当前 Host Key 信任展示及创建/更新时间。密码和 Passphrase 均为只写秘密；私钥文件只允许通过表单上传。查询 DTO 仅返回认证方式及必要的“凭据是否已配置”状态，不回传密码、Passphrase、私钥字节、密文或容器物理路径。准确字段和校验见 [服务器 API](#api-servers)，持久化字段见 [§12](#database)。

不实现通用密钥资产管理平台，不提供密钥生成、下载、复制、共享 UI、公钥分发、SSH Agent 或证书管理。Host Key 的完整可信公钥只由后端保存；前端只看到可信目标、算法和 SHA256 指纹。

## 8.2 界面与操作

支持新增、编辑、删除、测试连接、查看信任信息、按名称搜索、按分组筛选及分组折叠。分组使用服务器的 `group_name` 字符串，不新增独立分组管理系统。

删除前二次确认；存在正在创建、活动或正在关闭的 Session 时拒绝删除，提示先关闭相关会话。仅保留已结束元数据时允许删除，并由数据库级联清理这些元数据。

连接按钮创建新的 SSH Session。达到单服务器上限时明确报错，不自动关闭或踢掉其他会话。服务器编辑只影响后续建连任务，已固定快照的创建任务及已有 Session 按架构规则继续使用原目标。

---

# 9. 会话管理界面

会话管理入口独立于本地终端标签。浏览器关闭后 Session 仍可能存在，登录后通过服务端列表发现可打开的会话。

列表显示会话名称（创建时服务器名称快照）、连接目标、当前状态、创建时间、最近接入/断开时间及剩余保留时间。第一版不增加会话重命名或历史归档界面。

| 状态 | 可用操作 |
|---|---|
| `CONNECTING` | 显示正在连接；不能接管，允许取消/关闭 |
| `CONNECTED` | 用户可主动接管；旧端在新绑定提交后失去操作权 |
| `DISCONNECTED` | 可打开或主动接管；自动恢复只使用 attach 模式 |
| `CLOSING` | 显示关闭中；不能绑定，重复关闭幂等 |
| `CLOSED` | 从活动列表移除，详情返回不存在 |

同一浏览器已有视图时优先定位该标签。关闭标签只解除本地绑定，关闭 SSH Session 使用独立操作和二次确认。

列表和详情取自 Session Manager，不能从 SQLite 历史状态判断连接仍然可接管。剩余时间由服务端返回，前端倒计时仅用于展示；是否可接管仍由提交时服务端校验决定。

---

<a id="ui"></a>
# 10. 前端 UI 设计

## 10.1 视觉基准

参考 OpenCode 官方网站：

https://opencode.ai

这里指网站的 Web UI 视觉风格，不是 OpenCode TUI。

整体风格：

- 简洁、现代、有技术产品感。
- 白色主题作为默认主题。
- 黑白灰为主色。
- 低饱和度辅助色。
- 细边框。
- 克制的小圆角。
- 扁平化按钮。
- 清晰的现代字体。
- 适当留白。
- 少量轻量图标。
- 不使用大面积渐变。
- 不使用夸张阴影。
- 不使用传统后台系统的大量彩色卡片。

## 10.2 主题系统

必须实现主题切换。

默认主题：Light。

另提供 Dark 主题。

要求：

1. 首次打开默认使用白色主题。
2. 页面右上角提供主题切换入口。
3. 切换后立即生效。
4. 用户选择的主题保存到浏览器本地。
5. 下次打开继续使用上次选择的主题。
6. 终端区域、侧边栏、弹窗、按钮、输入框等都需要适配主题。
7. 使用 CSS Variables 统一管理颜色。

不要在各组件中散落大量硬编码颜色。

## 10.3 整体布局

正式页面以已确认的 [v2 原型](web-ssh-prototype-v2.html) 为准。桌面和移动端侧栏均默认收起，由左上角按钮显示或隐藏；桌面收起后释放宽度，移动端选择服务器后收起抽屉。下图仅表示侧栏展开时的结构：

```text
+--------------------------------------------------------------+
| Web SSH                              Theme / User / Logout   |
+--------------+-----------------------------------------------+
|              |                                               |
|  服务器      |  Terminal Workspace                           |
|              |                                               |
|  分组        |  +-----------------------------------------+  |
|  - Server A  |  | Tab A | Tab B | Tab C                   |  |
|  - Server B  |  +-----------------------------------------+  |
|  - Server C  |  |                                         |  |
|              |  |                                         |  |
|  会话管理    |  |              xterm.js                   |  |
|              |  |                                         |  |
|              |  |                                         |  |
|              |  +-----------------------------------------+  |
|              |                                               |
+--------------+-----------------------------------------------+
```

左侧：

- 服务器搜索。
- 服务器分组。
- 服务器列表。
- 会话管理入口。
- 第一版不提供 Settings 页面，配置由 YAML 管理。

右侧：

- 当前终端标签栏。
- 终端内容区域。
- 终端状态。
- 关闭当前标签等操作。

顶部使用紧凑产品栏，底部仅保留窄状态栏；不增加宣传标题、副文案、常驻说明或页脚。必要错误和历史截断提示按事件展示。登录页仅保留产品名称、密码框、登录按钮及主题切换。

## 10.4 终端工作区

终端必须使用 xterm.js；原型中的模拟命令输入框不实现为独立功能。

要求：

- 支持 ANSI 颜色。
- 支持复制和粘贴。
- 支持鼠标选择。
- 支持窗口大小调整。
- 支持终端滚动。
- 支持多个标签页。
- 支持关闭标签页。
- 支持切换标签页。
- 支持断线后重新接管已有 Session。

关闭浏览器标签或终端视图时，默认只解除浏览器视图，不应误关闭远端 SSH Session。

真正关闭 Session 需要明确的关闭操作和二次确认。

## 10.5 移动端

移动端不能简单地将桌面布局缩小。

要求：

- 左侧服务器导航可以通过抽屉展开。
- 终端区域占据主要屏幕。
- 标签栏支持横向滚动。
- 会话管理可以单独打开。
- 操作按钮适合触屏。
- 不出现横向页面溢出。
- 软键盘弹出时终端区域能够合理调整。
- 页面切换或浏览器后台挂起导致 WebSocket 断开时，SSH Session 仍然保持。

移动端第一版重点保证基本可用。

---

<a id="http-api"></a>
# 11. HTTP API、错误码与关闭原因

<a id="api-common"></a>
## 11.1 公共约定

API 成功/失败响应统一使用 JSON；成功返回 HTTP 200（创建资源为 201），`code` 为数字 `0`；失败使用本章命名字符串错误码。除服务器保存接口允许 `application/json` 或受限 `multipart/form-data` 请求外，其他 API 请求仍按各自 JSON/空请求契约解析。响应结构固定：

```json
{"code":0,"message":"success","data":{}}
```

```json
{"code":"BIND_CONFLICT","message":"会话状态已变化，请刷新后主动操作","data":{}}
```

- `message` 为面向用户的说明，客户端逻辑按 `code` 处理，不解析文案。除明确列出的错误数据外，失败 `data` 为 `{}`。不返回内部堆栈或秘密。
- 请求 DTO 严格校验必填、类型、未知字段与尺寸。普通 JSON 请求体不得超出 `server.max_request_body_size`；拒绝尾随 JSON 内容。服务器私钥保存 multipart 使用独立固定预算，见 §11.3。无业务字段的 POST/DELETE 使用空对象 `{}` 或无请求体。
- ID 均为 32 位小写十六进制字符串；时间响应使用 UTC RFC3339、固定毫秒精度，如 `2026-11-14T01:02:03.000Z`。可空字段明确返回 `null`，不随机省略。所有有效期在服务端 `now >= expires_at` 时失效，不延长原凭证期限。
- `binding_generation`、输出偏移均为 `0..9007199254740991` 的整数，禁止浮点、负数及回绕。客户端与服务端都检查运算溢出。
- 管理 API 与 WS 按 [§5](#authentication) 验证登录；登录、退出登录及健康检查遵守各自明确例外，退出登录不能被普通“必须有效登录”中间件拦截。状态改变接口验证 Origin。所有响应设置 `Cache-Control: no-store`。
- 业务错误的 HTTP 状态见[错误表](#error-codes)。删除接口使用 HTTP 200 和上述结构，不使用无 JSON 的 204。
- `POST` 创建响应丢失不等于创建失败，客户端不能无条件自动重复创建 Session；重新查询会话列表或等待离线回收。测试与凭证请求可以按各自规则重试。

健康检查：`GET /api/health` 无需登录或 Origin。仅当配置、密钥、迁移、启动清理完成且未开始退出时返回 HTTP 200、`data: {"status":"ready"}`；可接收请求但未就绪/正在退出时返回 HTTP 503、`code: "SERVER_UNAVAILABLE"`、`data: {"status":"not_ready"}`，不暴露数据库、配置或内部故障细节。

<a id="api-auth"></a>
## 11.2 认证 API

| 方法与路径 | 请求 | 成功 data |
|---|---|---|
| `POST /api/auth/login` | `{"password":"管理员密码"}`，必填字符串，最大 1024 UTF-8 字节 | `{"expires_at":"UTC 时间"}`；通过 Set-Cookie 签发登录令牌 |
| `POST /api/auth/logout` | `{}`；不要求 Cookie 当前仍有效，但校验 Origin 并按下文处理原 Cookie | `{}`；确认持久化撤销后清除 Cookie |
| `GET /api/auth/me` | 无 | `AuthView` |

`AuthView`：

```text
expires_at: UTC 时间
client_settings:
  max_control_message_size: 正整数，字节
  max_input_message_size: 正整数，字节
  output_chunk_size: 正整数，字节
  credential_timeout: 正整数，秒
  bind_timeout: 正整数，秒
  operation_wait_timeout: 正整数，秒
  write_timeout: 正整数，秒
  max_cols: 正整数，列
  max_rows: 正整数，行
  write_queue_bytes: 正整数，字节
  write_drain_timeout: 正整数，秒
  reconnect_max_attempts: 正整数，次数
  reconnect_delay: 正整数，秒
  reconnect_stable_time: 正整数，秒
```

`credential_timeout` 仅来自 `http.credential_timeout`，用于浏览器凭证 POST；`bind_timeout` 仅来自 `websocket.bind_timeout`，保留首条绑定期限的语义。两字段独立下发、独立校验，不互相引用或补默认值。`operation_wait_timeout` 来自 `session`；消息大小、`write_timeout` 来自 `websocket`；其余来自 `terminal`。不返回 Cookie 值、令牌哈希、内部登录会话 ID 或管理员密码哈希。未登录的 me 返回 `AUTH_REQUIRED`；密码错误为 `AUTH_FAILED`。登录限流与登录容量独立处理，见 §5.1。

**退出登录的成功与失败边界：**

1. 按原 Cookie 定位本次登录，取得其写锁，暂停本登录新的受保护操作；使用短事务删除对应登录记录。不能先清 Cookie 再尝试删除。
2. 删除提交成功，或已确认记录不存在/已经到期，才将内存登录置为撤销，失效 probe/绑定凭证，解除所属 BrowserBinding，清 Cookie 并返回 200。重复请求同样成功；SSH 保留。
3. 确定事务回滚时返回 `STORAGE_ERROR`，保留 Cookie 和原登录状态，明确提示“退出未完成，请重试”；不得返回成功或把页面切换为已退出。此时没有承诺该 Cookie 已失效。
4. 提交结果不确定时返回 `SERVER_UNAVAILABLE`，在当前进程冻结本登录，立即撤销所属 Binding、probe 和临时绑定凭证；保留 Cookie 仅供 logout 重试幂等删除，其他受保护操作均拒绝。后续只有删除提交成功或确认记录已不存在/到期，才完成第 2 步。不能因数据库异常恢复被冻结登录的操作权。
5. 进程在失败退出完成前重启时，以持久化记录为准；仍存在且未到期表示原退出没有完成，原 Cookie 可能仍然有效。因此未成功退出不能保证跨重启撤销，界面必须明确“退出未完成”，保留重试能力。任何已经收到退出成功的 Cookie 都必须在重启后继续无效。

以上区别避免“前端已清凭证并显示退出成功，但数据库仍允许旧 Cookie”的假成功。登录撤销事件只在持久化成功后发布；提交不确定导致的冻结不是已经成功持久化的撤销。

<a id="api-servers"></a>
## 11.3 服务器与 Host Key API

### 服务器写入字段

服务器保存接口按 Content-Type 分流，路径仍为现有 `POST /api/servers` 与 `PUT /api/servers/{id}`：

- `application/json` 保留既有密码新增/编辑及不涉及文件的普通资料修改契约。
- `multipart/form-data` 用于新建私钥认证、密码切换为私钥或替换私钥；包含且仅包含一个 `metadata` JSON part 和一个 `private_key` 文件 part。没有独立上传接口、上传令牌或临时上传资产。
- `metadata` 使用下表字段及私钥认证操作字段；Passphrase 是只写字段。multipart 不接受重复、未知或多余 part；不接受客户端路径、多文件、嵌套 multipart 或 Content-Transfer-Encoding。
- 普通编辑不更换凭据时继续使用 JSON；不需要为修改备注/分组重新上传文件。密钥配置如需新文件必须 multipart 提交，不能用 JSON 假装替换。
- JSON 创建省略 `auth_type` 时按 `password` 处理；JSON 编辑省略 `auth_type` 时保留当前模式。密钥模式请求中出现 `password` 字段属于歧义请求，拒绝，不隐式切换或静默忽略。

| 字段 | POST 创建 | PUT 编辑 | 约束 |
|---|---|---|---|
| `name` | 必填 | 必填 | 去除首尾空白后 1～128 UTF-8 字节 |
| `host` | 必填 | 必填 | IP 或 ASCII DNS 名称，最多 253 字节；不能包含 scheme、路径或端口 |
| `port` | 可省略，默认 22 | 必填 | 整数 1～65535 |
| `username` | 必填 | 必填 | 非空字符串，最多 128 UTF-8 字节，不含 NUL |
| `auth_type` | JSON 省略默认 `password`；multipart 必须明确 | JSON 省略保留现有；multipart 必须明确 | `password` 或 `private_key`，二者互斥 |
| `password` | password 模式必填 | 省略/空字符串保留原值 | 非空替换值最多 4096 UTF-8 字节，不裁剪；private_key 模式禁止出现该字段 |
| `private_key` part | private_key 模式必需 | 新增/替换时必需 | 单文件 part，最大 65,536 字节；原始字节保存 |
| `passphrase` | private_key 模式可选 | 上传/替换时可选 | 最大 4,096 UTF-8 字节；不裁剪、不截断；空值按无口令尝试解析，不复用旧值 |
| `group_name` | 可省略，默认空字符串 | 可省略，省略清为空 | 去除首尾空白后最多 128 UTF-8 字节 |
| `remark` | 可省略，默认空字符串 | 可省略，省略清为空 | 最多 1024 UTF-8 字节 |

**multipart 固定资源预算**：整个请求体最大 131,072 字节（128 KiB）；`metadata` JSON part 最大 49,152 字节（48 KiB）；所有 part headers 合计最大 8,192 字节，每个 part 最多 8 个 header 项；part 名称唯一且只允许契约定义的两项。服务端必须通过有界 Reader 累计实际读取字节，在受限内存中完成原始 headers 预算检查后使用 NextRawPart，不信任 `Content-Length`；使用原始 part 读取，拒绝 `Content-Transfer-Encoding`，不得触发默认 multipart 临时目录溢写。超过任一限制拒绝，不截断、不转换内容。

**全局资源准入**：私钥上传接收全进程最多同时 2 个，满额立即拒绝，不排无界等待队列；私钥解析/KDF 全进程共享最多 1 个执行名额，上传校验、Test 和 Creation 共用，满额立即拒绝。取消请求或 HTTP 超时不能提前释放解析名额；只有实际解析/KDF 完成后才释放。复用 `server.request_timeout`，不增加单独上传超时；HTTP 请求截止不能强制中断同步解析/KDF。

**解析限制**：OpenSSH bcrypt rounds 保留固定依赖已有 2,048 轮上限，不未经测量擅自降低；OpenSSH、PEM PKCS#1 RSA、PEM PKCS#8 RSA 统一限制 RSA 模数最大 16,384 bit。PEM RSA 完整解析前执行必要的低成本结构预检。不得声称 context、HTTP 超时或客户端取消能强制终止同步 KDF/解析；资源名额必须保持到真实计算结束。

Host 在保存前统一去除首尾空白、DNS 转小写、IP 按标准地址格式规范化；IPv6 不含方括号，连接地址使用安全的 Host/Port 组合方法。信任目标使用保存后的 Host/Port；只比较规范化值，不按 DNS 解析结果合并不同目标。禁止传入任何 Host Key、公钥、指纹、密码密文、Passphrase 密文、私钥存储名或 `config_version` 字段。

`HostKeyView` 字段：`host: string`、`port: integer`、`algorithm: string`、`fingerprint: string`（OpenSSH `SHA256:` 格式）。此对象不含完整公钥。

`ServerView` 字段：`id`、`name`、`host`、`port`、`username`、`auth_type`、`credential_configured`、`group_name`、`remark`、`created_at`、`updated_at`，以及 `host_key: HostKeyView | null`。`credential_configured` 仅表示所选模式的本地凭据对象/密文存在，不代表文件可读、口令可解密或远端认证成功。`host_key` 只是展示信息，不是完整公钥。所有写入和详情响应使用相同安全 DTO；绝不返回密码、Passphrase、私钥内容（包括其原生加密字节）、数据库密文/nonce、主密钥或容器物理路径。

| 方法与路径 | 请求 | 成功 data |
|---|---|---|
| `GET /api/servers` | 可选 query `search`（名称包含查询）、`group_name`（精确分组匹配）；未给筛选项即全部 | `{"items":[ServerView]}`，按名称、ID 升序 |
| `POST /api/servers` | JSON 密码创建或 multipart 私钥创建，见上文 | `ServerView`，HTTP 201 |
| `GET /api/servers/{id}` | 无 | `ServerView` |
| `PUT /api/servers/{id}` | JSON 普通/密码编辑或 multipart 私钥新增/替换，见上文 | `ServerView`，HTTP 200 |
| `DELETE /api/servers/{id}` | `{}` | `{}`；不存在返回 `SERVER_NOT_FOUND`，有活动或预占名额返回 `SERVER_HAS_ACTIVE_SESSIONS` |
| `POST /api/servers/{id}/test` | `{}`；仅测试已保存配置 | `{"host":"快照目标","port":22,"username":"快照用户名","authenticated":true}` |
| `POST /api/servers/{id}/host-key/confirm` | 只能包含必填 `probe_id: string` | 更新后的 `HostKeyView` |

搜索使用参数化查询，将搜索文本作为字面值，不允许通过 `%`、`_` 或 SQL 片段改变查询语义。分组无需独立接口。

测试或创建需要 Host Key 确认时，返回 HTTP 409、`code: "HOST_KEY_CONFIRMATION_REQUIRED"`，`data` 为：

```text
probe_id: 32 随机字节的无填充 Base64URL 字符串
expires_at: UTC 时间
host: 目标 Host
port: 目标 Port
algorithm: 实际探测算法
fingerprint: 实际探测 SHA256 指纹
previous_fingerprint: 原指纹或 null
reason: "first_trust" | "key_changed"
```

首次信任及 Host/Port 修改后清除信任均为 `first_trust`，原指纹为 null；同一目标公钥变化为 `key_changed`。该响应不包含可接管 Session ID、完整公钥、认证材料或内部版本。显示探测信息不能自动调用确认；用户确认后，客户端重新调用原测试/创建操作。

确认凭证关联当前登录会话、服务器、连接目标、配置版本、原信任和探测公钥。有效期/容量使用 `ssh.host_key_probe_ttl` / `ssh.host_key_probe_limit`。非法、过期、已用或已失效凭证统一 `INVALID_PROBE`；配置在探测期间变化返回 `SERVER_CONFIG_CHANGED`，需重新探测。确定回滚返回 `STORAGE_ERROR`，原凭证在原有效期内可重试；提交不确定按架构版本失效与重载规则返回 `SERVER_UNAVAILABLE`，不得盲目重试旧确认。确认成功但响应丢失后，重新测试并按当前信任继续，不重复保存原 probe。



## 11.4 Session API 与绑定凭证

`SessionView` 为当前内存 Session 快照：

| 字段 | 类型与语义 |
|---|---|
| `id`、`server_id` | string，不可预测 ID |
| `name` | string，创建时服务器名称快照 |
| `host`、`username` | string，创建时固定的非敏感连接目标，不能从已编辑服务器记录覆盖 |
| `port` | integer，快照端口，1～65535 |
| `status` | string，`CONNECTING`、`CONNECTED`、`DISCONNECTED` 或 `CLOSING` |
| `created_at` | UTC 时间字符串，创建开始时间 |
| `disconnected_at` | UTC 时间字符串或 null，最近进入离线的时间，尚未离线时 null |
| `last_attached_at` | UTC 时间字符串或 null，最近绑定成功时间，尚未绑定时 null |
| `offline_expires_at` | UTC 时间字符串或 null，当前离线截止时间，仅 DISCONNECTED 时非空 |
| `remaining_seconds` | 非负整数；离线剩余秒数向上取整，非离线时 null |
| `binding_generation` | 非负安全整数，当前绑定代次，初始 0，仅绑定成功时递增 |
| `current_cols`、`current_rows` | 正整数，当前 PTY 尺寸；CONNECTING 时为请求的初始尺寸 |

| 方法与路径 | 请求 | 成功 data |
|---|---|---|
| `GET /api/sessions` | 可选 query `server_id` | `{"items":[SessionView]}`，创建时间、ID 升序；仅运行中的 Manager 索引 |
| `POST /api/servers/{id}/sessions` | `{"cols":120,"rows":32}`；两项必填正整数，受 terminal 上限约束 | `SessionView`，HTTP 201；SSH/PTY/Shell 就绪且状态 DISCONNECTED、代次 0，不直接签发凭证 |
| `GET /api/sessions/{id}` | 无 | `SessionView`；已关闭或不存在返回 `SESSION_NOT_FOUND` |
| `POST /api/sessions/{id}/takeover` | `{"mode":"attach","expected_generation":0}`；两项均必填 | `{"credential":"一次性凭证","expires_at":"UTC 时间"}` |
| `DELETE /api/sessions/{id}` | `{}` | `{}`；进入幂等关闭流程后返回，尚在回收时详情可为 CLOSING；已不存在也成功 |

`mode` 仅允许以下两值：

- `attach`：用于首次绑定和自动恢复，只在 DISCONNECTED 且预期代次匹配时签发。代次相同但当前仍有绑定返回 `BIND_BUSY`，按协议有限重试；代次不同返回 `BIND_CONFLICT` 并停止自动恢复。
- `takeover`：仅由用户主动发起，可用于 CONNECTED 和 DISCONNECTED；仍必须匹配读取到的 `expected_generation`。API 只签发凭证，不解除旧绑定、不改变状态或离线期限。

状态 CONNECTING/CLOSING、已到离线期限或应用退出不能签发或提交绑定。即使离线扫描尚未开始关闭，只要截止时间已到，签发和提交都返回 `SESSION_NOT_READY`，不能借扫描延迟延长保留期限。预期代次在签发和 WebSocket 提交时都重新校验。多个同代次请求可以各自拿到凭证，但提交最多一个成功。

绑定代次无法继续递增时，以 `counter_exhausted` 关闭 Session，本次绑定返回 `SESSION_NOT_READY`；不能把整数回绕成已使用的代次。

凭证使用 32 随机字节、无填充 Base64URL；只在内存中以哈希索引保存，绑定当前登录会话、Session ID、mode、expected_generation、到期时间。TTL 为 `session.binding_credential_ttl`，全进程容量为 `session.binding_credential_limit`；清过期后容量仍满返回 `CREDENTIAL_LIMIT`。不得通过列表、详情、URL 或日志泄露。

凭证在 WebSocket 绑定原子提交时消费，并使该 Session 的其他凭证失效。提交前失败不消费仍有效的凭证，可在原有效期内新建 WS 重试；客户端若无法确定是否提交，仍按协议使用上次成功获知的代次有限恢复，冲突后停止，由用户刷新状态并主动操作，不能假定旧凭证仍有效或自动采用查询到的新代次。提交后响应丢失不回滚绑定。退出登录、登录过期、Session 关闭、进程退出使关联凭证失效；过期项在签发前及定期清理时回收。具体同步流程见[架构绑定](web-ssh-architecture-implementation-plan.md#binding)。

同代次竞争的失败端不保证只收到一种错误：若先发现代次变化，返回 `BIND_CONFLICT`；若赢家已删除其他凭证、失败端已找不到凭证，返回 `INVALID_CREDENTIAL`。客户端必须停止使用失败凭证；自动恢复不得改用查询到的新代次重新抢占，后续按协议判断并等待用户主动操作。

Session 创建名额包含 CONNECTING 与尚未回收的 CLOSING。未知信任先临时探测，不预占正式名额；已预占后发现 Host Key 变化，认证前关闭连接、清理资源并释放名额，再按旧快照版本验证后返回确认要求。创建初始元数据写入失败不能发布可用 Session，细节见[架构存储](web-ssh-architecture-implementation-plan.md#storage-runtime)。

## 11.5 WebSocket 入口

`GET /ws/sessions/{session_id}` 验证 Cookie、Origin、应用状态和连接容量后升级。超过 `websocket.max_connections` 返回 HTTP 429、`CONNECTION_LIMIT_REACHED`；未升级时使用本章 HTTP 错误结构。升级后首条消息提交凭证，二进制输入输出与全部控制消息仅按[WebSocket 协议](web-ssh-websocket-protocol.md#transport)定义。

HTTP 接管准备与 WebSocket 提交是两个请求；不得把 API 成功当成已经接管，也不得在 HTTP 返回前关闭旧通道。WS 错误不代表 SSH Session 关闭。

<a id="error-codes"></a>
## 11.6 统一错误码

下表是 HTTP 与 WS 共享名称的唯一注册表。`HTTP —` 表示只在已升级 WS 使用；WS 对应关闭码及是否允许恢复由[协议消息](web-ssh-websocket-protocol.md#messages)定义。所有 `error` 都结束该浏览器通道；不使用“发送后可能继续”的模糊规则。

| code | HTTP | 含义及处理 |
|---|---:|---|
| `INVALID_REQUEST` | 400 | HTTP JSON/multipart 字段或查询参数非法 |
| `INVALID_MESSAGE` | — | WS 消息格式、类型或发送状态非法 |
| `MESSAGE_TOO_LARGE` | 413 | HTTP/WS 完整消息超限；拒绝继续读取 |
| `INVALID_SIZE` | 400 | 初始或后续终端尺寸非法 |
| `AUTH_REQUIRED` | 401 | 登录缺失、过期或已撤销，重新登录 |
| `AUTH_FAILED` | 401 | 管理密码错误，不区分内部细节 |
| `PERMISSION_DENIED` | 403 | Origin、凭证归属或操作权限不符 |
| `LOGIN_RATE_LIMIT` | 429 | 登录尝试超限，附 Retry-After |
| `LOGIN_SESSION_LIMIT` | 429 | 有效登录会话容量已满 |
| `SERVER_NOT_FOUND` | 404 | 服务器不存在或已删除 |
| `SERVER_HAS_ACTIVE_SESSIONS` | 409 | 存在活动/创建中/回收中的会话，不可删除 |
| `SERVER_CONFIG_CHANGED` | 409 | 探测快照已过时，需要重新探测 |
| `SERVER_UNAVAILABLE` | 503 | 状态暂不可确定或未就绪；重载后再尝试，禁止沿用旧 probe |
| `HOST_KEY_CONFIRMATION_REQUIRED` | 409 | 尚未信任或公钥变化；data 为确认信息 |
| `INVALID_PROBE` | 409 | probe 无效、过期、已消费或已失效；重新探测 |
| `HOST_KEY_PROBE_LIMIT` | 429 | 探测凭证容量已满 |
| `STORAGE_ERROR` | 500 | 数据库操作失败且已确认回滚；不得宣称修改成功 |
| `SSH_CONNECT_TIMEOUT` | 504 | SSH 建连整体超时 |
| `SSH_CONNECT_FAILED` | 502 | TCP 或 SSH 握手失败，非 Host Key 确认场景 |
| `SSH_AUTH_FAILED` | 502 | 目标 SSH 认证失败，不等于 Web 登录失效 |
| `SSH_PTY_FAILED` | 502 | 目标拒绝 PTY |
| `SSH_SHELL_FAILED` | 502 | Shell 启动失败 |
| `SESSION_LIMIT` | 429 | 单服务器 Session 名额已满 |
| `SESSION_NOT_FOUND` | 404 | 活动 Manager 中不存在该 Session |
| `SESSION_NOT_READY` | 409 | 正在创建、关闭或离线期限已到，不能绑定 |
| `CREDENTIAL_LIMIT` | 429 | 一次性绑定凭证容量已满 |
| `BIND_TIMEOUT` | — | 首条绑定未在期限内完成 |
| `INVALID_CREDENTIAL` | — | WS 凭证无效、过期、已消费或已失效 |
| `INVALID_OFFSET` | — | 请求偏移非法或超过当前输出结束位置 |
| `BIND_BUSY` | 409 | 同代次仍有操作端，或串行操作许可等待超时；本次未提交切换 |
| `BIND_CONFLICT` | 409 | 预期代次已变化，停止自动恢复 |
| `ALREADY_BOUND` | — | 同一 WebSocket 重复发送 bind |
| `OUTPUT_GAP` | — | 已开始输出的通道无法连续衔接，终止后按协议重新绑定 |
| `INPUT_BACKPRESSURE` | — | 有界输入队列在操作等待预算内无法取得空位；终止浏览器通道、停止自动恢复，不重放未发送输入 |
| `CONNECTION_LIMIT_REACHED` | 429 | WS 已升级连接总容量已满，拒绝新的升级 |
| `SLOW_CLIENT` | — | WS 发送压力或客户端处理速度超过限制 |
| `SERVER_SHUTTING_DOWN` | 503 | 应用停止接入，不能发布新资源 |
| `INTERNAL_ERROR` | 500 | 未分类系统错误，日志记录上下文但响应不泄露内部数据 |

| 私钥 HTTP 错误 | HTTP | 含义 |
|---|---|---|
| `PRIVATE_KEY_UPLOAD_BUSY` | 429 | 全局上传接收名额已满 |
| `PRIVATE_KEY_PARSE_BUSY` | 429 | 全局解析/KDF 名额已满 |
| `PRIVATE_KEY_INVALID` | 400 | 上传格式、大小/RSA 或口令校验失败 |
| `PRIVATE_KEY_UNAVAILABLE` | 503 | 已存单个凭据无法安全读取或解析 |
| `PRIVATE_KEY_STORAGE_UNAVAILABLE` | 503 | 专用卷或安全写入不可用 |

私钥认证新增的本地故障必须与 `SSH_AUTH_FAILED` 区分：上传接收名额满、解析/KDF 名额满均应立即拒绝（HTTP 429），不能排无界队列；单个私钥对象缺失/损坏属于关联服务器凭据不可用；私钥卷/目录不可用属于存储能力故障，暂停私钥写入与回收。6-K.0 已定稿，新增错误登记如下；不得提前把本地文件故障映射成远端认证失败，也不得把单个凭据故障塞进 DB `State.unavailable`。

<a id="close-reasons"></a>
## 11.7 SSH Session 关闭原因

| reason | 含义 |
|---|---|
| `user_closed` | 用户主动关闭 |
| `idle_timeout` | 浏览器离线超过期限 |
| `ssh_exit` | 远端 Shell 正常退出 |
| `ssh_error` | SSH 连接、输入、resize 或传输故障，包括在途操作超时 |
| `keepalive_timeout` | SSH Keepalive 达到连续超时窗口上限 |
| `server_shutdown` | 应用正常退出 |
| `create_failed` | 创建失败后清理，仅元数据，尚无可用浏览器绑定 |
| `process_restarted` | 启动时清理上次遗留的活动元数据，不代表恢复旧连接 |
| `counter_exhausted` | 字节偏移或绑定代次将超过安全整数上限，禁止回绕 |

普通 WS 断开、被接管、协议错误、慢浏览器及 OUTPUT_GAP 都不是 SSH 关闭原因。`closed` 仅表示 SSH 已实际关闭；create_failed 与 process_restarted 不产生面向旧连接的虚假通知。

---

<a id="database"></a>
# 12. SQLite 数据库设计

本章定义持久化结构。SSH Connection、Channel、PTY、OutputBuffer、BrowserBinding、绑定代次、绑定凭证、probe 和 `config_version` 只在内存，不新增对应持久化表。活动 Session 的事实来源是 Session Manager，SQLite 不提供重启恢复。

## 12.1 通用约束

- ID 使用 TEXT；时间使用 INTEGER，单位 UTC Unix 毫秒，API 转成规定的 UTC 字符串。
- 所有字段除明确可空外均 NOT NULL。迁移 SQL 通过中文注释描述表、字段、单位和枚举；SQLite 不使用其他数据库的列级 COMMENT 语法。
- 开启 WAL 与逐物理连接的外键、busy_timeout；最大打开/空闲连接均为 1。实现及备份行为只在[架构存储](web-ssh-architecture-implementation-plan.md#storage-runtime)定义。
- 数据库操作使用参数化 SQL 和短事务；事务中不等待 SSH、WS 或用户确认。

## 12.2 servers

| 字段 | SQLite 类型与约束 | 含义 |
|---|---|---|
| `id` | TEXT PRIMARY KEY | 服务器随机 ID |
| `name` | TEXT | 显示名称 |
| `host` | TEXT | 规范化后的连接 Host |
| `port` | INTEGER，CHECK 1～65535 | SSH 端口 |
| `username` | TEXT | SSH 用户名 |
| `auth_type` | TEXT，CHECK `password`/`private_key` | 当前唯一 SSH 用户认证方式 |
| `password_encrypted` | BLOB，可空 | password 模式的既有 v1 AES-GCM 密文；private_key 模式必须为空 |
| `private_key_object_id` | TEXT，可空，引用私钥对象元数据 | private_key 模式必须引用一个对象；password 模式必须为空 |
| `passphrase_encrypted` | BLOB，可空 | private_key 模式可空；新格式 AES-GCM 密文，AAD 绑定格式版本、用途和服务器 ID；无 Passphrase 用 NULL 表示 |
| `group_name` | TEXT DEFAULT '' | 简单分组，空字符串为未分组 |
| `remark` | TEXT DEFAULT '' | 用户备注 |
| `trusted_host` | TEXT，可空 | 当前信任绑定的 Host |
| `trusted_port` | INTEGER，可空 | 当前信任绑定的 Port |
| `host_key` | BLOB，可空 | SSH 公钥标准 wire 编码，完整字节 |
| `host_key_algorithm` | TEXT，可空 | 公钥算法标识 |
| `host_key_fingerprint` | TEXT，可空 | 完整公钥派生的 SHA256 展示指纹 |
| `created_at` | INTEGER | 创建时间，UTC 毫秒 |
| `updated_at` | INTEGER | 最近资料或信任更新的提交时间，UTC 毫秒 |

新增版本迁移，不修改已应用的 `001_initial.sql`。旧记录迁移为 `auth_type=password`，保留 `password_encrypted` 原字节。迁移必须显式处理旧 `password_encrypted BLOB NOT NULL` 的放宽/表重建；保留服务器 ID、Host Key 信任、索引、时间、sessions 外键及 `ON DELETE CASCADE`，验证迁移不误删 Session 元数据。禁止使用假密码或空密文满足 NOT NULL。

数据库另需私钥对象元数据，用不透明随机对象 ID 定位专用目录中的原始文件，并持久化回收候选时间/状态、完整性摘要及安全删除所需状态；实际表为 `private_key_objects`，含 id、sha256、byte_size、created_at、file_device/file_inode、state、candidate_at、delete_attempts、last_delete_attempt_at、last_delete_error；状态为 promoting/ready/delete_claimed/deleted，扫描索引为 idx_private_key_gc。精确约束和事务见 [6-K.0](stage-6-k-design-contract.md) 与新增 002 migration。私钥内容和客户端文件路径不入库；文件字节只在 `/var/lib/web-ssh/ssh-keys/`。

必须通过 CHECK/事务保证认证互斥：`password` 模式有非空密码密文且私钥引用/Passphrase 均为空；`private_key` 模式有私钥对象引用、密码密文为空，Passphrase 可空。Host Key 五个信任字段仍同时为空或同时非空，非空时绑定当前 Host/Port。建立原有 `idx_servers_group_name_name(group_name, name)`；名称不设唯一约束。

## 12.3 sessions

| 字段 | SQLite 类型与约束 | 含义 |
|---|---|---|
| `id` | TEXT PRIMARY KEY | Session 随机 ID |
| `server_id` | TEXT，FK servers(id) ON DELETE CASCADE | 所属服务器 |
| `name` | TEXT | 创建时服务器名称快照 |
| `host`、`username` | TEXT | 非敏感连接快照 |
| `port` | INTEGER，CHECK 1～65535 | 连接快照端口 |
| `status` | TEXT，CHECK 五个生命周期状态 | 最近持久化状态，不是连接事实 |
| `created_at` | INTEGER | 开始创建时间，UTC 毫秒 |
| `disconnected_at` | INTEGER，可空 | 最近进入离线时间，UTC 毫秒 |
| `last_attached_at` | INTEGER，可空 | 最近绑定成功时间，UTC 毫秒 |
| `closed_at` | INTEGER，可空 | 完成关闭时间，UTC 毫秒 |
| `close_reason` | TEXT，可空，非空值限 §11.7 枚举 | 关闭原因 |

`status` 允许 `CONNECTING`、`CONNECTED`、`DISCONNECTED`、`CLOSING`、`CLOSED`。建立 `idx_sessions_server_id_status(server_id, status)`。不持久化认证材料、完整 Host Key、绑定凭证、输出、终端对象或可供恢复的连接对象。

服务器删除前在服务器同步边界内确认所有活动/预占名额已回收；外键级联清理实际已经结束的 Session 元数据；若之前 UPDATE 失败，数据库 status 仍可能陈旧，活动判断只依据内存名额，不要求历史行的 status 已是 CLOSED。不能用 SQL 级联替代关闭活动 SSH。第一版不提供历史归档 API。

初始 INSERT 失败不得发布 Session。运行期间元数据写入失败不回滚内存状态、不恢复旧绑定、不阻止资源回收；错误必须记录且写入限时，不创建无界重试队列。启动时将遗留非 CLOSED 记录置为 CLOSED，原因 process_restarted；启动清理失败则不开放服务。

## 12.4 admin_credentials

| 字段 | SQLite 类型与约束 | 含义 |
|---|---|---|
| `id` | INTEGER PRIMARY KEY，CHECK id = 1 | 唯一管理员记录 |
| `password_hash` | TEXT | Argon2id PHC 字符串，包含参数与 salt |
| `created_at`、`updated_at` | INTEGER | UTC 毫秒 |

不增加用户名、角色、权限或用户管理功能。

## 12.5 web_auth_sessions

| 字段 | SQLite 类型与约束 | 含义 |
|---|---|---|
| `id` | TEXT PRIMARY KEY | 内部登录会话 ID，不是 Cookie 令牌，不对外返回 |
| `token_hash` | BLOB UNIQUE，长度 32 | Cookie 令牌 SHA-256 哈希 |
| `created_at` | INTEGER | 登录时间，UTC 毫秒 |
| `expires_at` | INTEGER，CHECK expires_at > created_at | 绝对到期时间，UTC 毫秒 |

有效登录以记录存在且未到期为基础，并受当前进程的撤销/冻结状态约束；退出按 [§11.2](#api-auth) 的持久化成功边界执行。建立 `idx_web_auth_sessions_expires_at(expires_at)`。进程重启不必注销仍有效的 Web 登录，但所有内存 probe 和绑定凭证已失效、旧 SSH 不恢复。

## 12.6 schema_migrations

`version INTEGER PRIMARY KEY`、`applied_at INTEGER NOT NULL`（UTC 毫秒）。迁移按版本有序执行，重复启动不重复应用；迁移失败停止启动。不能自动删除或重建现有数据库来掩盖迁移失败。

---

# 13. 后端模块边界

后端目录、模块职责和内部操作统一见[架构模块](web-ssh-architecture-implementation-plan.md#modules)。采用 `config`、`auth`、`crypto`、`privatekey`、`database`、`server`、`sshclient`、`session`、`websocket`、`httpapi`，不在本文重复定义目录或内部结构。`privatekey` 专门负责私钥原始文件、对象元数据、短期租约及回收，不负责文件应用层加密。

产品约束：Session Manager 持有 SSH 生命周期；HTTP 和 WebSocket 负责协议接入、校验和响应，不能将关闭浏览器的事件直接变成关闭 SSH。所有权、锁顺序、唯一 Reader/Writer、输出缓存和关闭流程均按架构与协议实现。

---

# 14. 前端模块边界

使用 Vue 3 + TypeScript + Vite、xterm.js，Pinia 仅管理必要共享状态。主要页面为 `/login` 和 `/`，主页面包含服务器列表、终端工作区及独立会话管理。

明确区分服务端 Session 数据与本地标签、xterm.js 实例、WebSocket、主题状态；终端实例不能直接当成普通 JSON 状态。切换标签保留实例与连接，关闭视图释放资源而不调用关闭 SSH API。

目录职责见[架构模块](web-ssh-architecture-implementation-plan.md#modules)。输出队列、已处理偏移、截断重建、断线 drain 与有限恢复仅按[协议客户端算法](web-ssh-websocket-protocol.md#client-tracker)实现，不在各组件中另写恢复逻辑。

---

# 15. Docker 部署

最终构建为单容器。

1. 前端构建。
2. Go 后端构建。
3. 将前端静态文件嵌入 Go 程序。
4. 生成最终运行镜像。
   正式镜像构建和部署交给 ops 执行；项目交付 Dockerfile、Compose、配置和部署说明。可参考 `/Users/x/workspaces/deploy-agent/Dockerfile`，不复用无关业务逻辑。

建议目录：

```text
web-ssh/
├── docker-compose.yml
├── Dockerfile
├── config.yaml
├── .env
├── data/
└── ssh-keys/  # 私钥认证启用后独立挂载；宿主 UID/GID 与权限按本章要求准备
```

示例 Compose：

```yaml
services:
  web-ssh:
    container_name: web-ssh
    restart: unless-stopped
    stop_grace_period: 30s
    ports:
      - "127.0.0.1:8080:8080"
    volumes:
      - ./config.yaml:/app/config.yaml:ro
      - ./data:/data
      # 私钥原始文件专用持久化卷；宿主目录需预先设置 UID/GID 10001:10001 与受限权限
      - ./ssh-keys:/var/lib/web-ssh/ssh-keys
    environment:
      TZ: Asia/Shanghai
      APP_ENCRYPTION_KEY: ${APP_ENCRYPTION_KEY}
      APP_ADMIN_PASSWORD: ${APP_ADMIN_PASSWORD:-}
    networks:
      - global_net

networks:
  global_net:
    external: true
```

如果部署环境使用 Nginx Proxy Manager：

- 通过 NPM 反向代理访问。
- 开启 WebSocket 支持。
- 配置合理的代理读取超时时间。
- 不需要把 Go 服务直接暴露到公网。
- SQLite 数据目录必须使用持久化 Volume。
- 私钥原始文件目录 `/var/lib/web-ssh/ssh-keys/` 必须使用独立持久化 Volume；宿主目录由部署方预建并配置 UID/GID 10001:10001、目录 0700/文件 0600 及父目录/ACL 访问边界。不能仅凭镜像目录权限推断 bind mount 安全。
- 加密主密钥必须独立备份；私钥卷也必须作为敏感明文私钥数据保护。
- 外部访问来源配置为实际 HTTPS Origin；NPM 转发不改变 Cookie Secure 策略。
- 应用 `server.shutdown_timeout` 必须小于 Compose 停止宽限期；容器将 SIGTERM 传给 Go 主进程。
- 本地验证与 ops 的 Docker/NPM 验收分别记录，没有实际执行不得标记通过。
- 首版备份采用受控停机：停止服务访问和进程、关闭数据库后，生成自包含恢复集，包含一致 SQLite 数据库、该数据库引用的全部私钥文件、文件清单及完整性校验信息、匹配的主密钥及其独立保管信息、程序和数据结构版本。备份会终止现有 SSH Session。首版不宣称支持在线一致性备份；备份保留份数和天数由部署运维策略决定。恢复必须使用同一恢复集，不得把历史数据库与当前生产私钥目录任意拼配。

---

# 16. 日志与异常处理

需要记录：

- Web 登录成功或失败。
- 服务器创建、修改和删除。
- SSH 连接成功或失败。
- Session 创建。
- Session 接管。
- WebSocket 断开。
- Session 自动关闭。
- Session 手动关闭。
- SSH 异常断开。
- Host Key 变化。

日志不能记录：

- Web 管理密码。
- SSH 明文密码、Passphrase。
- SSH 私钥原始字节/内容、Signer、私钥存储物理路径、上传文件名中的敏感路径信息。
- SSH 认证凭据及其数据库密文、nonce。
- 完整终端输入、完整终端输出及不必要的敏感业务数据。
- Cookie、登录令牌、绑定凭证、probe_id、SSH 密文、nonce 或主密钥。

错误信息需要区分：

- SSH 连接超时。
- SSH 远端认证失败。
- 本地私钥文件缺失/损坏或私钥存储卷不可用（与远端拒绝认证区分）。
- Passphrase 密文格式错误或无法解密（数据库启动故障按 §5.3 处理）。
- Host Key 不匹配。
- 目标服务器拒绝 PTY。
- WebSocket 异常。
- Session 不存在。
- Session 已关闭。
- Session 数量达到上限。

前端应显示用户可以理解的错误提示，不要只显示内部异常堆栈。

---

# 17. 开发实施顺序

实施顺序、阶段依赖和验证任务只定义在[分阶段实施指南](web-ssh-implementation-stages.md#traceability)。七阶段依次为：项目基础设施、登录与服务器管理、首条真实终端链路、Session 生命周期与输出缓存、会话管理与跨设备接管、UI 与移动端完善、稳定性测试与交付。

第三阶段已具备通用绑定凭证/attach、最终协议、最小完整缓存与单次回放、输出 tracker、离线计时、Keepalive 和幂等关闭。第四阶段完善自动恢复、缺口及慢客户端/生命周期竞争；第五阶段只扩展主动接管和会话管理，不能重设计绑定机制。

不得以“后续阶段再实现”为理由，在第三阶段引入无凭证绑定、无界输出队列、由 WS 持有 SSH 或永不回收的 Session。

---

<a id="acceptance"></a>
# 18. 验收标准

第一版完成必须满足以下条件：

- [ ] 浏览器能够正常登录。
- [ ] 能够添加和管理多台 SSH 服务器。
- [ ] SSH 密码加密保存，旧密码 v1 数据兼容。
- [ ] 支持密码/上传私钥两种互斥认证；私钥原始字节保存，Passphrase 独立加密且不回传。
- [ ] SSH Host Key 能够校验。
- [ ] 可以打开真实 SSH 终端。
- [ ] 支持多个终端标签。
- [ ] 关闭浏览器不会自动关闭 SSH Session。
- [ ] 浏览器离线期间仍然读取 SSH 输出。
- [ ] 输出缓存同时受到字节数和历史行数限制。
- [ ] 能够查看已有 Session 列表。
- [ ] 能够从另一个设备接管 Session。
- [ ] 同一 Session 不允许两个设备同时操作。
- [ ] 重新接管能够恢复当前保留输出；已丢失历史明确提示，不能宣称完整恢复屏幕。
- [ ] 每条 WS 单次回放，后续输出缺口终止浏览器通道后有限恢复。
- [ ] xterm.js 回调后才推进偏移，断线等待已提交 write，不重复写入已处理输出。
- [ ] 缓存只保留原始字节，不解析 UTF-8/ANSI；有缺口时重建终端，提示在字节流外。
- [ ] 初次绑定、自动 attach 与主动 takeover 共用凭证提交机制，旧绑定与延迟回调均失效。
- [ ] 同代次并发接管只有一个提交成功，提交前失败保留旧端。
- [ ] HTTP、WS、数据库和配置按各自唯一权威定义实现。
- [ ] 离线 2 小时后自动关闭。
- [ ] 手动关闭立即生效。
- [ ] 每台服务器 Session 数量限制生效。
- [ ] 默认使用白色主题。
- [ ] 支持白色和深色主题切换。
- [ ] 手机浏览器能够基本使用。
- [ ] Docker 单容器部署成功，私钥专用卷权限和持久化正确。
- [ ] 私钥上传限额、全局并发准入、解析/KDF 资源预算及安全错误符合契约。
- [ ] 私钥文件缺失/损坏只影响关联服务器凭据；数据库 Passphrase 解密失败按启动策略阻止启动。
- [ ] 孤儿回收满足可配置 24 小时默认宽限、引用/租约/事务/备份保护及固定周期预算。
- [ ] 受控停机备份生成自包含恢复集并通过恢复验收；备份会终止现有 SSH Session。
- [ ] NPM 反向代理和 WebSocket 正常工作。
- [ ] 网关重启后能够正确清理失效 Session。

---

# 19. 给 Coding Agent 的执行要求

请将本文档作为项目的核心需求和实现约束。

## 19.1 先设计，再实现

开始编码前：

1. 完整阅读核心、架构、协议及阶段四份文档。
2. 梳理模块之间的依赖关系。
3. 明确 Session 生命周期设计。
4. 明确 WebSocket 与 SSH 的解耦方式。
5. 输出项目目录结构。
6. 核对并落实已定义的数据结构和接口，不另造字段或错误码。
7. 指出实现中可能存在的并发问题。
8. 按阶段指南给出当前获授权阶段的实施计划。

如果发现方案中存在技术问题，先说明原因和建议，不要擅自改变核心设计。

## 19.2 代码质量

- 代码保持简单、清晰、易维护。
- 不要为了抽象而抽象。
- 不要引入不必要的设计模式。
- 关键业务逻辑必须有必要注释。
- SQL 语句需要有清晰注释。
- 避免过度使用 `var`。
- 使用明确的数据类型和返回结构。
- 重要错误必须正确处理。
- 不要忽略 goroutine 和连接资源的释放。
- 不要将大量业务逻辑堆在单个文件中。

## 19.3 变更控制

- 完成代码、文档或配置修改后，先汇报改动和验证结果。
- 只有用户明确同意后，才可执行 git commit 或 git push。
- 未经明确授权，不执行强制推送、合并、变基、reset --hard、clean -fd，也不丢弃已有改动。
- 不要覆盖用户已有的代码和配置。

## 19.4 不允许擅自扩大范围

特别注意：

- 不使用 tmux。
- 不引入 Redis。
- 不引入独立数据库服务。
- 不实现网关重启恢复。
- 不实现 SFTP。
- 不实现文件管理。
- 不实现多用户权限系统。
- 不增加没有实际必要的后台管理模块。

如果确实需要增加功能或依赖，先说明必要性和复杂度，等待用户确认。

**最终目标：做出一个简单、稳定、轻量、可以长期使用的个人 Web SSH 客户端，而不是一个功能臃肿的远程运维平台。**
