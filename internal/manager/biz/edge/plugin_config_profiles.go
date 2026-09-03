package edge

import (
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/ongridio/ongrid/internal/pkg/errs"
)

func validateProfilesSpec(spec map[string]interface{}) error {
	mode := strings.ToLower(mapStringDefault(spec, "mode", "pprof"))
	if mode != "pprof" {
		return fmt.Errorf("%w: profiles.mode must be pprof", errs.ErrInvalid)
	}
	if raw, ok := spec["duration_seconds"]; ok {
		value, valid := exporterIntValue(raw)
		if !valid || value < 10 || value > 3600 {
			return fmt.Errorf("%w: profiles.duration_seconds must be an integer between 10 and 3600", errs.ErrInvalid)
		}
	}
	if raw, ok := spec["tls_insecure_skip_verify"]; ok {
		if _, valid := raw.(bool); !valid {
			return fmt.Errorf("%w: profiles.tls_insecure_skip_verify must be boolean", errs.ErrInvalid)
		}
	}
	target, ok := mapValue(spec["runtime_target"])
	if !ok {
		return fmt.Errorf("%w: profiles.runtime_target required for pprof mode", errs.ErrInvalid)
	}
	rawURL := mapString(target, "url")
	u, err := url.Parse(rawURL)
	if err != nil || len(rawURL) > 2048 || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") || u.User != nil {
		return fmt.Errorf("%w: profiles.runtime_target.url must be an http(s) URL without credentials", errs.ErrInvalid)
	}
	profileType := strings.ToLower(mapString(target, "profile_type"))
	switch profileType {
	case "cpu", "heap", "allocs", "goroutine", "mutex", "block":
	default:
		return fmt.Errorf("%w: profiles.runtime_target.profile_type unsupported %q", errs.ErrInvalid, profileType)
	}
	if name := mapString(target, "service_name"); len(name) > 128 || strings.ContainsAny(name, "\r\n") {
		return fmt.Errorf("%w: profiles.runtime_target.service_name is invalid", errs.ErrInvalid)
	}
	if raw, ok := target["process_pid"]; ok {
		pid, valid := exporterIntValue(raw)
		if !valid || pid < 1 {
			return fmt.Errorf("%w: profiles.runtime_target.process_pid must be a positive integer", errs.ErrInvalid)
		}
	}
	if raw, ok := target["collection_interval_seconds"]; ok {
		interval, valid := exporterIntValue(raw)
		if !valid || interval < 10 || interval > 3600 {
			return fmt.Errorf("%w: profiles.runtime_target.collection_interval_seconds must be between 10 and 3600", errs.ErrInvalid)
		}
	}
	if raw, ok := target["tls_insecure_skip_verify"]; ok {
		if _, valid := raw.(bool); !valid {
			return fmt.Errorf("%w: profiles.runtime_target.tls_insecure_skip_verify must be boolean", errs.ErrInvalid)
		}
	}
	return nil
}

func startProfilesSession(spec map[string]interface{}) map[string]interface{} {
	out := make(map[string]interface{}, len(spec)+2)
	for key, value := range spec {
		out[key] = value
	}
	duration := 30
	if value, valid := exporterIntValue(out["duration_seconds"]); valid {
		duration = value
	}
	out["duration_seconds"] = duration
	out["expires_at"] = time.Now().UTC().Add(time.Duration(duration) * time.Second).Format(time.RFC3339Nano)
	return out
}
