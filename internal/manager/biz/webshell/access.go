package webshell

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	wsmodel "github.com/ongridio/ongrid/internal/manager/model/webshell"
	"github.com/ongridio/ongrid/internal/pkg/errs"
	"github.com/ongridio/ongrid/internal/pkg/secretbox"
)

type AccessRepo interface {
	CreateCredential(context.Context, *wsmodel.Credential) error
	ListCredentials(context.Context, uint64, uint64) ([]*wsmodel.Credential, error)
	GetCredential(context.Context, uint64, uint64, uint64) (*wsmodel.Credential, error)
	DeleteCredential(context.Context, uint64, uint64, uint64) error
	MarkCredentialUsed(context.Context, uint64, uint64, uint64, time.Time) error
	GetKnownHost(context.Context, uint64, uint64, uint16) (*wsmodel.KnownHost, error)
	CreateKnownHost(context.Context, *wsmodel.KnownHost) error
	DeleteKnownHost(context.Context, uint64, uint64, uint16) error
}

type CredentialView struct {
	ID         uint64     `json:"id"`
	SSHUser    string     `json:"ssh_user"`
	SSHPort    uint16     `json:"ssh_port"`
	LastUsedAt *time.Time `json:"last_used_at,omitempty"`
	CreatedAt  time.Time  `json:"created_at"`
}

type ResolvedCredential struct {
	ID       uint64
	SSHUser  string
	SSHPort  uint16
	Password string
}

type HostKeyError struct {
	Kind     string
	Expected string
	Actual   string
}

func (e *HostKeyError) Error() string { return "ssh host key " + e.Kind }

type Access struct{ repo AccessRepo }

func NewAccess(repo AccessRepo) *Access { return &Access{repo: repo} }

func (a *Access) CreateCredential(ctx context.Context, userID, deviceID uint64, sshUser, password string, port uint16) (*CredentialView, error) {
	sshUser = strings.TrimSpace(sshUser)
	if userID == 0 || deviceID == 0 || sshUser == "" || len(sshUser) > 64 || password == "" || len(password) > 4096 || port == 0 {
		return nil, fmt.Errorf("%w: invalid SSH credential", errs.ErrInvalid)
	}
	sealed, err := secretbox.Encrypt(password)
	if err != nil {
		return nil, fmt.Errorf("encrypt SSH credential: %w", err)
	}
	row := &wsmodel.Credential{OngridUserID: userID, DeviceID: deviceID, Label: sshUser, SSHUser: sshUser, SSHPort: port, PasswordCiphertext: sealed}
	if err := a.repo.CreateCredential(ctx, row); err != nil {
		return nil, err
	}
	return credentialView(row), nil
}

func (a *Access) ListCredentials(ctx context.Context, userID, deviceID uint64) ([]*CredentialView, error) {
	rows, err := a.repo.ListCredentials(ctx, userID, deviceID)
	if err != nil {
		return nil, err
	}
	out := make([]*CredentialView, 0, len(rows))
	for _, row := range rows {
		out = append(out, credentialView(row))
	}
	return out, nil
}

func (a *Access) ResolveCredential(ctx context.Context, id, userID, deviceID uint64) (*ResolvedCredential, error) {
	row, err := a.repo.GetCredential(ctx, id, userID, deviceID)
	if err != nil {
		return nil, err
	}
	password, err := secretbox.Decrypt(row.PasswordCiphertext)
	if err != nil {
		return nil, fmt.Errorf("decrypt SSH credential: %w", err)
	}
	now := time.Now().UTC()
	if err := a.repo.MarkCredentialUsed(ctx, id, userID, deviceID, now); err != nil {
		return nil, fmt.Errorf("mark SSH credential used: %w", err)
	}
	return &ResolvedCredential{ID: row.ID, SSHUser: row.SSHUser, SSHPort: row.SSHPort, Password: password}, nil
}

func (a *Access) DeleteCredential(ctx context.Context, id, userID, deviceID uint64) error {
	return a.repo.DeleteCredential(ctx, id, userID, deviceID)
}

func (a *Access) VerifyHostKey(ctx context.Context, userID, deviceID uint64, port uint16, actual, accepted string) error {
	known, err := a.repo.GetKnownHost(ctx, userID, deviceID, port)
	if err == nil {
		if known.Fingerprint != actual {
			return &HostKeyError{Kind: "changed", Expected: known.Fingerprint, Actual: actual}
		}
		return nil
	}
	if !errors.Is(err, errs.ErrNotFound) {
		return err
	}
	if accepted == "" || accepted != actual {
		return &HostKeyError{Kind: "unknown", Actual: actual}
	}
	if err := a.repo.CreateKnownHost(ctx, &wsmodel.KnownHost{OngridUserID: userID, DeviceID: deviceID, SSHPort: port, Fingerprint: actual}); err != nil {
		// A concurrent first connection may have won the unique insert.
		known, getErr := a.repo.GetKnownHost(ctx, userID, deviceID, port)
		if getErr != nil || known.Fingerprint != actual {
			return fmt.Errorf("trust SSH host key: %w", err)
		}
	}
	return nil
}

func (a *Access) DeleteKnownHost(ctx context.Context, userID, deviceID uint64, port uint16) error {
	if port == 0 {
		return fmt.Errorf("%w: invalid SSH port", errs.ErrInvalid)
	}
	return a.repo.DeleteKnownHost(ctx, userID, deviceID, port)
}

func credentialView(row *wsmodel.Credential) *CredentialView {
	return &CredentialView{ID: row.ID, SSHUser: row.SSHUser, SSHPort: row.SSHPort, LastUsedAt: row.LastUsedAt, CreatedAt: row.CreatedAt}
}
