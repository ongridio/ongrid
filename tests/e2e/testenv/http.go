//go:build e2e

package testenv

import "net"

// freePort grabs a free TCP port by binding to :0 and immediately
// closing. There is a tiny race between Close and the manager's Listen,
// but the alternative (passing a *net.Listener to the manager) requires
// it to support fd inheritance, which it doesn't. The race is benign
// for e2e: if the port races, manager Listen errors and waitReady
// surfaces it within 20s.
func freePort() (int, error) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, err
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port, nil
}
