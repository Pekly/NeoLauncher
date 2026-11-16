package player

import (
	"strings"
	"testing"
)

func TestLocalUUIDMatchesMinecraft(t *testing.T) {
	for name, want := range map[string]string{"Notch": "b50ad385829d3141a2167e7d7539ba7f", "Steve": "5627dd98e6be3c21b8a8e92344183641"} {
		got, err := LocalUUID(name)
		if err != nil || got != want {
			t.Fatalf("%s: %s %v", name, got, err)
		}
	}
	a, _ := LocalUUID("Steve")
	b, _ := LocalUUID("steve")
	if a == b {
		t.Fatal("changed case-sensitive Minecraft UUID convention")
	}
	for _, name := range []string{"", "../a", "-option", "two words", "player\n", "x\x00", "é", strings.Repeat("a", 17)} {
		if _, err := LocalUUID(name); err == nil {
			t.Fatal("invalid local name accepted", name)
		}
	}
	for _, name := range []string{"a", "_", strings.Repeat("a", 16)} {
		if _, err := LocalUUID(name); err != nil {
			t.Fatal(err)
		}
	}
}
