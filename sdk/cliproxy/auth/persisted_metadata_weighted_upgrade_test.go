package auth

import (
	"testing"

	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
)

func TestPersistedMetadataDisableForgetsWeightedCredit(t *testing.T) {
	manager, _, auth := newMetadataControlsFixture(t, nil)
	weighted := &WeightedRoundRobinSelector{}
	manager.SetSelector(weighted)
	peer, errRegister := manager.Register(t.Context(), &Auth{
		ID: auth.ID + "-peer", Provider: auth.Provider, Status: StatusActive,
		Metadata: map[string]any{"access_token": "synthetic-peer-token"},
	})
	if errRegister != nil {
		t.Fatal(errRegister)
	}
	pick := func(current *Auth) string {
		t.Helper()
		selected, errPick := weighted.Pick(t.Context(), auth.Provider, "", cliproxyexecutor.Options{}, []*Auth{current, peer})
		if errPick != nil || selected == nil {
			t.Fatalf("weighted selection: auth=%v error=%v", selected, errPick)
		}
		return selected.ID
	}
	if id := pick(auth); id != auth.ID {
		t.Fatalf("initial selection = %q, want %q", id, auth.ID)
	}
	if _, errDisable := manager.UpdateAndPersistMetadata(t.Context(), auth.ID, func(current *Auth) error {
		current.Disabled = true
		return nil
	}); errDisable != nil {
		t.Fatal(errDisable)
	}
	reenabled, errEnable := manager.UpdateAndPersistMetadata(t.Context(), auth.ID, func(current *Auth) error {
		current.Disabled = false
		return nil
	})
	if errEnable != nil {
		t.Fatal(errEnable)
	}
	if reenabled.RegistrationEpoch != auth.RegistrationEpoch || reenabled.Generation <= auth.Generation {
		t.Fatal("metadata toggle lost its registration or generation fence")
	}
	if id := pick(reenabled); id != auth.ID {
		t.Fatalf("re-enabled selection = %q, want %q after forgetting its old weighted debt", id, auth.ID)
	}
}
