package dev.chatagent.ai

import android.content.Context
import android.security.keystore.KeyGenParameterSpec
import android.security.keystore.KeyProperties
import android.util.Base64
import java.security.KeyStore
import java.security.MessageDigest
import javax.crypto.Cipher
import javax.crypto.KeyGenerator
import javax.crypto.SecretKey
import javax.crypto.spec.GCMParameterSpec

/** Credentials survive WebView origins and process death, bound to the remote origin. */
class CredentialVault(context: Context) {
    private val prefs = context.getSharedPreferences("chat-credentials", Context.MODE_PRIVATE)
    private fun entry(origin: String) = MessageDigest.getInstance("SHA-256").digest(origin.toByteArray()).joinToString("") { "%02x".format(it) }
    private fun key(): SecretKey {
        val store = KeyStore.getInstance("AndroidKeyStore").apply { load(null) }
        (store.getKey("chat-credentials-v1", null) as? SecretKey)?.let { return it }
        return KeyGenerator.getInstance(KeyProperties.KEY_ALGORITHM_AES, "AndroidKeyStore").apply {
            init(KeyGenParameterSpec.Builder("chat-credentials-v1", KeyProperties.PURPOSE_ENCRYPT or KeyProperties.PURPOSE_DECRYPT)
                .setBlockModes(KeyProperties.BLOCK_MODE_GCM).setEncryptionPaddings(KeyProperties.ENCRYPTION_PADDING_NONE).setKeySize(256).build())
        }.generateKey()
    }
    @Synchronized fun read(origin: String): String? {
        val encrypted = prefs.getString(entry(origin), null) ?: return null
        return try {
            val bytes = Base64.decode(encrypted, Base64.NO_WRAP)
            require(bytes.size > 28)
            val cipher = Cipher.getInstance("AES/GCM/NoPadding")
            cipher.init(Cipher.DECRYPT_MODE, key(), GCMParameterSpec(128, bytes.copyOfRange(0, 12)))
            cipher.updateAAD(origin.toByteArray())
            String(cipher.doFinal(bytes.copyOfRange(12, bytes.size)), Charsets.UTF_8)
        } catch (_: Exception) { clear(origin); null }
    }
    @Synchronized fun save(origin: String, token: String) {
        require(token.length in 32..4096 && token.none { it == '\r' || it == '\n' })
        val cipher = Cipher.getInstance("AES/GCM/NoPadding")
        cipher.init(Cipher.ENCRYPT_MODE, key()); cipher.updateAAD(origin.toByteArray())
        val encrypted = cipher.iv + cipher.doFinal(token.toByteArray())
        check(prefs.edit().putString(entry(origin), Base64.encodeToString(encrypted, Base64.NO_WRAP)).commit())
    }
    @Synchronized fun clear(origin: String) { check(prefs.edit().remove(entry(origin)).commit()) }
}
