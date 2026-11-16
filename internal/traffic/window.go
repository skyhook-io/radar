package traffic

import (
	"fmt"
	"time"
)

// defaultRateWindowSeconds is the window used when the caller asks for less
// than a minute: the traffic view's own default.
const defaultRateWindowSeconds = 300

// rateWindow is the span every query of a metrics source rates over, and the span the
// byte totals are derived for. Both have to come from the same place: a rate is
// per-second, so turning it back into a total means multiplying by exactly the
// window that produced it.
type rateWindow struct {
	promQL  string
	seconds float64
}

// span is the window as a duration, for the cluster-scope checks that read it.
func (w rateWindow) span() time.Duration { return time.Duration(w.seconds * float64(time.Second)) }

// rateWindowFor turns the caller's requested span into a window these queries can
// use. The traffic view offers 1m to 1h and the value arrives here; ignoring it
// leaves the control inert, which is what it was.
//
// Anything shorter than a minute cannot be rated: Prometheus needs two samples in
// the window, and a scrape interval of 15s makes a sub-minute span a coin toss. The
// default matches the view's own default rather than being an arbitrary floor.
func rateWindowFor(since time.Duration) rateWindow {
	if since < time.Minute {
		return rateWindow{promQL: "5m", seconds: defaultRateWindowSeconds}
	}
	// Whole seconds, so any duration the caller asks for is valid PromQL
	// (time.Duration's own format renders 90.5s as "1m30.5s", which PromQL
	// rejects) and the rate, byte totals and scope checks all use one span.
	secs := int(since.Seconds())
	return rateWindow{promQL: fmt.Sprintf("%ds", secs), seconds: float64(secs)}
}
