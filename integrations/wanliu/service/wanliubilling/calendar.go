package unihub

import "time"

// Weekends are always off-peak in DeepSeek's tariff, including make-up workdays.
// The State Council published these holiday schedules for 2026 and 2027.
// https://www.beijing.gov.cn/cs/gncs/zcwj/202603/t20260327_4568275.html
// https://www.gov.cn/zhengce/content/202611/content_7085101.htm
func publishedProCalendar() proCalendar {
	cal := proCalendar{Version: "cn-2026-2027", Days: make(map[string]string)}
	for year, holidays := range map[int][][3]int{
		2026: {{1, 1, 3}, {2, 15, 23}, {4, 4, 6}, {5, 1, 5}, {6, 19, 21}, {9, 25, 27}, {10, 1, 7}},
		2027: {{1, 1, 3}, {2, 5, 13}, {4, 3, 5}, {5, 1, 5}, {6, 9, 9}, {9, 15, 15}, {10, 1, 7}},
	} {
		for d := time.Date(year, 1, 1, 0, 0, 0, 0, time.UTC); d.Year() == year; d = d.AddDate(0, 0, 1) {
			kind := "workday"
			for _, span := range holidays {
				if int(d.Month()) == span[0] && d.Day() >= span[1] && d.Day() <= span[2] {
					kind = "holiday"
					break
				}
			}
			cal.Days[d.Format(time.DateOnly)] = kind
		}
	}
	return cal
}

var defaultProCalendar = publishedProCalendar()
