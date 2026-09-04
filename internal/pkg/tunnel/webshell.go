package tunnel

import (
	"strconv"
	"strings"
)

const webshellSSHClientVersionPrefix = "SSH-2.0-Ongrid-WebShell-"

func WebshellSSHClientVersion(port uint16) string {
	return webshellSSHClientVersionPrefix + strconv.Itoa(int(port))
}

func ParseWebshellSSHClientVersion(version string) (uint16, bool) {
	raw, ok := strings.CutPrefix(strings.TrimSpace(version), webshellSSHClientVersionPrefix)
	if !ok {
		return 0, false
	}
	port, err := strconv.ParseUint(raw, 10, 16)
	return uint16(port), err == nil && port > 0
}
