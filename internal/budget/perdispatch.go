package budget

// The per-task cap, `budget.per_dispatch_usd` in config.yaml.
//
// A different guardrail from the rest of this package and worth keeping
// straight: the rolling window rations a *provider's* quota across a whole
// run, while this caps what a *single task* is allowed to cost across its
// attempts. They can each block while the other is happy.
//
// It lives here because in Python it does not live anywhere — the rule is
// written inline in `orch.py`'s retry branch, and the same config key is read
// a second time, differently, in `dispatcher.py`. One expression in one place
// is the whole change; the behaviour is unchanged.

// AllowEscalation reports whether a task that has already cost `spentSoFar`
// may take one more, more expensive, attempt.
//
// FR-D-8 gives a task a third attempt on a route with an `escalation_model`.
// That attempt is usually the priciest of the three, which is exactly why it
// is checked against the cap rather than waved through as "just one more".
//
// A cap of zero or less means unlimited — that is how an operator who has not
// set `per_dispatch_usd` gets the pre-FR-D-8 behaviour, and it is why the
// check cannot be written as a plain `spentSoFar < cap`.
//
// The comparison is strict, so a task that has spent exactly the cap does not
// get the extra attempt: Python's `spent_so_far < budget_cap` is false on
// equality. That is the same boundary the rolling window uses — usage landing
// exactly on the cap blocks there too — which is worth stating because the two
// expressions are written the opposite way round (`used < cap` allows there,
// `spent < cap` allows here) and read as though they disagree.
func AllowEscalation(spentSoFar, capUSD float64) bool {
	return capUSD <= 0 || spentSoFar < capUSD
}
