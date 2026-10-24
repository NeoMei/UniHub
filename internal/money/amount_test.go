package money

import "testing"

func TestParsePositiveMicroAmounts(t *testing.T) {
	for _, tc := range []struct {
		input string
		want  int64
	}{
		{"1", 1}, {"1000000", 1000000}, {"1000000000000000", 1000000000000000},
	} {
		t.Run(tc.input, func(t *testing.T) {
			got, err := Parse(tc.input)
			if err != nil || got != tc.want {
				t.Fatalf("Parse(%q) = %d, %v; want %d", tc.input, got, err, tc.want)
			}
		})
	}
}

func TestParseRejectsInvalidOrOutOfRangeAmounts(t *testing.T) {
	for _, input := range []string{"", "0", "-1", "+1", "1.2", "1e6", "1E6", " 1", "1 ", "1\n", "１", "1000000000000001", "9223372036854775808", "999999999999999999999999999"} {
		t.Run(input, func(t *testing.T) {
			if got, err := Parse(input); err == nil {
				t.Fatalf("Parse(%q) accepted %d", input, got)
			}
		})
	}
}

func TestParseRechargeRequiresWholeCents(t *testing.T) {
	for _, tc := range []struct {
		input string
		want  int64
	}{{"10000", 10000}, {"1000000", 1000000}, {"1000000000000000", 1000000000000000}} {
		got, err := ParseRecharge(tc.input)
		if err != nil || got != tc.want {
			t.Errorf("ParseRecharge(%q) = %d, %v", tc.input, got, err)
		}
	}
	for _, input := range []string{"1", "9999", "10001", "1000001", "0", "-10000", "1.00", "1e6", "1000000000000001"} {
		if got, err := ParseRecharge(input); err == nil {
			t.Errorf("ParseRecharge(%q) accepted %d", input, got)
		}
	}
}
