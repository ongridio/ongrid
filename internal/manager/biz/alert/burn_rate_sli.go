package alert

import (
	"regexp"
	"strings"
)

var burnRateSimpleRangeSelectorRE = regexp.MustCompile(`\[(\d+(?:ms|s|m|h|d|w|y))\]`)

func normalizeBurnRateSLIExpression(sli string) string {
	sli = strings.TrimSpace(sli)
	if strings.Contains(sli, "$window") {
		return sli
	}
	return burnRateSimpleRangeSelectorRE.ReplaceAllString(sli, "[$$window]")
}

func burnRateSLIUsesWindow(sli string) bool {
	return strings.Contains(sli, "$window")
}

func normalizeBurnRateSLOPercent(slo float64) float64 {
	if slo > 0 && slo <= 1 {
		return slo * 100
	}
	return slo
}
