package dev.chatagent.ai

import android.Manifest
import android.app.Notification
import android.app.NotificationChannel
import android.app.NotificationManager
import android.app.PendingIntent
import android.app.Service
import android.content.Context
import android.content.Intent
import android.content.pm.PackageManager
import android.content.pm.ServiceInfo
import android.os.Build
import android.os.IBinder
import org.json.JSONObject
import java.security.MessageDigest
import java.util.concurrent.ThreadLocalRandom
import java.util.concurrent.atomic.AtomicBoolean

/** User-visible message receiver. Not sticky, no boot receiver or battery-policy
 * bypass: Android can still suspend networking in Doze or terminate this service. */
class PushReceiverService : Service() {
    private val stopped = AtomicBoolean(false)
    @Volatile private var client: PushClient? = null
    private var worker: Thread? = null
    private var remote = ""
    private var notice = ""
    override fun onBind(intent: Intent?): IBinder? = null

    override fun onStartCommand(intent: Intent?, flags: Int, startId: Int): Int {
        if (intent?.action == STOP) {
            val origin = remote.ifBlank { intent.getStringExtra("origin") ?: "" }
            stop(this, origin, true)
            stopSelf()
            return START_NOT_STICKY
        }
        val origin = intent?.getStringExtra("origin") ?: ""
        if (worker != null) return START_NOT_STICKY
        val eligible = runCatching { origin.isNotBlank() && requested(this, origin) && permitted(this) &&
            CredentialVault(this).read(origin).orEmpty().startsWith("dvc1_") && !DeviceIdentity(this).identity(origin).optBoolean("suspended") }.getOrDefault(false)
        if (!eligible) {
            state(this, origin, "需要登录并允许系统通知后重新开启")
            stopSelf(); return START_NOT_STICKY
        }
        remote = origin
        try {
            manager().createNotificationChannel(NotificationChannel(CHANNEL, "后台消息接收", NotificationManager.IMPORTANCE_LOW).apply {
                description = "维持用户开启的加密接收连接；可随时停止，不保证系统省电或强制停止后的触达。"
                lockscreenVisibility = Notification.VISIBILITY_PRIVATE
            })
            val notification = notification("正在连接消息接收服务…")
            if (Build.VERSION.SDK_INT >= 34) startForeground(NOTICE_ID, notification, ServiceInfo.FOREGROUND_SERVICE_TYPE_REMOTE_MESSAGING)
            else startForeground(NOTICE_ID, notification)
        } catch (_: Exception) {
            state(this, origin, "系统未允许后台接收，请回到应用重新开启")
            stopSelf(); return START_NOT_STICKY
        }
        val explicitEnable = intent?.getBooleanExtra("enable_subscription", false) == true
        runningService = this
        worker = Thread({ receive(origin, explicitEnable) }, "chat-agent-receiver").apply { start() }
        return START_NOT_STICKY
    }

    private fun receive(origin: String, explicitEnable: Boolean) {
        var delaySeconds = 5L
        var enabledOnce = false
        try {
            val address = ServerAddress.parse(origin, BuildConfig.DEBUG)
            while (!stopped.get() && requested(this, origin)) {
                try {
                    if (!permitted(this) || DeviceIdentity(this).identity(origin).optBoolean("suspended")) {
                        finishWith("登录或通知权限已关闭，请重新开启"); break
                    }
                    val transport = PushClient(this, address)
                    synchronized(this) {
                        if (stopped.get()) { transport.close(); return }
                        client = transport
                    }
                    transport.use {
                        it.authenticate()
                        // An ordinary reconnect must never revive a server-side
                        // disabled subscription. Only this explicit user action may.
                        val settings = if (explicitEnable && !enabledOnce) synchronized(subscriptionMutationLock) {
                            if (stopped.get() || !requested(this, origin)) return
                            it.settings(true)
                        } else it.settings()
                        if (!settings.optBoolean("enabled")) { finishWith("服务器已关闭此设备订阅，请手动重新开启"); return }
                        enabledOnce = true
                        state(this, origin, "加密接收已连接")
                        updateNotice("加密接收已连接 · 可随时停止")
                        var cursor = ""
                        delaySeconds = 5
                        while (!stopped.get() && requested(this, origin)) {
                            if (!permitted(this) || DeviceIdentity(this).identity(origin).optBoolean("suspended")) {
                                finishWith("登录或通知权限已关闭，请重新开启"); return
                            }
                            val started = System.nanoTime()
                            val inbox = it.inbox(cursor)
                            val next = inbox.getString("cursor")
                            require(next.matches(Regex("[0-9a-f]{64}")))
                            CareNotifications.get(this).deliverPush(origin, inbox.getJSONArray("items"))
                            cursor = next
                            val truncated = inbox.optBoolean("truncated")
                            state(this, origin, if (truncated) "已连接；补取数量超限，请打开收件箱查看全部" else "加密接收已连接")
                            updateNotice(if (truncated) "待查看内容较多 · 打开应用查看全部" else "加密接收已连接 · 可随时停止")
                            // Do not spin against an older/misbehaving server that
                            // answers every long-poll immediately.
                            val elapsed = (System.nanoTime() - started) / 1_000_000
                            if (elapsed < 1000) Thread.sleep(1000 - elapsed)
                        }
                    }
                } catch (error: PushClient.Failure) {
                    if (error.status in listOf(401, 403, 404, 409)) {
                        finishWith(when (error.status) { 403 -> "设备授权已撤销，请重新登录或联系管理员"; 404 -> "服务器暂不支持后台接收，请更新服务"; 409 -> "服务器已关闭接收订阅，请手动重新开启"; else -> "设备验证失败，请重新登录" })
                        break
                    }
                    if (explicitEnable && !enabledOnce) { finishWith("订阅未能开启，请联网后重试"); break }
                    if (!pauseRetry(origin, delaySeconds)) break
                    delaySeconds = minOf(delaySeconds * 2, 300)
                } catch (_: InterruptedException) { break }
                catch (_: Exception) {
                    if (explicitEnable && !enabledOnce) { finishWith("订阅未能开启，请联网后重试"); break }
                    if (!pauseRetry(origin, delaySeconds)) break
                    delaySeconds = minOf(delaySeconds * 2, 300)
                } finally { client = null }
            }
        } catch (_: Exception) {
            finishWith("接收服务暂时不可用，请回到应用重新开启")
        } finally {
            if (runningService === this) runningService = null
            if (!stopped.get()) stopSelf()
        }
    }

    private fun pauseRetry(origin: String, seconds: Long): Boolean {
        if (stopped.get() || !requested(this, origin)) return false
        state(this, origin, "连接中断，稍后自动重连；系统省电可能延迟接收")
        updateNotice("连接中断 · 稍后重连")
        return try { Thread.sleep(seconds * 1000 + ThreadLocalRandom.current().nextLong(1000)); !stopped.get() }
        catch (_: InterruptedException) { false }
    }
    private fun finishWith(message: String) {
        preferences(this).edit().putBoolean(key(remote), false).putString("state:${key(remote)}", message).commit()
    }
    private fun manager() = getSystemService(NotificationManager::class.java)
    private fun updateNotice(text: String) {
        if (stopped.get() || text == notice) return
        notice = text
        runCatching { manager().notify(NOTICE_ID, notification(text)) }
    }
    private fun notification(text: String): Notification {
        val open = PendingIntent.getActivity(this, 0, Intent(this, MainActivity::class.java).addFlags(Intent.FLAG_ACTIVITY_CLEAR_TOP or Intent.FLAG_ACTIVITY_SINGLE_TOP), PendingIntent.FLAG_UPDATE_CURRENT or PendingIntent.FLAG_IMMUTABLE)
        val stop = PendingIntent.getService(this, 0, Intent(this, PushReceiverService::class.java).setAction(STOP).putExtra("origin", remote), PendingIntent.FLAG_UPDATE_CURRENT or PendingIntent.FLAG_IMMUTABLE)
        return Notification.Builder(this, CHANNEL).setSmallIcon(android.R.drawable.ic_dialog_info)
            .setContentTitle("chat-agent 后台接收").setContentText(text).setContentIntent(open)
            .setOngoing(true).setOnlyAlertOnce(true).setVisibility(Notification.VISIBILITY_PRIVATE)
            .setCategory(Notification.CATEGORY_SERVICE)
            .addAction(Notification.Action.Builder(null, "停止接收", stop).build()).build()
    }
    override fun onDestroy() {
        synchronized(this) { stopped.set(true); client?.close() }
        worker?.interrupt()
        if (runningService === this) runningService = null
        if (remote.isNotBlank() && requested(this, remote)) state(this, remote, "接收服务已中断；回到应用后恢复，系统省电可能延迟接收")
        stopForeground(STOP_FOREGROUND_REMOVE)
        super.onDestroy()
    }

    companion object {
        private const val CHANNEL = "background-messages"
        private const val NOTICE_ID = 812
        private const val STOP = "dev.chatagent.ai.STOP_RECEIVER"
        @Volatile private var runningService: PushReceiverService? = null
        private val subscriptionMutationLock = Any()
        private fun preferences(context: Context) = context.getSharedPreferences("push-receiver", Context.MODE_PRIVATE)
        private fun key(origin: String) = MessageDigest.getInstance("SHA-256").digest(origin.toByteArray(Charsets.UTF_8)).joinToString("") { "%02x".format(it.toInt() and 255) }
        fun requested(context: Context, origin: String) = preferences(context).getBoolean(key(origin), false)
        fun permitted(context: Context): Boolean {
            val manager = context.getSystemService(NotificationManager::class.java)
            return (Build.VERSION.SDK_INT < 33 || context.checkSelfPermission(Manifest.permission.POST_NOTIFICATIONS) == PackageManager.PERMISSION_GRANTED) &&
                manager.areNotificationsEnabled() && manager.getNotificationChannel(CHANNEL)?.importance != NotificationManager.IMPORTANCE_NONE &&
                manager.getNotificationChannel("care-inbox")?.importance != NotificationManager.IMPORTANCE_NONE
        }
        private fun state(context: Context, origin: String, message: String) {
            preferences(context).edit().putString("state:${key(origin)}", message).apply()
        }
        fun status(context: Context, origin: String): JSONObject = JSONObject()
            .put("background_supported", true).put("background_enabled", requested(context, origin))
            .put("background_running", runningService?.remote == origin)
            .put("background_state", preferences(context).getString("state:${key(origin)}", "后台接收未开启"))
        /** Called only from a visible Activity, never a broadcast/background job. */
        fun enable(context: Context, origin: String) {
            require(CredentialVault(context).read(origin).orEmpty().startsWith("dvc1_")) { "请先完成设备登录" }
            check(!DeviceIdentity(context).identity(origin).optBoolean("suspended"))
            if (!requested(context, origin)) CareNotifications.get(context).resetPushBaseline(origin)
            check(preferences(context).edit().putBoolean(key(origin), true).putBoolean("permission:${key(origin)}", !permitted(context)).putString("state:${key(origin)}", "正在请求订阅；允许通知后连接").commit())
            if (permitted(context)) start(context, origin, true)
        }
        fun resume(context: Context, origin: String) {
            if (!requested(context, origin) || runningService?.remote == origin || !permitted(context) || preferences(context).getBoolean("permission:${key(origin)}", false)) return
            if (!CredentialVault(context).read(origin).orEmpty().startsWith("dvc1_") || DeviceIdentity(context).identity(origin).optBoolean("suspended")) return
            start(context, origin, false)
        }
        fun afterPermission(context: Context, origin: String) {
            preferences(context).edit().putBoolean("permission:${key(origin)}", false).commit()
            if (requested(context, origin) && permitted(context)) start(context, origin, true)
            else if (requested(context, origin)) state(context, origin, "通知权限未允许，后台接收尚未开启")
        }
        private fun start(context: Context, origin: String, explicit: Boolean) {
            try { context.startForegroundService(Intent(context, PushReceiverService::class.java).putExtra("origin", origin).putExtra("enable_subscription", explicit)) }
            catch (_: Exception) { state(context, origin, "系统限制后台启动，请在应用内重新开启") }
        }
        fun stop(context: Context, origin: String, disableRemote: Boolean) {
            if (origin.isBlank()) return
            val saved = CredentialVault(context).read(origin)
            preferences(context).edit().putBoolean(key(origin), false).putBoolean("permission:${key(origin)}", false).putString("state:${key(origin)}", "后台接收已停止").commit()
            context.stopService(Intent(context, PushReceiverService::class.java))
            if (disableRemote && saved?.startsWith("dvc1_") == true) {
                // A best-effort unsubscribe must never postpone local cancellation.
                Thread({
                    try {
                        synchronized(subscriptionMutationLock) {
                            // A delayed offline unsubscribe must not race a newer
                            // explicit enable into silently turning it back off.
                            if (!requested(context, origin)) PushClient(context.applicationContext, ServerAddress.parse(origin, BuildConfig.DEBUG)).use { it.disableSavedSession(saved) }
                        }
                    } catch (_: Exception) { state(context, origin, "本机已停止；服务器取消订阅未确认，连接租约到期后停止后台关怀") }
                }, "chat-agent-unsubscribe").apply { isDaemon = true; start() }
            }
        }
    }
}
