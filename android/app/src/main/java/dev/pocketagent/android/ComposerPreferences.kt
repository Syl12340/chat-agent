package dev.pocketagent.android

import android.content.Context
import org.json.JSONObject
import java.security.MessageDigest

/** Non-secret composer choices, scoped to the remote server and identity.
 * The WebView's ephemeral loopback port must never become the storage key. */
class ComposerPreferences(context: Context) {
    private val preferences = context.getSharedPreferences("chat-agent-composer", Context.MODE_PRIVATE)
    private val levels = setOf("off", "medium", "high", "ultra")
    private val forbidden = setOf("__proto__", "constructor", "prototype")
    private fun model(value: String) = value.length in 1..128 && value !in forbidden && value.none { it.isISOControl() }
    private fun key(origin: String, scope: String): String {
        require(scope == "owner" || scope.matches(Regex("device:[A-Za-z0-9_-]{1,128}")))
        return MessageDigest.getInstance("SHA-256").digest("$origin\n$scope".toByteArray(Charsets.UTF_8))
            .joinToString("") { "%02x".format(it.toInt() and 255) }
    }
    private fun validate(value: JSONObject): JSONObject {
        require(value.length() == 3 && value.has("version") && value.has("current_model") && value.has("levels"))
        require(value.get("version") is Int && value.getInt("version") == 2)
        val current = value.get("current_model")
        require(current is String && (current.isEmpty() || model(current)))
        val source = value.getJSONObject("levels")
        require(source.length() <= 64)
        val clean = JSONObject()
        source.keys().forEach { id ->
            val level = source.get(id)
            require(model(id) && level is String && level in levels)
            clean.put(id, level)
        }
        return JSONObject().put("version", 2).put("current_model", current).put("levels", clean)
    }
    fun load(origin: String, scope: String): JSONObject? = preferences.getString(key(origin, scope), null)?.let {
        runCatching { validate(JSONObject(it)) }.getOrNull()
    }
    fun save(origin: String, scope: String, value: JSONObject) {
        val clean = validate(value).toString()
        require(clean.toByteArray(Charsets.UTF_8).size <= 24576)
        check(preferences.edit().putString(key(origin, scope), clean).commit())
    }
}
