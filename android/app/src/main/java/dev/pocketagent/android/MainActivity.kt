package dev.pocketagent.android

import android.annotation.SuppressLint
import android.app.Activity
import android.app.AlertDialog
import android.content.ActivityNotFoundException
import android.content.Intent
import android.content.res.ColorStateList
import android.graphics.Color
import android.graphics.drawable.GradientDrawable
import android.graphics.drawable.RippleDrawable
import android.net.Uri
import android.net.http.SslError
import android.os.Build
import android.os.Bundle
import android.os.Handler
import android.os.Looper
import android.os.Message
import android.provider.Settings
import android.speech.RecognizerIntent
import android.text.InputType
import android.text.TextUtils
import android.util.Base64
import android.view.Gravity
import android.view.View
import android.view.ViewGroup
import android.view.WindowInsets
import android.view.WindowManager
import android.webkit.CookieManager
import android.webkit.PermissionRequest
import android.webkit.RenderProcessGoneDetail
import android.webkit.SslErrorHandler
import android.webkit.WebChromeClient
import android.webkit.WebMessage
import android.webkit.WebMessagePort
import android.webkit.WebResourceError
import android.webkit.WebResourceRequest
import android.webkit.WebResourceResponse
import android.webkit.WebSettings
import android.webkit.WebStorage
import android.webkit.WebView
import android.webkit.WebViewClient
import android.widget.Button
import android.widget.EditText
import android.widget.FrameLayout
import android.widget.LinearLayout
import android.widget.ProgressBar
import android.widget.TextView
import android.widget.Toast
import android.window.OnBackInvokedDispatcher
import org.json.JSONObject
import java.io.ByteArrayInputStream
import java.io.File
import java.util.UUID
import java.util.concurrent.Executors

/** Small platform-only shell. Chat, authentication and agent state stay on the server. */
class MainActivity : Activity() {
    @Volatile private var server: ServerAddress? = null
    @Volatile private var configuredServer: ServerAddress? = null
    private var gateway: WireGateway? = null
    private lateinit var web: WebView
    private lateinit var content: FrameLayout
    private lateinit var root: LinearLayout
    private lateinit var title: TextView
    private lateinit var progress: ProgressBar
    private lateinit var errorPanel: LinearLayout
    private lateinit var errorText: TextView
    private var pageFailed = false
    @Volatile private var exportPort: WebMessagePort? = null
    @Volatile private var exportMarker = ""
    private data class SpeechReply(val port: WebMessagePort, val id: String, val marker: String, val origin: String)
    private var speechReply: SpeechReply? = null
    private var speechInFlight = false
    private var pendingExport: ByteArray? = null
    private val io = Executors.newSingleThreadExecutor()
    private val prefs by lazy { getSharedPreferences("pocket-shell", MODE_PRIVATE) }
    private val vault by lazy { CredentialVault(this) }
    private val deviceIdentity by lazy { DeviceIdentity(this) }
    private val careNotifications by lazy { CareNotifications(this) }
    private var updateBusy = false
    private var autoUpdateChecked = false
    private var pendingInstall: File? = null
    // Resolved from resources so values-night can switch the shell to true black.
    private val paper by lazy { getColor(R.color.pocket_paper) }
    private val primary by lazy { getColor(R.color.pocket_primary) }
    private val primaryContainer by lazy { getColor(R.color.pocket_primary_container) }
    private val onPrimaryContainer by lazy { getColor(R.color.pocket_on_primary_container) }
    private val surfaceContainer by lazy { getColor(R.color.pocket_surface_container) }
    private val onSurface by lazy { getColor(R.color.pocket_on_surface) }

    override fun onCreate(savedInstanceState: Bundle?) {
        super.onCreate(savedInstanceState)
        window.setSoftInputMode(WindowManager.LayoutParams.SOFT_INPUT_ADJUST_RESIZE)
        buildLayout()
        makeWebView()
        if (Build.VERSION.SDK_INT >= 33) {
            onBackInvokedDispatcher.registerOnBackInvokedCallback(OnBackInvokedDispatcher.PRIORITY_DEFAULT) { handleBack() }
        }
        val saved = prefs.getString("server_origin", null)
        configuredServer = saved?.let { runCatching { ServerAddress.parse(it, BuildConfig.DEBUG) }.getOrNull() }
        if (configuredServer == null) {
            showError("给 chat-agent 一个地址\n\n将本机指纹加入服务器白名单后，即可安全连接。")
            root.post { configureServer(true) }
        } else loadServer()
    }

    private fun dp(value: Int) = (value * resources.displayMetrics.density).toInt()
    private fun button(label: String, action: () -> Unit) = Button(this).apply {
        text = label; isAllCaps = false; setTextColor(onPrimaryContainer); textSize = 12f
        minHeight = dp(48); minimumHeight = dp(48); minWidth = dp(48); minimumWidth = dp(48)
        setPadding(dp(12), 0, dp(12), 0)
        background = RippleDrawable(ColorStateList.valueOf(primary and 0x00FFFFFF or (35 shl 24)),
            GradientDrawable().apply { setColor(primaryContainer); cornerRadius = dp(24).toFloat() }, null)
        setOnClickListener { action() }
    }
    private fun showDialog(dialog: AlertDialog) {
        dialog.show()
        dialog.window?.setBackgroundDrawable(GradientDrawable().apply { setColor(surfaceContainer); cornerRadius = dp(28).toFloat() })
        for (which in listOf(AlertDialog.BUTTON_POSITIVE, AlertDialog.BUTTON_NEGATIVE, AlertDialog.BUTTON_NEUTRAL)) {
            dialog.getButton(which)?.apply { setTextColor(primary); minHeight = dp(48); minimumHeight = dp(48); isAllCaps = false }
        }
    }
    private fun buildLayout() {
        root = LinearLayout(this).apply { orientation = LinearLayout.VERTICAL; setBackgroundColor(paper) }
        val bar = LinearLayout(this).apply { gravity = Gravity.CENTER_VERTICAL; setPadding(dp(16), dp(8), dp(8), dp(8)); setBackgroundColor(paper) }
        title = TextView(this).apply { text = "chat-agent"; textSize = 14f; setTextColor(primary); maxLines = 1; ellipsize = TextUtils.TruncateAt.MIDDLE }
        bar.addView(title, LinearLayout.LayoutParams(0, dp(48), 1f).apply { gravity = Gravity.CENTER_VERTICAL; rightMargin = dp(8) })
        title.gravity = Gravity.CENTER_VERTICAL
        bar.addView(button("刷新") { loadServer() }, LinearLayout.LayoutParams(dp(64), dp(48)).apply { marginEnd = dp(6) })
        bar.addView(button("更新") { checkUpdates(true) }, LinearLayout.LayoutParams(dp(64), dp(48)).apply { marginEnd = dp(6) })
        bar.addView(button("服务器") { configureServer(false) }, LinearLayout.LayoutParams(dp(76), dp(48)))
        root.addView(bar)
        progress = ProgressBar(this, null, android.R.attr.progressBarStyleHorizontal).apply { max = 100; visibility = View.GONE; progressTintList = ColorStateList.valueOf(primary) }
        root.addView(progress, LinearLayout.LayoutParams(-1, dp(2)))
        content = FrameLayout(this)
        root.addView(content, LinearLayout.LayoutParams(-1, 0, 1f))
        errorPanel = LinearLayout(this).apply {
            orientation = LinearLayout.VERTICAL; gravity = Gravity.CENTER; setPadding(dp(25), dp(25), dp(25), dp(25));
            background = GradientDrawable().apply { setColor(surfaceContainer); cornerRadius = dp(28).toFloat() }
        }
        errorText = TextView(this).apply { textSize = 16f; setTextColor(onSurface); gravity = Gravity.CENTER; setLineSpacing(dp(5).toFloat(), 1f) }
        errorPanel.addView(errorText, LinearLayout.LayoutParams(-1, -2))
        errorPanel.addView(button("重试连接") { loadServer() }, LinearLayout.LayoutParams(-1, -2).apply { topMargin = dp(20) })
        errorPanel.addView(button("设置服务器") { configureServer(configuredServer == null) }, LinearLayout.LayoutParams(-1, -2).apply { topMargin = dp(8) })
        content.addView(errorPanel, FrameLayout.LayoutParams(-1, -2, Gravity.CENTER).apply { setMargins(dp(24), dp(16), dp(24), dp(16)) })
        setContentView(root)
        if (Build.VERSION.SDK_INT >= 30) {
            window.setDecorFitsSystemWindows(false)
            root.setOnApplyWindowInsetsListener { view, insets ->
                val bars = insets.getInsets(WindowInsets.Type.systemBars())
                val ime = insets.getInsets(WindowInsets.Type.ime())
                view.setPadding(bars.left, bars.top, bars.right, maxOf(bars.bottom, ime.bottom)); insets
            }
            root.requestApplyInsets()
        }
    }

    @SuppressLint("SetJavaScriptEnabled")
    private fun makeWebView() {
        web = WebView(this).apply {
            setBackgroundColor(paper)
            settings.javaScriptEnabled = true
            settings.domStorageEnabled = true
            settings.allowFileAccess = false
            settings.allowContentAccess = false
            settings.mixedContentMode = WebSettings.MIXED_CONTENT_NEVER_ALLOW
            settings.safeBrowsingEnabled = true
            settings.javaScriptCanOpenWindowsAutomatically = false
            settings.setSupportMultipleWindows(true)
            settings.userAgentString += " PocketAgentAndroid/0.1"
        }
        CookieManager.getInstance().setAcceptCookie(false)
        CookieManager.getInstance().setAcceptThirdPartyCookies(web, false)
        WebView.setWebContentsDebuggingEnabled(BuildConfig.DEBUG)
        web.webViewClient = object : WebViewClient() {
            override fun shouldOverrideUrlLoading(view: WebView, request: WebResourceRequest): Boolean {
                if (server?.contains(request.url.toString()) == true) return false
                if (request.isForMainFrame && request.hasGesture()) openExternal(request.url)
                else if (request.isForMainFrame) showError("已阻止跳转到其他站点。请检查服务器地址，或使用浏览器打开外部链接。")
                return true
            }
            override fun shouldInterceptRequest(view: WebView, request: WebResourceRequest): WebResourceResponse? {
                if (server?.contains(request.url.toString()) == true) return null
                return WebResourceResponse("text/plain", "UTF-8", 403, "Blocked", emptyMap(), ByteArrayInputStream(ByteArray(0)))
            }
            override fun onPageStarted(view: WebView, url: String?, favicon: android.graphics.Bitmap?) {
                closePort(); pageFailed = false; progress.visibility = View.VISIBLE
            }
            override fun onPageFinished(view: WebView, url: String?) {
                progress.visibility = View.GONE
                if (!pageFailed && server?.contains(url) == true) {
                    errorPanel.visibility = View.GONE; web.visibility = View.VISIBLE; attachExportChannel()
                }
            }
            override fun onReceivedError(view: WebView, request: WebResourceRequest, error: WebResourceError) {
                if (request.isForMainFrame) showError("暂时无法连接 chat-agent\n\n请检查网络、服务器地址，以及服务是否已启动。")
                else if (server?.contains(request.url.toString()) == true && request.url.path?.endsWith(".js") == true) showError("页面脚本没有完整加载。\n请点击重试连接；聊天请求不会自动重发。")
            }
            override fun onReceivedHttpError(view: WebView, request: WebResourceRequest, response: WebResourceResponse) {
                if (request.isForMainFrame) showError("服务器返回错误（${response.statusCode}）。\n请检查服务或稍后重试。")
                else if (server?.contains(request.url.toString()) == true && request.url.path?.endsWith(".js") == true) showError("页面脚本加载失败（${response.statusCode}）。\n请点击重试连接。")
            }
            override fun onReceivedSslError(view: WebView, handler: SslErrorHandler, error: SslError) {
                handler.cancel(); showError("HTTPS 证书未通过验证。\n请为服务器配置有效证书后重试。")
            }
            override fun onRenderProcessGone(view: WebView, detail: RenderProcessGoneDetail): Boolean {
                closePort(); content.removeView(view); view.destroy(); makeWebView(); showError("页面进程已退出。\n点击重试可重新连接。"); return true
            }
        }
        web.webChromeClient = object : WebChromeClient() {
            override fun onProgressChanged(view: WebView, value: Int) { progress.progress = value; if (value == 100) progress.visibility = View.GONE }
            override fun onPermissionRequest(request: PermissionRequest) { request.deny() }
            override fun onCreateWindow(view: WebView, isDialog: Boolean, isUserGesture: Boolean, resultMsg: Message): Boolean {
                if (!isUserGesture) return false
                val popup = WebView(this@MainActivity)
                var handled = false
                fun finish(url: String?) {
                    if (handled || url.isNullOrBlank() || url == "about:blank") return
                    handled = true; openExternal(Uri.parse(url)); popup.stopLoading(); popup.destroy()
                }
                popup.webViewClient = object : WebViewClient() {
                    override fun shouldOverrideUrlLoading(view: WebView, request: WebResourceRequest): Boolean { finish(request.url.toString()); return true }
                    override fun onPageStarted(view: WebView, url: String?, favicon: android.graphics.Bitmap?) { finish(url) }
                }
                (resultMsg.obj as WebView.WebViewTransport).webView = popup; resultMsg.sendToTarget()
                root.postDelayed({ if (!handled) { handled = true; popup.destroy() } }, 5000)
                return true
            }
        }
        web.setDownloadListener { url, _, _, _, _ ->
            val origin = server?.origin ?: return@setDownloadListener
            if (url.startsWith("blob:$origin/") && server?.contains(web.url) == true) {
                // Supports older Pocket web versions that still download a Blob.
                web.evaluateJavascript("fetch(${JSONObject.quote(url)}).then(r=>r.text()).then(t=>window.PocketAndroidExports?.saveText(t,'pocket-memory.md')).catch(()=>{});", null)
            } else toast("请通过网页的 Markdown 导出按钮保存文件。")
        }
        content.addView(web, 0, FrameLayout.LayoutParams(-1, -1))
    }

    private fun configureServer(required: Boolean) {
        val input = EditText(this).apply {
            inputType = InputType.TYPE_CLASS_TEXT or InputType.TYPE_TEXT_VARIATION_URI
            setSingleLine(true); hint = getString(R.string.default_server_url)
            setText(configuredServer?.origin ?: getString(R.string.default_server_url)); setSelection(text.length)
        }
        val box = LinearLayout(this).apply { orientation = LinearLayout.VERTICAL; setPadding(dp(24), dp(8), dp(24), 0) }
        box.addView(TextView(this).apply { text = "本机使用安全密钥生成设备指纹。请将指纹加入服务器白名单；私钥保存在本机，退出登录不会更换指纹。"; textSize = 13f; setTextColor(onSurface); setPadding(0, 0, 0, dp(12)) })
        box.addView(input)
        val dialog = AlertDialog.Builder(this).setTitle("连接 chat-agent").setView(box).setCancelable(!required)
            .setPositiveButton("连接", null).setNegativeButton(if (required) "退出" else "取消") { _, _ -> if (required) finish() }.create()
        dialog.setOnShowListener {
            dialog.getButton(AlertDialog.BUTTON_POSITIVE).setOnClickListener {
                val address = try { ServerAddress.parse(input.text.toString(), BuildConfig.DEBUG) }
                catch (error: IllegalArgumentException) { input.error = error.message; return@setOnClickListener }
                if (configuredServer?.origin != address.origin) {
                    autoUpdateChecked = false; pendingInstall = null
                    gateway?.close(); gateway = null; server = null
                    closePort(); web.stopLoading(); content.removeView(web); web.destroy()
                    WebStorage.getInstance().deleteAllData(); CookieManager.getInstance().removeAllCookies(null)
                    makeWebView(); web.clearCache(true)
                }
                configuredServer = address; prefs.edit().putString("server_origin", address.origin).apply(); dialog.dismiss(); loadServer()
            }
        }
        showDialog(dialog)
    }

    private fun loadServer() {
        val address = configuredServer ?: return configureServer(true)
        try {
            if(address.encryptedTCP) {
                if(gateway == null) {
                    val pem = resources.openRawResource(R.raw.pocket_server_public).bufferedReader().use { it.readText() }
                    val publicKey = Base64.decode(pem.lineSequence().filterNot { it.startsWith("---") }.joinToString(""), Base64.DEFAULT)
                    gateway = WireGateway(address, publicKey)
                }
                server = ServerAddress.parse(gateway!!.origin, true)
            } else server = address
            title.text = (if(address.encryptedTCP) "加密 · " else "") + address.host
            pageFailed = false; errorPanel.visibility = View.GONE; web.visibility = View.VISIBLE
            web.loadUrl(server!!.origin + "/")
        } catch (_: Exception) { showError("无法启动本地加密连接。\n请检查服务器配置并重试。") }
    }
    private fun showError(message: String) { pageFailed = true; closePort(); errorText.text = message; errorPanel.visibility = View.VISIBLE; errorPanel.bringToFront(); if (::web.isInitialized) web.visibility = View.INVISIBLE; progress.visibility = View.GONE }
    private fun openExternal(uri: Uri) {
        if (uri.scheme !in listOf("https", "http") || uri.host.isNullOrBlank() || uri.userInfo != null) { toast("仅支持在浏览器打开 HTTP(S) 链接。"); return }
        try { startActivity(Intent(Intent.ACTION_VIEW, uri).addCategory(Intent.CATEGORY_BROWSABLE)) }
        catch (_: ActivityNotFoundException) { toast("没有可用的浏览器。") }
    }
    private fun cancelSpeechReply() {
        val pending = speechReply; speechReply = null
        if (pending != null) runCatching {
            pending.port.postMessage(WebMessage(JSONObject().put("id", pending.id).put("ok", true).put("available", true).put("canceled", true).toString()))
        }
    }
    private fun closePort() { cancelSpeechReply(); exportPort?.close(); exportPort = null; exportMarker = "" }

    private fun speechIntent() = Intent(RecognizerIntent.ACTION_RECOGNIZE_SPEECH)
        .putExtra(RecognizerIntent.EXTRA_LANGUAGE_MODEL, RecognizerIntent.LANGUAGE_MODEL_FREE_FORM)
        .putExtra(RecognizerIntent.EXTRA_LANGUAGE, "zh-CN")
        .putExtra(RecognizerIntent.EXTRA_MAX_RESULTS, 1)
        .putExtra(RecognizerIntent.EXTRA_PROMPT, "说完后可在输入框编辑，点击发送才会提交")

    private fun attachExportChannel() {
        val address = server ?: return
        val remoteOrigin = configuredServer?.origin ?: return
        closePort(); exportMarker = "pocket-export:" + UUID.randomUUID()
        val marker = exportMarker
        val ports = web.createWebMessageChannel(); exportPort = ports[0]
        ports[0].setWebMessageCallback(object : WebMessagePort.WebMessageCallback() {
            override fun onMessage(port: WebMessagePort, message: WebMessage?) {
                if (exportMarker != marker || exportPort !== port || !address.contains(web.url)) return
                val data = message?.data ?: return
                if (data.length > MAX_EXPORT * 2 + 2048) { toast("导出内容过大。"); return }
                try {
                    val payload = JSONObject(data)
                    if (payload.optString("marker") != marker || configuredServer?.origin != remoteOrigin) return
                    val type = payload.optString("type")
                    if (type == "device_identity" || type == "device_sign") {
                        val id = payload.optString("id")
                        val encoded = if (type == "device_sign") payload.optString("payload") else ""
                        fun bindingCurrent() = exportPort === port && exportMarker == marker &&
                            configuredServer?.origin == remoteOrigin && server?.origin == address.origin
                        if (id.length !in 1..64 || data.length > 4096 || encoded.length > 2732) {
                            port.postMessage(WebMessage(JSONObject().put("id", id).put("ok", false).toString())); return
                        }
                        io.execute {
                            if (!bindingCurrent()) return@execute
                            val reply = try {
                                (if (type == "device_identity") deviceIdentity.identity(remoteOrigin)
                                else deviceIdentity.sign(remoteOrigin, encoded)).put("ok", true)
                            } catch (_: Exception) { JSONObject().put("ok", false) }
                            reply.put("id", id)
                            runOnUiThread {
                                if (!isFinishing && !isDestroyed && bindingCurrent() && address.contains(web.url)) {
                                    runCatching { port.postMessage(WebMessage(reply.toString())) }
                                }
                            }
                        }
                        return
                    }
                    if (type == "voice_status" || type == "voice_recognize") {
                        val reply = JSONObject().put("id", payload.optString("id")).put("ok", true)
                        val available = speechIntent().resolveActivity(packageManager) != null
                        reply.put("available", available)
                        if (type == "voice_recognize" && available && !speechInFlight) {
                            speechReply = SpeechReply(port, payload.optString("id"), marker, remoteOrigin)
                            speechInFlight = true
                            try { startActivityForResult(speechIntent(), SPEECH_INPUT); return }
                            catch (_: Exception) { speechReply = null; speechInFlight = false; reply.put("available", false) }
                        } else if (type == "voice_recognize" && speechInFlight) reply.put("busy", true)
                        port.postMessage(WebMessage(reply.toString())); return
                    }
                    if (type in listOf("auth_restore", "auth_save", "auth_forget")) {
                        val reply = JSONObject().put("id", payload.optString("id"))
                        try {
                            when (type) {
                                "auth_restore" -> reply.put("token", vault.read(remoteOrigin) ?: "")
                                "auth_save" -> {
                                    val token = payload.getString("token")
                                    vault.save(remoteOrigin, token)
                                    if (token.startsWith("dvc1_")) deviceIdentity.setSuspended(remoteOrigin, false)
                                    root.post { checkUpdates(false) }
                                }
                                "auth_forget" -> { cancelSpeechReply(); deviceIdentity.setSuspended(remoteOrigin, true); vault.clear(remoteOrigin); pendingInstall = null; careNotifications.clearVisible(remoteOrigin) }
                            }
                            reply.put("ok", true)
                        } catch (_: Exception) { reply.put("ok", false) }
                        port.postMessage(WebMessage(reply.toString())); return
                    }
                    if (type in listOf("care_status", "care_configure", "care_deliver")) {
                        val reply = try {
                            require(data.length <= 100000)
                            val result = when (type) {
                                "care_configure" -> careNotifications.configure(remoteOrigin, payload.getBoolean("enabled"))
                                "care_deliver" -> careNotifications.deliver(remoteOrigin, payload.getString("role"), payload.getJSONArray("items"))
                                else -> careNotifications.status(remoteOrigin)
                            }
                            if (type == "care_configure" && result.optBoolean("request_permission")) {
                                root.post {
                                    if (configuredServer?.origin == remoteOrigin && Build.VERSION.SDK_INT >= 33 && !isFinishing && !isDestroyed) {
                                        requestPermissions(arrayOf(android.Manifest.permission.POST_NOTIFICATIONS), CARE_PERMISSION)
                                    }
                                }
                            }
                            result.put("ok", true)
                        } catch (_: Exception) { JSONObject().put("ok", false) }
                        reply.put("id", payload.optString("id"))
                        port.postMessage(WebMessage(reply.toString())); return
                    }
                    if (type != "save_markdown") return
                    val bytes = payload.getString("content").toByteArray(Charsets.UTF_8)
                    if (bytes.size > MAX_EXPORT) { toast("导出内容超过 4 MiB。"); return }
                    if (pendingExport != null) { toast("请先完成当前保存操作。"); return }
                    val name = payload.optString("filename").takeIf { it.matches(Regex("[A-Za-z0-9_-]{1,128}\\.md")) } ?: "pocket-memory.md"
                    pendingExport = bytes
                    val intent = Intent(Intent.ACTION_CREATE_DOCUMENT).addCategory(Intent.CATEGORY_OPENABLE).setType("text/markdown").putExtra(Intent.EXTRA_TITLE, name)
                    try { startActivityForResult(intent, SAVE_DOCUMENT) }
                    catch (_: ActivityNotFoundException) { pendingExport = null; toast("系统没有可用的文件保存窗口。") }
                } catch (_: Exception) { toast("导出请求无效。"); pendingExport = null }
            }
        }, Handler(Looper.getMainLooper()))
        val script = assets.open("export-bridge.js").bufferedReader().use { it.readText() }.replace("__POCKET_MARKER__", JSONObject.quote(marker))
        web.evaluateJavascript(script) {
            if (exportMarker == marker && address.contains(web.url)) web.postWebMessage(WebMessage(marker, arrayOf(ports[1])), Uri.parse(address.origin))
            else ports[1].close()
        }
    }

    @Deprecated("Platform result callback retained to avoid an AndroidX dependency")
    override fun onActivityResult(requestCode: Int, resultCode: Int, data: Intent?) {
        super.onActivityResult(requestCode, resultCode, data)
        if (requestCode == SPEECH_INPUT) {
            speechInFlight = false
            val pending = speechReply; speechReply = null
            if (pending != null && pending.marker == exportMarker && pending.origin == configuredServer?.origin && server?.contains(web.url) == true) {
                val text = if (resultCode == RESULT_OK) data?.getStringArrayListExtra(RecognizerIntent.EXTRA_RESULTS)?.firstOrNull()?.take(8000) ?: "" else ""
                runCatching { pending.port.postMessage(WebMessage(JSONObject().put("id", pending.id).put("ok", true).put("available", true).put("text", text).put("canceled", text.isBlank()).toString())) }
            }
            return
        }
        if (requestCode == INSTALL_PERMISSION) {
            if (packageManager.canRequestPackageInstalls()) pendingInstall?.let { installUpdate(it) }
            else toast("安装授权未开启，可再次点击更新。"); return
        }
        if (requestCode != SAVE_DOCUMENT) return
        val bytes = pendingExport; pendingExport = null
        if (resultCode != RESULT_OK) return
        val uri = data?.data
        if (bytes == null || uri?.scheme != "content") { toast("保存状态已失效，请重新导出。"); return }
        io.execute {
            val ok = runCatching { contentResolver.openOutputStream(uri, "w").use { output -> checkNotNull(output); output.write(bytes) } }.isSuccess
            runOnUiThread { if (!isFinishing && !isDestroyed) toast(if (ok) "Markdown 已保存。" else "保存失败，请重新导出。") }
        }
    }
    private fun handleBack() {
        if (pageFailed || server?.contains(web.url) != true) { moveTaskToBack(true); return }
        web.evaluateJavascript("(()=>{const d=document.querySelector('dialog[open]');if(d){d.close();return true;}const s=document.getElementById('drawer-scrim');if(s&&!s.hidden){s.click();return true;}return false;})()") { handled ->
            if (handled != "true") { if (web.canGoBack()) web.goBack() else moveTaskToBack(true) }
        }
    }
    private fun checkUpdates(manual: Boolean) {
        if (updateBusy || (!manual && autoUpdateChecked)) return
        val address = server ?: return
        val remote = configuredServer?.origin ?: return
        val token = vault.read(remote)
        if (token == null || !token.startsWith("dvc1_")) {
            autoUpdateChecked = false
            if (server?.contains(web.url) == true) web.evaluateJavascript("window.dispatchEvent(new CustomEvent('pocket-native-session-required',{detail:{reason:'app-update'}}));", null)
            if (manual) toast("请先完成设备认证，再检查更新。")
            return
        }
        autoUpdateChecked = true; updateBusy = true
        if (manual) toast("正在检查更新 · 当前 ${BuildConfig.VERSION_NAME}")
        val client = AppUpdates(this, address.origin, token)
        io.execute {
            val result = runCatching { client.check() }
            runOnUiThread {
                updateBusy = false
                if (isFinishing || isDestroyed || configuredServer?.origin != remote || vault.read(remote) != token) return@runOnUiThread
                val release = result.getOrNull()
                if (result.isFailure) {
                    if (result.exceptionOrNull()?.message?.contains("（401）") == true) {
                        autoUpdateChecked = false
                        if (server?.contains(web.url) == true) web.evaluateJavascript("window.dispatchEvent(new CustomEvent('pocket-native-session-required',{detail:{reason:'app-update'}}));", null)
                        if (manual) toast("登录会话已到期，正在请求重新认证。")
                    } else if (manual) toast(result.exceptionOrNull()?.message ?: "检查更新失败")
                    return@runOnUiThread
                }
                if (release == null || release.versionCode <= BuildConfig.VERSION_CODE) { if (manual) toast("当前已是最新版本 ${BuildConfig.VERSION_NAME}"); return@runOnUiThread }
                AlertDialog.Builder(this).setTitle("发现新版本 ${release.versionName}")
                    .setMessage("当前 ${BuildConfig.VERSION_NAME}，更新包 ${(release.size + 1023) / 1024} KB。下载完成并验证后打开系统安装窗口，聊天和登录状态会保留。")
                    .setNegativeButton("稍后", null).setPositiveButton("下载并更新") { _, _ ->
                        if (updateBusy || configuredServer?.origin != remote || vault.read(remote) != token) return@setPositiveButton
                        updateBusy = true; toast("正在下载并验证更新包…")
                        io.execute {
                            val downloaded = runCatching { client.download(release) }
                            runOnUiThread {
                                updateBusy = false
                                if (isFinishing || isDestroyed || configuredServer?.origin != remote || vault.read(remote) != token) return@runOnUiThread
                                downloaded.onSuccess { installUpdate(it) }.onFailure { toast(it.message ?: "更新包验证失败") }
                            }
                        }
                    }.create().also { showDialog(it) }
            }
        }
    }
    private fun installUpdate(file: File) {
        if (!file.isFile) { pendingInstall = null; toast("更新包已被清理，请重新下载。"); return }
        pendingInstall = file
        if (!packageManager.canRequestPackageInstalls()) {
            AlertDialog.Builder(this).setTitle("允许安装 chat-agent 更新")
                .setMessage("请在下一页允许此应用安装更新，然后返回。安装仍需系统确认。")
                .setNegativeButton("稍后", null).setPositiveButton("去授权") { _, _ ->
                    startActivityForResult(Intent(Settings.ACTION_MANAGE_UNKNOWN_APP_SOURCES, Uri.parse("package:$packageName")), INSTALL_PERMISSION)
                }.create().also { showDialog(it) }; return
        }
        try {
            val uri = Uri.parse("content://$packageName.updates/${file.name}")
            startActivity(Intent(Intent.ACTION_VIEW).setDataAndType(uri, "application/vnd.android.package-archive").addFlags(Intent.FLAG_GRANT_READ_URI_PERMISSION))
        } catch (_: ActivityNotFoundException) { toast("系统安装器不可用。"); }
    }
    @Deprecated("Used only on Android versions before OnBackInvokedDispatcher")
    override fun onBackPressed() { handleBack() }
    override fun onPause() { web.onPause(); super.onPause() }
    override fun onResume() { super.onResume(); if (::web.isInitialized) web.onResume() }
    override fun onDestroy() { closePort(); gateway?.close(); pendingExport = null; content.removeView(web); web.destroy(); io.shutdown(); super.onDestroy() }
    private fun toast(message: String) { Toast.makeText(this, message, Toast.LENGTH_SHORT).show() }
    companion object { private const val SAVE_DOCUMENT = 20; private const val INSTALL_PERMISSION = 21; private const val CARE_PERMISSION = 22; private const val SPEECH_INPUT = 23; private const val MAX_EXPORT = 4 * 1024 * 1024 }
}
