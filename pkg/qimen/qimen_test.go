package qimen

import (
	"encoding/xml"
	"strings"
	"testing"
	"time"

	"github.com/6tail/lunar-go/calendar"
	"github.com/deminzhang/qimen-go/xuan"
)

func TestRenderIncludesFormattedTimeChart(t *testing.T) {
	location, err := time.LoadLocation("Asia/Shanghai")
	if err != nil {
		t.Fatal(err)
	}
	result := Render(time.Date(2024, time.February, 10, 12, 0, 0, 0, location))
	for _, expected := range []string{
		"[奇门遁甲 snapshot begin]",
		"公历: 2024年2月10日 12:0",
		"局:",
		"值符:",
		"<九宫>",
		"<宫 洛书数=\"1\">",
		"<标记>",
		"[奇门遁甲 snapshot end]",
	} {
		if !strings.Contains(result, expected) {
			t.Fatalf("rendered chart does not contain %q:\n%s", expected, result)
		}
	}
}

func TestRenderNinePalacesXMLContainsNinePalaces(t *testing.T) {
	location, err := time.LoadLocation("Asia/Shanghai")
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2024, time.February, 10, 12, 0, 0, 0, location)
	solar := calendar.NewSolar(now.Year(), int(now.Month()), now.Day(), now.Hour(), now.Minute(), now.Second())
	game := xuan.NewQMGame(solar, xuan.QMParams{Type: xuan.QMTypeRotating, HostingType: xuan.QMHostingType2, FlyType: xuan.QMFlyTypeAllOrder, JuType: xuan.QMJuTypeSplit, HideGanType: xuan.QMHideGanDutyDoorHour, YMDH: xuan.QMGameHour})
	game.ShowTimeGame()

	result := renderNinePalacesXML(game.ShowPan)
	var decoded ninePalacesXML
	if err := xml.Unmarshal([]byte(result), &decoded); err != nil {
		t.Fatalf("rendered XML is invalid: %v\n%s", err, result)
	}
	if len(decoded.Palaces) != 9 {
		t.Fatalf("palace count = %d, want 9", len(decoded.Palaces))
	}
	for index, palace := range decoded.Palaces {
		if palace.Number != index+1 {
			t.Fatalf("palace %d has 洛书数 %d", index+1, palace.Number)
		}
	}
}

func TestPalaceMarkersIncludeSupportedMarkers(t *testing.T) {
	pan := &xuan.QMPan{
		DutyStarPos: 1,
		DutyDoorPos: 1,
		Horse:       "子",
		KongWang:    "子",
		RollHosting: 1,
		Xun:         "甲子",
	}
	pan.Gongs[1] = xuan.QMPalace{Idx: 1, God: "值符", GuestGan: "戊", HostGan: "戊", Door: "开"}
	markers := strings.Join(palaceMarkers(pan, &pan.Gongs[1]), "；")
	for _, expected := range []string{"值符", "值使", "马星", "空亡", "禽"} {
		if !strings.Contains(markers, expected) {
			t.Fatalf("markers %q do not contain %q", markers, expected)
		}
	}
}

func TestChartPatternsFuyin(t *testing.T) {
	pan := &xuan.QMPan{DutyStar: "蓬", DutyDoor: "休", DutyStarPos: 1, DutyDoorPos: 1}
	patterns := strings.Join(chartPatterns(pan), "、")
	if !strings.Contains(patterns, "全局伏吟") {
		t.Fatalf("patterns = %q, want 全局伏吟", patterns)
	}
}

func TestChartPatternsFanyin(t *testing.T) {
	pan := &xuan.QMPan{DutyStar: "蓬", DutyDoor: "休", DutyStarPos: 9, DutyDoorPos: 9}
	patterns := strings.Join(chartPatterns(pan), "、")
	if !strings.Contains(patterns, "全局反吟") {
		t.Fatalf("patterns = %q, want 全局反吟", patterns)
	}
}

func TestChartPatternsMixedStarFanDoorFu(t *testing.T) {
	pan := &xuan.QMPan{DutyStar: "蓬", DutyDoor: "休", DutyStarPos: 9, DutyDoorPos: 1}
	patterns := strings.Join(chartPatterns(pan), "、")
	for _, expected := range []string{"九星反吟", "八门伏吟"} {
		if !strings.Contains(patterns, expected) {
			t.Fatalf("patterns %q do not contain %q", patterns, expected)
		}
	}
}

func TestChartPatternsQinHostedToKun(t *testing.T) {
	pan := &xuan.QMPan{DutyStar: "禽", DutyDoor: "杜", DutyStarPos: 2, DutyDoorPos: 2, RollHosting: 2}
	patterns := strings.Join(chartPatterns(pan), "、")
	if !strings.Contains(patterns, "九星伏吟") {
		t.Fatalf("patterns = %q, want 九星伏吟", patterns)
	}
	pan.DutyStarPos = 8
	patterns = strings.Join(chartPatterns(pan), "、")
	if !strings.Contains(patterns, "九星反吟") {
		t.Fatalf("patterns = %q, want 九星反吟", patterns)
	}
}

func TestPalaceMarkersIncludeFuyinFanyin(t *testing.T) {
	pan := &xuan.QMPan{}
	pan.Gongs[1] = xuan.QMPalace{Idx: 1, GuestGan: "戊", HostGan: "戊"}
	if markers := strings.Join(palaceMarkers(pan, &pan.Gongs[1]), "；"); !strings.Contains(markers, "天盘伏吟") {
		t.Fatalf("markers %q do not contain 天盘伏吟", markers)
	}
	pan.Gongs[1] = xuan.QMPalace{Idx: 1, GuestGan: "甲", HostGan: "庚"}
	if markers := strings.Join(palaceMarkers(pan, &pan.Gongs[1]), "；"); !strings.Contains(markers, "天盘反吟") {
		t.Fatalf("markers %q do not contain 天盘反吟", markers)
	}
	// 休门本宫在坎一(1)，落离九(9) 为门反吟
	pan.Gongs[9] = xuan.QMPalace{Idx: 9, Door: "休"}
	if markers := strings.Join(palaceMarkers(pan, &pan.Gongs[9]), "；"); !strings.Contains(markers, "门反吟") {
		t.Fatalf("markers %q do not contain 门反吟", markers)
	}
}
