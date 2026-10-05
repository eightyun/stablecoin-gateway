package sweep

import (
	"errors"
	"testing"
	"time"
)

func TestValidatePolicyRejectsInvalidMaximum(t *testing.T) {
	for _, maximum := range []string{"0", "49", "invalid"} {
		err := validatePolicy(Policy{
			AssetID: "asset", MinimumAmount: "50", MaximumAmount: maximum,
			MaxSnapshotAge: time.Minute,
		})
		if !errors.Is(err, ErrInvalidPolicy) {
			t.Fatalf("maximum=%q validatePolicy() error = %v", maximum, err)
		}
	}
}
