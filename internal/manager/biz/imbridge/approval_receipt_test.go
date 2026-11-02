package imbridge

import (
	"errors"
	"fmt"
	"testing"
	"unicode/utf8"

	"github.com/ongridio/ongrid/internal/pkg/errs"
	"github.com/stretchr/testify/require"
)

func TestApprovalReceiptLocalizedSafeAndWithinCallbackLimit(t *testing.T) {
	for _, provider := range []string{"feishu", "slack", "telegram", "dingtalk"} {
		for _, locale := range []string{"zh", "en"} {
			for _, err := range []error{nil, ErrIdentityUnbound, errs.ErrForbidden, errs.ErrNotFound, errs.ErrConflict, errs.ErrInvalid, errors.New("private-secret-error")} {
				t.Run(fmt.Sprintf("%s/%s/%v", provider, locale, err), func(t *testing.T) {
					wrapped := err
					if err != nil {
						wrapped = fmt.Errorf("context: %w", err)
					}
					text := ApprovalReceipt(wrapped, locale, provider)
					require.NotEmpty(t, text)
					require.LessOrEqual(t, utf8.RuneCountInString(text), 200)
					require.NotContains(t, text, "private-secret-error")
					require.NotContains(t, text, "/whoami")
					if err == ErrIdentityUnbound {
						command := "/bind"
						if provider == "slack" {
							command = "/ongrid-bind"
						}
						require.Contains(t, text, command)
					}
				})
			}
		}
	}
}
