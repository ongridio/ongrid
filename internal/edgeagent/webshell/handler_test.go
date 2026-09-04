package webshell

import (
	"bytes"
	"io"
	"testing"

	"github.com/ongridio/ongrid/internal/pkg/tunnel"
)

func TestValidateTargetAllowsAnyLoopbackPort(t *testing.T) {
	tests := []struct {
		name, target string
		wantErr      bool
	}{
		{name: "default ssh", target: "127.0.0.1:22"},
		{name: "custom ssh port", target: "127.0.0.1:2222"},
		{name: "highest tcp port", target: "127.0.0.1:65535"},
		{name: "zero port denied", target: "127.0.0.1:0", wantErr: true},
		{name: "out of range port denied", target: "127.0.0.1:65536", wantErr: true},
		{name: "remote host denied", target: "10.0.0.2:22", wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateTarget(tt.target)
			if (err != nil) != tt.wantErr {
				t.Fatalf("validateTarget(%q) error = %v", tt.target, err)
			}
		})
	}
}

func TestResolveTargetFromSSHIdentification(t *testing.T) {
	for _, tt := range []struct {
		name, version, want string
	}{
		{name: "custom port", version: tunnel.WebshellSSHClientVersion(2222), want: "127.0.0.1:2222"},
		{name: "legacy manager", version: "SSH-2.0-Go", want: "127.0.0.1:22"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			wire := tt.version + "\r\npayload"
			stream := &memoryStream{reader: bytes.NewBufferString(wire)}
			target, replay, err := resolveTarget(stream)
			if err != nil {
				t.Fatal(err)
			}
			if target != tt.want {
				t.Fatalf("target = %q, want %q", target, tt.want)
			}
			got, err := io.ReadAll(replay)
			if err != nil {
				t.Fatal(err)
			}
			if string(got) != wire {
				t.Fatalf("replayed stream = %q, want %q", got, wire)
			}
		})
	}
}

type memoryStream struct {
	reader io.Reader
	meta   []byte
}

func (s *memoryStream) Read(p []byte) (int, error)  { return s.reader.Read(p) }
func (s *memoryStream) Write(p []byte) (int, error) { return len(p), nil }
func (s *memoryStream) Close() error                { return nil }
func (s *memoryStream) Meta() []byte                { return s.meta }
