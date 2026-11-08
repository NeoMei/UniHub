package payments

import "testing"

func TestExactCNY6(t *testing.T) {
	for _, tt := range []struct {
		in   string
		fen  int64
		yuan string
	}{{"10000", 1, "0.01"}, {"199000000", 19900, "199.00"}, {"9223372036854770000", 922337203685477, "9223372036854.77"}} {
		fen, err := toFen(tt.in)
		if err != nil || fen != tt.fen {
			t.Fatalf("%s: got %d %v", tt.in, fen, err)
		}
		if got := fenYuan(fen); got != tt.yuan {
			t.Fatalf("yuan %s", got)
		}
		got, err := yuanAmount(tt.yuan)
		if err != nil || got != tt.in {
			t.Fatalf("roundtrip %s %v", got, err)
		}
	}
	for _, in := range []string{"", "0", "-10000", "+10000", "010000", "10001", "1.00", "1e6", "9223372036854780000"} {
		if _, err := toFen(in); err == nil {
			t.Errorf("accepted %q", in)
		}
	}
	for _, in := range []string{"1.001", "1e2", "-1", "+1.00", "01.00", ".01", "1.", "0.00"} {
		if _, err := yuanAmount(in); err == nil {
			t.Errorf("accepted yuan %q", in)
		}
	}
}
