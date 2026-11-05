package dev.pocketagent.android

import java.io.BufferedInputStream
import java.io.ByteArrayOutputStream
import java.io.Closeable
import java.io.IOException
import java.net.InetAddress
import java.net.ServerSocket
import java.net.Socket
import java.nio.charset.StandardCharsets
import java.util.concurrent.ConcurrentHashMap
import java.util.concurrent.ThreadPoolExecutor
import java.util.concurrent.TimeUnit
import java.util.concurrent.atomic.AtomicBoolean
import java.util.concurrent.atomic.AtomicReference

/** A loopback-only HTTP facade for WebView. All public-network bytes use PocketWire. */
class WireGateway(private val target: ServerAddress, private val publicKey: ByteArray) : Closeable {
    private val listener = ServerSocket(0, 16, InetAddress.getByName("127.0.0.1"))
    private val sockets = ConcurrentHashMap.newKeySet<Socket>()
    private val workers = ThreadPoolExecutor(0, 16, 30, TimeUnit.SECONDS, java.util.concurrent.SynchronousQueue())
    val origin = "http://127.0.0.1:${listener.localPort}"
    private val accepting = Thread({
        while (!listener.isClosed) {
            val socket = try { listener.accept() } catch (_: IOException) { break }
            sockets.add(socket)
            try { workers.execute { try { proxy(socket) } finally { sockets.remove(socket); runCatching { socket.close() } } } }
            catch (_: java.util.concurrent.RejectedExecutionException) { sockets.remove(socket); socket.close() }
        }
    }, "pocket-wire-loopback").apply { isDaemon = true; start() }

    private fun proxy(local: Socket) {
        var responseStarted = false
        try {
            local.soTimeout = 10000
            val input = BufferedInputStream(local.getInputStream())
            val head = ByteArrayOutputStream()
            var ending = 0
            while (head.size() < 16 * 1024) {
                val b = input.read(); if(b == -1) throw IOException("Incomplete local request")
                head.write(b)
                ending = (ending shl 8) or b
                if(ending == 0x0d0a0d0a) break
            }
            val text = head.toString("ISO-8859-1")
            require(text.endsWith("\r\n\r\n"))
            val lines = text.split("\r\n")
            val request = lines.first().split(' ')
            require(request.size == 3 && request[0] in listOf("GET","HEAD","POST","PATCH","DELETE") && request[1].startsWith("/") && !request[1].startsWith("//") && request[2] == "HTTP/1.1")
            val headers = lines.drop(1).filter { it.isNotEmpty() }.map {
                require(!it.startsWith(' ') && !it.startsWith('\t') && it.contains(':'))
                it.substringBefore(':').lowercase() to it.substringAfter(':').trim()
            }
            require(headers.count { it.first == "host" } == 1 && headers.first { it.first == "host" }.second == "127.0.0.1:${listener.localPort}")
            require(headers.none { it.first in listOf("transfer-encoding","expect","upgrade","proxy-authorization","proxy-connection") })
            val lengths = headers.filter { it.first == "content-length" }
            require(lengths.size <= 1)
            val size = if(lengths.isEmpty()) 0 else lengths[0].second.toInt()
            val imageUpload = request[0] == "POST" && request[1].substringBefore('?').matches(Regex("/api/sessions/[A-Za-z0-9_-]{1,128}/images"))
            require(size in 0..if (imageUpload) 4*1024*1024 else 128*1024)
            val body = ByteArray(size); var offset=0
            while(offset<size) { val n=input.read(body,offset,size-offset);if(n<0)throw IOException("Incomplete local body");offset+=n }
            val forward = buildString {
                append(lines.first()).append("\r\n")
                for(line in lines.drop(1)) {
                    if(line.isEmpty()) continue
                    if(line.substringBefore(':').lowercase() !in listOf("connection","keep-alive")) append(line).append("\r\n")
                }
                append("Connection: close\r\n\r\n")
            }.toByteArray(StandardCharsets.ISO_8859_1)
            val staticAsset = request[0] == "GET" && request[1].substringBefore('?') in STATIC_ASSETS
            val canceled = AtomicBoolean(false)
            val activeRemote = AtomicReference<PocketWire?>(null)
            local.soTimeout = 0
            Thread({
                try { input.read() } catch (_: IOException) { }
                finally { canceled.set(true); activeRemote.get()?.close() }
            }, "pocket-wire-cancel").apply { isDaemon = true; start() }
            var attempt = 0
            while (true) {
                try {
                    PocketWire(target.host.removeSurrounding("[","]"), target.port, publicKey).use { remote ->
                        activeRemote.set(remote)
                        if(canceled.get()) throw IOException("Local request canceled")
                        remote.write(forward); if(body.isNotEmpty()) remote.write(body)
                        val output = local.getOutputStream()
                        if (staticAsset) {
                            val complete = ByteArrayOutputStream()
                            while(true) {
                                val bytes=remote.read() ?: break
                                require(complete.size()+bytes.size<=MAX_STATIC_RESPONSE) { "Static response too large" }
                                complete.write(bytes)
                            }
                            val bytes=complete.toByteArray()
                            validateStaticResponse(bytes)
                            responseStarted=true; output.write(bytes); output.flush()
                        } else {
                            while(true) {
                                val bytes=remote.read() ?: break
                                responseStarted=true; output.write(bytes); output.flush()
                            }
                        }
                    }
                    break
                } catch(error: Exception) {
                    if(!staticAsset || responseStarted || canceled.get() || attempt++>=1) throw error
                } finally {
                    activeRemote.set(null)
                }
            }
        } catch (_: Exception) {
            if(!responseStarted) runCatching {
                val body="加密连接不可用。请检查服务器地址、网络与 App 公钥版本。".toByteArray()
                local.getOutputStream().write(("HTTP/1.1 502 Bad Gateway\r\nContent-Type: text/plain; charset=utf-8\r\nCache-Control: no-store\r\nConnection: close\r\nContent-Length: ${body.size}\r\n\r\n").toByteArray()+body)
            }
        }
    }
    override fun close() { listener.close(); sockets.forEach { runCatching { it.close() } }; workers.shutdownNow() }
    companion object {
        private const val MAX_STATIC_RESPONSE = 1024*1024
        private val STATIC_ASSETS = setOf("/","/index.html","/app.js","/tasks.js","/companion.js","/life.js","/turns.js","/activities.js","/native.js","/native-care.js","/voice.js","/device-auth.js","/role-proposals.js","/topics.js","/theme.js","/chat-input.js","/chat-ui.css","/style.css","/sw.js","/manifest.webmanifest","/icon.svg","/icon-192.png","/icon-512.png")
    internal fun validateStaticResponse(bytes: ByteArray) {
        val headerEnd = bytes.indices.firstOrNull { it+3<bytes.size && bytes[it]==13.toByte() && bytes[it+1]==10.toByte() && bytes[it+2]==13.toByte() && bytes[it+3]==10.toByte() }
            ?: throw IOException("Incomplete static headers")
        require(headerEnd<16*1024)
        val lines=String(bytes,0,headerEnd,StandardCharsets.ISO_8859_1).split("\r\n")
        val status=lines.first().split(' ').getOrNull(1)?.toIntOrNull() ?: throw IOException("Invalid static response")
        if(status==304 || status==204) { require(bytes.size==headerEnd+4); return }
        val lengths=lines.drop(1).filter { it.substringBefore(':').equals("Content-Length",true) }
        require(lengths.size==1 && lengths[0].substringAfter(':').trim().toLongOrNull()==(bytes.size-headerEnd-4).toLong()) { "Incomplete static body" }
    }
    }
}
