package dev.pocketagent.android

import java.net.URI
import java.util.Locale

/** Only an origin is accepted: the existing web app serves its APIs at /. */
class ServerAddress private constructor(val origin: String, val host: String) {
    val encryptedTCP: Boolean get() = origin.startsWith("chat://")
    val port: Int get() = port(URI(origin))
    fun contains(value: String?): Boolean = try {
        val uri = URI(value ?: "")
        val base = URI(origin)
        uri.rawUserInfo == null && uri.scheme?.lowercase(Locale.ROOT) == base.scheme &&
            uri.host?.lowercase(Locale.ROOT) == base.host && port(uri) == port(base)
    } catch (_: Exception) { false }

    companion object {
        private fun port(uri: URI): Int = if (uri.port != -1) uri.port else when(uri.scheme?.lowercase(Locale.ROOT)) { "https" -> 443; "chat" -> 9992; else -> 80 }

        fun parse(value: String, debug: Boolean): ServerAddress {
            val text = value.trim()
            require(text.isNotEmpty() && text.length <= 2048) { "请输入服务器地址。" }
            val uri = try { URI(if (text.contains("://")) text else "https://$text") }
                catch (_: Exception) { throw IllegalArgumentException("服务器地址格式无效。") }
            val scheme = uri.scheme?.lowercase(Locale.ROOT)
            val host = uri.host?.lowercase(Locale.ROOT)
            require(host != null && uri.rawUserInfo == null && uri.rawQuery == null && uri.rawFragment == null &&
                (uri.rawPath.isNullOrEmpty() || uri.rawPath == "/") && (uri.port == -1 || uri.port in 1..65535)) {
                "只填写服务器根地址，不要包含路径、账号、查询参数或口令。"
            }
            require(scheme == "chat" || scheme == "https" || (debug && scheme == "http" && localHost(host))) {
                "请使用 chat:// 加密连接或 HTTPS。仅调试版允许本机或局域网 HTTP。"
            }
            val suffix = if (uri.port == -1 || scheme == "https" && uri.port == 443 || scheme == "http" && uri.port == 80) "" else ":${uri.port}"
            return ServerAddress("$scheme://$host$suffix", host)
        }

        private fun localHost(host: String): Boolean {
            if (host == "localhost" || host == "[::1]") return true
            val parts = host.split('.')
            if (parts.size != 4 || parts.any { it.isEmpty() || it.length > 3 || it.any { c -> c !in '0'..'9' } }) return false
            val bytes = parts.map { it.toIntOrNull() ?: -1 }
            if (bytes.any { it !in 0..255 }) return false
            return bytes[0] == 127 || bytes[0] == 10 || bytes[0] == 192 && bytes[1] == 168 || bytes[0] == 172 && bytes[1] in 16..31
        }
    }
}
