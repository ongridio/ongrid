package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/ongridio/ongrid/internal/edgeagent/enrollment"
)

const enrollmentTokenEnv = "ONGRID_ENROLLMENT_TOKEN"

func runEnrollmentCommand(ctx context.Context, args []string) (bool, error) {
	if len(args) == 0 || args[0] != "enroll" {
		return false, nil
	}
	flags := flag.NewFlagSet("enroll", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	managerURL := flags.String("manager-url", "", "manager HTTPS base URL")
	output := flags.String("output", "", "environment file destination")
	cloudAddrFallback := flags.String("cloud-addr-fallback", "", "fallback tunnel address")
	tlsInsecure := flags.Bool("tls-insecure", false, "skip TLS certificate verification")
	if err := flags.Parse(args[1:]); err != nil {
		return true, fmt.Errorf("parse enrollment flags: %w", err)
	}
	if flags.NArg() != 0 {
		return true, fmt.Errorf("unexpected enrollment arguments")
	}
	token := strings.TrimSpace(os.Getenv(enrollmentTokenEnv))
	if token == "" {
		return true, fmt.Errorf("%s is required", enrollmentTokenEnv)
	}
	// Do not propagate the bootstrap capability to any later subprocess. The
	// current process still needs the local copy for this one request.
	if err := os.Unsetenv(enrollmentTokenEnv); err != nil {
		return true, fmt.Errorf("clear enrollment token environment: %w", err)
	}
	edgeID, err := enrollment.Enroll(ctx, *managerURL, *output, *cloudAddrFallback, token, version, *tlsInsecure)
	if err != nil {
		return true, err
	}
	fmt.Fprintf(os.Stdout, "edge enrollment completed (edge_id=%d)\n", edgeID)
	return true, nil
}
