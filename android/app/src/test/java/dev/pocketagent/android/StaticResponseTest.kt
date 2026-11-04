package dev.pocketagent.android

import org.junit.Assert.assertThrows
import org.junit.Test

class StaticResponseTest {
    @Test fun incompleteModuleIsRejectedBeforeForwarding() {
        fun response(body: String, length: Int) = "HTTP/1.1 200 OK\r\nContent-Length: $length\r\n\r\n$body".toByteArray(Charsets.ISO_8859_1)
        val module = "export const ready = true;".repeat(900)
        WireGateway.validateStaticResponse(response(module,module.length))
        assertThrows(IllegalArgumentException::class.java) { WireGateway.validateStaticResponse(response(module.take(3620),module.length)) }
        assertThrows(IllegalArgumentException::class.java) { WireGateway.validateStaticResponse(response(module,module.length-1)) }
        WireGateway.validateStaticResponse("HTTP/1.1 304 Not Modified\r\n\r\n".toByteArray())
    }
}
