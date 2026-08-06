package compress

import "testing"

func TestEstimateLength(t *testing.T) {
	cases := []struct {
		text string
		want int
	}{
		{"", 0},
		{"ab", 1},   // 2 runes / 2.0 divisor
		{"abcd", 2}, // 4 runes / 2.0 divisor
		{"你好", 1},   // 2 CJK runes / 2.0 divisor
	}
	for _, c := range cases {
		if got := EstimateLength(c.text); got != c.want {
			t.Errorf("EstimateLength(%q) = %d, want %d", c.text, got, c.want)
		}
	}
}
