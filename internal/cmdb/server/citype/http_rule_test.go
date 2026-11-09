package citype

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"

	citypebiz "github.com/ongridio/ongrid/internal/cmdb/biz/citype"
	model "github.com/ongridio/ongrid/internal/cmdb/model"
	citypemodel "github.com/ongridio/ongrid/internal/cmdb/model/citype"
)

// --- the rule half of the server double ---

func (s *attrStub) UniqueConstraintsForType(_ context.Context, typeID model.ID) ([]citypemodel.CITypeUniqueConstraint, error) {
	out := make([]citypemodel.CITypeUniqueConstraint, 0, len(s.constraints))
	for _, c := range s.constraints {
		if c.TypeID == typeID {
			out = append(out, c)
		}
	}
	return out, nil
}

func (s *attrStub) UniqueConstraintByID(_ context.Context, id model.ID) (*citypemodel.CITypeUniqueConstraint, error) {
	for _, c := range s.constraints {
		if c.ID == id {
			copied := c
			return &copied, nil
		}
	}
	return nil, nil
}

func (s *attrStub) CreateUniqueConstraint(_ context.Context, in citypebiz.UniqueConstraintCreateSpec) (*citypemodel.CITypeUniqueConstraint, error) {
	if s.ruleErr != nil {
		return nil, s.ruleErr
	}
	s.createdConstraint = &in
	row := citypemodel.CITypeUniqueConstraint{TypeID: in.TypeID, AttrIDs: citypemodel.IDList(in.AttrIDs)}
	row.ID = model.ID(len(s.constraints) + 900)
	s.constraints = append(s.constraints, row)
	return &row, nil
}

func (s *attrStub) UpdateUniqueConstraint(_ context.Context, in citypebiz.UniqueConstraintUpdateSpec) error {
	if s.ruleErr != nil {
		return s.ruleErr
	}
	s.updatedConstraint = &in
	return nil
}

func (s *attrStub) DeleteUniqueConstraint(_ context.Context, in citypebiz.UniqueConstraintDeleteSpec) error {
	if s.ruleErr != nil {
		return s.ruleErr
	}
	s.deletedConstraint = &in
	return nil
}

func (s *attrStub) TriggersForType(_ context.Context, typeID model.ID) ([]citypemodel.CITypeTrigger, error) {
	out := make([]citypemodel.CITypeTrigger, 0, len(s.triggers))
	for _, t := range s.triggers {
		if t.TypeID == typeID {
			out = append(out, t)
		}
	}
	return out, nil
}

func (s *attrStub) TriggerByID(_ context.Context, id model.ID) (*citypemodel.CITypeTrigger, error) {
	for _, t := range s.triggers {
		if t.ID == id {
			copied := t
			return &copied, nil
		}
	}
	return nil, nil
}

func (s *attrStub) CreateTrigger(_ context.Context, in citypebiz.TriggerCreateSpec) (*citypemodel.CITypeTrigger, error) {
	if s.ruleErr != nil {
		return nil, s.ruleErr
	}
	s.createdTrigger = &in
	row := citypemodel.CITypeTrigger{TypeID: in.TypeID, AttrID: in.AttrID, Notify: in.Option}
	row.ID = model.ID(len(s.triggers) + 800)
	s.triggers = append(s.triggers, row)
	return &row, nil
}

func (s *attrStub) UpdateTrigger(_ context.Context, in citypebiz.TriggerUpdateSpec) error {
	if s.ruleErr != nil {
		return s.ruleErr
	}
	s.updatedTrigger = &in
	for i := range s.triggers {
		if s.triggers[i].ID == in.ID {
			s.triggers[i].AttrID = in.AttrID
			s.triggers[i].Notify = in.Option
		}
	}
	return nil
}

func (s *attrStub) DeleteTrigger(_ context.Context, in citypebiz.TriggerDeleteSpec) error {
	if s.ruleErr != nil {
		return s.ruleErr
	}
	s.deletedTrigger = &in
	return nil
}

// ruleStub seeds a model with one attribute so the rules have something to
// reference.
func ruleStub() *attrStub {
	stub := newAttrStub().
		seedAttribute(attr(1, "hostname")).
		seedAttribute(attr(2, "serial"))
	ct := typeRow(10, "host")
	ct.UniqueID = 1
	return stub.seedType(ct)
}

func constraintRow(id, typeID model.ID, attrIDs ...model.ID) citypemodel.CITypeUniqueConstraint {
	row := citypemodel.CITypeUniqueConstraint{TypeID: typeID, AttrIDs: citypemodel.IDList(attrIDs)}
	row.ID = id
	return row
}

func triggerRow(id, typeID model.ID, option map[string]any) citypemodel.CITypeTrigger {
	row := citypemodel.CITypeTrigger{TypeID: typeID, Notify: option}
	row.ID = id
	return row
}

// --- authorization ---

// Both the reads AND the writes are admin-gated. cantor gated these two on
// CONFIG as well, unlike the attribute routes — a trigger's option names the
// endpoints the product calls out to.
func TestRuleRoutesRequireAdmin(t *testing.T) {
	cases := []struct{ method, path, body string }{
		{http.MethodGet, "/v1/cmdb/ci-types/10/unique-constraints", ""},
		{http.MethodPost, "/v1/cmdb/ci-types/10/unique-constraints", `{"attr_ids":[1]}`},
		{http.MethodPut, "/v1/cmdb/ci-types/10/unique-constraints/900", `{"attr_ids":[1]}`},
		{http.MethodDelete, "/v1/cmdb/ci-types/10/unique-constraints/900", ""},
		{http.MethodGet, "/v1/cmdb/ci-types/10/triggers", ""},
		{http.MethodPost, "/v1/cmdb/ci-types/10/triggers", `{"option":{"enable":true}}`},
		{http.MethodPut, "/v1/cmdb/ci-types/10/triggers/800", `{"option":{"enable":true}}`},
		{http.MethodDelete, "/v1/cmdb/ci-types/10/triggers/800", ""},
		{http.MethodPost, "/v1/cmdb/ci-types/10/triggers/800/test", ""},
	}
	for _, tc := range cases {
		t.Run(tc.method+" "+tc.path, func(t *testing.T) {
			rec := send(t, attrRouter(t, ruleStub(), "user", false, 7), tc.method, tc.path, tc.body)
			if rec.Code != http.StatusForbidden {
				t.Fatalf("status = %d, want 403 (body %s)", rec.Code, rec.Body.String())
			}
		})
	}
}

func TestRuleRoutesWithoutIdentityAreUnauthorized(t *testing.T) {
	h := NewHandler(citypebiz.NewUsecase(ruleStub(), nil))
	r := chi.NewRouter()
	h.Register(r) // no identity middleware at all

	rec := send(t, r, http.MethodGet, "/v1/cmdb/ci-types/10/triggers", "")
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401 when no identity is on the context", rec.Code)
	}
}

// --- unique constraints ---

func TestCreateConstraintReturns201(t *testing.T) {
	stub := ruleStub()
	rec := send(t, attrRouter(t, stub, "admin", false, 7), http.MethodPost,
		"/v1/cmdb/ci-types/10/unique-constraints", `{"attr_ids":[1,2]}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201 (body %s)", rec.Code, rec.Body.String())
	}
	var got constraintListResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.Total != 1 {
		t.Fatalf("total = %d, want the created constraint returned", got.Total)
	}
	if stub.createdConstraint == nil {
		t.Fatal("the create was not forwarded")
	}
}

func TestCreateConstraintWithoutAttrsIsBadRequest(t *testing.T) {
	rec := send(t, attrRouter(t, ruleStub(), "admin", false, 7), http.MethodPost,
		"/v1/cmdb/ci-types/10/unique-constraints", `{"attr_ids":[]}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 (body %s)", rec.Code, rec.Body.String())
	}
}

// A shape refusal is malformed input, but a DUPLICATE is a statement about
// state, so it is a 409.
func TestConstraintDuplicateIsConflict(t *testing.T) {
	stub := ruleStub()
	stub.ruleErr = citypebiz.ErrConstraintDuplicate

	rec := send(t, attrRouter(t, stub, "admin", false, 7), http.MethodPost,
		"/v1/cmdb/ci-types/10/unique-constraints", `{"attr_ids":[1]}`)
	if rec.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409 (body %s)", rec.Code, rec.Body.String())
	}
	var body errorBody
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.Code != "conflict" {
		t.Fatalf("code = %q, want conflict", body.Code)
	}
}

func TestConstraintInvalidAttributeIsBadRequest(t *testing.T) {
	stub := ruleStub()
	stub.ruleErr = citypebiz.ErrConstraintInvalid

	rec := send(t, attrRouter(t, stub, "admin", false, 7), http.MethodPost,
		"/v1/cmdb/ci-types/10/unique-constraints", `{"attr_ids":[1]}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 (body %s)", rec.Code, rec.Body.String())
	}
}

func TestConstraintOfAnotherModelIsNotFound(t *testing.T) {
	stub := ruleStub().seedType(typeRow(20, "theirs"))
	stub.constraints = []citypemodel.CITypeUniqueConstraint{constraintRow(900, 20, 1)}

	rec := send(t, attrRouter(t, stub, "admin", false, 7), http.MethodDelete,
		"/v1/cmdb/ci-types/10/unique-constraints/900", "")
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404 — a rule of another model is not addressable here (body %s)",
			rec.Code, rec.Body.String())
	}
}

func TestConstraintMissingIsNotFound(t *testing.T) {
	rec := send(t, attrRouter(t, ruleStub(), "admin", false, 7), http.MethodDelete,
		"/v1/cmdb/ci-types/10/unique-constraints/999", "")
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404 (body %s)", rec.Code, rec.Body.String())
	}
}

func TestConstraintNonNumericIDIsBadRequest(t *testing.T) {
	rec := send(t, attrRouter(t, ruleStub(), "admin", false, 7), http.MethodDelete,
		"/v1/cmdb/ci-types/10/unique-constraints/abc", "")
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 (body %s)", rec.Code, rec.Body.String())
	}
}

func TestDeleteConstraintReturns204(t *testing.T) {
	stub := ruleStub()
	stub.constraints = []citypemodel.CITypeUniqueConstraint{constraintRow(900, 10, 1)}

	rec := send(t, attrRouter(t, stub, "admin", false, 7), http.MethodDelete,
		"/v1/cmdb/ci-types/10/unique-constraints/900", "")
	if rec.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want 204 (body %s)", rec.Code, rec.Body.String())
	}
}

func TestListConstraintsUsesTheEnvelopeShape(t *testing.T) {
	stub := ruleStub()
	stub.constraints = []citypemodel.CITypeUniqueConstraint{constraintRow(900, 10, 1, 2)}

	rec := send(t, attrRouter(t, stub, "admin", false, 7), http.MethodGet,
		"/v1/cmdb/ci-types/10/unique-constraints", "")
	var got constraintListResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v (body %s)", err, rec.Body.String())
	}
	if got.Total != 1 || len(got.Items) != 1 {
		t.Fatalf("response = %+v, want one item", got)
	}
	// The attributes are resolved, because the SPA does not resolve attr ids.
	if len(got.Items[0].Attributes) != 2 {
		t.Fatalf("attributes = %+v, want both resolved", got.Items[0].Attributes)
	}
}

// --- triggers ---

func TestCreateTriggerReturns201(t *testing.T) {
	stub := ruleStub()
	rec := send(t, attrRouter(t, stub, "admin", false, 7), http.MethodPost,
		"/v1/cmdb/ci-types/10/triggers",
		`{"option":{"enable":true,"action":"0"}}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201 (body %s)", rec.Code, rec.Body.String())
	}
	if stub.createdTrigger == nil {
		t.Fatal("the create was not forwarded")
	}
}

func TestCreateTriggerWithoutOptionIsBadRequest(t *testing.T) {
	rec := send(t, attrRouter(t, ruleStub(), "admin", false, 7), http.MethodPost,
		"/v1/cmdb/ci-types/10/triggers", `{"option":null}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 (body %s)", rec.Code, rec.Body.String())
	}
}

func TestCreateTriggerWithAMalformedOptionIsBadRequest(t *testing.T) {
	rec := send(t, attrRouter(t, ruleStub(), "admin", false, 7), http.MethodPost,
		"/v1/cmdb/ci-types/10/triggers", `{"option":{"action":"nonsense"}}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 (body %s)", rec.Code, rec.Body.String())
	}
}

func TestTriggerDuplicateIsConflict(t *testing.T) {
	stub := ruleStub()
	stub.ruleErr = citypebiz.ErrTriggerDuplicate

	rec := send(t, attrRouter(t, stub, "admin", false, 7), http.MethodPost,
		"/v1/cmdb/ci-types/10/triggers", `{"option":{"enable":true,"action":"0"}}`)
	if rec.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409 (body %s)", rec.Code, rec.Body.String())
	}
}

func TestTriggerOfAnotherModelIsNotFound(t *testing.T) {
	stub := ruleStub().seedType(typeRow(20, "theirs"))
	stub.triggers = []citypemodel.CITypeTrigger{triggerRow(800, 20, map[string]any{"enable": true})}

	rec := send(t, attrRouter(t, stub, "admin", false, 7), http.MethodDelete,
		"/v1/cmdb/ci-types/10/triggers/800", "")
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404 (body %s)", rec.Code, rec.Body.String())
	}
}

// The credential is a write-only field. This is asserted on the WIRE, because
// the view could omit it and a later change to the encoder could put it back.
func TestTriggerResponseNeverContainsTheCredential(t *testing.T) {
	stub := ruleStub()
	stub.triggers = []citypemodel.CITypeTrigger{triggerRow(800, 10, map[string]any{
		citypebiz.TriggerKeyEnable: true,
		citypebiz.TriggerKeyAction: "0",
		citypebiz.TriggerKeyWebhooks: map[string]any{
			"url":                    "https://example.invalid/hook",
			citypebiz.WebhookAuthKey: map[string]any{"Authorization": "v1:distinctive-ciphertext"},
		},
	})}

	rec := send(t, attrRouter(t, stub, "admin", false, 7), http.MethodGet,
		"/v1/cmdb/ci-types/10/triggers", "")
	body := rec.Body.String()
	for _, leak := range []string{"distinctive-ciphertext", `"authorization"`} {
		if strings.Contains(body, leak) {
			t.Fatalf("body leaks %q: %s", leak, body)
		}
	}
	if !strings.Contains(body, `"has_webhook_authorization":true`) {
		t.Fatalf("body = %s, want the presence reported instead of the value", body)
	}
	// The rest of the document survives, or the console could not edit it.
	if !strings.Contains(body, "example.invalid") {
		t.Fatalf("body = %s, want the non-secret parts returned", body)
	}
}

// cantor fired the trigger and answered 200. A route that appears to work while
// sending nothing would send an operator to debug their webhook endpoint.
func TestTestTriggerReportsNotImplemented(t *testing.T) {
	stub := ruleStub()
	stub.triggers = []citypemodel.CITypeTrigger{triggerRow(800, 10, map[string]any{"enable": true})}

	rec := send(t, attrRouter(t, stub, "admin", false, 7), http.MethodPost,
		"/v1/cmdb/ci-types/10/triggers/800/test", "")
	if rec.Code != http.StatusNotImplemented {
		t.Fatalf("status = %d, want 501 (body %s)", rec.Code, rec.Body.String())
	}
	var body errorBody
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.Code != "not_wired_yet" {
		t.Fatalf("code = %q, want not_wired_yet", body.Code)
	}
}

func TestDeleteTriggerReturns204(t *testing.T) {
	stub := ruleStub()
	stub.triggers = []citypemodel.CITypeTrigger{triggerRow(800, 10, map[string]any{"enable": true})}

	rec := send(t, attrRouter(t, stub, "admin", false, 7), http.MethodDelete,
		"/v1/cmdb/ci-types/10/triggers/800", "")
	if rec.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want 204 (body %s)", rec.Code, rec.Body.String())
	}
}

func TestListTriggersUsesTheEnvelopeShape(t *testing.T) {
	stub := ruleStub()
	stub.triggers = []citypemodel.CITypeTrigger{triggerRow(800, 10, map[string]any{"enable": true})}

	rec := send(t, attrRouter(t, stub, "admin", false, 7), http.MethodGet,
		"/v1/cmdb/ci-types/10/triggers", "")
	var got triggerListResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v (body %s)", err, rec.Body.String())
	}
	if got.Total != 1 {
		t.Fatalf("total = %d, want 1", got.Total)
	}
}

// A repo failure must not be reported as a client error.
func TestRuleRepoFailureIsInternalError(t *testing.T) {
	stub := ruleStub()
	stub.ruleErr = errors.New("boom")

	rec := send(t, attrRouter(t, stub, "admin", false, 7), http.MethodPost,
		"/v1/cmdb/ci-types/10/unique-constraints", `{"attr_ids":[1]}`)
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500 (body %s)", rec.Code, rec.Body.String())
	}
}
