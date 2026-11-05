package citype

import (
	"context"
	"errors"
	"strings"
	"testing"

	model "github.com/ongridio/ongrid/internal/cmdb/model"
	citypemodel "github.com/ongridio/ongrid/internal/cmdb/model/citype"
	"github.com/ongridio/ongrid/internal/pkg/errs"
	"github.com/ongridio/ongrid/internal/pkg/secretbox"
)

// --- the rule half of the attribute double ---

func (a *attrRepo) UniqueConstraintsForType(_ context.Context, typeID model.ID) ([]citypemodel.CITypeUniqueConstraint, error) {
	if a.failOn == "UniqueConstraintsForType" {
		return nil, errBoom
	}
	out := make([]citypemodel.CITypeUniqueConstraint, 0, len(a.constraints))
	for _, c := range a.constraints {
		if c.TypeID == typeID {
			out = append(out, c)
		}
	}
	return out, nil
}

func (a *attrRepo) UniqueConstraintByID(_ context.Context, id model.ID) (*citypemodel.CITypeUniqueConstraint, error) {
	for _, c := range a.constraints {
		if c.ID == id {
			copied := c
			return &copied, nil
		}
	}
	return nil, nil
}

func (a *attrRepo) CreateUniqueConstraint(_ context.Context, in UniqueConstraintCreateSpec) (*citypemodel.CITypeUniqueConstraint, error) {
	if a.failOn == "CreateUniqueConstraint" {
		return nil, errBoom
	}
	a.createdConstraint = &in
	row := citypemodel.CITypeUniqueConstraint{TypeID: in.TypeID, AttrIDs: citypemodel.IDList(in.AttrIDs)}
	row.ID = model.ID(len(a.constraints) + 900)
	a.constraints = append(a.constraints, row)
	return &row, nil
}

func (a *attrRepo) UpdateUniqueConstraint(_ context.Context, in UniqueConstraintUpdateSpec) error {
	if a.failOn == "UpdateUniqueConstraint" {
		return errBoom
	}
	a.updatedConstraint = &in
	for i := range a.constraints {
		if a.constraints[i].ID == in.ID {
			a.constraints[i].AttrIDs = citypemodel.IDList(in.AttrIDs)
		}
	}
	return nil
}

func (a *attrRepo) DeleteUniqueConstraint(_ context.Context, in UniqueConstraintDeleteSpec) error {
	if a.failOn == "DeleteUniqueConstraint" {
		return errBoom
	}
	a.deletedConstraint = &in
	return nil
}

func (a *attrRepo) TriggersForType(_ context.Context, typeID model.ID) ([]citypemodel.CITypeTrigger, error) {
	if a.failOn == "TriggersForType" {
		return nil, errBoom
	}
	out := make([]citypemodel.CITypeTrigger, 0, len(a.triggers))
	for _, t := range a.triggers {
		if t.TypeID == typeID {
			out = append(out, t)
		}
	}
	return out, nil
}

func (a *attrRepo) TriggerByID(_ context.Context, id model.ID) (*citypemodel.CITypeTrigger, error) {
	for _, t := range a.triggers {
		if t.ID == id {
			copied := t
			return &copied, nil
		}
	}
	return nil, nil
}

func (a *attrRepo) CreateTrigger(_ context.Context, in TriggerCreateSpec) (*citypemodel.CITypeTrigger, error) {
	if a.failOn == "CreateTrigger" {
		return nil, errBoom
	}
	a.createdTrigger = &in
	row := citypemodel.CITypeTrigger{TypeID: in.TypeID, AttrID: in.AttrID, Notify: in.Option}
	row.ID = model.ID(len(a.triggers) + 800)
	a.triggers = append(a.triggers, row)
	return &row, nil
}

func (a *attrRepo) UpdateTrigger(_ context.Context, in TriggerUpdateSpec) error {
	if a.failOn == "UpdateTrigger" {
		return errBoom
	}
	a.updatedTrigger = &in
	for i := range a.triggers {
		if a.triggers[i].ID == in.ID {
			a.triggers[i].AttrID = in.AttrID
			a.triggers[i].Notify = in.Option
		}
	}
	return nil
}

func (a *attrRepo) DeleteTrigger(_ context.Context, in TriggerDeleteSpec) error {
	if a.failOn == "DeleteTrigger" {
		return errBoom
	}
	a.deletedTrigger = &in
	return nil
}

// --- fixtures ---

// ruleRepo seeds one model plus the given attributes.
func ruleRepo(hostID model.ID, attrs ...citypemodel.Attribute) *attrRepo {
	r := newAttrRepo()
	ct := newCIType(hostID, "host", "主机")
	ct.UniqueID = attrs[0].ID
	r.types = []citypemodel.CIType{ct}
	r.aliases[hostID] = "主机"
	for _, a := range attrs {
		r.withAttr(a)
	}
	return r
}

func textAttr(id model.ID, name string) citypemodel.Attribute {
	a := newAttr(id, name)
	a.ValueType = "2"
	return a
}

func intAttr(id model.ID, name string) citypemodel.Attribute {
	a := newAttr(id, name)
	a.ValueType = "0"
	return a
}

// --- unique constraints ---

// The validation cantor ran only on create, now run on both paths.
func TestConstraintShapeValidatedOnCreateAndUpdate(t *testing.T) {
	multi := textAttr(2, "tags")
	multi.IsList = true
	doc := newAttr(3, "meta") // ValueType "2" by default
	doc.ValueType = "6"       // JSON

	for _, tc := range []struct {
		name string
		attr citypemodel.Attribute
	}{
		{"multi-valued", multi},
		{"json", doc},
	} {
		t.Run(tc.name, func(t *testing.T) {
			repo := ruleRepo(10, textAttr(1, "hostname"), tc.attr)
			repo.constraints = []citypemodel.CITypeUniqueConstraint{
				{TypeID: 10, AttrIDs: citypemodel.IDList{1}},
			}
			repo.constraints[0].ID = 100
			uc := attrUsecase(repo)
			ctx := context.Background()

			if _, err := uc.CreateUniqueConstraint(ctx, "10", []model.ID{tc.attr.ID}, 7); !errors.Is(err, ErrConstraintInvalid) {
				t.Fatalf("create error = %v, want ErrConstraintInvalid", err)
			}
			// cantor's UPDATE had no shape check at all, so this was the bypass.
			if _, err := uc.UpdateUniqueConstraint(ctx, "10", 100, []model.ID{tc.attr.ID}, 7); !errors.Is(err, ErrConstraintInvalid) {
				t.Fatalf("update error = %v, want ErrConstraintInvalid — the update path is not a way "+
					"to install what create refuses", err)
			}
		})
	}
}

// cantor dereferenced a missing attribute and answered 500 with a Python type
// error.
func TestConstraintRejectsAnUnknownAttribute(t *testing.T) {
	repo := ruleRepo(10, textAttr(1, "hostname"))
	uc := attrUsecase(repo)

	_, err := uc.CreateUniqueConstraint(context.Background(), "10", []model.ID{9999}, 7)
	if !errors.Is(err, ErrBindingAttrNotFound) {
		t.Fatalf("create error = %v, want the missing attribute reported", err)
	}
	if errors.Is(err, errs.ErrNotWiredYet) || errs.HTTPStatus(err) != 404 {
		t.Fatalf("status = %d, want 404 for an unknown attribute", errs.HTTPStatus(err))
	}
}

func TestConstraintRequiresAttributes(t *testing.T) {
	repo := ruleRepo(10, textAttr(1, "hostname"))
	if _, err := attrUsecase(repo).CreateUniqueConstraint(context.Background(), "10", nil, 7); !errors.Is(err, ErrConstraintAttrRequired) {
		t.Fatalf("error = %v, want ErrConstraintAttrRequired", err)
	}
}

// cantor stored list(set(attr_ids)), so the order depended on hash iteration.
func TestConstraintPreservesTheCallerOrder(t *testing.T) {
	repo := ruleRepo(10, textAttr(1, "a"), textAttr(2, "b"), textAttr(3, "c"))
	if _, err := attrUsecase(repo).CreateUniqueConstraint(context.Background(), "10",
		[]model.ID{3, 1, 2}, 7); err != nil {
		t.Fatalf("CreateUniqueConstraint() error = %v", err)
	}
	got := repo.createdConstraint.AttrIDs
	if len(got) != 3 || got[0] != 3 || got[1] != 1 || got[2] != 2 {
		t.Fatalf("attr_ids = %v, want the caller's order 3,1,2", got)
	}
}

// THE IDOR FIX. cantor's PUT/DELETE took only the row id and used the path
// type solely to resolve the permission, so a caller authorised for one model
// could rewrite another's rule.
func TestRuleScopedToItsModel(t *testing.T) {
	repo := ruleRepo(10, textAttr(1, "a"))
	other := newCIType(20, "other", "其他")
	repo.types = append(repo.types, other)
	repo.aliases[20] = "其他"

	foreign := citypemodel.CITypeUniqueConstraint{TypeID: 20, AttrIDs: citypemodel.IDList{1}}
	foreign.ID = 500
	repo.constraints = []citypemodel.CITypeUniqueConstraint{foreign}
	trigger := citypemodel.CITypeTrigger{TypeID: 20, Notify: map[string]any{TriggerKeyEnable: true}}
	trigger.ID = 600
	repo.triggers = []citypemodel.CITypeTrigger{trigger}

	uc := attrUsecase(repo)
	ctx := context.Background()

	if _, err := uc.UpdateUniqueConstraint(ctx, "10", 500, []model.ID{1}, 7); !errors.Is(err, ErrRuleNotInType) {
		t.Fatalf("update constraint error = %v, want ErrRuleNotInType", err)
	}
	if err := uc.DeleteUniqueConstraint(ctx, "10", 500, 7); !errors.Is(err, ErrRuleNotInType) {
		t.Fatalf("delete constraint error = %v, want ErrRuleNotInType", err)
	}
	if err := uc.DeleteTrigger(ctx, "10", 600, 7); !errors.Is(err, ErrRuleNotInType) {
		t.Fatalf("delete trigger error = %v, want ErrRuleNotInType", err)
	}
	if err := uc.TestTrigger(ctx, "10", 600); !errors.Is(err, ErrRuleNotInType) {
		t.Fatalf("test trigger error = %v, want ErrRuleNotInType", err)
	}
}

// --- triggers ---

func TestTriggerRejectsAMalformedOption(t *testing.T) {
	repo := ruleRepo(10, textAttr(1, "hostname"))
	uc := attrUsecase(repo)
	ctx := context.Background()

	for _, tc := range []struct {
		name   string
		option map[string]any
	}{
		{"empty", map[string]any{}},
		{"bad action", map[string]any{TriggerKeyAction: "99"}},
		{"action not a string", map[string]any{TriggerKeyAction: 1}},
		{"attr_ids a nested list", map[string]any{TriggerKeyAttrIDs: []any{[]any{1}}}},
		{"attr_ids zero", map[string]any{TriggerKeyAttrIDs: []any{0}}},
		{"webhooks not an object", map[string]any{TriggerKeyWebhooks: "http://x"}},
		{"empty webhook url", map[string]any{TriggerKeyWebhooks: map[string]any{"url": "  "}}},
		{"authorization not flat", map[string]any{TriggerKeyWebhooks: map[string]any{
			"url": "https://x", WebhookAuthKey: map[string]any{"a": map[string]any{"b": 1}},
		}}},
		{"notifies.tos not strings", map[string]any{TriggerKeyNotifies: map[string]any{"tos": []any{1}}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := uc.CreateTrigger(ctx, "10", TriggerInput{Option: tc.option, Actor: 7}); !errors.Is(err, ErrTriggerOptionInvalid) && !errors.Is(err, ErrTriggerOptionRequired) {
				t.Fatalf("error = %v, want the malformed option refused", err)
			}
		})
	}
}

func TestTriggerAcceptsAWellFormedOption(t *testing.T) {
	repo := ruleRepo(10, textAttr(1, "hostname"))
	view, err := attrUsecase(repo).CreateTrigger(context.Background(), "10", TriggerInput{
		Option: map[string]any{
			TriggerKeyEnable:  true,
			TriggerKeyAction:  string(model.OperateAdd),
			TriggerKeyName:    "notify ops",
			TriggerKeyAttrIDs: []any{float64(1)},
			TriggerKeyWebhooks: map[string]any{
				"url": "https://example.invalid/hook",
			},
		},
		Actor: 7,
	})
	if err != nil {
		t.Fatalf("CreateTrigger() error = %v", err)
	}
	if view == nil || view.ID == 0 {
		t.Fatalf("view = %+v", view)
	}
}

// THE ENCRYPTION BOUNDARY. The store must never see a plaintext credential, so
// the use case seals before handing the document over.
func TestTriggerSealsAuthorizationBeforeTheStoreSeesIt(t *testing.T) {
	repo := ruleRepo(10, textAttr(1, "hostname"))
	_, err := attrUsecase(repo).CreateTrigger(context.Background(), "10", TriggerInput{
		Option: map[string]any{
			TriggerKeyEnable: true,
			TriggerKeyAction: string(model.OperateAdd),
			TriggerKeyWebhooks: map[string]any{
				"url": "https://example.invalid/hook",
				WebhookAuthKey: map[string]any{
					"Authorization": "Bearer supersecret",
				},
			},
		},
		Actor: 7,
	})
	if err != nil {
		t.Fatalf("CreateTrigger() error = %v", err)
	}

	sealed := TriggerAuthorization(repo.createdTrigger.Option)
	got := sealed["Authorization"]
	if got == "Bearer supersecret" {
		t.Fatal("the store received the plaintext credential")
	}
	if !strings.HasPrefix(got, "v1:") {
		t.Fatalf("stored value = %q, want secretbox ciphertext", got)
	}
	// And it must still be recoverable.
	back, err := secretbox.Decrypt(got)
	if err != nil {
		t.Fatalf("the sealed value does not decrypt: %v", err)
	}
	if back != "Bearer supersecret" {
		t.Fatalf("decrypted = %q, want the original", back)
	}
}

// The read never echoes the credential, whatever the caller's role.
func TestTriggerViewNeverEchoesAuthorization(t *testing.T) {
	repo := ruleRepo(10, textAttr(1, "hostname"))
	if _, err := attrUsecase(repo).CreateTrigger(context.Background(), "10", TriggerInput{
		Option: map[string]any{
			TriggerKeyEnable: true,
			TriggerKeyAction: string(model.OperateAdd),
			TriggerKeyWebhooks: map[string]any{
				"url":          "https://example.invalid/hook",
				WebhookAuthKey: map[string]any{"Authorization": "Bearer supersecret"},
			},
		},
		Actor: 7,
	}); err != nil {
		t.Fatal(err)
	}

	items, err := attrUsecase(repo).ListTriggers(context.Background(), "10")
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 {
		t.Fatalf("triggers = %d, want 1", len(items))
	}
	if !items[0].HasWebhookAuthorization {
		t.Fatal("the presence of a credential must be reported")
	}
	if auth := TriggerAuthorization(items[0].Option); len(auth) != 0 {
		t.Fatalf("the option still carries authorization: %+v", auth)
	}
	// The rest of the document survives redaction.
	if items[0].Option[TriggerKeyAction] != string(model.OperateAdd) {
		t.Fatalf("option = %+v, want the other keys intact", items[0].Option)
	}
}

// A console editing a trigger cannot send back what it was never given, so an
// absent authorization has to mean "keep the stored one".
func TestTriggerUpdateKeepsTheStoredAuthorization(t *testing.T) {
	repo := ruleRepo(10, textAttr(1, "hostname"))
	uc := attrUsecase(repo)
	ctx := context.Background()

	created, err := uc.CreateTrigger(ctx, "10", TriggerInput{
		Option: map[string]any{
			TriggerKeyEnable: true,
			TriggerKeyAction: string(model.OperateAdd),
			TriggerKeyWebhooks: map[string]any{
				"url":          "https://example.invalid/hook",
				WebhookAuthKey: map[string]any{"Authorization": "Bearer supersecret"},
			},
		},
		Actor: 7,
	})
	if err != nil {
		t.Fatal(err)
	}
	before := TriggerAuthorization(repo.createdTrigger.Option)["Authorization"]

	// A PUT that changes only the name, with no authorization at all.
	if _, err := uc.UpdateTrigger(ctx, "10", created.ID, TriggerInput{
		Option: map[string]any{
			TriggerKeyEnable: true,
			TriggerKeyAction: string(model.OperateAdd),
			TriggerKeyName:   "renamed",
			TriggerKeyWebhooks: map[string]any{
				"url": "https://example.invalid/hook",
			},
		},
		Actor: 7,
	}); err != nil {
		t.Fatalf("UpdateTrigger() error = %v", err)
	}

	after := TriggerAuthorization(repo.updatedTrigger.Option)["Authorization"]
	if after == "" {
		t.Fatal("the stored credential was dropped by a request that never mentioned it")
	}
	plain, err := secretbox.Decrypt(after)
	if err != nil {
		t.Fatalf("the kept value does not decrypt: %v", err)
	}
	if plain != "Bearer supersecret" {
		t.Fatalf("kept credential decrypts to %q, want the original — a value that is not the "+
			"original means the stored ciphertext was sealed a second time", plain)
	}
	_ = before
}

// An explicitly empty authorization clears the credential; an absent one keeps
// it. Without the distinction a console editing a trigger would wipe the
// credential every time, because the read never returns it.
func TestTriggerEmptyAuthorizationClearsTheCredential(t *testing.T) {
	repo := ruleRepo(10, textAttr(1, "hostname"))
	uc := attrUsecase(repo)
	ctx := context.Background()

	created, err := uc.CreateTrigger(ctx, "10", TriggerInput{
		Option: map[string]any{
			TriggerKeyEnable: true,
			TriggerKeyAction: string(model.OperateAdd),
			TriggerKeyWebhooks: map[string]any{
				"url":          "https://example.invalid/hook",
				WebhookAuthKey: map[string]any{"Authorization": "Bearer supersecret"},
			},
		},
		Actor: 7,
	})
	if err != nil {
		t.Fatal(err)
	}

	if _, err := uc.UpdateTrigger(ctx, "10", created.ID, TriggerInput{
		Option: map[string]any{
			TriggerKeyEnable: true,
			TriggerKeyAction: string(model.OperateAdd),
			TriggerKeyWebhooks: map[string]any{
				"url":          "https://example.invalid/hook",
				WebhookAuthKey: map[string]any{},
			},
		},
		Actor: 7,
	}); err != nil {
		t.Fatalf("UpdateTrigger() error = %v", err)
	}

	if auth := TriggerAuthorization(repo.updatedTrigger.Option); len(auth) != 0 {
		t.Fatalf("authorization = %+v, want cleared by the explicit empty object", auth)
	}
}

// secretbox draws a fresh nonce per encryption, so the SAME credential seals to
// a different string every time. Duplicate detection therefore cannot compare
// sealed documents — it has to compare plaintext.
func TestTriggerDuplicateDetectedDespiteFreshNonces(t *testing.T) {
	repo := ruleRepo(10, textAttr(1, "hostname"))
	uc := attrUsecase(repo)
	ctx := context.Background()

	option := func() map[string]any {
		return map[string]any{
			TriggerKeyEnable: true,
			TriggerKeyAction: string(model.OperateAdd),
			TriggerKeyWebhooks: map[string]any{
				"url":          "https://example.invalid/hook",
				WebhookAuthKey: map[string]any{"Authorization": "Bearer supersecret"},
			},
		}
	}

	if _, err := uc.CreateTrigger(ctx, "10", TriggerInput{Option: option(), Actor: 7}); err != nil {
		t.Fatal(err)
	}
	first := TriggerAuthorization(repo.triggers[0].Notify)["Authorization"]

	// The second call must be refused, and NOT because the ciphertext matched.
	_, err := uc.CreateTrigger(ctx, "10", TriggerInput{Option: option(), Actor: 7})
	if !errors.Is(err, ErrTriggerDuplicate) {
		t.Fatalf("error = %v, want ErrTriggerDuplicate", err)
	}

	// Non-vacuity: prove the two sealings really do differ, so a ciphertext
	// comparison could not have been what caught this.
	sealed, err := sealTriggerOption(option(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if TriggerAuthorization(sealed)["Authorization"] == first {
		t.Fatal("two sealings produced the same ciphertext; this test would pass with a ciphertext " +
			"comparison and therefore does not prove the plaintext one")
	}
}

// Different attribute scope is a different rule, so it is not a duplicate.
func TestTriggerDuplicateIsScopedByAttribute(t *testing.T) {
	repo := ruleRepo(10, textAttr(1, "hostname"), textAttr(2, "cpu"))
	uc := attrUsecase(repo)
	ctx := context.Background()
	option := map[string]any{TriggerKeyEnable: true, TriggerKeyAction: string(model.OperateAdd)}

	attrA, attrB := model.ID(1), model.ID(2)
	if _, err := uc.CreateTrigger(ctx, "10", TriggerInput{AttrID: &attrA, Option: option, Actor: 7}); err != nil {
		t.Fatal(err)
	}
	if _, err := uc.CreateTrigger(ctx, "10", TriggerInput{AttrID: &attrB, Option: option, Actor: 7}); err != nil {
		t.Fatalf("error = %v, want a different attribute scope to be a different trigger", err)
	}
}

// NOTHING IN THIS BUILD DECRYPTS A TRIGGER CREDENTIAL, and this test pins that
// as a fact rather than leaving it to be assumed.
//
// cantor's read path decrypted them and its Crypto.decrypt returned its input
// on failure, so a value it could not read came back as ciphertext,
// indistinguishable from a credential. The port never takes that path: reads
// redact, and the duplicate check compares the credential-free form. So a row
// whose credential this deployment cannot read — every row on a database
// adopted from cantor, whose ciphertext has no `v1:` prefix — is inert rather
// than misreported.
//
// When the dispatch path lands it WILL have to decrypt, and it must fail closed
// there. That obligation is recorded on the model field rather than carried by
// code that does not exist yet.
func TestAdoptedTriggerCredentialIsInert(t *testing.T) {
	repo := ruleRepo(10, textAttr(1, "hostname"))
	bad := citypemodel.CITypeTrigger{
		TypeID: 10,
		Notify: map[string]any{
			TriggerKeyEnable: true,
			TriggerKeyAction: string(model.OperateAdd),
			TriggerKeyWebhooks: map[string]any{
				"url":          "https://example.invalid/hook",
				WebhookAuthKey: map[string]any{"Authorization": "v1:bm90LXJlYWxseS1jaXBoZXJ0ZXh0"},
			},
		},
	}
	bad.ID = 700
	repo.triggers = []citypemodel.CITypeTrigger{bad}
	uc := attrUsecase(repo)
	ctx := context.Background()

	// A create with a different configuration succeeds: the unreadable row is
	// never consulted for decryption.
	if _, err := uc.CreateTrigger(ctx, "10", TriggerInput{
		Option: map[string]any{TriggerKeyEnable: true, TriggerKeyAction: string(model.OperateDelete)},
		Actor:  7,
	}); err != nil {
		t.Fatalf("CreateTrigger() error = %v, want the unreadable row to be irrelevant", err)
	}

	// And the read reports that a credential exists while returning none of it.
	items, err := uc.ListTriggers(ctx, "10")
	if err != nil {
		t.Fatalf("ListTriggers() error = %v", err)
	}
	for _, item := range items {
		if item.ID == 700 {
			if !item.HasWebhookAuthorization {
				t.Fatal("the presence of the stored credential must still be reported")
			}
			if len(TriggerAuthorization(item.Option)) != 0 {
				t.Fatal("the ciphertext was echoed")
			}
		}
	}
}

// cantor fired the trigger and answered 200. The port has no sender, and a
// route that appears to work while sending nothing is worse than one that says
// so.
func TestTestTriggerReportsDispatchUnsupported(t *testing.T) {
	repo := ruleRepo(10, textAttr(1, "hostname"))
	row := citypemodel.CITypeTrigger{TypeID: 10, Notify: map[string]any{TriggerKeyEnable: true}}
	row.ID = 700
	repo.triggers = []citypemodel.CITypeTrigger{row}

	err := attrUsecase(repo).TestTrigger(context.Background(), "10", 700)
	if !errors.Is(err, errs.ErrNotWiredYet) {
		t.Fatalf("error = %v, want ErrNotWiredYet", err)
	}
	if errs.HTTPStatus(err) != 501 {
		t.Fatalf("status = %d, want 501", errs.HTTPStatus(err))
	}
}

func TestRuleUsecaseWithoutRepoReportsNotWired(t *testing.T) {
	uc := NewUsecase(nil, nil)
	ctx := context.Background()
	if _, err := uc.ListUniqueConstraints(ctx, "1"); !errors.Is(err, errs.ErrNotWiredYet) {
		t.Fatalf("ListUniqueConstraints() error = %v", err)
	}
	if _, err := uc.ListTriggers(ctx, "1"); !errors.Is(err, errs.ErrNotWiredYet) {
		t.Fatalf("ListTriggers() error = %v", err)
	}
}
