package emquant

import (
	"errors"
	"time"
)

// The exact field meanings and units are vendor/account dependent; leave raw.
const usageIndicators = "FUNCENAME,SECUTYPE,PERIOD,STARTDATE,ENDDATE,THRESHOLD,USEDDATA,AVAILABEDATA"

func usageOptions(function, from, to string) (string, error) {
	if function != "EM_CSD" && function != "EM_CSS" {
		return "", errors.New("emquant: unsupported usage function")
	}
	start, err := time.Parse("2006-01-02", from)
	if err != nil {
		return "", errors.New("emquant: invalid usage start date")
	}
	end, err := time.Parse("2006-01-02", to)
	if err != nil || end.Before(start) || end.Sub(start) > 366*24*time.Hour {
		return "", errors.New("emquant: invalid usage end date")
	}
	return "StartDate=" + from + ",EndDate=" + to, nil
}
