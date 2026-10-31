package topology

import (
	"encoding/json"
	"testing"
)

// Cantor's console wrote path values as strings. An adopted row must still
// decode, or one such view makes every topology read fail.
func TestPathAcceptsStringAndNumericIDs(t *testing.T) {
	var path Path
	if err := json.Unmarshal([]byte(`{"1":["2",3],"-1":["4"]}`), &path); err != nil {
		t.Fatalf("decode path: %v", err)
	}
	if got := path["1"]; len(got) != 2 || got[0] != 2 || got[1] != 3 {
		t.Fatalf("level 1 = %v", got)
	}
	if got := path["-1"]; len(got) != 1 || got[0] != 4 {
		t.Fatalf("level -1 = %v", got)
	}
}
