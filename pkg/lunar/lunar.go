// Package lunar converts Gregorian times into Chinese lunar-calendar context.
package lunar

import (
	"fmt"
	"time"

	"github.com/6tail/lunar-go/calendar"
)

// Date contains the lunar date and the four pillars for one instant.
type Date struct {
	Date  string
	Year  string
	Month string
	Day   string
	Hour  string
}

// FromTime converts t using its location and returns Chinese display values.
func FromTime(t time.Time) Date {
	lunar := calendar.NewSolarFromDate(t).GetLunar()
	pillars := lunar.GetBaZi()
	return Date{
		Date:  fmt.Sprintf("%s年%s月%s", lunar.GetYearInChinese(), lunar.GetMonthInChinese(), lunar.GetDayInChinese()),
		Year:  pillars[0],
		Month: pillars[1],
		Day:   pillars[2],
		Hour:  pillars[3],
	}
}
