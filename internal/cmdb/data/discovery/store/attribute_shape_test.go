package store

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	discoverybiz "github.com/ongridio/ongrid/internal/cmdb/biz/discovery"
)

// The shape of c_ad_rules.attributes.
//
// IT IS NOT A COSMETIC CHOICE, and the direction that broke was measurable:
// cantor's only writer of this column was `parse_plugin_script`, which returned a
// JSON ARRAY, and its only reader was `[i.get("name") for i in adr.attributes]`,
// which raises on a mapping because a string has no `.get`. A port that declared
// the column as a `map[string]any` therefore
//
//   - FAILED TO READ an adopted database at all — measured, `json: cannot
//     unmarshal array into Go value of type map[string]interface {}` on the rule
//     LIST endpoint, so one plugin rule made the whole page unavailable; and
//   - WROTE a shape cantor cannot consume, which is only felt on a rollback and
//     would not be felt until then.
//
// BOTH DIRECTIONS ARE PINNED HERE, on a row inserted as raw JSON rather than
// through the port — a fixture written by the port would test the port against
// itself.

func TestReadsACantorWrittenFieldList(t *testing.T) {
	repo := newRepo(t)
	raw := `[{"name":"asset_id","type":"string","desc":""},{"name":"rack","type":"string","desc":"机柜"}]`
	if err := repo.db.Exec(
		`INSERT INTO c_ad_rules (name, type, is_inner, is_plugin, attributes, plugin_script, unique_key)
		 VALUES ('自研采集', 'agent', 0, 1, ?, 'x', 'asset_id')`, raw).Error; err != nil {
		t.Fatalf("seed: %v", err)
	}

	rows, err := repo.ListRules(context.Background())
	if err != nil {
		t.Fatalf("a cantor-written plugin rule did not read: %v", err)
	}
	if len(rows) != 1 || len(rows[0].Attributes) != 2 {
		t.Fatalf("read %+v", rows)
	}
	// THE ORDER IS THE OPERATOR'S, and a reader that keyed the list by field name
	// would have lost it.
	if rows[0].Attributes[0]["name"] != "asset_id" || rows[0].Attributes[1]["name"] != "rack" {
		t.Fatalf("the stored order was not kept: %v", rows[0].Attributes)
	}
	if rows[0].Attributes[1]["desc"] != "机柜" {
		t.Fatalf("an entry lost a field: %v", rows[0].Attributes[1])
	}
}

// THE WRITE GOES THROUGH THE USE CASE, not straight to the store: the
// conversion from the wire's field list to the column's document lives there, and
// a test that called the store directly would prove the marshaller and nothing
// about the shape the port actually writes. That mistake was made here first,
// and two mutations — dropping an entry, reordering the list — passed against it.
func TestWritesAnArrayCantorsReaderCanConsume(t *testing.T) {
	repo := newRepo(t)
	ctx := context.Background()
	script := "class AutoDiscovery:\n    pass\n"
	uc := discoverybiz.NewUsecase(repo)
	if _, err := uc.CreateRule(ctx, discoverybiz.RuleInput{
		Name: "自研采集", Type: strp("agent"), IsPlugin: boolp(true),
		PluginScript: &script, UniqueKey: strp("asset_id"),
		Attributes: []discoverybiz.AttributeField{
			{Name: "asset_id", Type: "string"},
			{Name: "rack", Type: "string", Desc: "机柜"},
		},
	}, discoverybiz.Actor{UserID: 1, IsAdmin: true}); err != nil {
		t.Fatalf("create: %v", err)
	}

	var stored string
	if err := repo.db.Raw(`SELECT attributes FROM c_ad_rules WHERE name = ?`, "自研采集").
		Scan(&stored).Error; err != nil {
		t.Fatalf("read back: %v", err)
	}
	if !strings.HasPrefix(strings.TrimSpace(stored), "[") {
		t.Fatalf("the column holds %q, which is not the array cantor's reader expects", stored)
	}
	// AND THE CONTENT ROUND-TRIPS IN ORDER. The leading bracket is what cantor's
	// reader needs; the order is what the operator wrote, and a writer that
	// dropped an entry or sorted by field name would satisfy the first and lose
	// the second.
	var back []map[string]any
	if err := json.Unmarshal([]byte(stored), &back); err != nil {
		t.Fatalf("the written column is not JSON: %v", err)
	}
	if len(back) != 2 || back[0]["name"] != "asset_id" || back[1]["name"] != "rack" {
		t.Fatalf("the written list = %v", back)
	}
	if back[1]["desc"] != "机柜" {
		t.Fatalf("an entry lost a field on the way out: %v", back[1])
	}
}

// A MAPPING IS STILL READ, because an earlier revision of this port wrote one.
// Accepted on the way in, never written back.
func TestReadsTheLegacyMappingShape(t *testing.T) {
	repo := newRepo(t)
	raw := `{"asset_id":{"name":"asset_id","type":"string"},"rack":"bare"}`
	if err := repo.db.Exec(
		`INSERT INTO c_ad_rules (name, type, is_inner, is_plugin, attributes)
		 VALUES ('legacy', 'agent', 0, 1, ?)`, raw).Error; err != nil {
		t.Fatalf("seed: %v", err)
	}
	rows, err := repo.ListRules(context.Background())
	if err != nil {
		t.Fatalf("a legacy mapping did not read: %v", err)
	}
	if len(rows) != 1 || len(rows[0].Attributes) != 2 {
		t.Fatalf("read %+v", rows)
	}
	// SORTED KEYS make the legacy conversion deterministic, and an entry whose
	// value was not an object still names its field.
	if rows[0].Attributes[0]["name"] != "asset_id" || rows[0].Attributes[1]["name"] != "rack" {
		t.Fatalf("legacy conversion = %v", rows[0].Attributes)
	}
}

// A RULE WITH NO FIELD LIST READS AS NO FIELD LIST rather than as an error —
// every built-in rule is written that way, and one of them failing would take
// the whole list with it.
func TestReadsARuleWithNoFieldList(t *testing.T) {
	repo := newRepo(t)
	if err := repo.db.Exec(
		`INSERT INTO c_ad_rules (name, type, is_inner, is_plugin, attributes) VALUES ('阿里云', 'http', 1, 0, NULL)`).
		Error; err != nil {
		t.Fatalf("seed: %v", err)
	}
	rows, err := repo.ListRules(context.Background())
	if err != nil {
		t.Fatalf("a built-in rule did not read: %v", err)
	}
	if len(rows) != 1 || len(rows[0].Attributes) != 0 {
		t.Fatalf("read %+v", rows)
	}
}
