package telemetry

import (
	"context"
	"fmt"
	"strings"
	"time"
)

const cacheHit = "hit"

// IdentityCache records bounded outcomes without token, subject or organization labels.
func (t *Telemetry) IdentityCache(_ context.Context, result string, age time.Duration) {
	switch result {
	case cacheHit, "miss", "joined", "fresh", deniedOutcome, errorOutcome, "cleared":
	default:
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	t.identityCacheEvents[result]++
	if result == cacheHit {
		t.identityCacheAge = observe(t.identityCacheAge, age)
	}
}

// identityCacheMetrics is called with the telemetry lock held.
func (t *Telemetry) identityCacheMetrics(b *strings.Builder) {
	fmt.Fprintln(b, "# HELP clx_identity_cache_total Identity cache outcomes.\n# TYPE clx_identity_cache_total counter")
	for _, result := range []string{cacheHit, "miss", "joined", "fresh", deniedOutcome, errorOutcome, "cleared"} {
		fmt.Fprintf(b, "clx_identity_cache_total{result=%q} %d\n", result, t.identityCacheEvents[result])
	}
	fmt.Fprintln(
		b,
		"# HELP clx_identity_cache_age_seconds Age of reused identity claims.\n# TYPE clx_identity_cache_age_seconds histogram",
	)
	histogram(b, "clx_identity_cache_age_seconds", `cache="permissions"`, t.identityCacheAge)
}
