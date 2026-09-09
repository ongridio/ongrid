package apm

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	model "github.com/ongridio/ongrid/internal/manager/model/aiops"
	"github.com/ongridio/ongrid/internal/pkg/errs"
)

type SourceRevisionResolver interface {
	ResolveSourceRevision(context.Context, string, string) (string, error)
}

func (s *Service) WithSourceRevisions(source SourceRevisionResolver) { s.source = source }

// ResolveAPMSource captures a server-verified source identity once, before the
// model runs. An unresolved scope still blocks source tools after a restart.
func (s *Service) ResolveAPMSource(ctx context.Context, target model.APMSourceTarget) *model.APMSourceScope {
	out := &model.APMSourceScope{TraceID: canonicalTraceID(target.TraceID)}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	if err := s.resolveAPMSource(ctx, target, out); err != nil {
		out.Error = err.Error()
	}
	return out
}

func (s *Service) resolveAPMSource(ctx context.Context, target model.APMSourceTarget, out *model.APMSourceScope) error {
	if out.TraceID == "" || target.ServiceName == "" {
		return fmt.Errorf("APM source: valid trace and service identity required")
	}
	if s.source == nil || s.traces == nil {
		return fmt.Errorf("APM source: backend unavailable")
	}
	id := Identity{ServiceName: target.ServiceName, ServiceNamespace: target.ServiceNamespace, Environment: target.Environment}
	binding, err := s.GetRepositoryBinding(ctx, id)
	if err != nil {
		return fmt.Errorf("APM source binding: %w", err)
	}
	if binding == nil || binding.RepoMissing {
		return fmt.Errorf("APM source: repository unbound or deleted")
	}
	out.RepoID, out.SourceDirectory = strconv.FormatUint(binding.RepoID, 10), binding.SourceDirectory
	trace, err := s.traces.GetTrace(ctx, out.TraceID)
	if err != nil {
		return fmt.Errorf("APM source trace: %w", err)
	}
	if trace == nil {
		return fmt.Errorf("APM source: empty trace")
	}
	resources, err := decodeTrace(trace.Body)
	if err != nil {
		return err
	}
	for _, resource := range resources {
		attrs := resource.attributes()
		env := attrs["deployment.environment.name"]
		if env == "" {
			env = attrs["deployment.environment"]
		}
		if attrs["service.name"] != id.ServiceName || attrs["service.namespace"] != id.ServiceNamespace || env != id.Environment {
			continue
		}
		if target.ServiceVersion != "" && attrs["service.version"] != target.ServiceVersion {
			continue
		}
		if target.InstanceID != "" && attrs["service.instance.id"] != target.InstanceID {
			continue
		}
		failingServer := false
		for _, span := range resource.spans() {
			if (string(span.Kind) == "2" || string(span.Kind) == `"SPAN_KIND_SERVER"`) && (string(span.Status.Code) == "2" || string(span.Status.Code) == `"STATUS_CODE_ERROR"`) {
				failingServer = true
			}
		}
		if !failingServer {
			continue
		}
		revision := attrs["vcs.ref.head.revision"]
		version := attrs["service.version"]
		if revision != "" {
			if _, err := hex.DecodeString(revision); err != nil || (len(revision) != 40 && len(revision) != 64) {
				return fmt.Errorf("APM source: invalid build commit")
			}
		} else {
			if version == "" {
				return fmt.Errorf("APM source: missing build commit and service.version")
			}
			revision = "refs/tags/" + strings.ReplaceAll(binding.TagPattern, "{version}", version)
		}
		commit, err := s.source.ResolveSourceRevision(ctx, out.RepoID, revision)
		if err != nil {
			return fmt.Errorf("APM source revision: %w", err)
		}
		// A published version tag must not disagree with the deployed build SHA.
		if attrs["vcs.ref.head.revision"] != "" && version != "" {
			tag := "refs/tags/" + strings.ReplaceAll(binding.TagPattern, "{version}", version)
			tagged, tagErr := s.source.ResolveSourceRevision(ctx, out.RepoID, tag)
			if tagErr == nil && tagged != commit {
				return fmt.Errorf("APM source: build commit conflicts with version tag")
			}
			if tagErr != nil && !errors.Is(tagErr, errs.ErrNotFound) {
				return fmt.Errorf("APM source tag: %w", tagErr)
			}
		}
		if out.CommitSHA != "" && out.CommitSHA != commit {
			return fmt.Errorf("APM source: multiple deployed commits match; select an instance/version")
		}
		out.CommitSHA, out.Revision = commit, revision
	}
	if out.CommitSHA == "" {
		return fmt.Errorf("APM source: no failing server span matches the selected service/instance/version")
	}
	return nil
}
