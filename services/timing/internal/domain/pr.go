package domain

// Personal-record detection (Story 3.4, FR37). Pure and I/O-free, like the lap
// rules above: given a driver's current all-time PR (nil = none yet) and a freshly
// counted lap time, decide whether the lap broke the record and, if so, what value
// it beat. Timing holds a LOCAL copy of each driver's all-time PR (an ECST cache,
// seeded/refreshed by Driver's driver.pr_updated); this rule is what it applies to
// every counted lap. Driver is the system of record that confirms the canonical PR.
//
// Decisions (Q&A Round 37):
//   - Q37.2: the FIRST-ever lap (current == nil) IS a break, with no previousMs —
//     Timing emits personal_record.broken with previousMs omitted.
//   - a lap STRICTLY faster than the current PR is a break carrying the beaten value.
//   - a tie (equal to the current PR) does NOT beat it.
//
// CheckPR reports whether lapMs beats current and, on a break, the value beaten.
// previousMs is nil for a first-ever PR (nothing to beat) and non-nil (== the old
// PR) for a subsequent break. It never returns a non-nil previousMs when broken is
// false. The caller (the durable driver_prs store) advances the local copy on a
// break so the next lap compares against the new best (Q37.3, optimistic advance).
func CheckPR(current *int64, lapMs int64) (broken bool, previousMs *int64) {
	if current == nil {
		return true, nil
	}
	if lapMs < *current {
		beaten := *current
		return true, &beaten
	}
	return false, nil
}
