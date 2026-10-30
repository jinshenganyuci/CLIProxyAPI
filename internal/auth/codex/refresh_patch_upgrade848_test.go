package codex

import (
	"errors"
	"testing"
)

func TestUpgrade848RefreshPatchFailureIdentityAndWriteAuthority(t *testing.T) {
	for _, failedSIWC := range []bool{false, true} {
		name := "outer_failure"
		if failedSIWC {
			name = "siwc_failure"
		}
		t.Run(name, func(t *testing.T) {
			before := map[string]any{"refresh_token": "old-outer", "siwc": map[string]any{"refresh_token": "old-siwc"}}
			disk := map[string]any{"refresh_token": " disk-outer\n", "siwc": map[string]any{"refresh_token": "\tdisk-siwc "}, "use_siwc": false, "weight": 7}
			after := map[string]any{"refresh_token": "new-outer", "siwc": map[string]any{"refresh_token": "new-siwc"}, "use_siwc": true, "weight": 99}
			patch := NewCredentialRefreshPatchWithDiskBaseline(before, disk, after, failedSIWC, !failedSIWC)
			terminal := errors.New("invalid_grant: synthetic rejected credential")
			if failedSIWC {
				patch.SetPartitionErrors(nil, WrapSIWCRefreshError(terminal))
				if got := patch.FailedSIWCRefreshToken(); got != "disk-siwc" {
					t.Errorf("submitted SIWC identity = %q, want trimmed disk token", got)
				}
				disk["siwc"].(map[string]any)["refresh_token"] = "newer-siwc"
			} else {
				patch.SetPartitionErrors(terminal, nil)
				if got := patch.FailedOuterRefreshToken(); got != "disk-outer" {
					t.Errorf("submitted outer identity = %q, want trimmed disk token", got)
				}
				disk["refresh_token"] = "newer-outer"
			}
			merged := patch.Apply(disk)
			wantOuter, wantSIWC := "newer-outer", "new-siwc"
			if failedSIWC {
				wantOuter, wantSIWC = "new-outer", "newer-siwc"
			}
			if merged["refresh_token"] != wantOuter || merged["siwc"].(map[string]any)["refresh_token"] != wantSIWC || merged["use_siwc"] != false || merged["weight"] != 7 {
				t.Fatal("failed partition or unrelated controls gained write authority")
			}
			patch.SetUpstreamRefreshed(false)
			if patch.UpstreamRefreshed() || !patch.CanApplyTo(disk) {
				t.Fatal("disk adoption must remain distinct from upstream refresh success")
			}
		})
	}
}

func TestUpgrade848PartitionErrorsRetainTypedCauses(t *testing.T) {
	outer, siwc := errors.New("synthetic outer"), errors.New("synthetic SIWC")
	wrapped := WrapSIWCRefreshError(siwc)
	combined := NewPartitionRefreshError(outer, wrapped)
	var typed *SIWCRefreshError
	if !errors.Is(combined, outer) || !errors.Is(combined, siwc) || !errors.As(combined, &typed) || !typed.IsSIWCRefreshError() || WrapSIWCRefreshError(wrapped) != wrapped {
		t.Fatal("partition error classification lost its typed causes")
	}
}
