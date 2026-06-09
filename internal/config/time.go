package config

import "time"

func timeNowUTC() string {
	return time.Now().UTC().Format("2006-01-02T15:04:05Z")
}
