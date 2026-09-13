package nofxos

import "fmt"

// Language represents the language for formatting output
type Language string

const (
	LangChinese Language = "zh-CN"
	LangEnglish Language = "en-US"
)

// formatValue formats a numeric value with sign and appropriate suffix
func formatValue(v float64) string {
	sign := "+"
	if v < 0 {
		sign = ""
	}
	absV := v
	if absV < 0 {
		absV = -absV
	}
	if absV >= 1e9 {
		return fmt.Sprintf("%s%.2fB", sign, v/1e9)
	} else if absV >= 1e6 {
		return fmt.Sprintf("%s%.2fM", sign, v/1e6)
	} else if absV >= 1e3 {
		return fmt.Sprintf("%s%.2fK", sign, v/1e3)
	}
	return fmt.Sprintf("%s%.2f", sign, v)
}

// formatPrice renders a USD price with precision scaled to its magnitude, so
// sub-dollar altcoins keep meaningful digits while BTC stays readable.
func formatPrice(v float64) string {
	absV := v
	if absV < 0 {
		absV = -absV
	}
	switch {
	case absV >= 1000:
		return fmt.Sprintf("$%.2f", v)
	case absV >= 1:
		return fmt.Sprintf("$%.4f", v)
	default:
		return fmt.Sprintf("$%.6f", v)
	}
}

// formatPercent renders a percentage with an explicit sign.
func formatPercent(v float64, withPlus bool) string {
	if withPlus && v > 0 {
		return fmt.Sprintf("+%.2f%%", v)
	}
	return fmt.Sprintf("%.2f%%", v)
}
