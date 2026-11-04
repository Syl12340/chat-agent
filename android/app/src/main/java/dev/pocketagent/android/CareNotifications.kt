package dev.pocketagent.android

import android.Manifest
import android.app.Notification
import android.app.NotificationChannel
import android.app.NotificationManager
import android.app.PendingIntent
import android.content.Context
import android.content.Intent
import android.content.pm.PackageManager
import android.os.Build
import org.json.JSONArray
import org.json.JSONObject
import java.security.MessageDigest
import java.time.Instant
import java.time.OffsetDateTime

/** Notifications for newly synchronized inbox items, without background work. */
class CareNotifications(private val context: Context) {
    private val preferences = context.getSharedPreferences("care-notifications", Context.MODE_PRIVATE)
    private val manager = context.getSystemService(NotificationManager::class.java)

    init {
        manager.createNotificationChannel(NotificationChannel(CHANNEL, "应用内关怀", NotificationManager.IMPORTANCE_DEFAULT).apply {
            description = "只通知本机已同步的新关怀，不提供关闭应用后的远程推送。"
            lockscreenVisibility = Notification.VISIBILITY_PRIVATE
        })
    }

    private fun hash(value: String) = MessageDigest.getInstance("SHA-256").digest(value.toByteArray(Charsets.UTF_8)).joinToString("") { "%02x".format(it.toInt() and 255) }
    private fun scope(origin: String) = hash(origin)
    private fun prefix(origin: String) = "scope:${scope(origin)}:"
    private fun granted() = Build.VERSION.SDK_INT < 33 || context.checkSelfPermission(Manifest.permission.POST_NOTIFICATIONS) == PackageManager.PERMISSION_GRANTED
    private fun permitted() = granted() && manager.areNotificationsEnabled() && manager.getNotificationChannel(CHANNEL)?.importance != NotificationManager.IMPORTANCE_NONE

    @Synchronized
    fun status(origin: String): JSONObject = JSONObject()
        .put("available", true)
        .put("enabled", preferences.getBoolean(prefix(origin) + "enabled", false))
        .put("permission", permitted())
        .put("permission_required", Build.VERSION.SDK_INT >= 33 && !granted())
        .put("permission_requested", preferences.getBoolean("permission_requested", false))
        .put("channel_blocked", manager.getNotificationChannel(CHANNEL)?.importance == NotificationManager.IMPORTANCE_NONE)

    @Synchronized
    fun configure(origin: String, enabled: Boolean): JSONObject {
        val currentPrefix = prefix(origin)
        val editor = preferences.edit()
        val scopes = readStrings(preferences.getString("scopes", "[]")).toMutableList()
        val currentScope = scope(origin)
        if (!scopes.contains(currentScope)) scopes.add(currentScope)
        while (scopes.size > MAX_SCOPES) {
            val removed = scopes.removeAt(0)
            preferences.all.keys.filter { it.startsWith("scope:$removed:") }.forEach { editor.remove(it) }
            cancelScope(removed)
        }
        val wasEnabled = preferences.getBoolean(currentPrefix + "enabled", false)
        editor.putBoolean(currentPrefix + "enabled", enabled).putString("scopes", JSONArray(scopes).toString())
        // Every explicit re-enable starts by establishing a fresh baseline.
        if (enabled && !wasEnabled) editor.putString(currentPrefix + "roles", "{}")
        val requestPermission = enabled && Build.VERSION.SDK_INT >= 33 && !granted() && !preferences.getBoolean("permission_requested", false)
        if (requestPermission) editor.putBoolean("permission_requested", true)
        check(editor.commit()) { "Notification preferences were not saved" }
        if (!enabled) cancelScope(currentScope)
        return status(origin).put("request_permission", requestPermission)
    }

    private data class Item(val id: String, val title: String, val content: String, val at: Instant?)

    private fun timestamp(item: JSONObject): Instant? {
        val value = item.optString("attempted_at").ifBlank { item.optString("created_at") }
        if (value.isBlank() || value.length > 80) return null
        return runCatching { Instant.parse(value) }.getOrNull() ?: runCatching { OffsetDateTime.parse(value).toInstant() }.getOrNull()
    }

    @Synchronized
    fun deliver(origin: String, role: String, payload: JSONArray): JSONObject {
        require(role.matches(Regex("[A-Za-z0-9_-]{1,128}")))
        require(payload.length() <= 20)
        val currentStatus = status(origin)
        if (!currentStatus.getBoolean("enabled")) return currentStatus.put("notified", 0).put("seeded", false)
        val items = (0 until payload.length()).map { index ->
            val item = payload.getJSONObject(index)
            val id = item.getString("id")
            val title = item.getString("title")
            val content = item.getString("content")
            require(id.matches(Regex("[A-Za-z0-9_-]{1,128}")) && title.length <= 256 && content.length <= 4096)
            require(title.none { it.isISOControl() })
            Item(id, title, content, timestamp(item))
        }
        val currentPrefix = prefix(origin)
        val roles = JSONObject(preferences.getString(currentPrefix + "roles", "{}") ?: "{}")
        val roleKey = hash(role)
        val firstSync = !roles.has(roleKey)
        val previous = roles.optString(roleKey).takeIf { it.isNotBlank() }?.let { runCatching { Instant.parse(it) }.getOrNull() }
        val seen = readStrings(preferences.getString(currentPrefix + "seen", "[]")).toMutableList()
        val toNotify = mutableListOf<Pair<String, Item>>()
        var newest = previous
        for (item in items.sortedWith(compareBy<Item> { it.at ?: Instant.MIN }.thenBy { it.id })) {
            val identity = hash(role + "\u0000" + item.id)
            val known = seen.contains(identity)
            if (!known) {
                seen.add(identity)
                while (seen.size > MAX_SEEN) seen.removeAt(0)
                // Unknown/missing times and older records only establish the baseline.
                // Equal timestamps remain eligible by ID, avoiding same-second loss.
                if (!firstSync && item.at != null && (previous == null || item.at >= previous)) toNotify.add(identity to item)
            }
            if (item.at != null && (newest == null || item.at > newest)) newest = item.at
        }
        if (firstSync && roles.length() >= MAX_ROLES) {
            val oldestKey = roles.keys().asSequence().minByOrNull { roles.optString(it) }
            if (oldestKey != null) roles.remove(oldestKey)
        }
        roles.put(roleKey, newest?.toString() ?: "")
        // Persist before posting so process death cannot replay an alert.
        check(preferences.edit().putString(currentPrefix + "roles", roles.toString()).putString(currentPrefix + "seen", JSONArray(seen).toString()).commit())
        var notified = 0
        if (currentStatus.getBoolean("permission")) {
            for ((identity, item) in toNotify) {
                runCatching { post(scope(origin), identity, item) }.onSuccess { notified++ }
            }
        }
        return status(origin).put("notified", notified).put("seeded", firstSync)
    }

    @Synchronized
    fun clearVisible(origin: String) { cancelScope(scope(origin)) }

    private fun post(scope: String, identity: String, item: Item) {
        val intent = Intent(context, MainActivity::class.java).addFlags(Intent.FLAG_ACTIVITY_CLEAR_TOP or Intent.FLAG_ACTIVITY_SINGLE_TOP)
        val pending = PendingIntent.getActivity(context, 0, intent, PendingIntent.FLAG_UPDATE_CURRENT or PendingIntent.FLAG_IMMUTABLE)
        val public = Notification.Builder(context, CHANNEL).setSmallIcon(android.R.drawable.ic_dialog_info).setContentTitle("chat-agent").setContentText("有新的关怀").build()
        val title = truncate(item.title.ifBlank { "一条新的关怀" }, 80)
        val body = truncate(item.content, 600)
        val notification = Notification.Builder(context, CHANNEL)
            .setSmallIcon(android.R.drawable.ic_dialog_info).setContentTitle(title).setContentText(body)
            .setStyle(Notification.BigTextStyle().bigText(body)).setContentIntent(pending)
            .setAutoCancel(true).setOnlyAlertOnce(true).setVisibility(Notification.VISIBILITY_PRIVATE)
            .setPublicVersion(public).setGroup(GROUP).setCategory(Notification.CATEGORY_MESSAGE).build()
        val active = manager.activeNotifications.filter { it.tag?.startsWith("care:") == true }.sortedBy { it.postTime }
        if (active.size >= MAX_ACTIVE) active.take(active.size - MAX_ACTIVE + 1).forEach { manager.cancel(it.tag, it.id) }
        manager.notify("care:$scope:$identity", 0, notification)
    }

    private fun cancelScope(scope: String) {
        manager.activeNotifications.filter { it.tag?.startsWith("care:$scope:") == true }.forEach { manager.cancel(it.tag, it.id) }
    }
    private fun truncate(value: String, count: Int): String = if (value.codePointCount(0, value.length) <= count) value else value.substring(0, value.offsetByCodePoints(0, count))
    private fun readStrings(value: String?): List<String> {
        val array = runCatching { JSONArray(value ?: "[]") }.getOrElse { JSONArray() }
        return (0 until minOf(array.length(), MAX_SEEN)).map { array.getString(it) }
    }
    companion object {
        private const val CHANNEL = "care-inbox"
        private const val GROUP = "chat-agent-care"
        private const val MAX_SCOPES = 16
        private const val MAX_ROLES = 32
        private const val MAX_SEEN = 256
        private const val MAX_ACTIVE = 20
    }
}
