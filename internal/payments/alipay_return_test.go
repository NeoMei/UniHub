package payments

import (
	"context"
	"net/url"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestAlipaySignsCheckoutReturnURL(t *testing.T) {
	merchant, platform := testKey(t), testKey(t)
	p, err := NewAlipay(aliConfig(merchant, platform, nil))
	require.NoError(t, err)
	r := Request{OrderID: "ct_return1", Amount: "20000000", Description: "Topup", ExpiresAt: time.Now().Add(time.Minute), ReturnURL: "https://llmrouter.quukk.com/wallet?cash_checkout=ct_return1"}
	session, err := p.Create(context.Background(), r)
	require.NoError(t, err)
	u, err := url.Parse(session.URL)
	require.NoError(t, err)
	assert.Equal(t, r.ReturnURL, u.Query().Get("return_url"))
	assert.NoError(t, verifyRSA(&p.cfg.PrivateKey.PublicKey, []byte(canonical(u.Query(), false)), u.Query().Get("sign")))
	r.ReturnURL = "javascript:alert(1)"
	_, err = p.Create(context.Background(), r)
	assert.ErrorIs(t, err, ErrInvalid)
	r.ReturnURL = ""
	session, err = p.Create(context.Background(), r)
	require.NoError(t, err)
	u, err = url.Parse(session.URL)
	require.NoError(t, err)
	assert.Equal(t, p.cfg.ReturnURL, u.Query().Get("return_url"))
}
