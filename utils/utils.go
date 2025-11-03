package utils

import "time"

// GetLastResetDate 计算上一个重置日期
// resetDay: 每月重置的日期 (1-31)
// currentDate: 当前日期
// 返回: 上一个重置日期
func GetLastResetDate(resetDay int, currentDate time.Time) time.Time {
	if resetDay < 1 || resetDay > 31 {
		// 如果重置日不合法，返回当前日期
		return currentDate
	}

	currentYear := currentDate.Year()
	currentMonth := currentDate.Month()

	// 获取本月实际的重置日（考虑顺延情况）
	actualResetDateThisMonth := getActualResetDate(currentYear, currentMonth, resetDay, currentDate.Location())

	// 如果当前日期 >= 本月实际重置日，返回本月的重置日
	if !currentDate.Before(actualResetDateThisMonth) {
		return actualResetDateThisMonth
	}

	// 如果当前日期 < 本月实际重置日，需要找上个月的重置日
	// 获取上个月
	prevMonth := currentMonth - 1
	prevYear := currentYear
	if prevMonth < 1 {
		prevMonth = 12
		prevYear--
	}

	return getActualResetDate(prevYear, prevMonth, resetDay, currentDate.Location())
}

// getActualResetDate 获取某月的实际重置日期（考虑顺延）
// 如果该月没有指定的日期，则顺延到下个月1日
func getActualResetDate(year int, month time.Month, resetDay int, location *time.Location) time.Time {
	// 获取该月的最后一天
	firstDayOfNextMonth := time.Date(year, month+1, 1, 0, 0, 0, 0, location)
	lastDayOfMonth := firstDayOfNextMonth.AddDate(0, 0, -1).Day()

	// 如果该月有这个日期，返回该日期
	if resetDay <= lastDayOfMonth {
		return time.Date(year, month, resetDay, 0, 0, 0, 0, location)
	}

	// 如果该月没有这个日期，顺延到下个月1日
	nextMonth := month + 1
	nextYear := year
	if nextMonth > 12 {
		nextMonth = 1
		nextYear++
	}
	return time.Date(nextYear, nextMonth, 1, 0, 0, 0, 0, location)
}
