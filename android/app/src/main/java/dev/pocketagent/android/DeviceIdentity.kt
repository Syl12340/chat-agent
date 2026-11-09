package dev.pocketagent.android

import android.content.Context
import android.os.Build
import android.security.keystore.KeyGenParameterSpec
import android.security.keystore.KeyProperties
import android.util.Base64
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
import java.util.Locale

/** Per remote server identity. The private key never leaves AndroidKeyStore. */
class DeviceIdentity(context: Context) {
    private val preferences = context.getSharedPreferences("chat-device-identity", Context.MODE_PRIVATE)
    private fun alias(remoteOrigin: String): String {
        val uri = URI(remoteOrigin)
        val scheme = requireNotNull(uri.scheme).lowercase(Locale.ROOT)
        val host = requireNotNull(uri.host).lowercase(Locale.ROOT)
        val defaultPort = when (scheme) { "https" -> 443; "chat" -> 9992; "http" -> 80; else -> error("Unsupported server origin") }
        val port = if (uri.port == -1) defaultPort else uri.port
        return "chat-device-v1-" + fingerprint("$scheme://$host:$port".toByteArray(Charsets.UTF_8))
    }

    private fun keyStore(remoteOrigin: String): Pair<KeyStore, String> {
        val alias = alias(remoteOrigin)
        val store = KeyStore.getInstance("AndroidKeyStore").apply { load(null) }
        if (!store.containsAlias(alias)) {
            KeyPairGenerator.getInstance(KeyProperties.KEY_ALGORITHM_EC, "AndroidKeyStore").apply {
                initialize(KeyGenParameterSpec.Builder(alias, KeyProperties.PURPOSE_SIGN)
                    .setAlgorithmParameterSpec(ECGenParameterSpec("secp256r1"))
                    .setDigests(KeyProperties.DIGEST_SHA256)
                    .setUserAuthenticationRequired(false)
                    .build())
            }.generateKeyPair()
        }
        return store to alias
    }

    @Synchronized
    fun identity(remoteOrigin: String): JSONObject {
        val (store, alias) = keyStore(remoteOrigin)
        val publicKey = store.getCertificate(alias).publicKey.encoded
        return JSONObject().put("device_id", fingerprint(publicKey))
            .put("public_key", Base64.encodeToString(publicKey, Base64.NO_WRAP))
            .put("name", Build.MODEL)
            .put("suspended", preferences.getBoolean("$alias.suspended", false))
    }

    @Synchronized
    fun setSuspended(remoteOrigin: String, suspended: Boolean) {
        check(preferences.edit().putBoolean("${alias(remoteOrigin)}.suspended", suspended).commit())
    }

    @Synchronized
    fun sign(remoteOrigin: String, payloadBase64: String): JSONObject {
        require(payloadBase64.length in 1..2732 && payloadBase64.matches(Regex("[A-Za-z0-9+/]+={0,2}")))
        val bytes = Base64.decode(payloadBase64, Base64.NO_WRAP)
        require(bytes.size in 1..2048 && Base64.encodeToString(bytes, Base64.NO_WRAP) == payloadBase64)
        val text = Charsets.UTF_8.newDecoder().onMalformedInput(CodingErrorAction.REPORT)
            .onUnmappableCharacter(CodingErrorAction.REPORT).decode(ByteBuffer.wrap(bytes)).toString()
        val lines = text.split('\n')
        require(lines.size == 6 && lines[0] == "chat-agent-device-v1" && '\r' !in text)
        require(lines.drop(1).all { it.isNotBlank() && it.none { char -> char.isISOControl() } })
        val (store, alias) = keyStore(remoteOrigin)
        require(lines[2] == fingerprint(store.getCertificate(alias).publicKey.encoded))
        val signature = Signature.getInstance("SHA256withECDSA").apply {
            initSign(store.getKey(alias, null) as PrivateKey)
            update(bytes)
        }.sign()
        return JSONObject().put("signature", Base64.encodeToString(signature, Base64.NO_WRAP))
    }

    private fun fingerprint(bytes: ByteArray): String = MessageDigest.getInstance("SHA-256").digest(bytes)
        .joinToString("") { "%02x".format(it.toInt() and 0xff) }
}
