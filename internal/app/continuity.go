package app

import (
	"context"
	"errors"
	"fmt"
	"net"
	"strings"
	"time"

	"github.com/m9rph/vibewatch/internal/registry"
)

// continuityExclusiveKey marks contexts that already own the global
// control-plane continuity writer lock. It makes nested rollback/recovery
// helpers safe without requiring a re-entrant mutex.
type continuityExclusiveKey struct{}
type continuitySharedKey struct{}

// withHeldContinuityMarker re-attaches lock ownership to a durable caller
// context without acquiring the RWMutex again. It is used when a restore-point
// capture transfers an already-held guard to the update pipeline. Crucially,
// callers must pass their long-lived job context here rather than reusing the
// short-lived capture context that originally acquired the lock.
func withHeldContinuityMarker(ctx context.Context, exclusive bool) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	if exclusive {
		return context.WithValue(ctx, continuityExclusiveKey{}, true)
	}
	return context.WithValue(ctx, continuitySharedKey{}, true)
}

// acquireContinuityRead protects network-dependent discovery/control-plane
// operations from overlapping a service disruption. Go's RWMutex gives a
// waiting writer priority over new readers, so once an update is ready to
// mutate a container, new registry checks stop entering while already-running
// checks are allowed to finish cleanly.
func (a *App) acquireContinuityRead(ctx context.Context) func() {
	if ctx != nil {
		if held, _ := ctx.Value(continuityExclusiveKey{}).(bool); held {
			return func() {}
		}
		if held, _ := ctx.Value(continuitySharedKey{}).(bool); held {
			return func() {}
		}
	}
	a.continuityMu.RLock()
	return a.continuityMu.RUnlock
}

// acquireContinuityShared protects a complete ordinary update/rollback
// mutation window. Multiple shared mutation windows may run concurrently on
// different hosts, but a pending DNS/control-plane writer prevents new shared
// mutations and registry readers from entering. The context marker also makes
// nested registry reads in the same pipeline no-ops, avoiding recursive RLock
// deadlocks when a writer is already waiting.
func (a *App) acquireContinuityShared(ctx context.Context) (context.Context, func()) {
	if ctx == nil {
		ctx = context.Background()
	}
	if held, _ := ctx.Value(continuityExclusiveKey{}).(bool); held {
		return ctx, func() {}
	}
	if held, _ := ctx.Value(continuitySharedKey{}).(bool); held {
		return ctx, func() {}
	}
	a.continuityMu.RLock()
	lockedCtx := context.WithValue(ctx, continuitySharedKey{}, true)
	return lockedCtx, a.continuityMu.RUnlock
}

// acquireContinuityExclusive opens a short destructive window. Discovery and
// registry calls can run concurrently before and after this window, but never
// while a container may be stopped/recreated or while its runtime/application
// readiness is still being established.
func (a *App) acquireContinuityExclusive(ctx context.Context) (context.Context, func()) {
	if ctx == nil {
		ctx = context.Background()
	}
	if held, _ := ctx.Value(continuityExclusiveKey{}).(bool); held {
		return ctx, func() {}
	}
	a.continuityMu.Lock()
	lockedCtx := context.WithValue(ctx, continuityExclusiveKey{}, true)
	return lockedCtx, a.continuityMu.Unlock
}

// acquireContinuityMutation selects the narrowest safe guard for a lifecycle
// mutation. Ordinary services use a shared guard and therefore remain parallel
// across hosts. A DNS-capable target takes the exclusive guard because stopping
// it can remove name resolution required by every other update pipeline.
func (a *App) acquireContinuityMutation(ctx context.Context, target inspectContainer) (context.Context, func(), bool) {
	if containerProvidesDNS(target) {
		lockedCtx, release := a.acquireContinuityExclusive(ctx)
		return lockedCtx, release, true
	}
	lockedCtx, release := a.acquireContinuityShared(ctx)
	return lockedCtx, release, false
}

func isPort53Key(v string) bool {
	v = strings.TrimSpace(strings.ToLower(v))
	if slash := strings.Index(v, "/"); slash >= 0 {
		v = v[:slash]
	}
	return v == "53"
}

// containerProvidesDNS intentionally uses runtime configuration rather than
// names/images. This catches AdGuard, Pi-hole, CoreDNS, Unbound and custom DNS
// containers without app-specific heuristics.
func containerProvidesDNS(c inspectContainer) bool {
	for port := range c.Config.ExposedPorts {
		if isPort53Key(port) {
			return true
		}
	}
	for port := range c.HostConfig.PortBindings {
		if isPort53Key(port) {
			return true
		}
	}
	return false
}

// verifyDNSControlPlaneRecovery is an additional readiness condition only for
// containers that actually expose DNS port 53. A normal HTTP application is
// governed solely by Docker/custom verification. For a DNS provider, however,
// Vibewatch must also prove that the controller can resolve the registry host
// needed by the remaining update queue before the continuity barrier opens.
//
// This is deliberately a DNS lookup rather than another manifest request: a
// registry 401/429/5xx would still prove DNS continuity and must not trigger a
// rollback of an otherwise healthy DNS service.
func (a *App) verifyDNSControlPlaneRecovery(ctx context.Context, target inspectContainer) error {
	return a.verifyDNSControlPlaneRecoveryWithLookup(ctx, target, net.DefaultResolver.LookupHost)
}

func dnsLookupAnswered(err error) bool {
	if err == nil {
		return true
	}
	var dnsErr *net.DNSError
	return errors.As(err, &dnsErr) && dnsErr.IsNotFound
}

func (a *App) verifyDNSControlPlaneRecoveryWithLookup(ctx context.Context, target inspectContainer, lookup func(context.Context, string) ([]string, error)) error {
	if !containerProvidesDNS(target) {
		return nil
	}
	imageRef := strings.TrimSpace(target.Config.Image)
	if imageRef == "" {
		imageRef = strings.TrimSpace(target.Image)
	}
	parsed, err := registry.Parse(imageRef)
	if err != nil || strings.TrimSpace(parsed.Registry) == "" {
		return fmt.Errorf("DNS-capable target recovered, but registry hostname could not be derived from image %q", imageRef)
	}
	host := strings.TrimSpace(parsed.Registry)
	if net.ParseIP(strings.Trim(host, "[]")) != nil {
		return nil
	}

	// Probe a unique child label first. The canonical registry hostname may be
	// present in Docker's embedded DNS cache from the pre-update discovery pass;
	// a cached positive answer would not prove that the upstream resolver (for
	// example AdGuard) is actually reachable again. A response to the unique
	// name, including an authoritative NXDOMAIN, proves a live DNS path. Only
	// then do we require the real registry hostname to resolve successfully.
	delays := []time.Duration{0, time.Second, 2 * time.Second, 4 * time.Second, 7 * time.Second}
	var lastErr error
	for i, delay := range delays {
		if delay > 0 {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(delay):
			}
		}
		probeCtx, cancel := context.WithTimeout(ctx, 4*time.Second)
		probeHost := fmt.Sprintf("vibewatch-%d.%s", time.Now().UnixNano(), host)
		_, probeErr := lookup(probeCtx, probeHost)
		if !dnsLookupAnswered(probeErr) {
			cancel()
			lastErr = probeErr
			if a.Logger != nil {
				a.Logger.Warn("DNS control-plane cache-busting probe failed", "registry_host", host, "attempt", i+1, "attempts", len(delays), "error", probeErr)
			}
			continue
		}
		addrs, lookupErr := lookup(probeCtx, host)
		cancel()
		if lookupErr == nil && len(addrs) > 0 {
			return nil
		}
		if lookupErr == nil {
			lookupErr = fmt.Errorf("resolver returned no addresses")
		}
		lastErr = lookupErr
		if a.Logger != nil {
			a.Logger.Warn("DNS control-plane registry lookup failed", "registry_host", host, "attempt", i+1, "attempts", len(delays), "error", lookupErr)
		}
	}
	if lastErr == nil {
		lastErr = fmt.Errorf("resolver did not answer")
	}
	return fmt.Errorf("DNS-capable target did not restore uncached controller name resolution for %s: %w", host, lastErr)
}
