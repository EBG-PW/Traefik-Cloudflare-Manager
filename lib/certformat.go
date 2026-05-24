package lib

import (
	"fmt"
	"time"
)

func FormatCertTime(t time.Time) string {
	if t.IsZero() {
		return "-"
	}
	return t.Local().Format("2006-01-02 15:04 MST")
}

func FormatCertDuration(until time.Time) string {
	if until.IsZero() {
		return "-"
	}
	d := time.Until(until)
	if d <= 0 {
		return "expired"
	}
	days := int(d.Hours()) / 24
	hours := int(d.Hours()) % 24
	if days > 0 {
		return fmt.Sprintf("%dd %dh", days, hours)
	}
	minutes := int(d.Minutes()) % 60
	return fmt.Sprintf("%dh %dm", hours, minutes)
}
