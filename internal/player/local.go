// Package player defines local player identities. These are not Microsoft accounts.
package player

import (
	"crypto/md5" // Minecraft's OfflinePlayer UUID convention, not a security primitive.
	"encoding/hex"
	"errors"
	"regexp"
)

var localName = regexp.MustCompile(`^[A-Za-z0-9_]{1,16}$`)

// LocalUUID matches Java UUID.nameUUIDFromBytes("OfflinePlayer:" + name).
// Names are case-sensitive for identity; callers must not silently rename players.
func LocalUUID(name string) (string, error) {
	if !localName.MatchString(name) {
		return "", errors.New("use 1–16 letters, numbers, or underscores for a local player name")
	}
	hash := md5.Sum([]byte("OfflinePlayer:" + name))
	hash[6] = (hash[6] & 0x0f) | 0x30
	hash[8] = (hash[8] & 0x3f) | 0x80
	return hex.EncodeToString(hash[:]), nil
}
