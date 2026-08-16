// Package qimen renders hour-based Qimen Dunjia charts for prompt context.
package qimen

import (
	"encoding/xml"
	"fmt"
	"strings"
	"time"

	"github.com/6tail/lunar-go/calendar"
	"github.com/deminzhang/qimen-go/xuan"
)

// oppositePalace maps a palace to its directly opposing palace (对冲宫).
var oppositePalace = map[int]int{
	1: 9, 2: 8, 3: 7, 4: 6, 6: 4, 7: 3, 8: 2, 9: 1,
}

// Render returns a formatted time-based rotating Qimen Dunjia chart for t.
func Render(t time.Time) string {
	solar := calendar.NewSolar(t.Year(), int(t.Month()), t.Day(), t.Hour(), t.Minute(), t.Second())
	game := xuan.NewQMGame(solar, xuan.QMParams{
		Type:        xuan.QMTypeRotating,
		HostingType: xuan.QMHostingType2,
		FlyType:     xuan.QMFlyTypeAllOrder,
		JuType:      xuan.QMJuTypeSplit,
		HideGanType: xuan.QMHideGanDutyDoorHour,
		YMDH:        xuan.QMGameHour,
	})
	game.ShowTimeGame()
	pan := game.ShowPan

	var output strings.Builder
	output.WriteString("[奇门遁甲 snapshot begin]\n")
	output.WriteString(xuan.RenderQiMenHead(pan, game))
	if patterns := chartPatterns(pan); len(patterns) > 0 {
		output.WriteString(fmt.Sprintf("格局: %s\n", strings.Join(patterns, "、")))
	}
	output.WriteString(pillarPalaces(pan, game))
	output.WriteString("\n")
	output.WriteString(renderNinePalacesXML(pan))
	output.WriteString("[奇门遁甲 snapshot end]")
	return output.String()
}

type ninePalacesXML struct {
	XMLName xml.Name    `xml:"九宫"`
	Palaces []palaceXML `xml:"宫"`
}

type palaceXML struct {
	Number    int    `xml:"洛书数,attr"`
	Diagram   string `xml:"八卦"`
	HostGan   string `xml:"地盘"`
	GuestGan  string `xml:"天盘"`
	Star      string `xml:"九星"`
	Door      string `xml:"八门,omitempty"`
	Markers   string `xml:"标记,omitempty"`
	God       string `xml:"八神,omitempty"`
	HiddenGan string `xml:"暗干"`
}

func renderNinePalacesXML(pan *xuan.QMPan) string {
	palaces := make([]palaceXML, 0, 9)
	for index := 1; index <= 9; index++ {
		palace := pan.Gongs[index]
		palaces = append(palaces, palaceXML{
			Number:    palace.Idx,
			Diagram:   xuan.Diagrams9(index),
			HostGan:   palace.HostGan,
			GuestGan:  palace.GuestGan,
			Star:      palace.Star,
			Door:      palace.Door,
			God:       palace.God,
			HiddenGan: palace.HideGan,
			Markers:   strings.Join(palaceMarkers(pan, &palace), "；"),
		})
	}
	encoded, err := xml.MarshalIndent(ninePalacesXML{Palaces: palaces}, "", "  ")
	if err != nil {
		panic(fmt.Sprintf("render Qimen nine palaces: %v", err))
	}
	return string(encoded) + "\n"
}

func palaceMarkers(pan *xuan.QMPan, palace *xuan.QMPalace) []string {
	var markers []string
	if palace.Idx == pan.DutyStarPos {
		markers = append(markers, "值符")
	}
	if palace.Idx == pan.DutyDoorPos {
		markers = append(markers, "值使")
	}
	if palace.Idx == xuan.ZhiGong9[pan.Horse] {
		markers = append(markers, "马星")
	}
	for _, branch := range pan.KongWang {
		if palace.Idx == xuan.ZhiGong9[string(branch)] {
			markers = append(markers, "空亡")
			break
		}
	}
	if pan.RollHosting > 0 && palace.Idx == pan.RollHosting {
		markers = append(markers, "禽")
	}
	// 伏吟/反吟（天盘伏吟/天盘反吟/门伏吟/门反吟）是盘面级格局，
	// 由 chartPatterns 在 "格局:" 行统一输出，逐宫不重复标记。

	guestTomb := palace.GuestGan != "" && palace.Idx == xuan.ZhiGong9[xuan.QMTomb[palace.GuestGan]]
	hostTomb := palace.HostGan != "" && palace.Idx == xuan.ZhiGong9[xuan.QMTomb[palace.HostGan]]
	penalty := palace.God == "值符" && palace.Idx == xuan.ZhiGong9[xuan.QM6YiJiXing[pan.Xun]]
	markers = append(markers, ganMarkers("天盘", guestTomb, penalty)...)
	markers = append(markers, ganMarkers("地盘", hostTomb, penalty)...)

	diagram := xuan.Diagrams9(palace.Idx)
	if palace.Door != "" && xuan.WuXingKe[xuan.DoorWuxing[palace.Door]] == xuan.DiagramsWuxing[diagram] {
		markers = append(markers, fmt.Sprintf("%s门迫%s", palace.Door, diagram))
	}
	return markers
}

func ganMarkers(plane string, tomb, penalty bool) []string {
	if penalty && tomb {
		return []string{plane + "刑墓"}
	}
	if penalty {
		return []string{plane + "刑"}
	}
	if tomb {
		return []string{plane + "入墓"}
	}
	return nil
}

// starHome returns the effective home palace of the duty star. The middle
// palace (禽) is hosted to the rotating host palace, matching upstream
// rendering where DutyStarPos is normalized to 2 or 8.
func starHome(pan *xuan.QMPan) int {
	home := xuan.StarHome[pan.DutyStar]
	if home == 5 {
		if pan.RollHosting > 0 {
			return pan.RollHosting
		}
		return 2
	}
	return home
}

// chartPatterns returns the chart-level 伏吟/反吟 patterns: the 九星, 八门,
// and 全局 (both) forms for each of 伏吟 and 反吟.
func chartPatterns(pan *xuan.QMPan) []string {
	starHomePos := starHome(pan)
	doorHomePos := xuan.DoorHome[pan.DutyDoor]

	starFu := starHomePos != 0 && pan.DutyStarPos == starHomePos
	doorFu := doorHomePos != 0 && pan.DutyDoorPos == doorHomePos
	starFan := starHomePos != 0 && pan.DutyStarPos == oppositePalace[starHomePos]
	doorFan := doorHomePos != 0 && pan.DutyDoorPos == oppositePalace[doorHomePos]

	var patterns []string
	switch {
	case starFu && doorFu:
		patterns = append(patterns, "全局伏吟")
	case starFu:
		patterns = append(patterns, "九星伏吟")
	case doorFu:
		patterns = append(patterns, "八门伏吟")
	}
	switch {
	case starFan && doorFan:
		patterns = append(patterns, "全局反吟")
	case starFan:
		patterns = append(patterns, "九星反吟")
	case doorFan:
		patterns = append(patterns, "八门反吟")
	}
	return patterns
}

// pillarPalaces returns the 年月日时 four pillars' 用神落宫 for the heaven,
// earth, and hidden stems. A 甲 stem is resolved to the 六仪 it 遁于 via
// xuan.HideJia (甲子戊、甲戌己、甲申庚、甲午辛、甲辰壬、甲寅癸) before searching;
// the 中宫 (5) is hosted to 坤二 (2) per QMHostingType2, matching the duty-star
// hosting used elsewhere in this package.
func pillarPalaces(pan *xuan.QMPan, game *xuan.QMGame) string {
	c8 := game.Lunar.GetEightChar()
	pillars := []struct {
		name string
		gan  string
		zhi  string
	}{
		{"年", c8.GetYearGan(), c8.GetYearZhi()},
		{"月", c8.GetMonthGan(), c8.GetMonthZhi()},
		{"日", c8.GetDayGan(), c8.GetDayZhi()},
		{"时", c8.GetTimeGan(), c8.GetTimeZhi()},
	}
	planes := []struct {
		name  string
		field string
	}{
		{"天盘", "天盘"},
		{"地盘", "地盘"},
		{"暗干", "暗干"},
	}
	var output strings.Builder
	for _, plane := range planes {
		var parts []string
		for _, p := range pillars {
			gan := p.gan
			note := ""
			if gan == "甲" {
				gan = xuan.HideJia[p.gan+p.zhi]
				note = "遁" + gan
			}
			palace := ganPalaceInPlane(pan, gan, plane.field)
			if palace == 0 {
				parts = append(parts, fmt.Sprintf("%s干%s%s(未现%s)", p.name, p.gan, note, plane.name))
				continue
			}
			part := fmt.Sprintf("%s干%s%s→%s%d宫", p.name, p.gan, note, xuan.Diagrams9(palace), palace)
			if markers := useGodMarkers(pan, palace, plane.field); len(markers) > 0 {
				part += fmt.Sprintf("（%s）", strings.Join(markers, "；"))
			}
			parts = append(parts, part)
		}
		fmt.Fprintf(&output, "取用神·%s: %s\n", plane.name, strings.Join(parts, "  "))
	}
	return output.String()
}

// ganPalace returns the palace whose heaven plate (天盘奇仪) holds gan. A
// result in the middle palace is hosted to 坤二 (2), per QMHostingType2.
func ganPalace(pan *xuan.QMPan, gan string) int {
	return ganPalaceInPlane(pan, gan, "天盘")
}

func ganPalaceInPlane(pan *xuan.QMPan, gan, plane string) int {
	for idx := 1; idx <= 9; idx++ {
		palace := pan.Gongs[idx]
		var palaceGan string
		switch plane {
		case "天盘":
			palaceGan = palace.GuestGan
		case "地盘":
			palaceGan = palace.HostGan
		case "暗干":
			palaceGan = palace.HideGan
		}
		if palaceGan == gan {
			if idx == 5 {
				return 2
			}
			return idx
		}
	}
	return 0
}

func useGodMarkers(pan *xuan.QMPan, palace int, plane string) []string {
	markers := palaceMarkers(pan, &pan.Gongs[palace])
	otherPlane := ""
	switch plane {
	case "天盘":
		otherPlane = "地盘"
	case "地盘":
		otherPlane = "天盘"
	}
	filtered := markers[:0]
	for _, marker := range markers {
		if !strings.HasPrefix(marker, otherPlane) {
			filtered = append(filtered, marker)
		}
	}
	return filtered
}
