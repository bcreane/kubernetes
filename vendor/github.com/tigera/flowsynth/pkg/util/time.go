// Copyright (c) 2018-2019 Tigera, Inc. All rights reserved.

package util

import (
	"time"
)

func ParseANSITime(t string) (time.Time, error) {
	s, err := time.ParseInLocation("2006-01-02 15:04:05", t, time.Local)
	if err != nil {
		s, err = time.ParseInLocation("2006-01-02", t, time.Local)
		if err != nil {
			return time.Time{}, err
		}
	}
	return s, nil
}
