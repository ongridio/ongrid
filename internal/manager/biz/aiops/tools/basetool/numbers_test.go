package basetool

import (
	"context"
	"encoding/json"
	"testing"
)

func TestIntegerLexemeExactConversion(t *testing.T) {
	for input, want := range map[string]string{"1.0": "1", "1e0": "1", "1e2": "100", "1.20e1": "12", "-0.0": "0", "0.01e2": "1", "9007199254740993.0": "9007199254740993", "18446744073709551615.0": "18446744073709551615", "-9223372036854775808.0": "-9223372036854775808", "1.5": "1.5", "1e-3": "1e-3", "1e9999999": "1e9999999"} {
		if got := integerLexeme(json.Number(input)); string(got) != want {
			t.Errorf("%s => %s want %s", input, got, want)
		}
	}
}

type integerDecodingTool struct {
	origin   string
	received string
	ids      []uint64
}

func (t *integerDecodingTool) Info(context.Context) (*ToolInfo, error) {
	return &ToolInfo{Name: "integer_probe", Origin: t.origin, Parameters: json.RawMessage(`{"type":"object","properties":{"ids":{"type":"array","items":{"type":"integer"}}},"required":["ids"]}`)}, nil
}
func (t *integerDecodingTool) InvokableRun(_ context.Context, args string, _ ...InvokeOption) (string, error) {
	t.received = args
	if t.origin != OriginBuiltin {
		return "ok", nil
	}
	var in struct {
		IDs []uint64 `json:"ids"`
	}
	if err := json.Unmarshal([]byte(args), &in); err != nil {
		return "", err
	}
	t.ids = in.IDs
	return "ok", nil
}
func TestValidationNormalizesIntegersBeforeTypedDecode(t *testing.T) {
	inner := &integerDecodingTool{}
	if _, err := WithValidation(inner).InvokableRun(context.Background(), `{"ids":[1.0,1e2,18446744073709551615.0]}`); err != nil {
		t.Fatal(err)
	}
	if len(inner.ids) != 3 || inner.ids[0] != 1 || inner.ids[1] != 100 || inner.ids[2] != ^uint64(0) {
		t.Fatalf("precision lost: %v", inner.ids)
	}
	if _, err := WithValidation(inner).InvokableRun(context.Background(), `{"ids":[1.5]}`); err == nil {
		t.Fatal("fraction accepted as integer")
	}
	external := &integerDecodingTool{origin: OriginMCP}
	const raw = `{"ids":[1.0]}`
	if _, err := WithValidation(external).InvokableRun(context.Background(), raw); err != nil {
		t.Fatal(err)
	}
	if external.received != raw {
		t.Fatal("external arguments changed")
	}
}

func TestDecodeUint64(t *testing.T) {
	for _, raw := range []string{"42", "42.0", "4.2e1", "420e-1"} {
		got, err := DecodeUint64(json.RawMessage(raw))
		if err != nil || got != 42 {
			t.Fatalf("%s: got=%d err=%v", raw, got, err)
		}
	}
	got, err := DecodeUint64(json.RawMessage("18446744073709551615.0"))
	if err != nil || got != ^uint64(0) {
		t.Fatalf("max: got=%d err=%v", got, err)
	}
	for _, raw := range []string{"null", "\"42\"", "true", "[]", "{}", "-1", "1.5", "18446744073709551616", "1e10000", "1 2"} {
		if _, err := DecodeUint64(json.RawMessage(raw)); err == nil {
			t.Fatalf("accepted %s", raw)
		}
	}
}
