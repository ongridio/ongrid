package topology

import (
	"errors"
	"strings"
	"testing"

	model "github.com/ongridio/ongrid/internal/cmdb/model"
	"github.com/ongridio/ongrid/internal/pkg/errs"
)

// The columns are varchar(64)/varchar(32). A longer value reaches MySQL in
// strict mode and comes back as a driver 500; it belongs at the validation
// boundary as a 400.
func TestViewAndGroupNamesFitTheirColumns(t *testing.T) {
	longName := strings.Repeat("界", 65)
	path := map[string][]model.ID{}

	if _, err := validateView(ViewInput{Name: longName, CentralNodeType: 1, Path: path}); !errors.Is(err, errs.ErrInvalid) {
		t.Fatalf("long view name err = %v, want invalid", err)
	}
	if _, err := validateView(ViewInput{
		Name: "ok", Category: strings.Repeat("x", 33), CentralNodeType: 1, Path: path,
	}); !errors.Is(err, errs.ErrInvalid) {
		t.Fatalf("long category err = %v, want invalid", err)
	}
	if _, err := validateView(ViewInput{Name: "ok", CentralNodeType: 1, Path: path}); err != nil {
		t.Fatalf("valid view: %v", err)
	}
	if _, err := validateGroupName(longName); !errors.Is(err, errs.ErrInvalid) {
		t.Fatalf("long group name err = %v, want invalid", err)
	}
	name, err := validateGroupName(" 网络 ")
	if err != nil || name != "网络" {
		t.Fatalf("group name = %q, %v", name, err)
	}
}
