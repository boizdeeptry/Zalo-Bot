package daemon

import (
	"context"
	"sync/atomic"
	"testing"
)

func requireOnboardingLeaseGlobalsClean(t *testing.T) {
	t.Helper()
	onboardingTestMu.Lock()
	defer onboardingTestMu.Unlock()
	if onboardingTestActive != nil || onboardingProviderTransition != nil {
		t.Fatalf("onboarding lease globals are dirty: active=%p transition=%p",
			onboardingTestActive, onboardingProviderTransition)
	}
}

func TestOnboardingTestLeasePromotionOwnsCleanup(t *testing.T) {
	requireOnboardingLeaseGlobalsClean(t)
	owner, ok := beginAppOnboardingTest()
	if !ok || owner == nil {
		t.Fatal("beginAppOnboardingTest() did not return an owner lease")
	}
	var cancelCalls atomic.Int32
	if !owner.bindCancel(func() { cancelCalls.Add(1) }) {
		owner.finish()
		t.Fatal("owner.bindCancel() = false; want true")
	}

	onboardingMutationMu.Lock()
	transition, promoted := promoteAppOnboardingTest(context.Background(), owner)
	onboardingMutationMu.Unlock()
	if !promoted || transition == nil {
		owner.finish()
		t.Fatal("promoteAppOnboardingTest() did not promote exact owner")
	}

	owner.finish()
	select {
	case <-owner.done:
		transition.release()
		t.Fatal("stale Test finish closed a promoted owner's done channel")
	default:
	}
	if cancelCalls.Load() != 0 {
		transition.release()
		t.Fatalf("promotion canceled its own Test %d times", cancelCalls.Load())
	}
	onboardingTestMu.Lock()
	active, current := onboardingTestActive, onboardingProviderTransition
	onboardingTestMu.Unlock()
	if active != nil || current != transition {
		transition.release()
		t.Fatalf("promotion ownership = active %p transition %p; want nil/%p",
			active, current, transition)
	}

	transition.release()
	select {
	case <-owner.done:
	default:
		t.Fatal("exact transition release did not close the promoted owner")
	}
	owner.finish()
	transition.release()
	requireOnboardingLeaseGlobalsClean(t)

	fresh, ok := beginAppOnboardingTest()
	if !ok || fresh == owner {
		t.Fatal("fresh Test lease did not receive new pointer identity")
	}
	owner.finish()
	onboardingTestMu.Lock()
	active = onboardingTestActive
	onboardingTestMu.Unlock()
	if active != fresh {
		fresh.finish()
		t.Fatalf("stale owner finish cleared newer active lease: got %p want %p", active, fresh)
	}
	select {
	case <-fresh.done:
		fresh.finish()
		t.Fatal("stale owner finish closed newer active lease")
	default:
	}
	onboardingMutationMu.Lock()
	staleTransition, stalePromoted := promoteAppOnboardingTest(context.Background(), owner)
	onboardingMutationMu.Unlock()
	if stalePromoted || staleTransition != nil {
		fresh.finish()
		t.Fatal("stale owner promoted over the current Test lease")
	}
	onboardingMutationMu.Lock()
	freshTransition, freshPromoted := promoteAppOnboardingTest(context.Background(), fresh)
	onboardingMutationMu.Unlock()
	if !freshPromoted || freshTransition == nil {
		fresh.finish()
		t.Fatal("fresh owner did not promote")
	}
	transition.release()
	onboardingTestMu.Lock()
	current = onboardingProviderTransition
	onboardingTestMu.Unlock()
	if current != freshTransition {
		freshTransition.release()
		t.Fatalf("stale transition release cleared newer owner: got %p want %p", current, freshTransition)
	}
	select {
	case <-fresh.done:
		freshTransition.release()
		t.Fatal("stale transition release closed newer owner")
	default:
	}
	fresh.finish()
	freshTransition.release()
	select {
	case <-fresh.done:
	default:
		t.Fatal("fresh transition did not own final cleanup")
	}
	requireOnboardingLeaseGlobalsClean(t)
}

func TestOnboardingTestLeaseRejectsWrongCanceledOrForeignOwner(t *testing.T) {
	t.Run("wrong owner and bind", func(t *testing.T) {
		requireOnboardingLeaseGlobalsClean(t)
		owner, ok := beginAppOnboardingTest()
		if !ok {
			t.Fatal("begin owner = false")
		}
		wrong := &appOnboardingTestLease{}
		var rejectedCancel atomic.Int32
		if wrong.bindCancel(func() { rejectedCancel.Add(1) }) {
			owner.finish()
			t.Fatal("foreign owner bound cancellation")
		}
		if rejectedCancel.Load() != 1 {
			owner.finish()
			t.Fatalf("rejected cancel calls = %d; want 1", rejectedCancel.Load())
		}
		onboardingMutationMu.Lock()
		transition, promoted := promoteAppOnboardingTest(context.Background(), wrong)
		onboardingMutationMu.Unlock()
		if promoted || transition != nil {
			owner.finish()
			t.Fatal("foreign owner promoted")
		}
		owner.finish()
		requireOnboardingLeaseGlobalsClean(t)
	})

	t.Run("canceled context", func(t *testing.T) {
		requireOnboardingLeaseGlobalsClean(t)
		owner, ok := beginAppOnboardingTest()
		if !ok {
			t.Fatal("begin owner = false")
		}
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		onboardingMutationMu.Lock()
		transition, promoted := promoteAppOnboardingTest(ctx, owner)
		onboardingMutationMu.Unlock()
		if promoted || transition != nil {
			owner.finish()
			t.Fatal("canceled owner promoted")
		}
		owner.finish()
		requireOnboardingLeaseGlobalsClean(t)
	})

	t.Run("foreign transition", func(t *testing.T) {
		requireOnboardingLeaseGlobalsClean(t)
		owner, ok := beginAppOnboardingTest()
		if !ok {
			t.Fatal("begin owner = false")
		}
		var cancelCalls atomic.Int32
		if !owner.bindCancel(func() { cancelCalls.Add(1) }) {
			owner.finish()
			t.Fatal("bind owner = false")
		}
		onboardingMutationMu.Lock()
		foreign, acquired := acquireAppOnboardingProviderTransitionLease()
		transition, promoted := promoteAppOnboardingTest(context.Background(), owner)
		onboardingMutationMu.Unlock()
		if !acquired || foreign == nil || promoted || transition != nil {
			owner.finish()
			if foreign != nil {
				foreign.release()
			}
			t.Fatalf("foreign transition result acquired=%v promoted=%v", acquired, promoted)
		}
		if cancelCalls.Load() != 0 {
			owner.finish()
			foreign.release()
			t.Fatal("acquisition invoked cancel while holding the Test mutex")
		}
		owner.finish()
		foreign.release()
		requireOnboardingLeaseGlobalsClean(t)
	})
}
