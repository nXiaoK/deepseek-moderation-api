package audit

import "time"

// The rolling window counts sent attempts plus all outstanding reservations.
// Reserving under routeMu prevents concurrent requests from overspending RPM.
// An unsent attempt is refunded; pending reservations do not expire mid-flight.
func (st *channelState) rpmUsed(now time.Time) int {
	cutoff := now.Add(-time.Minute)
	n := 0
	for n < len(st.rpmCalls) && !st.rpmCalls[n].After(cutoff) {
		st.rpmCalls[n] = time.Time{}
		n++
	}
	st.rpmCalls = st.rpmCalls[n:]
	return len(st.rpmCalls) + st.rpmPending
}
