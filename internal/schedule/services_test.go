package schedule

import (
	"slices"
	"strings"
	"testing"
)

func TestServiceCodes(t *testing.T) {
	for _, input := range [][]string{nil, {}, {"chat-assistant", "a_b", strings.Repeat("a", 64)}} {
		got, err := ServiceCodes(input)
		if err != nil || got == nil || !slices.IsSorted(got) {
			t.Fatal(got, err)
		}
	}
	for _, input := range [][]string{{"a", "a"}, {""}, {"Upper"}, {"1first"}, {"with space"}, {"a.b"}, {strings.Repeat("a", 65)}} {
		if _, err := ServiceCodes(input); err == nil {
			t.Fatal("invalid service codes accepted", input)
		}
	}
}
