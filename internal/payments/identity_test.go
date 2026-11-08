package payments

import "testing"

func TestMerchantIdentityBindsApplicationButAllowsKeyRotation(t *testing.T) {
	first, second := testKey(t), testKey(t)
	w, err := NewWeChat(wcConfig(first, first, nil))
	if err != nil {
		t.Fatal(err)
	}
	rotated, err := NewWeChat(wcConfig(second, second, nil))
	if err != nil {
		t.Fatal(err)
	}
	c := wcConfig(first, first, nil)
	c.AppID = "another-app"
	changed, err := NewWeChat(c)
	if err != nil {
		t.Fatal(err)
	}
	if w.ConfigurationID() == "" || w.ConfigurationID() != rotated.ConfigurationID() || w.ConfigurationID() == changed.ConfigurationID() {
		t.Fatal("WeChat commercial identity did not distinguish app from rotatable keys")
	}
	a, err := NewAlipay(aliConfig(first, first, nil))
	if err != nil {
		t.Fatal(err)
	}
	ar, err := NewAlipay(aliConfig(second, second, nil))
	if err != nil {
		t.Fatal(err)
	}
	ac := aliConfig(first, first, nil)
	ac.AppID = "another-app"
	changedA, err := NewAlipay(ac)
	if err != nil {
		t.Fatal(err)
	}
	if a.ConfigurationID() == "" || a.ConfigurationID() != ar.ConfigurationID() || a.ConfigurationID() == changedA.ConfigurationID() {
		t.Fatal("Alipay commercial identity did not distinguish app from rotatable keys")
	}
}
