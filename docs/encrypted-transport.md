# PocketWire v1：Android ↔ Go 加密 TCP

2026-10-27 已实现并部署。Android 填写 `pocket://47.117.40.74:9992`，使用公网加密 TCP；Go 服务调用真实模型时继续使用 HTTPS。模型 API Key 不进入手机。

## 设计来源和实现范围

参考本地 AuthMesh 的服务器公钥预分发、服务器签名挑战、临时 ECDH 会话密钥与 AEAD 分帧设计。没有导入或复制其订阅、聊天室、Rust 客户端或旧协议代码，也没有增加第三方 Go/Kotlin 运行时库。

AuthMesh 使用 Ed25519/X25519/ChaCha20-Poly1305；这里为匹配现有 Android API 26 下的系统密码接口，使用 P-256 ECDSA/ECDH、HKDF-SHA256 与 AES-256-GCM。**这是本项目的 PocketWire v1，不与 AuthMesh 二进制线缆协议兼容。** 算法由 Go 与 Android/Java 标准密码库实现，项目只实现握手、密钥派生参数、分帧和代理。

```text
手机 WebView
  │ 本机回环 HTTP：127.0.0.1:<动态端口>
Android 原生 WireGateway
  │ PocketWire 加密 TCP：47.117.40.74:9992
Go securewire.Listener → 原有 HTTP Handler / Bearer 鉴权 / SSE
  │ HTTPS（仅 live 模式调用）
模型服务
```

本机回环接口仅绑定设备自身的 127.0.0.1。网页、访问口令、API 请求和流式响应经过公网前都已加密，不存在公网 HTTP 页面引导步骤。入口不是通用代理，只服务原有 Agent HTTP Handler；Android 代理只转发有界的现有网页请求，不支持 CONNECT、WebSocket、分块上传或任意目标转发。

## 密钥和身份

- 服务端长期签名私钥：`/etc/pocket-agent/transport/signing.key`，root:pocket-agent、0640，仅服务组可读。
- 私钥父目录：root:pocket-agent、0750；模型/访问口令配置仍为 root:0600。
- 客户端固定公钥：`android/app/src/main/res/raw/pocket_server_public.pem`，经已验证主机身份的 SSH 通道取得。
- 当前 SPKI DER 的 SHA-256 指纹：`e5713636ec70ae659b667ac294b1b14ace17bc10b9d4c669e8abb5f7dcc8078b`。
- 服务端没有把长期私钥、会话密钥或模型密钥发给客户端；公钥不属于秘密。
- 握手认证服务器身份，客户端用户权限继续由加密通道内的 Bearer 访问口令决定。本版没有引入客户端证书或设备公钥白名单。

新部署可在服务器以 root 执行 `sh init-transport.sh`，它拒绝覆盖已存在的 transport 目录。不要把 AuthMesh 示例身份或旧项目私钥搬进本项目。

## 握手与记录格式

所有整数使用大端编码。帧通用格式：`uint32 payload_length || payload`。

1. 服务端发送 `PKS1 || challenge[32] || ephemeral_P256_public[65] || uint16 signature_length || DER_signature`。签名为 SHA256withECDSA，签名原文是 `PocketWire-v1/server || challenge || ephemeral_P256_public`。
2. Android 用固定 SPKI 公钥验签，失败时结束连接；验证通过后创建自己的临时 P-256 密钥，发送 `PKC1 || client_ephemeral_public[65]`。
3. 双方执行 P-256 ECDH。派生上下文为 `SHA256(PocketWire-v1/session || challenge || server_ephemeral_public || client_ephemeral_public)`。
4. HKDF-SHA256 的 salt 为 challenge、info 为上述 32 字节摘要，输出 72 字节：客户端到服务端 AES 密钥 32 字节、反方向 AES 密钥 32 字节、两个方向各 4 字节 nonce 前缀。
5. 数据帧 payload 为 `uint64 sequence || ciphertext_and_GCM_tag`。nonce 为方向前缀与 8 字节序号拼接；AAD 为握手上下文摘要、方向字节（c2s=0/s2c=1）、4 字节 payload_length 和 8 字节序号。
6. 加密明文为 `kind || bytes`；kind=1 表示非空数据，kind=2 表示无数据的认证结束记录。单条数据最多 16 KiB，每方向最多 2^32 条记录。接收序号必须严格连续。

服务端发送认证结束记录；客户端取消或销毁页面时可直接断开 TCP，服务端按中断处理，不将其视为完整回复。未知记录、错误长度、认证失败、截断、重复/乱序记录均拒绝；重连使用新的临时密钥。传输防重放不替代业务 API 的去重键与 revision。

## 接入与生命周期

服务器配置增加：

```text
POCKET_WIRE_LISTEN=0.0.0.0:9992
POCKET_WIRE_KEY=/etc/pocket-agent/transport/signing.key
```

等效 CLI 参数为 `-wire-listen` 与 `-wire-key`，必须成对配置。原回环 HTTP `127.0.0.1:9991` 和 SSH 隧道继续可用。

每个网页请求建立一个加密连接，不做自动业务重试。服务端最多同时接受 48 条加密连接，握手期限 5 秒，现有消息大小、请求超时和模型并发限制继续生效。Android 回环代理最多 16 个工作连接，限制请求头和请求体；流式响应直接转发。页面取消、退出或切换服务器时关闭相应连接，避免后台继续生成。

部署启动脚本在 live 模式要求 `POCKET_BASE_URL` 为 HTTPS。开发用 Go Provider 仍支持本机 HTTP 测试端点；本次没有使用真实 API 配置或消耗模型额度。

## 更换身份与回滚

签名私钥应随服务器敏感配置独立加密备份。更换签名身份需要通过可信渠道更新客户端公钥并发布新版 APK；不能通过未认证的网络响应自动替换固定公钥。当前只有一把服务器身份公钥，未实现双公钥轮换窗口或密钥撤销服务。

若要临时停用公网入口，移除服务器环境文件中的两个 `POCKET_WIRE_*` 配置并重启本项目服务，再使用原 SSH 隧道。上线前环境文件副本位于 `/etc/pocket-agent/pocket-agent.env.before-wire`，权限 0600；旧应用版本 `20261027-ssh-01` 仍保留。回滚时需同时恢复匹配的应用版本与环境配置，不回滚或删除数据目录。

## 最小验证与实际边界

- 9 项 Go 核心检查通过，其中本轮只新增 1 项协议检查：有界分帧、多记录 HTTP 流、错误固定公钥、篡改、重放、反射和截断拒绝。
- 保留 1 项 Kotlin 地址测试，增加 pocket 地址与公网 HTTP 拒绝检查；开发 APK 构建成功。
- 隔离 API 35 模拟器通过公网 9992 直接连接真实 Linux Go 服务：页面加载、原生导出通道建立、登录、演示时间工具 SSE、流式回复与取消通过，无网页错误。取消用足够长的演示回复验证，避免短回复先于公网取消请求完成。
- 升级后健康检查、鉴权和已有聊天历史恢复通过；9990/frps 与 9999/Node 保持原服务。

当前交付为配对公钥的开发 APK；未做真机与其他 Android 版本验收。此自定义协议没有经过独立密码协议审计，不宣称达到 TLS 的成熟审计水平；尚未完成公网速率限制、自动备份、正式签名发布及长期资源验证。上述模拟器流程使用演示模式；之后已接通真实模型，见[ECNU 模型验证](model-connection.md)。仍为单用户、单实例，不包含后台推送。

实现参考：[Go crypto/ecdh](https://pkg.go.dev/crypto/ecdh)、[Go crypto/hkdf](https://pkg.go.dev/crypto/hkdf)、[Android KeyAgreement](https://developer.android.com/reference/javax/crypto/KeyAgreement)、[Android Cipher](https://developer.android.com/reference/javax/crypto/Cipher)。
