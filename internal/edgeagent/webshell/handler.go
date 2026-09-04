// Package webshell turns the edge agent into a generic stream port-
// forwarder. Manager carries the loopback SSH port in the SSH client
// identification line because Frontier does not relay stream Meta;
// the edge dials that local TCP socket and io.Copy's bytes both ways.
//
// SSH lives entirely on the manager side now: manager wraps the
// stream with ssh.NewClientConn, runs PTY + Shell, and pumps to the
// browser WebSocket. The edge has no SSH client, no pty management,
// no session map — it's a one-screen TCP forwarder. This keeps the
// edge tiny and lets the manager be the sole owner of webshell
// policy / audit / concurrency / kick-out logic.
//
// Targets are restricted to device loopback so forged metadata cannot aim the
// edge at arbitrary intranet hosts. Any valid TCP port may be used.
package webshell

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"strconv"
	"strings"
	"time"

	"github.com/ongridio/ongrid/internal/pkg/tunnel"
)

// Acceptor accepts inbound streams. *tunnel.Client satisfies it.
type Acceptor interface {
	AcceptStream() (tunnel.StreamConn, error)
}

// Register kicks off the AcceptStream loop in a goroutine. Each
// accepted stream is dispatched to a forwarder goroutine that lives
// for the duration of the stream. Returns immediately; stops only
// when AcceptStream returns a fatal error (tunnel torn down).
func Register(client Acceptor, log *slog.Logger) {
	if log == nil {
		log = slog.Default()
	}
	go acceptLoop(client, log)
	log.Info("webshell: stream forwarder running")
}

func HandleStream(stream tunnel.StreamConn, log *slog.Logger) {
	handleStream(stream, log)
}

// acceptLoop pumps AcceptStream calls forever (until tunnel close).
// Each stream is handed to a separate goroutine — concurrent shells
// don't block one another.
func acceptLoop(client Acceptor, log *slog.Logger) {
	for {
		stream, err := client.AcceptStream()
		if err != nil {
			// Treat "not dialed" / EOF / closed as transient — wait
			// a beat and retry. The tunnel layer drives reconnect.
			if errors.Is(err, io.EOF) || strings.Contains(err.Error(), "not dialed") || strings.Contains(err.Error(), "closed") {
				time.Sleep(500 * time.Millisecond)
				continue
			}
			log.Warn("webshell: accept stream", slog.Any("err", err))
			time.Sleep(time.Second)
			continue
		}
		go handleStream(stream, log)
	}
}

// streamMeta is the JSON shape the manager puts in the stream's Meta
// blob. Keep field names stable; future fields (ttl, audit_id, ...)
// can be added without breaking older edges as long as we json.Decode
// with allow-unknown-fields semantics (default).
type streamMeta struct {
	Target string `json:"target"`
}

func handleStream(stream tunnel.StreamConn, log *slog.Logger) {
	defer stream.Close()
	target, routedStream, err := resolveTarget(stream)
	if err != nil {
		writeStreamError(stream, err.Error())
		return
	}
	stream = routedStream
	if err := validateTarget(target); err != nil {
		writeStreamError(stream, fmt.Sprintf("target %q not allowed", target))
		log.Warn("webshell: rejected target", slog.String("target", target), slog.Any("err", err))
		return
	}

	conn, err := net.DialTimeout("tcp", target, 5*time.Second)
	if err != nil {
		writeStreamError(stream, fmt.Sprintf("dial %s: %v", target, err))
		return
	}
	defer conn.Close()

	log.Info("webshell: forwarding", slog.String("target", target))

	// Bidirectional copy. First side to error closes the other.
	errs := make(chan error, 2)
	go func() {
		_, err := io.Copy(conn, stream)
		errs <- err
	}()
	go func() {
		_, err := io.Copy(stream, conn)
		errs <- err
	}()
	<-errs
	// Closing both ends releases the surviving io.Copy.
	_ = conn.Close()
	_ = stream.Close()
	<-errs
}

func resolveTarget(stream tunnel.StreamConn) (string, tunnel.StreamConn, error) {
	var m streamMeta
	if raw := stream.Meta(); len(raw) > 0 {
		if err := json.Unmarshal(raw, &m); err != nil {
			return "", stream, fmt.Errorf("bad meta: %w", err)
		}
	}
	target := strings.TrimSpace(m.Target)
	if target != "" {
		return target, stream, nil
	}

	// Frontier v1.2.4 does not copy stream Meta across its service-to-edge
	// bridge. SSH writes its identification line first, so carry the requested
	// loopback port there and replay the line unchanged to sshd.
	reader := bufio.NewReaderSize(stream, 257)
	line, err := reader.ReadSlice('\n')
	if err != nil {
		return "", stream, fmt.Errorf("read SSH identification: %w", err)
	}
	replay := &replayStream{
		StreamConn: stream,
		reader:     io.MultiReader(bytes.NewReader(bytes.Clone(line)), reader),
	}
	port, ok := tunnel.ParseWebshellSSHClientVersion(string(line))
	if !ok {
		return "127.0.0.1:22", replay, nil
	}
	return net.JoinHostPort("127.0.0.1", strconv.Itoa(int(port))), replay, nil
}

type replayStream struct {
	tunnel.StreamConn
	reader io.Reader
}

func (s *replayStream) Read(p []byte) (int, error) {
	return s.reader.Read(p)
}

// validateTarget fixes the host to device loopback and accepts any valid TCP
// port. The manager never gets access to other services on the device network.
func validateTarget(target string) error {
	host, rawPort, err := net.SplitHostPort(target)
	if err != nil || host != "127.0.0.1" {
		return errors.New("only 127.0.0.1 targets are allowed")
	}
	port, err := strconv.ParseUint(rawPort, 10, 16)
	if err != nil || port == 0 {
		return errors.New("invalid port")
	}
	return nil
}

// writeStreamError sends a brief plain-text error to the stream so
// the manager-side ssh.NewClientConn fails with a useful message
// rather than a generic "EOF on protocol read".
func writeStreamError(s io.Writer, msg string) {
	_, _ = io.WriteString(s, "ongrid-edge webshell forwarder: "+msg+"\n")
}
