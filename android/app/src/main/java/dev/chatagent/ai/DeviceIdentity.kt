package dev.chatagent.ai

import android.content.Context
import android.os.Build
import android.security.keystore.KeyGenParameterSpec
import android.security.keystore.KeyProperties
import android.util.Base64
import org.json.JSONArray
import org.json.JSONObject
import java.net.URI
import java.nio.ByteBuffer
import java.nio.charset.CodingErrorAction
import java.security.KeyPairGenerator
import java.security.KeyStore
import java.security.MessageDigest
import java.security.PrivateKey
import java.security.Signature
import java.security.spec.ECGenParameterSpec
import java.util.Collections
import java.util.Locale

/** Stable per server instance, with support for existing origin-bound keys. */
class DeviceIdentity(context: Context) {
    companion object {
        private const val LEGACY = "chat-device-v1-"
        private const val INSTANCE = "chat-device-v2-"
        private val idFormat = Regex("[0-9a-f]{64}")
        private val lock = Any()
    }
    private val preferences = context.getSharedPreferences("chat-device-identity", Context.MODE_PRIVATE)
    private data class Selection(val origin: String, val server: String, val alias: String)
    private var selection: Selection? = null
    private fun originAlias(remoteOrigin: String): String {
        val uri = URI(remoteOrigin)
        val scheme = requireNotNull(uri.scheme).lowercase(Locale.ROOT)
        val host = requireNotNull(uri.host).lowercase(Locale.ROOT)
        val defaultPort = when (scheme) { "https" -> 443; "chat" -> 9992; "http" -> 80; else -> error("Unsupported server origin") }
        val port = if (uri.port == -1) defaultPort else uri.port
        return LEGACY + fingerprint("$scheme://$host:$port".toByteArray(Charsets.UTF_8))
    }

    private fun keyStore(): KeyStore = KeyStore.getInstance("AndroidKeyStore").apply { load(null) }

    private fun createKey(store: KeyStore, alias: String) {
        if (!store.containsAlias(alias)) {
            KeyPairGenerator.getInstance(KeyProperties.KEY_ALGORITHM_EC, "AndroidKeyStore").apply {
                initialize(KeyGenParameterSpec.Builder(alias, KeyProperties.PURPOSE_SIGN)
                    .setAlgorithmParameterSpec(ECGenParameterSpec("secp256r1"))
                    .setDigests(KeyProperties.DIGEST_SHA256)
                    .setUserAuthenticationRequired(false)
                    .build())
            }.generateKeyPair()
        }
    }

    private fun aliases(store: KeyStore, remoteOrigin: String, serverId: String): List<String> {
        require(idFormat.matches(serverId))
        val legacy = Collections.list(store.aliases()).filter { it.startsWith(LEGACY) }.sorted()
        return (listOfNotNull(preferences.getString("$INSTANCE$serverId.key", null), "$INSTANCE$serverId", originAlias(remoteOrigin)) + legacy)
            .distinct().filter(store::containsAlias)
    }

    private fun selected(store: KeyStore, remoteOrigin: String, serverId: String, deviceId: String): String {
        require(idFormat.matches(deviceId))
        return aliases(store, remoteOrigin, serverId).first { fingerprint(store.getCertificate(it).publicKey.encoded) == deviceId }
    }

    fun candidates(remoteOrigin: String, serverId: String): JSONObject = synchronized(lock) {
        val store = keyStore()
        val ids = aliases(store, remoteOrigin, serverId).take(40).map { fingerprint(store.getCertificate(it).publicKey.encoded) }
        JSONObject().put("candidates", JSONArray(ids))
    }

    fun identity(remoteOrigin: String, serverId: String = "", deviceId: String = ""): JSONObject = synchronized(lock) {
        val store = keyStore()
        val origin = originAlias(remoteOrigin)
        val alias = if (serverId.isEmpty()) {
            preferences.getString("$origin.key", null)?.takeIf(store::containsAlias) ?: origin
        } else {
            require(idFormat.matches(serverId))
            if (deviceId.isEmpty()) "$INSTANCE$serverId" else selected(store, remoteOrigin, serverId, deviceId)
        }
        createKey(store, alias)
        if (serverId.isNotEmpty()) selection = Selection(origin, serverId, alias)
        val publicKey = store.getCertificate(alias).publicKey.encoded
        JSONObject().put("device_id", fingerprint(publicKey))
            .put("public_key", Base64.encodeToString(publicKey, Base64.NO_WRAP))
            .put("name", Build.MODEL)
            .put("suspended", preferences.getBoolean("$origin.suspended", false))
    }

    fun remember(remoteOrigin: String, serverId: String, deviceId: String): JSONObject = synchronized(lock) {
        val alias = selected(keyStore(), remoteOrigin, serverId, deviceId)
        val origin = originAlias(remoteOrigin)
        require(selection == Selection(origin, serverId, alias))
        check(preferences.edit().putString("$INSTANCE$serverId.key", alias)
            .putString("$origin.key", alias).putString("$origin.server", serverId).commit())
        JSONObject()
    }

    fun setSuspended(remoteOrigin: String, suspended: Boolean) {
        check(preferences.edit().putBoolean("${originAlias(remoteOrigin)}.suspended", suspended).commit())
    }

    fun sign(remoteOrigin: String, payloadBase64: String): JSONObject = synchronized(lock) {
        require(payloadBase64.length in 1..2732 && payloadBase64.matches(Regex("[A-Za-z0-9+/]+={0,2}")))
        val bytes = Base64.decode(payloadBase64, Base64.NO_WRAP)
        require(bytes.size in 1..2048 && Base64.encodeToString(bytes, Base64.NO_WRAP) == payloadBase64)
        val text = Charsets.UTF_8.newDecoder().onMalformedInput(CodingErrorAction.REPORT)
            .onUnmappableCharacter(CodingErrorAction.REPORT).decode(ByteBuffer.wrap(bytes)).toString()
        val lines = text.split('\n')
        require(lines.size == 6 && lines[0] == "chat-agent-device-v1" && '\r' !in text)
        require(lines.drop(1).all { it.isNotBlank() && it.none { char -> char.isISOControl() } })
        val store = keyStore()
        val origin = originAlias(remoteOrigin)
        val active = selection?.takeIf { it.origin == origin }
        val expectedServer = active?.server ?: preferences.getString("$origin.server", null)
        require(expectedServer == null || expectedServer == lines[1])
        val alias = active?.alias ?: preferences.getString("$origin.key", null)?.takeIf(store::containsAlias) ?: origin
        require(store.containsAlias(alias) && lines[2] == fingerprint(store.getCertificate(alias).publicKey.encoded))
        val signature = Signature.getInstance("SHA256withECDSA").apply {
            initSign(store.getKey(alias, null) as PrivateKey)
            update(bytes)
        }.sign()
        JSONObject().put("signature", Base64.encodeToString(signature, Base64.NO_WRAP))
    }

    private fun fingerprint(bytes: ByteArray): String = MessageDigest.getInstance("SHA-256").digest(bytes)
        .joinToString("") { "%02x".format(it.toInt() and 0xff) }
}
