package deposit

import "testing"

func TestAccumulatedAmount(t *testing.T) {
	tests := []struct {
		name        string
		current     string
		incoming    string
		expected    string
		wantAmount  string
		wantCompare int
		wantError   bool
	}{
		{name: "少付", current: "10", incoming: "20", expected: "100", wantAmount: "30", wantCompare: -1},
		{name: "足额", current: "40", incoming: "60", expected: "100", wantAmount: "100", wantCompare: 0},
		{name: "超付", current: "40", incoming: "70", expected: "100", wantAmount: "110", wantCompare: 1},
		{name: "超大整数", current: "0", incoming: "999999999999999999999", expected: "999999999999999999999", wantAmount: "999999999999999999999", wantCompare: 0},
		{name: "无效金额", current: "0", incoming: "0", expected: "1", wantError: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			amount, comparison, err := accumulatedAmount(test.current, test.incoming, test.expected)
			if (err != nil) != test.wantError || amount != test.wantAmount || comparison != test.wantCompare {
				t.Fatalf("accumulatedAmount() = %q, %d, %v", amount, comparison, err)
			}
		})
	}
}
