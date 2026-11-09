package dev.pocketagent.android

import android.content.Context
import android.content.pm.PackageInfo
import android.content.pm.PackageManager
import android.os.Build
import org.json.JSONObject
import java.io.File
import java.io.ByteArrayOutputStream
import java.net.HttpURLConnection
import java.net.URL
import java.security.MessageDigest

data class AppRelease(val versionCode: Long, val versionName: String, val packageName: String, val sha256: String, val size: Long, val apkPath: String) {
    companion object {
        fun parse(text: String, expectedPackage: String): AppRelease {
            val json = JSONObject(text)
            val release = AppRelease(json.getLong("version_code"), json.getString("version_name"), json.getString("package_name"), json.getString("sha256"), json.getLong("size"), json.getString("apk_path"))
            require(release.packageName == expectedPackage && release.versionCode in 1..2100000000L && release.versionName.length in 1..64 && release.sha256.matches(Regex("[0-9a-f]{64}")) && release.size in 1..64*1024*1024L)
            require(release.apkPath == "/api/updates/android/${release.sha256}/app.apk")
            return release
        }
    }
}

/** Fixed, authenticated same-origin endpoints; the loopback gateway uses pinned ChatWire. */
class AppUpdates(private val context: Context, private val origin: String, private val token: String) {
    private fun request(path: String): HttpURLConnection = (URL(origin + path).openConnection() as HttpURLConnection).apply {
        connectTimeout = 8000; readTimeout = 30000; instanceFollowRedirects = false
        setRequestProperty("Authorization", "Bearer $token"); setRequestProperty("Accept-Encoding", "identity")
        useCaches = false
    }
    fun check(): AppRelease? {
        val connection = request("/api/updates/android")
        try {
            if (connection.responseCode == 404) return null
            require(connection.responseCode == 200) { "无法读取更新信息（${connection.responseCode}）" }
            val bytes = connection.inputStream.use { input ->
                val output = ByteArrayOutputStream(); val buffer = ByteArray(1024)
                while (true) { val count = input.read(buffer); if (count < 0) break; require(output.size() + count <= 4096); output.write(buffer, 0, count) }
                output.toByteArray()
            }
            return AppRelease.parse(String(bytes, Charsets.UTF_8), context.packageName)
        } finally { connection.disconnect() }
    }
    fun download(release: AppRelease): File {
        require(release.versionCode > BuildConfig.VERSION_CODE) { "拒绝安装旧版本" }
        val file = File(context.cacheDir, "chat-update-${release.sha256}.apk")
        val connection = request(release.apkPath)
        var verified = false
        try {
            require(connection.responseCode == 200) { "更新包已变化或下载失败，请重新检查更新" }
            val digest = MessageDigest.getInstance("SHA-256")
            var total = 0L
            connection.inputStream.use { input -> file.outputStream().use { output ->
                val buffer = ByteArray(16384)
                while (true) {
                    val count = input.read(buffer); if (count < 0) break
                    total += count; require(total <= release.size) { "更新包大小不匹配" }
                    digest.update(buffer, 0, count); output.write(buffer, 0, count)
                }
            } }
            require(total == release.size && hex(digest.digest()) == release.sha256) { "更新包哈希校验失败" }
            verifyAPK(file, release)
            verified = true
            return file
        } finally { connection.disconnect(); if (!verified) file.delete() }
    }
    @Suppress("DEPRECATION")
    private fun verifyAPK(file: File, release: AppRelease) {
        val flags = if (Build.VERSION.SDK_INT >= 28) PackageManager.GET_SIGNING_CERTIFICATES else PackageManager.GET_SIGNATURES
        val archive = context.packageManager.getPackageArchiveInfo(file.absolutePath, flags) ?: error("无法解析安装包")
        val current = context.packageManager.getPackageInfo(context.packageName, flags)
        val version = if (Build.VERSION.SDK_INT >= 28) archive.longVersionCode else archive.versionCode.toLong()
        require(archive.packageName == context.packageName && version == release.versionCode && archive.versionName == release.versionName) { "安装包名称或版本不匹配" }
        val signers = signatures(current)
        require(signers.isNotEmpty() && signers == signatures(archive)) { "安装包签名与当前应用不一致" }
    }
    @Suppress("DEPRECATION")
    private fun signatures(info: PackageInfo): Set<String> {
        val certs = if (Build.VERSION.SDK_INT >= 28) info.signingInfo?.apkContentsSigners else info.signatures
        return certs.orEmpty().map { hex(MessageDigest.getInstance("SHA-256").digest(it.toByteArray())) }.toSet()
    }
    private fun hex(bytes: ByteArray) = bytes.joinToString("") { "%02x".format(it) }
}
