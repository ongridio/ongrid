package tunnel

import "testing"

func TestWebshellSSHClientVersionRoundTrip(t *testing.T) {
	for _, port := range []uint16{22, 2222, 65535} {
		got, ok := ParseWebshellSSHClientVersion(WebshellSSHClientVersion(port))
		if !ok || got != port {
			t.Fatalf("round trip port %d = (%d, %v)", port, got, ok)
		}
	}
	if _, ok := ParseWebshellSSHClientVersion("SSH-2.0-Go"); ok {
		t.Fatal("ordinary SSH client version parsed as WebShell metadata")
	}
}
