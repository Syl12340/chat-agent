package dev.pocketagent.android

import org.junit.Assert.*
import org.junit.Test

class ServerAddressTest {
    @Test fun keepsOriginBoundaryAndLimitsCleartext() {
        val address = ServerAddress.parse("https://agent.example:443/", false)
        assertEquals("https://agent.example", address.origin)
        assertTrue(address.contains("https://agent.example/api/sessions"))
        assertFalse(address.contains("https://agent.example.evil.test/"))
        assertFalse(address.contains("https://agent.example:8443/"))
        assertFalse(address.contains("https://user@agent.example/"))
        assertEquals("http://10.0.2.2:8080", ServerAddress.parse("http://10.0.2.2:8080", true).origin)
        assertEquals("http://192.168.1.2:8080", ServerAddress.parse("http://192.168.1.2:8080", true).origin)
        assertEquals("https://agent.example", ServerAddress.parse("agent.example", false).origin)
        val encrypted = ServerAddress.parse("pocket://47.117.40.74:9992", false)
        assertTrue(encrypted.encryptedTCP)
        assertEquals(9992, encrypted.port)
        assertThrows(IllegalArgumentException::class.java) { ServerAddress.parse("http://47.117.40.74:9992", true) }
        for (bad in listOf("http://agent.example", "file:///secret", "javascript:alert(1)", "https://user:pass@agent.example", "https://agent.example/path", "https://agent.example/?token=secret", "https://agent.example/#secret")) {
            assertThrows(IllegalArgumentException::class.java) { ServerAddress.parse(bad, false) }
        }
        assertThrows(IllegalArgumentException::class.java) { ServerAddress.parse("http://example.org", true) }
        assertThrows(IllegalArgumentException::class.java) { ServerAddress.parse("http://10.0.2.2:8080", false) }
    }
}
