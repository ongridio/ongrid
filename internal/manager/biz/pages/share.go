package pages

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"strconv"
	"strings"
	"time"
)

// ShareTTL bounds how long a minted page share link stays valid — matches
// the report share TTL so the two share models behave the same.
const ShareTTL = 30 * 24 * time.Hour

// MintShareToken returns a stateless signed token that grants
// unauthenticated read of pageID until exp. Pages are file-based (no DB row to
// hang a token on like reports), so the token carries its own claims:
// base64url(pageID|expUnix).hmac. Deleting the page still revokes it (the serve
// path checks the file exists); short TTL bounds exposure otherwise.
func MintShareToken(secret, pageID string, exp time.Time) string {
	body := pageID + "|" + strconv.FormatInt(exp.Unix(), 10)
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(body))
	sig := base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
	return base64.RawURLEncoding.EncodeToString([]byte(body)) + "." + sig
}

// VerifyShareToken validates a share token's signature + expiry and returns
// the page id it grants.
func VerifyShareToken(secret, token string) (string, bool) {
	parts := strings.SplitN(token, ".", 2)
	if len(parts) != 2 {
		return "", false
	}
	bodyB, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		return "", false
	}
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(bodyB)
	want := base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
	if !hmac.Equal([]byte(want), []byte(parts[1])) {
		return "", false
	}
	seg := strings.SplitN(string(bodyB), "|", 2)
	if len(seg) != 2 || !ValidID(seg[0]) {
		return "", false
	}
	expUnix, err := strconv.ParseInt(seg[1], 10, 64)
	if err != nil || time.Now().Unix() > expUnix {
		return "", false
	}
	return seg[0], true
}

// ValidID guards the /pages/{id} route against path traversal — id must be
// a bare lowercase-hex token.
func ValidID(s string) bool {
	if len(s) < 16 || len(s) > 64 {
		return false
	}
	for _, c := range s {
		if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f')) {
			return false
		}
	}
	return true
}
