package dev.pocketagent.android

import android.content.Context
import android.util.Base64
import org.json.JSONObject
import java.io.ByteArrayOutputStream
import java.io.Closeable
import java.net.HttpURLConnection
import java.net.URL
import java.time.Instant

/** A private transport/session for the receiver; never borrows an owner token or
 * replaces the WebView's credential. Only fixed same-origin endpoints are used. */
class PushClient(private val context: Context, private val address: ServerAddress) : Closeable {
    class Failure(val status: Int, val code: String) : Exception("接收服务请求失败（$status）")
    @Volatile private var closed = false
    @Volatile private var connection: HttpURLConnection? = null
    private val gateway: WireGateway? = if (address.encryptedTCP) {
        val pem = context.resources.openRawResource(R.raw.chat_server_public).bufferedReader().use { it.readText() }
        WireGateway(address, Base64.decode(pem.lineSequence().filterNot { it.startsWith("---") }.joinToString(""), Base64.DEFAULT))
    } else null
    private val origin = gateway?.origin ?: address.origin
    private val identity = DeviceIdentity(context)
    private var token = ""
    private var expires = Instant.MIN

    fun authenticate() {
        check(!closed)
        val id = identity.identity(address.origin)
        check(!id.optBoolean("suspended")) { "此设备已退出，请重新登录" }
        // Authentication is challenge-only: a background receiver cannot register
        // or approve an unknown device, or use a saved maintenance credential.
        val challenge = request("POST", "/api/device/challenge", JSONObject().put("device_id", id.getString("device_id")), false)
        val payload = challenge.getString("payload")
        val lines = String(Base64.decode(payload, Base64.NO_WRAP), Charsets.UTF_8).split('\n')
        require(lines.size == 6 && lines[0] == "chat-agent-device-v1" &&
            lines[1] == challenge.getString("server_id") && lines[2] == id.getString("device_id") &&
            lines[3] == challenge.getString("challenge_id") && lines[5] == challenge.getString("expires_at"))
        val signed = identity.sign(address.origin, payload)
        val result = request("POST", "/api/device/verify", JSONObject().put("device_id", id.getString("device_id"))
            .put("challenge_id", challenge.getString("challenge_id")).put("signature", signed.getString("signature")), false)
        require(result.getString("server_id") == challenge.getString("server_id"))
        token = result.getString("token")
        require(token.startsWith("dvc1_") && token.length <= 4096 && token.none { it.isISOControl() })
        expires = Instant.parse(result.getString("expires_at"))
        require(expires > Instant.now())
    }

    fun settings(enabled: Boolean? = null): JSONObject = authorized(if (enabled == null) "GET" else "PATCH", "/api/push/settings", enabled?.let { JSONObject().put("enabled", it) })
    fun disableSavedSession(savedToken: String) {
        require(savedToken.startsWith("dvc1_") && savedToken.length in 32..4096 && savedToken.none { it == '\r' || it == '\n' })
        token = savedToken
        request("PATCH", "/api/push/settings", JSONObject().put("enabled", false), true)
    }
    fun inbox(cursor: String): JSONObject {
        require(cursor.isEmpty() || cursor.matches(Regex("[0-9a-f]{64}")))
        return authorized("GET", "/api/push/inbox?cursor=$cursor&wait=20", null)
    }
    private fun authorized(method: String, path: String, body: JSONObject?): JSONObject {
        if (token.isEmpty() || expires <= Instant.now().plusSeconds(60)) authenticate()
        try { return request(method, path, body, true) }
        catch (error: Failure) {
            if (error.status != 401) throw error
            authenticate() // Exactly one renewed challenge for an expired session.
            return request(method, path, body, true)
        }
    }
    private fun request(method: String, path: String, body: JSONObject?, authenticated: Boolean): JSONObject {
        check(!closed)
        val request = (URL(origin + path).openConnection() as HttpURLConnection).apply {
            connectTimeout = 8000; readTimeout = 30000; instanceFollowRedirects = false
            useCaches = false; requestMethod = method
            setRequestProperty("Accept-Encoding", "identity")
            if (authenticated) setRequestProperty("Authorization", "Bearer $token")
            if (body != null) { doOutput = true; setRequestProperty("Content-Type", "application/json") }
        }
        connection = request
        try {
            check(!closed)
            if (body != null) request.outputStream.use { it.write(body.toString().toByteArray(Charsets.UTF_8)) }
            val status = request.responseCode
            val stream = if (status in 200..299) request.inputStream else request.errorStream
            val bytes = stream?.use { input ->
                val output = ByteArrayOutputStream(); val buffer = ByteArray(4096)
                while (true) { val n = input.read(buffer); if (n < 0) break; require(output.size() + n <= 1024 * 1024); output.write(buffer, 0, n) }
                output.toByteArray()
            } ?: byteArrayOf()
            val json = if (bytes.isEmpty()) JSONObject() else JSONObject(String(bytes, Charsets.UTF_8))
            if (status !in 200..299) throw Failure(status, json.optString("code"))
            return json
        } finally { connection = null; request.disconnect() }
    }
    override fun close() {
        closed = true; connection?.disconnect(); gateway?.close(); token = ""
    }
}
