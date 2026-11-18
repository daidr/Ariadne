package projectsettings

import (
	"strconv"
	"time"
)

// NextRun follows JavaScript Date's compatible disambiguation: earlier on an
// autumn overlap, and forward by the gap when a spring wall-clock time is absent.
func NextRun(routine Routine, after time.Time, location *time.Location) *time.Time {
	if !routine.Enabled || len(routine.Days) == 0 || !clockPattern.MatchString(routine.Time) {
		return nil
	}
	if location == nil {
		location = time.Local
	}
	hour, _ := strconv.Atoi(routine.Time[:2])
	minute, _ := strconv.Atoi(routine.Time[3:])
	day := after.In(location)
	for n := 0; n <= 7; n++ {
		wall := time.Date(day.Year(), day.Month(), day.Day()+n, hour, minute, 0, 0, time.UTC)
		at := time.Date(wall.Year(), wall.Month(), wall.Day(), hour, minute, 0, 0, location)
		actual := time.Date(at.Year(), at.Month(), at.Day(), at.Hour(), at.Minute(), 0, 0, time.UTC)
		if gap := wall.Sub(actual); gap > 0 {
			at = at.Add(gap)
		}
		start, _ := at.ZoneBounds()
		if !start.IsZero() {
			_, currentOffset := at.Zone()
			_, beforeOffset := start.Add(-time.Nanosecond).Zone()
			if delta := currentOffset - beforeOffset; delta < 0 {
				earlier := at.Add(time.Duration(delta) * time.Second)
				if earlier.Year() == at.Year() && earlier.YearDay() == at.YearDay() && earlier.Hour() == at.Hour() && earlier.Minute() == at.Minute() {
					at = earlier
				}
			}
		}
		matches := false
		for _, weekday := range routine.Days {
			if weekday == int(at.Weekday()) {
				matches = true
				break
			}
		}
		if at.After(after) && matches {
			return &at
		}
	}
	return nil
}
