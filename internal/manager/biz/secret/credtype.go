package secret

// credtype.go — the reusable credential-TYPE layer (HLD-017, n8n's
// ICredentialType analog). A type declares (a) which fields a credential of
// that type holds and (b) how those fields inject into a skill/MCP exec
// environment. The injection rule lives on the TYPE so it's defined once and
// reused — crucially, this lets an UNDECLARED skills.sh skill still receive
// credentials: the operator attaches a typed credential at install and the
// type's inject rule applies, even though the skill itself declares nothing.
//
// Built-ins are registered here; the set is extensible (a pack could ship
// more later). The special "custom" type has no fixed fields or inject rule —
// each of its fields becomes an env var of the same name (the escape hatch
// for "I just need to set some env vars").

// CredField is one field a credential type expects.
type CredField struct {
	Key    string `json:"key"`
	Label  string `json:"label"`
	Secret bool   `json:"secret"` // mask in UI (passwords/keys); false for region etc.
}

// CredType is a reusable credential type.
type CredType struct {
	Name string `json:"name"` // machine name (Secret.Type references this)
	// Label is the display name.
	Label string `json:"label"`
	// Fields are the expected fields (form schema for the create dialog).
	Fields []CredField `json:"fields"`
	// InjectEnv maps ENV_VAR -> "{{field}}" template. Empty for "custom"
	// (each field injects as an env var of the same name).
	InjectEnv map[string]string `json:"inject_env,omitempty"`
	// Builtin marks types shipped by ongrid (vs pack-supplied later).
	Builtin bool `json:"builtin"`
}

// IsCustom reports the field-name-as-env-var escape-hatch type.
func (t *CredType) IsCustom() bool { return t.Name == CredTypeCustom }

// CredTypeCustom is the untyped escape hatch.
const CredTypeCustom = "custom"

var credTypes = map[string]*CredType{}

func registerCredType(t *CredType) { credTypes[t.Name] = t }

// LookupCredType returns the type or nil. An unknown/empty name resolves to
// the "custom" type so an untyped credential still injects (field->env).
func LookupCredType(name string) *CredType {
	if t, ok := credTypes[name]; ok {
		return t
	}
	return credTypes[CredTypeCustom]
}

// AllCredTypes returns every registered type (for the create-credential UI).
func AllCredTypes() []*CredType {
	out := make([]*CredType, 0, len(credTypes))
	for _, t := range credTypes {
		out = append(out, t)
	}
	return out
}

func init() {
	registerCredType(&CredType{
		Name: CredTypeCustom, Label: "自定义 (Custom)", Builtin: true,
		// Fields + inject are user-defined; injection = each field as a
		// same-named env var (handled in ResolveInjection).
	})
	registerCredType(&CredType{
		Name: "tencentcloud", Label: "腾讯云 (Tencent Cloud)", Builtin: true,
		Fields: []CredField{
			{Key: "secret_id", Label: "SecretId", Secret: true},
			{Key: "secret_key", Label: "SecretKey", Secret: true},
			{Key: "region", Label: "Region (可选)"},
		},
		InjectEnv: map[string]string{
			"TENCENTCLOUD_SECRET_ID":  "{{secret_id}}",
			"TENCENTCLOUD_SECRET_KEY": "{{secret_key}}",
			"TENCENTCLOUD_REGION":     "{{region}}",
		},
	})
	registerCredType(&CredType{
		Name: "aws", Label: "AWS", Builtin: true,
		Fields: []CredField{
			{Key: "access_key_id", Label: "Access Key ID", Secret: true},
			{Key: "secret_access_key", Label: "Secret Access Key", Secret: true},
			{Key: "region", Label: "Region (可选)"},
		},
		InjectEnv: map[string]string{
			"AWS_ACCESS_KEY_ID":     "{{access_key_id}}",
			"AWS_SECRET_ACCESS_KEY": "{{secret_access_key}}",
			"AWS_DEFAULT_REGION":    "{{region}}",
		},
	})
	registerCredType(&CredType{
		Name: "alicloud", Label: "阿里云 (Alibaba Cloud)", Builtin: true,
		Fields: []CredField{
			{Key: "access_key_id", Label: "AccessKey ID", Secret: true},
			{Key: "access_key_secret", Label: "AccessKey Secret", Secret: true},
			{Key: "region", Label: "Region (可选)"},
		},
		InjectEnv: map[string]string{
			"ALICLOUD_ACCESS_KEY": "{{access_key_id}}",
			"ALICLOUD_SECRET_KEY": "{{access_key_secret}}",
			"ALICLOUD_REGION":     "{{region}}",
		},
	})
	registerCredType(&CredType{
		Name: "github", Label: "GitHub", Builtin: true,
		Fields:    []CredField{{Key: "token", Label: "Personal Access Token", Secret: true}},
		InjectEnv: map[string]string{"GITHUB_TOKEN": "{{token}}"},
	})
}
