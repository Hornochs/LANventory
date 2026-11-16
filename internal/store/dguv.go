package store

import (
	"fmt"
	"time"
)

// Inspection results.
const (
	ResultPass = "pass"
	ResultFail = "fail"
)

// ProtectionClasses lists the valid protection classes.
var ProtectionClasses = []string{"I", "II", "III"}

// Inspection reasons (Prüfanlass); "" = not recorded.
const (
	ReasonInitial = "initial" // before first use
	ReasonRepeat  = "repeat"  // periodic repeat inspection
	ReasonRepair  = "repair"  // after repair or modification
)

// InspectionReasons lists the valid inspection reasons.
var InspectionReasons = []string{ReasonInitial, ReasonRepeat, ReasonRepair}

// Measuring methods of the leakage current; "" = not recorded.
const (
	MethodDirect       = "direct"
	MethodDifferential = "differential"
	MethodAlternative  = "alternative" // substitute leakage current
)

// LeakageMethods lists the valid measuring methods of the leakage current.
var LeakageMethods = []string{MethodDirect, MethodDifferential, MethodAlternative}

// Comparison qualifiers of a reading beyond the instrument's range.
const (
	CmpLess    = "<"
	CmpGreater = ">"
)

// Due states of an asset that needs DGUV V3 inspections.
const (
	DueOverdue = "overdue" // next due date is before today
	DueSoon    = "soon"    // due today or within DueSoonDays
	DueOK      = "ok"      // due later than that
	DueNever   = "never"   // never inspected
)

// DueSoonDays is the look-ahead of the "due soon" state: an inspection is due
// soon when today <= next_due <= today + DueSoonDays.
const DueSoonDays = 30

// Upper bounds of accepted measurements (plausibility checks, not limits of
// the standard).
const (
	MaxPEResistanceOhm = 1000
	MaxInsulationMOhm  = 10000
	MaxLeakageMA       = 1000
	MaxTouchMA         = 1000
)

// AddMonths adds calendar months to t and clamps the day to the end of the
// target month (31 January + 1 month = 28 or 29 February). The time of day is
// dropped; the result is midnight UTC.
func AddMonths(t time.Time, months int) time.Time {
	y, m, d := t.Date()
	idx := y*12 + int(m) - 1 + months
	y, mi := idx/12, idx%12
	if mi < 0 {
		mi += 12
		y--
	}
	m = time.Month(mi + 1)
	if last := daysIn(y, m); d > last {
		d = last
	}
	return time.Date(y, m, d, 0, 0, 0, 0, time.UTC)
}

func daysIn(y int, m time.Month) int {
	return time.Date(y, m+1, 0, 0, 0, 0, 0, time.UTC).Day()
}

// NextDue computes the next due date (YYYY-MM-DD) from the inspection date
// (YYYY-MM-DD) and the interval in months.
func NextDue(inspectedOn string, months int) (string, error) {
	t, err := time.Parse(DateLayout, inspectedOn)
	if err != nil {
		return "", fmt.Errorf("inspection date %q: %w", inspectedOn, err)
	}
	if months < 1 || months > 120 {
		return "", fmt.Errorf("interval %d months out of range", months)
	}
	return AddMonths(t, months).Format(DateLayout), nil
}

// EffectiveInterval returns the interval that applies to an asset: its own
// override when set, else the category's.
func EffectiveInterval(assetOverride, categoryInterval int) int {
	if assetOverride > 0 {
		return assetOverride
	}
	return categoryInterval
}

// DueState classifies a next due date (YYYY-MM-DD, empty = never inspected)
// relative to today: overdue when next_due < today, soon when
// today <= next_due <= today + DueSoonDays, else ok.
func DueState(nextDue string, today time.Time) string {
	if nextDue == "" {
		return DueNever
	}
	t := today.UTC().Format(DateLayout)
	switch {
	case nextDue < t:
		return DueOverdue
	case nextDue <= today.UTC().AddDate(0, 0, DueSoonDays).Format(DateLayout):
		return DueSoon
	}
	return DueOK
}

// Guidance limits in the spirit of DIN VDE 0701-0702. They are SIMPLIFIED
// values for orientation only: the standard's detailed rules (for example the
// dependence of the protective conductor resistance on the cable length, or
// the different limits for other classes and device types) apply, and the
// inspector alone decides about pass or fail. LANventory never derives the
// result from them.
const (
	LimitPEResistanceOhm   = 0.3 // protective conductor resistance, at most
	LimitInsulationClassI  = 1.0 // insulation resistance in MOhm, at least
	LimitInsulationClassII = 2.0
	LimitLeakageClassI     = 3.5 // protective conductor current in mA, at most
	LimitTouch             = 0.5 // touch current in mA, at most (classes I and II)
)

// Guidance flag keys.
const (
	FlagPEHigh        = "pe_high"
	FlagInsulationLow = "insulation_low"
	FlagLeakageHigh   = "leakage_high"
	FlagTouchHigh     = "touch_high"
)

// GuidanceFlag marks a measurement outside the simplified guidance limits.
type GuidanceFlag struct {
	Key   string  // one of the Flag* constants
	Field string  // the measurement it concerns
	Limit float64 // the limit that was exceeded or undercut
}

// Guidance returns the flags for the measurements of an inspection. The
// numbers are compared as they are, ignoring comparison qualifiers: ">299"
// MOhm of insulation is not low, "<0.01" mA of leakage is not high. Missing
// measurements and unknown protection classes yield no flag.
func Guidance(class string, pe, insulation, leakage, touch *float64) []GuidanceFlag {
	var out []GuidanceFlag
	if pe != nil && *pe > LimitPEResistanceOhm {
		out = append(out, GuidanceFlag{FlagPEHigh, "pe_resistance_ohm", LimitPEResistanceOhm})
	}
	if insulation != nil {
		switch {
		case class == "I" && *insulation < LimitInsulationClassI:
			out = append(out, GuidanceFlag{FlagInsulationLow, "insulation_mohm", LimitInsulationClassI})
		case class == "II" && *insulation < LimitInsulationClassII:
			out = append(out, GuidanceFlag{FlagInsulationLow, "insulation_mohm", LimitInsulationClassII})
		}
	}
	if leakage != nil && class == "I" && *leakage > LimitLeakageClassI {
		out = append(out, GuidanceFlag{FlagLeakageHigh, "leakage_ma", LimitLeakageClassI})
	}
	if touch != nil && (class == "I" || class == "II") && *touch > LimitTouch {
		out = append(out, GuidanceFlag{FlagTouchHigh, "touch_ma", LimitTouch})
	}
	return out
}
