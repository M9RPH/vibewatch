package app

import (
	"context"
	"sync"
)

// mutationPipelineGate is the single concurrency authority for destructive
// container lifecycle work. Update jobs, rollback jobs and chain
// restart/recreate actions share it so the invariants are global rather than an
// implementation detail of updateWorker:
//   - at most maxConcurrentUpdatePipelines mutations globally;
//   - at most one mutation pipeline per Docker host.
//
// Host ownership is acquired before a global slot so multiple waiters for the
// same busy host cannot consume all global capacity and head-of-line block work
// on unrelated hosts.
type mutationPipelineGate struct {
	mu       sync.Mutex
	hostGate map[int64]chan struct{}
	slots    chan struct{}
}

func newMutationPipelineGate(limit int) *mutationPipelineGate {
	if limit < 1 {
		limit = 1
	}
	return &mutationPipelineGate{hostGate: map[int64]chan struct{}{}, slots: make(chan struct{}, limit)}
}

func (g *mutationPipelineGate) gateForHost(hostID int64) chan struct{} {
	g.mu.Lock()
	defer g.mu.Unlock()
	ch := g.hostGate[hostID]
	if ch == nil {
		ch = make(chan struct{}, 1)
		g.hostGate[hostID] = ch
	}
	return ch
}

func (g *mutationPipelineGate) acquire(ctx context.Context, hostID int64) (func(), error) {
	if g == nil {
		return func() {}, nil
	}
	host := g.gateForHost(hostID)
	select {
	case host <- struct{}{}:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	select {
	case g.slots <- struct{}{}:
	case <-ctx.Done():
		<-host
		return nil, ctx.Err()
	}
	var once sync.Once
	return func() {
		once.Do(func() {
			<-g.slots
			<-host
		})
	}, nil
}

type mutationPipelineContextKey struct{}

type mutationPipelineOwner struct {
	HostID int64
}

func mutationPipelineHeld(ctx context.Context, hostID int64) bool {
	owner, _ := ctx.Value(mutationPipelineContextKey{}).(mutationPipelineOwner)
	return owner.HostID > 0 && owner.HostID == hostID
}

// acquireMutationPipeline is context-reentrant for the same host. Nested
// recovery (for example an automatic rollback inside executeUpdate) must reuse
// the already-held scheduler slot instead of deadlocking on itself.
func (a *App) acquireMutationPipeline(ctx context.Context, hostID int64) (context.Context, func(), error) {
	if mutationPipelineHeld(ctx, hostID) {
		return ctx, func() {}, nil
	}
	release, err := a.mutationGate.acquire(ctx, hostID)
	if err != nil {
		return ctx, nil, err
	}
	return context.WithValue(ctx, mutationPipelineContextKey{}, mutationPipelineOwner{HostID: hostID}), release, nil
}
