package textvariant

import (
	"reflect"
	"strings"
	"testing"
)

func TestVariants(t *testing.T) {
	cases := map[string][]string{
		"简体中文":  {"简体中文", "簡體中文"},
		"簡體中文":  {"簡體中文", "简体中文"},
		"hello": {"hello"},
		"中文":    {"中文"},
		"搜索记录":  {"搜索记录", "搜索記錄", "搜索記彔", "蒐索記錄", "蒐索記彔"},
		"头发":    {"头发", "頭發", "頭髮"},
	}
	for input, want := range cases {
		if got := Variants(input); !reflect.DeepEqual(got, want) {
			t.Errorf("Variants(%q) = %q, want %q", input, got, want)
		}
	}
}

func TestVariantsKeepLateCharacterAlternatesWhenCapped(t *testing.T) {
	// Far more combinations than maxVariants: the last character's
	// alternate spellings must still appear.
	variants := Variants("发发发发发发发发发发")
	var hasLastHair bool
	for _, v := range variants {
		if strings.HasSuffix(v, "髮") && strings.HasPrefix(v, "發發發發發發發發發") {
			hasLastHair = true
		}
	}
	if len(variants) > maxVariants || !hasLastHair {
		t.Fatalf("variants = %v", variants)
	}
}
