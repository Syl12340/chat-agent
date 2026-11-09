package dev.pocketagent.android

import java.io.Closeable
import java.io.DataInputStream
import java.io.DataOutputStream
import java.io.IOException
import java.net.InetSocketAddress
import java.net.Socket
import java.nio.ByteBuffer
import java.security.KeyFactory
import java.security.KeyPairGenerator
import java.security.MessageDigest
import java.security.Signature
import java.security.interfaces.ECPublicKey
import java.security.spec.ECGenParameterSpec
import java.security.spec.ECPoint
import java.security.spec.ECPublicKeySpec
import java.security.spec.X509EncodedKeySpec
import java.math.BigInteger
import javax.crypto.Cipher
import javax.crypto.KeyAgreement
import javax.crypto.Mac
import javax.crypto.spec.GCMParameterSpec
import javax.crypto.spec.SecretKeySpec

/** ChatWire v1, interoperable with internal/securewire. The pin is provisioned over SSH. */
class ChatWire(host: String, port: Int, publicKey: ByteArray) : Closeable {
    private val socket = Socket()
    private val input: DataInputStream
    private val output: DataOutputStream
    private val rx: Records
    private val tx: Records
    init {
        try {
            socket.connect(InetSocketAddress(host, port), 8000)
            socket.tcpNoDelay = true; socket.soTimeout = 5000
            input = DataInputStream(socket.getInputStream()); output = DataOutputStream(socket.getOutputStream())
            val hello = readFrame(input, 104, 183)
            require(hello.copyOfRange(0, 4).contentEquals("PKS1".toByteArray())) { "Invalid encrypted server hello" }
            val challenge = hello.copyOfRange(4, 36)
            val serverEphemeral = hello.copyOfRange(36, 101)
            val signatureLength = ByteBuffer.wrap(hello, 101, 2).short.toInt() and 65535
            require(signatureLength in 8..80 && hello.size == 103 + signatureLength) { "Invalid server signature length" }
            val identity = KeyFactory.getInstance("EC").generatePublic(X509EncodedKeySpec(publicKey))
            val verifier = Signature.getInstance("SHA256withECDSA")
            verifier.initVerify(identity); verifier.update("ChatWire-v1/server".toByteArray() + challenge + serverEphemeral)
            require(verifier.verify(hello.copyOfRange(103, hello.size))) { "Encrypted server identity mismatch" }
            val generator = KeyPairGenerator.getInstance("EC"); generator.initialize(ECGenParameterSpec("secp256r1"))
            val pair = generator.generateKeyPair(); val local = pair.public as ECPublicKey
            val clientEphemeral = byteArrayOf(4) + fixed(local.w.affineX) + fixed(local.w.affineY)
            require(serverEphemeral[0] == 4.toByte())
            val point = ECPoint(BigInteger(1, serverEphemeral.copyOfRange(1,33)), BigInteger(1, serverEphemeral.copyOfRange(33,65)))
            val peer = KeyFactory.getInstance("EC").generatePublic(ECPublicKeySpec(point, local.params))
            val agreement = KeyAgreement.getInstance("ECDH"); agreement.init(pair.private); agreement.doPhase(peer, true)
            val context = MessageDigest.getInstance("SHA-256").digest("ChatWire-v1/session".toByteArray() + challenge + serverEphemeral + clientEphemeral)
            val material = hkdf(agreement.generateSecret(), challenge, context, 72)
            tx = Records(material.copyOfRange(0,32), material.copyOfRange(64,68), context, 0)
            rx = Records(material.copyOfRange(32,64), material.copyOfRange(68,72), context, 1)
            writeFrame(output, "PKC1".toByteArray() + clientEphemeral)
            material.fill(0)
            socket.soTimeout = 45000
        } catch (error: Exception) { socket.close(); throw IOException("无法建立加密连接，请检查网络、服务器地址和 App 公钥版本。", error) }
    }

    fun write(bytes: ByteArray) {
        var offset = 0
        while (offset < bytes.size) {
            val end = minOf(offset + MAX_DATA, bytes.size)
            tx.write(output, 1, bytes.copyOfRange(offset,end)); offset = end
        }
    }
    fun read(): ByteArray? = rx.read(input)
    override fun close() { runCatching { socket.soTimeout = 1000; socket.close() } }

    private class Records(key: ByteArray, private val prefix: ByteArray, private val context: ByteArray, private val direction: Byte) {
        private val keySpec = SecretKeySpec(key, "AES")
        private var sequence = 0L
        private var ended = false
        private fun nonce() = ByteBuffer.allocate(12).put(prefix).putLong(sequence).array()
        private fun aad(length: Int) = ByteBuffer.allocate(45).put(context).put(direction).putInt(length).putLong(sequence).array()
        fun write(output: DataOutputStream, kind: Int, bytes: ByteArray) {
            check(!ended && sequence < (1L shl 32) && bytes.size <= MAX_DATA)
            val length = 25 + bytes.size
            val cipher = Cipher.getInstance("AES/GCM/NoPadding")
            cipher.init(Cipher.ENCRYPT_MODE, keySpec, GCMParameterSpec(128, nonce())); cipher.updateAAD(aad(length))
            writeFrame(output, ByteBuffer.allocate(length).putLong(sequence).put(cipher.doFinal(byteArrayOf(kind.toByte()) + bytes)).array())
            sequence++; if(kind == 2) ended = true
        }
        fun read(input: DataInputStream): ByteArray? {
            if (ended) return null
            check(sequence < (1L shl 32))
            val packet = readFrame(input, 25, MAX_DATA + 25)
            require(ByteBuffer.wrap(packet).long == sequence) { "Replayed or out-of-order encrypted record" }
            val cipher = Cipher.getInstance("AES/GCM/NoPadding")
            cipher.init(Cipher.DECRYPT_MODE, keySpec, GCMParameterSpec(128, nonce())); cipher.updateAAD(aad(packet.size))
            val plain = cipher.doFinal(packet, 8, packet.size-8); sequence++
            if(plain.size == 1 && plain[0] == 2.toByte()) { ended = true; return null }
            require(plain.size >= 2 && plain[0] == 1.toByte()) { "Invalid encrypted record" }
            return plain.copyOfRange(1, plain.size)
        }
    }
    companion object {
        private const val MAX_DATA = 16 * 1024
        private fun fixed(value: BigInteger): ByteArray {
            val bytes = value.toByteArray()
            return ByteArray(32).also { val n=minOf(bytes.size,32); System.arraycopy(bytes,bytes.size-n,it,32-n,n) }
        }
        private fun hkdf(secret: ByteArray, salt: ByteArray, info: ByteArray, size: Int): ByteArray {
            fun hmac(key: ByteArray, data: ByteArray): ByteArray = Mac.getInstance("HmacSHA256").run { init(SecretKeySpec(key,"HmacSHA256")); doFinal(data) }
            val prk = hmac(salt,secret); secret.fill(0)
            val result = ByteArray(size); var previous=ByteArray(0); var offset=0; var counter=1
            while(offset<size) { previous=hmac(prk,previous+info+byteArrayOf(counter++.toByte())); val n=minOf(previous.size,size-offset); System.arraycopy(previous,0,result,offset,n); offset+=n }
            prk.fill(0); return result
        }
        private fun readFrame(input: DataInputStream, minimum: Int, maximum: Int): ByteArray {
            val n = input.readInt(); require(n in minimum..maximum) { "Invalid bounded encrypted frame" }
            return ByteArray(n).also { input.readFully(it) }
        }
        private fun writeFrame(output: DataOutputStream, bytes: ByteArray) {
            output.write(ByteBuffer.allocate(4 + bytes.size).putInt(bytes.size).put(bytes).array()); output.flush()
        }
    }
}
