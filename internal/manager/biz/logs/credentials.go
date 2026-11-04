package logs

import (
	"context"
	"crypto/subtle"
	"errors"
	"fmt"
	"strings"
	"unicode"

	model "github.com/ongridio/ongrid/internal/manager/model/logs"
	"github.com/ongridio/ongrid/internal/pkg/errs"
)

const (
	authTypeAPIKey = "api_key"
	authTypeBasic  = "basic"
)

type elasticsearchCredential struct {
	APIKey        string
	BasicUser     string
	BasicPassword string
}

func backendAuthType(backend *model.Backend) string {
	if backend == nil || backend.AuthType == "" {
		return authTypeAPIKey
	}
	return backend.AuthType
}

func (c elasticsearchCredential) fields() map[string]string {
	if c.APIKey != "" {
		return map[string]string{"api_key": c.APIKey}
	}
	return map[string]string{"username": c.BasicUser, "password": c.BasicPassword}
}

func (c elasticsearchCredential) empty() bool {
	return c.APIKey == "" && c.BasicUser == "" && c.BasicPassword == ""
}

func (c elasticsearchCredential) validate(authType string) error {
	switch authType {
	case authTypeAPIKey:
		if c.APIKey == "" || len(c.APIKey) > maxAPIKeyBytes || strings.ContainsAny(c.APIKey, " \t\r\n") || c.BasicUser != "" || c.BasicPassword != "" {
			return errors.New("an API key without Basic Auth fields is required")
		}
	case authTypeBasic:
		if c.APIKey != "" || c.BasicUser == "" || len(c.BasicUser) > 256 || strings.Contains(c.BasicUser, ":") || strings.IndexFunc(c.BasicUser, unicode.IsControl) >= 0 || c.BasicPassword == "" || len(c.BasicPassword) > maxAPIKeyBytes {
			return errors.New("a valid username and password without an API key are required")
		}
	default:
		return errors.New("unsupported Elasticsearch authentication type")
	}
	return nil
}

func (s *Service) credential(ctx context.Context, ref, authType string) (elasticsearchCredential, error) {
	if s.secrets == nil {
		return elasticsearchCredential{}, errors.New("log backend secret resolver is disabled")
	}
	fields, err := s.secrets.ResolveFields(ctx, ref)
	if err != nil {
		return elasticsearchCredential{}, fmt.Errorf("resolve Elasticsearch credential: %w", err)
	}
	c := elasticsearchCredential{APIKey: strings.TrimSpace(fields["api_key"]), BasicUser: fields["username"], BasicPassword: fields["password"]}
	if err := c.validate(authType); err != nil {
		return elasticsearchCredential{}, err
	}
	return c, nil
}

// Save and draft probes share retention and validation so testing cannot accept
// credentials that saving would interpret differently.
func (s *Service) resolveInputCredentials(ctx context.Context, input *SaveInput, previous *model.Backend) (write, query elasticsearchCredential, err error) {
	if input.AuthType == "" {
		input.AuthType = backendAuthType(previous)
	}
	if previous != nil {
		if input.WriteCredentialRef == "" {
			input.WriteCredentialRef = previous.WriteCredentialRef
		}
		if input.QueryCredentialRef == "" {
			input.QueryCredentialRef = previous.QueryCredentialRef
		}
	}
	write = elasticsearchCredential{APIKey: input.WriteAPIKey, BasicUser: input.WriteUsername, BasicPassword: input.WritePassword}
	query = elasticsearchCredential{APIKey: input.QueryAPIKey, BasicUser: input.QueryUsername, BasicPassword: input.QueryPassword}
	for _, role := range []struct {
		name string
		ref  string
		cred *elasticsearchCredential
	}{{"write", input.WriteCredentialRef, &write}, {"query", input.QueryCredentialRef, &query}} {
		if role.cred.empty() {
			*role.cred, err = s.credential(ctx, role.ref, input.AuthType)
		} else {
			err = role.cred.validate(input.AuthType)
		}
		if err != nil {
			return write, query, fmt.Errorf("%w: invalid %s credential for %s authentication", errs.ErrInvalid, role.name, input.AuthType)
		}
	}
	if input.AuthType == authTypeAPIKey && subtle.ConstantTimeCompare([]byte(write.APIKey), []byte(query.APIKey)) == 1 {
		return write, query, fmt.Errorf("%w: write and query API keys must differ", errs.ErrInvalid)
	}
	return write, query, nil
}
