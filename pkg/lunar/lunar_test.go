package lunar

import (
	"testing"
	"time"
)

func TestFromTime(t *testing.T) {
	timezone, err := time.LoadLocation("Asia/Shanghai")
	if err != nil {
		t.Fatal(err)
	}

	got := FromTime(time.Date(2024, time.February, 10, 12, 0, 0, 0, timezone))
	if got.Date != "二〇二四年正月初一" {
		t.Fatalf("Date = %q, want 二〇二四年正月初一", got.Date)
	}
	if got.Year != "甲辰" || got.Month != "丙寅" || got.Day != "甲辰" || got.Hour != "庚午" {
		t.Fatalf("unexpected four pillars: %+v", got)
	}
}
