package billing

import "testing"

func TestNativeConversionUsesFrozenCumulativeCeiling(t *testing.T) {
	c := NativeConversion{Version: "lr-quota-usd-cny-v1", Numerator: "73", Denominator: "5", Rounding: "lr-cumulative-ceil-v1"}
	for _, test := range []struct{ quota, amount string }{{"0", "0"}, {"1", "15"}, {"2", "30"}, {"5", "73"}} {
		got, err := nativeAmount(test.quota, c)
		if err != nil || got != test.amount {
			t.Fatalf("quota %s got %s, %v", test.quota, got, err)
		}
	}
	c.Denominator = "0"
	if _, err := nativeAmount("1", c); err == nil {
		t.Fatal("zero denominator accepted")
	}
	c.Denominator = "1"
	if _, err := nativeAmount("9223372036854775807", c); err == nil {
		t.Fatal("overflow accepted")
	}
}
