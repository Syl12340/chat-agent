# 自动登录与 Android 更新

当前更新通道（2026-11-05）：**0.20.0-debug / versionCode 17** 已发布到 `android-20261105-03`。原签名、包名、设备身份与完整下载哈希已核对；新增四组件静态资源完整性名单，现有应用内更新可用。未安装真机，见[发布记录](release-0.20.md)。

当前更新通道（2026-11-05）：**0.19.0-debug / versionCode 16** 已发布到 `android-20261105-02`，原签名与完整下载哈希已核对，未安装手机。新包支持图片选择及上传；网页旧包入口会提示升级，已有登录身份和后台接收偏好保留。见[发布记录](release-0.19.md)。以下为历史记录与通用机制。

当前更新通道（2026-11-05）：**0.18.0-debug / versionCode 15** 已发布到 `android-20261105-01`，同签名、包名、完整下载哈希已核对。手机尚未安装；新增的自建后台接收须更新后在关怀面板显式开启，升级不自动开启常驻通知。见[发布记录](release-0.18.md)和[接收说明](push-channel.md)。以下为历史记录与通用更新机制。

当前更新（2026-11-04）：**0.17.0-debug / versionCode 14**，通道 `android-20261104-02`。清单和 APK 下载哈希检查通过，未在手机安装。见[发布记录](release-0.17.md)。以下旧版本为历史记录。

当前更新（2026-11-04）：**0.16.0-debug / versionCode 13** 已发布到 `android-20261104-01`，原包名和签名延续。线上清单与下载哈希检查通过，未在手机安装；打开应用使用现有“更新”入口获取。详见[本次发布](release-0.16.md)。下文旧版本号为历史记录。

当前开发包为 **chat-agent 0.15.0-debug，versionCode 12**，包名仍为 `dev.pocketagent.android.debug`，覆盖更新保留原登录与数据。下载沿用 `pocket://47.117.40.74:9992` 的加密连接，无新增公网端口。[本次设备鉴权与界面](release-0.15.md)。

服务端 0.7.1 已取消显式聊天模式，Android 顶部点“刷新”生效；此次未重新构建或发布 APK。[说明](adaptive-chat.md)。

服务端 0.8.0 增加[活动聊天控制](natural-activities-0.8.md)，仍由网页加载，原 versionCode 7 兼容，无需新 APK。

0.9.0 起服务端已改为[模型驱动的工具运行时](model-driven-runtime-0.9.md)，移除关键词门控；同样刷新网页即可，原安装包兼容。

## 当前鉴权

0.15 起默认设备指纹登记及 challenge 签名登录，经管理员加入白名单后无感连接；旧访问口令只作为兼容的高权限维护入口保留。Android 更新器只使用短期设备会话，过期时通知网页续期。以下早期口令存储说明是历史实现，当前行为以[0.15 说明](release-0.15.md)为准。

## 使用

旧版 0.2.0 没有应用内更新入口，需要先手动安装一次 `bin/chat-agent-android-debug.apk`。保留应用数据、覆盖安装即可。第一次登录输入原有访问口令，此后启动、进程重启和覆盖升级会自动恢复登录。

Android 使用系统 Keystore 的 AES-256-GCM 密钥加密口令，密文保存在应用私有设置中，并绑定实际服务器地址。回环代理随机端口变化不会丢失口令。新包不内置访问口令或模型密钥。退出登录会删除当前服务器保存的口令，服务端更换访问口令后需重新登录。更换服务器不发送旧地址的口令；切回已记住的地址可自动登录。卸载或清除应用数据后需重新登录。

普通浏览器使用当前站点的 localStorage 记住口令，退出时清除；公共设备使用后请退出。Android 使用来源受限的原生消息通道，口令只保存在 Keystore 加密存储，运行时网页内存仍会使用 Bearer 鉴权；这里没有新增设备身份、独立设备令牌或自动撤销机制。

登录后每次启动自动检查一次更新，也可点击顶部“更新”。发现更高版本时选择“下载并更新”，客户端验证：

- 仅从当前服务器的固定鉴权接口取包，不跟随重定向。
- 下载字节数和 SHA-256 与清单一致，包体上限 64 MiB。
- APK 包名、versionCode、versionName 与清单匹配，版本号高于当前版本。
- APK 当前签名证书集合与已安装应用一致；本版不支持签名轮换。

通过检查才向系统安装器授予该 APK 的临时只读 URI。首次需要为 chat-agent 开启“允许来自此来源的应用”，随后仍需在系统窗口确认安装。这是应用内检查和安装流程，不是后台强制安装或通知推送。安装取消后可重新点击“更新”。同包名同签名覆盖安装保留应用数据和服务端聊天。

## 发布下一版

提高 `android/app/build.gradle.kts` 中的 versionCode 和 versionName，使用同一签名构建。不要更换开发机 debug keystore；换签名会被客户端和 Android 拒绝。正式签名包须单独维护发布渠道，不能覆盖当前 debug 包。

```powershell
$env:JAVA_HOME = 'C:\Program Files\Java\jdk-20'
$env:ANDROID_HOME = 'C:\Users\Shaoyilei\AppData\Local\Android\Sdk'
.\android\gradlew.bat --offline --no-daemon -p android :app:assembleDebug
Copy-Item android/app/build/outputs/apk/debug/app-debug.apk bin/chat-agent-android-debug.apk
.\deploy\publish-android.ps1
```

发布脚本从 APK 读取真实版本与包名、验证签名、生成大小与 SHA-256 清单，通过 SSH 上传服务器，拒绝版本回退，然后原子切换发布目录并验证接口鉴权和实际下载哈希。无需重启已配置的服务，也不会调用模型。`-Server`、`-Apk`、`-Release` 可覆盖默认参数。

服务器配置 `/etc/pocket-agent/pocket-agent.env`：

```dotenv
POCKET_ANDROID_RELEASE_DIR=/opt/pocket-agent/android/current
```

首次设置该变量需要重启 pocket-agent，当前服务器已经完成。发布文件归 root:pocket-agent 所有，目录 0750、文件 0640，Agent 只能读取。目录结构：

```text
/opt/pocket-agent/android/
  current -> releases/android-20261103-07
  releases/android-20261103-07/
    manifest.json
    app.apk
```

两个接口均要求现有 Bearer 鉴权，响应不缓存：

- `GET /api/updates/android`：版本、包名、大小、SHA-256 和固定 APK 路径。
- `GET /api/updates/android/{sha256}/app.apk`：对应安装包；发布已变化时返回 409，重新检查即可。

服务按请求固定解析同一发布目录并复核文件哈希，客户端独立复核 APK 哈希与签名。清单与 APK 必须放在新的不可变目录再原子切换，不要原地覆盖在线文件。已有目录保留用于排障；回退服务器发布目录不会使客户端自动降级。

## 验证范围

2026-11-03：0.14.0 / versionCode 11（917,076 字节）已发布至 `android-20261103-06`，SHA-256 `6e8485e00a3db74711d3eb64aa69d659c555e604ddaad3861503ca84b04adb17`。原包名、签名和登录保留，修复识别退出与结果竞争；同签名覆盖安装、原生通道与本地断线恢复流程通过。真机验收待完成。[联合交付](release-0.14.md)。

2026-11-03：0.12.0 / versionCode 9（914,160 字节）已发布至 `android-20261103-04`，SHA-256 `1c8b2f42d0f5da405b9ba7d2c2c87387212648d7228b31fd9da2fb8070bef27a`。延续原签名与登录存储，新增系统语音输入桥。发布验证实际下载与鉴权；隔离模拟器验证播放、重听、停止和识别草稿。[语音交付](release-0.12.md)。

2026-11-03：0.11.0 / versionCode 8（912,356字节）已发布，SHA-256 `f4a79c5c71ea932b2dcdefddae45e2f3417ebf68207416c9652c266ebc9b4733`。原签名、覆盖安装和登录桥保留；隔离模拟器通过本机通知权限、首批基线、投递一次/去重、关闭。通知只针对已同步内容，不提供关闭应用后的远程推送。[联合交付](release-0.11.md)。

2026-11-03：0.7.0（versionCode 7）延续原签名，APK 900,880 字节，SHA-256 `a5ac6007b7755cfa55a76ec686b73b6d41e9f4f49fd644ad7e47f87c6d3bada8`。模拟器通过共同活动完整流程，公网自动登录和更新实际下载哈希通过；原生代码只更新版本号。

2026-11-03：0.6.1 同签名覆盖安装后，模拟器公网加密连接自动登录通过；应用标签与网页品牌均为 chat-agent，更新接口完整下载哈希与清单一致。功能验收见[三项交付说明](companion-0.6.md)。

2026-10-30：API 最小验证覆盖未鉴权拒绝、版本清单、完整下载、过时路径拒绝及损坏包拒绝；原有 Kotlin 地址测试通过。隔离 API 35 模拟器验证 Keystore 密文、网页无明文持久化、应用进程重启自动登录、退出清理，并通过服务器更新入口进行覆盖升级。没有真实手机或其他 Android 版本验收，没有运行完整测试矩阵，也没有消耗模型额度。

实现依据：[Android Keystore AES-GCM](https://developer.android.com/reference/android/security/keystore/KeyGenParameterSpec)、[应用安装授权](https://developer.android.com/reference/android/content/pm/PackageManager#canRequestPackageInstalls())。
