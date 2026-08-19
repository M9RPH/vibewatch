package app

import (
	"context"
	"fmt"
	"net"
	"strings"
	"testing"
	"time"
)

func TestContinuityExclusiveBlocksNetworkReaders(t *testing.T) {
	a := &App{}
	_, releaseExclusive := a.acquireContinuityExclusive(context.Background())

	acquired := make(chan struct{})
	go func() {
		releaseRead := a.acquireContinuityRead(context.Background())
		close(acquired)
		releaseRead()
	}()

	select {
	case <-acquired:
		t.Fatal("network reader entered while destructive continuity window was active")
	case <-time.After(40 * time.Millisecond):
	}

	releaseExclusive()
	select {
	case <-acquired:
	case <-time.After(time.Second):
		t.Fatal("network reader did not resume after destructive continuity window closed")
	}
}

func TestContinuityNestedReadInsideExclusiveDoesNotDeadlock(t *testing.T) {
	a := &App{}
	lockedCtx, releaseExclusive := a.acquireContinuityExclusive(context.Background())
	defer releaseExclusive()

	done := make(chan struct{})
	go func() {
		releaseRead := a.acquireContinuityRead(lockedCtx)
		releaseRead()
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("nested registry read deadlocked while the same pipeline owned continuity exclusivity")
	}
}

func TestContainerProvidesDNSUsesRuntimePorts(t *testing.T) {
	var c inspectContainer
	c.Config.ExposedPorts = map[string]struct{}{"53/udp": {}}
	if !containerProvidesDNS(c) {
		t.Fatal("expected exposed DNS port to identify control-plane DNS service")
	}

	var bound inspectContainer
	bound.HostConfig.PortBindings = map[string][]struct {
		HostIP   string `json:"HostIp"`
		HostPort string `json:"HostPort"`
	}{"53/tcp": {{HostPort: "53"}}}
	if !containerProvidesDNS(bound) {
		t.Fatal("expected published DNS port to identify control-plane DNS service")
	}

	var web inspectContainer
	web.HostConfig.PortBindings = map[string][]struct {
		HostIP   string `json:"HostIp"`
		HostPort string `json:"HostPort"`
	}{"80/tcp": {{HostPort: "80"}}}
	if containerProvidesDNS(web) {
		t.Fatal("ordinary web service must not be treated as DNS infrastructure")
	}
}

func TestDNSControlPlaneRecoveryUsesUncachedProbeThenTargetRegistryHostname(t *testing.T) {
	a := &App{}
	var target inspectContainer
	target.Config.Image = "adguard/adguardhome:latest"
	target.Config.ExposedPorts = map[string]struct{}{"53/tcp": {}}

	called := 0
	err := a.verifyDNSControlPlaneRecoveryWithLookup(context.Background(), target, func(_ context.Context, host string) ([]string, error) {
		called++
		if host == "registry-1.docker.io" {
			return []string{"192.0.2.10"}, nil
		}
		if !strings.HasSuffix(host, ".registry-1.docker.io") {
			return nil, fmt.Errorf("unexpected DNS probe hostname %q", host)
		}
		// NXDOMAIN is a valid response and proves that an uncached request reached
		// a working resolver path.
		return nil, &net.DNSError{Err: "no such host", Name: host, IsNotFound: true}
	})
	if err != nil {
		t.Fatalf("expected DNS continuity probe to succeed: %v", err)
	}
	if called != 2 {
		t.Fatalf("expected cache-busting plus canonical DNS lookup, got %d calls", called)
	}
}

func TestDNSControlPlaneRecoveryRejectsCachedCanonicalAnswerWhenUncachedProbeFails(t *testing.T) {
	a := &App{}
	var target inspectContainer
	target.Config.Image = "adguard/adguardhome:latest"
	target.Config.ExposedPorts = map[string]struct{}{"53/udp": {}}

	canonicalCalls := 0
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	err := a.verifyDNSControlPlaneRecoveryWithLookup(ctx, target, func(_ context.Context, host string) ([]string, error) {
		if host == "registry-1.docker.io" {
			canonicalCalls++
			return []string{"192.0.2.10"}, nil // models a stale embedded-DNS cache hit
		}
		return nil, &net.DNSError{Err: "server misbehaving", Name: host, IsTemporary: true}
	})
	if err == nil {
		t.Fatal("expected failed uncached DNS probe to block continuity recovery")
	}
	if canonicalCalls != 0 {
		t.Fatalf("canonical cached answer must not bypass failed uncached probe, got %d canonical calls", canonicalCalls)
	}
}

func TestDNSControlPlaneRecoverySkipsOrdinaryContainers(t *testing.T) {
	a := &App{}
	var target inspectContainer
	target.Config.Image = "nginx:latest"
	target.Config.ExposedPorts = map[string]struct{}{"80/tcp": {}}

	err := a.verifyDNSControlPlaneRecoveryWithLookup(context.Background(), target, func(_ context.Context, host string) ([]string, error) {
		return nil, fmt.Errorf("lookup should not run for %s", host)
	})
	if err != nil {
		t.Fatalf("ordinary container unexpectedly required DNS continuity probe: %v", err)
	}
}

func TestContinuityReadersRemainParallelOutsideMutation(t *testing.T) {
	a := &App{}
	releaseFirst := a.acquireContinuityRead(context.Background())
	defer releaseFirst()

	acquired := make(chan struct{})
	go func() {
		releaseSecond := a.acquireContinuityRead(context.Background())
		close(acquired)
		releaseSecond()
	}()

	select {
	case <-acquired:
	case <-time.After(time.Second):
		t.Fatal("parallel discovery reader was unnecessarily serialized")
	}
}

func TestContinuityPendingMutationStopsNewReaders(t *testing.T) {
	a := &App{}
	releaseInitialRead := a.acquireContinuityRead(context.Background())

	writerStarted := make(chan struct{})
	order := make(chan string, 2)
	go func() {
		close(writerStarted)
		_, releaseExclusive := a.acquireContinuityExclusive(context.Background())
		order <- "writer"
		time.Sleep(20 * time.Millisecond)
		releaseExclusive()
	}()
	<-writerStarted
	// Give the writer a chance to become pending behind the initial reader.
	time.Sleep(20 * time.Millisecond)

	go func() {
		releaseRead := a.acquireContinuityRead(context.Background())
		order <- "reader"
		releaseRead()
	}()

	releaseInitialRead()
	select {
	case first := <-order:
		if first != "writer" {
			t.Fatalf("new discovery reader overtook pending destructive window: %s", first)
		}
	case <-time.After(time.Second):
		t.Fatal("continuity scheduler did not make progress")
	}
}

func TestContinuityNestedReadInsideSharedMutationDoesNotDeadlock(t *testing.T) {
	a := &App{}
	sharedCtx, releaseShared := a.acquireContinuityShared(context.Background())

	writerWaiting := make(chan struct{})
	writerDone := make(chan struct{})
	go func() {
		close(writerWaiting)
		_, releaseExclusive := a.acquireContinuityExclusive(context.Background())
		releaseExclusive()
		close(writerDone)
	}()
	<-writerWaiting
	time.Sleep(20 * time.Millisecond)

	done := make(chan struct{})
	go func() {
		releaseRead := a.acquireContinuityRead(sharedCtx)
		releaseRead()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("nested registry read deadlocked inside shared mutation while DNS writer was pending")
	}

	releaseShared()
	select {
	case <-writerDone:
	case <-time.After(time.Second):
		t.Fatal("pending DNS writer did not proceed after shared mutation completed")
	}
}

func TestOrdinaryMutationsRemainParallelButDNSMutationIsExclusive(t *testing.T) {
	a := &App{}
	var ordinary inspectContainer
	ordinary.Config.Image = "nginx:latest"
	ordinary.Config.ExposedPorts = map[string]struct{}{"80/tcp": {}}
	var dns inspectContainer
	dns.Config.Image = "adguard/adguardhome:latest"
	dns.Config.ExposedPorts = map[string]struct{}{"53/udp": {}}

	_, release1, exclusive1 := a.acquireContinuityMutation(context.Background(), ordinary)
	if exclusive1 {
		t.Fatal("ordinary mutation unexpectedly acquired exclusive continuity")
	}

	ordinary2 := make(chan struct{})
	release2Ch := make(chan func(), 1)
	go func() {
		_, release2, exclusive2 := a.acquireContinuityMutation(context.Background(), ordinary)
		if exclusive2 {
			return
		}
		release2Ch <- release2
		close(ordinary2)
	}()
	select {
	case <-ordinary2:
	case <-time.After(time.Second):
		t.Fatal("ordinary mutations on separate pipelines were unnecessarily serialized")
	}
	release2 := <-release2Ch

	dnsEntered := make(chan struct{})
	go func() {
		_, releaseDNS, _ := a.acquireContinuityMutation(context.Background(), dns)
		close(dnsEntered)
		releaseDNS()
	}()
	select {
	case <-dnsEntered:
		t.Fatal("DNS mutation entered while ordinary mutations were active")
	case <-time.After(30 * time.Millisecond):
	}

	release2()
	release1()
	select {
	case <-dnsEntered:
	case <-time.After(time.Second):
		t.Fatal("DNS mutation did not enter after ordinary mutations completed")
	}
}

func TestNextDispatchableUpdateSkipsBusyHostWithoutHeadOfLineBlocking(t *testing.T) {
	pending := []updateRequest{
		{JobID: 1, HostID: 2, Container: "dns"},
		{JobID: 2, HostID: 2, Container: "agent"},
		{JobID: 3, HostID: 5, Container: "media"},
	}
	idx := nextDispatchableUpdate(pending, map[int64]bool{2: true})
	if idx != 2 {
		t.Fatalf("expected scheduler to bypass busy host and dispatch host 5, got index %d", idx)
	}
}

func TestNextDispatchableUpdatePreservesFIFOAmongEligibleHosts(t *testing.T) {
	pending := []updateRequest{
		{JobID: 10, HostID: 3, Container: "first"},
		{JobID: 11, HostID: 4, Container: "second"},
	}
	idx := nextDispatchableUpdate(pending, map[int64]bool{})
	if idx != 0 {
		t.Fatalf("expected first eligible job to retain FIFO order, got index %d", idx)
	}
}

func TestTransferredContinuityMarkerDoesNotInheritCaptureCancellation(t *testing.T) {
	a := &App{}
	captureCtx, cancelCapture := context.WithCancel(context.Background())
	lockedCaptureCtx, release := a.acquireContinuityShared(captureCtx)
	exclusive := false
	defer release()
	if exclusiveMarker, _ := lockedCaptureCtx.Value(continuityExclusiveKey{}).(bool); exclusiveMarker {
		t.Fatal("shared capture unexpectedly marked exclusive")
	}

	// This models preflight's bounded restoreCtx ending immediately after the
	// restore point has been created. The continuity lock is intentionally still
	// owned by the update pipeline at this point.
	cancelCapture()
	if lockedCaptureCtx.Err() == nil {
		t.Fatal("test setup expected the capture context to be cancelled")
	}

	jobCtx, cancelJob := context.WithCancel(context.Background())
	defer cancelJob()
	transferred := withHeldContinuityMarker(jobCtx, exclusive)
	if err := transferred.Err(); err != nil {
		t.Fatalf("transferred continuity marker inherited capture cancellation: %v", err)
	}

	// Nested registry work from the same pipeline must recognize the transferred
	// marker and must not try to acquire another RLock while an exclusive writer
	// could be pending.
	done := make(chan struct{})
	go func() {
		releaseRead := a.acquireContinuityRead(transferred)
		releaseRead()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("transferred continuity marker did not preserve nested-read ownership")
	}
}

func TestTransferredExclusiveContinuityMarkerUsesDurableJobContext(t *testing.T) {
	_, cancelCapture := context.WithCancel(context.Background())
	cancelCapture()
	jobCtx := context.Background()
	transferred := withHeldContinuityMarker(jobCtx, true)
	if err := transferred.Err(); err != nil {
		t.Fatalf("exclusive marker unexpectedly inherited unrelated capture cancellation: %v", err)
	}
	if held, _ := transferred.Value(continuityExclusiveKey{}).(bool); !held {
		t.Fatal("exclusive continuity ownership marker was not preserved")
	}
	if held, _ := transferred.Value(continuitySharedKey{}).(bool); held {
		t.Fatal("exclusive continuity context must not also be marked shared")
	}
}
