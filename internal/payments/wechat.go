package payments

import (
	"bytes"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/rsa"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"time"
)

const wechatGateway = "https://api.mch.weixin.qq.com"

var configID = regexp.MustCompile(`^[A-Za-z0-9_-]{1,128}$`)

type WeChatConfig struct {
	AppID, MerchantID, CertificateSerial, NotifyURL string
	PrivateKey                                      *rsa.PrivateKey
	APIv3Key                                        []byte
	// PlatformKeys is an explicitly trusted key/certificate set indexed by the
	// exact Wechatpay-Serial value (including PUB_KEY_ID_ ids in public-key mode).
	PlatformKeys map[string]*rsa.PublicKey
	HTTPClient   *http.Client
}
type WeChat struct {
	cfg  WeChatConfig
	http *http.Client
}

var _ Provider = (*WeChat)(nil)

func NewWeChat(c WeChatConfig) (*WeChat, error) {
	if c.AppID == "" || c.MerchantID == "" || c.CertificateSerial == "" || c.NotifyURL == "" || c.PrivateKey == nil || len(c.APIv3Key) == 0 || len(c.PlatformKeys) == 0 {
		return nil, ErrUnconfigured
	}
	if !configID.MatchString(c.AppID) || !configID.MatchString(c.MerchantID) || !configID.MatchString(c.CertificateSerial) || !trustedHTTPS(c.NotifyURL) || !validPrivateKey(c.PrivateKey) || len(c.APIv3Key) != 32 {
		return nil, ErrInvalid
	}
	keys := make(map[string]*rsa.PublicKey, len(c.PlatformKeys))
	for id, key := range c.PlatformKeys {
		if !configID.MatchString(id) || !validPublicKey(key) {
			return nil, ErrInvalid
		}
		keys[id] = key
	}
	c.PlatformKeys = keys
	c.APIv3Key = bytes.Clone(c.APIv3Key)
	return &WeChat{cfg: c, http: client(c.HTTPClient)}, nil
}
func (w *WeChat) ID() string         { return "wechat" }
func (w *WeChat) MerchantID() string { return w.cfg.MerchantID }
func (w *WeChat) verify(h http.Header, b []byte) error {
	for _, k := range []string{"Wechatpay-Timestamp", "Wechatpay-Nonce", "Wechatpay-Serial", "Wechatpay-Signature"} {
		if len(h.Values(k)) != 1 || h.Get(k) == "" {
			return ErrVerification
		}
	}
	ts := h.Get("Wechatpay-Timestamp")
	n, e := strconv.ParseInt(ts, 10, 64)
	if e != nil || strconv.FormatInt(n, 10) != ts {
		return ErrVerification
	}
	now := time.Now().Unix()
	if n < now-300 || n > now+300 {
		return ErrVerification
	}
	key := w.cfg.PlatformKeys[h.Get("Wechatpay-Serial")]
	return verifyRSA(key, []byte(ts+"\n"+h.Get("Wechatpay-Nonce")+"\n"+string(b)+"\n"), h.Get("Wechatpay-Signature"))
}
func (w *WeChat) call(ctx context.Context, method, path string, payload any) ([]byte, int, error) {
	var body []byte
	var e error
	if payload != nil {
		body, e = json.Marshal(payload)
		if e != nil {
			return nil, 0, ErrInvalid
		}
	}
	ts := strconv.FormatInt(time.Now().Unix(), 10)
	nonce := make([]byte, 16)
	if _, e = rand.Read(nonce); e != nil {
		return nil, 0, ErrUnavailable
	}
	ns := hex.EncodeToString(nonce)
	sig, e := signRSA(w.cfg.PrivateKey, []byte(method+"\n"+path+"\n"+ts+"\n"+ns+"\n"+string(body)+"\n"))
	if e != nil {
		return nil, 0, e
	}
	req, e := http.NewRequestWithContext(ctx, method, wechatGateway+path, bytes.NewReader(body))
	if e != nil {
		return nil, 0, ErrInvalid
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", fmt.Sprintf(`WECHATPAY2-SHA256-RSA2048 mchid="%s",nonce_str="%s",timestamp="%s",serial_no="%s",signature="%s"`, w.cfg.MerchantID, ns, ts, w.cfg.CertificateSerial, sig))
	resp, e := w.http.Do(req)
	if e != nil {
		return nil, 0, ErrUnavailable
	}
	b, e := readBody(resp)
	if e != nil {
		return nil, resp.StatusCode, e
	}
	if e = w.verify(resp.Header, b); e != nil {
		return nil, resp.StatusCode, e
	}
	return b, resp.StatusCode, nil
}
func (w *WeChat) Create(ctx context.Context, r Request) (Session, error) {
	if e := validRequest(r); e != nil {
		return Session{}, e
	}
	fen, _ := toFen(r.Amount)
	if fen > math.MaxInt32 {
		return Session{}, ErrInvalid
	}
	payload := map[string]any{"appid": w.cfg.AppID, "mchid": w.cfg.MerchantID, "description": r.Description, "out_trade_no": r.OrderID, "time_expire": r.ExpiresAt.Format(time.RFC3339), "notify_url": w.cfg.NotifyURL, "amount": map[string]any{"total": fen, "currency": "CNY"}}
	b, status, e := w.call(ctx, "POST", "/v3/pay/transactions/native", payload)
	if e != nil {
		return Session{}, e
	}
	if status != 200 {
		return Session{}, ErrUnavailable
	}
	var response struct {
		CodeURL string `json:"code_url"`
	}
	if e = strictJSON(b, &response); e != nil {
		return Session{}, e
	}
	u, e := url.Parse(response.CodeURL)
	if e != nil || u.Scheme != "weixin" || u.Host != "wxpay" || u.Path != "/bizpayurl" || u.User != nil || u.Fragment != "" || u.RawQuery == "" {
		return Session{}, ErrVerification
	}
	return Session{Kind: "qr", URL: response.CodeURL, ExpiresAt: r.ExpiresAt}, nil
}

type wechatTrade struct {
	AppID         string `json:"appid"`
	MerchantID    string `json:"mchid"`
	OrderID       string `json:"out_trade_no"`
	TransactionID string `json:"transaction_id"`
	State         string `json:"trade_state"`
	SuccessTime   string `json:"success_time"`
	Amount        struct {
		Total    int64  `json:"total"`
		Currency string `json:"currency"`
	} `json:"amount"`
}

func (w *WeChat) trade(b []byte, order string) (Result, error) {
	var v wechatTrade
	if e := strictJSON(b, &v); e != nil {
		return Result{}, e
	}
	if v.AppID != w.cfg.AppID || v.MerchantID != w.cfg.MerchantID || !identifier.MatchString(v.OrderID) || (order != "" && order != v.OrderID) || v.Amount.Currency != "CNY" {
		return Result{}, ErrVerification
	}
	amount, e := fenAmount(v.Amount.Total)
	if e != nil {
		return Result{}, e
	}
	r := Result{OrderID: v.OrderID, TransactionID: v.TransactionID, Amount: amount, Currency: "CNY"}
	switch v.State {
	case "SUCCESS":
		r.Status = "PAID"
	case "NOTPAY", "USERPAYING":
		r.Status = "PENDING"
	case "CLOSED", "REVOKED":
		r.Status = "CLOSED"
	default:
		return Result{}, ErrUnavailable
	}
	if r.Status == "PAID" {
		if r.TransactionID == "" {
			return Result{}, ErrVerification
		}
		r.PaidAt, e = time.Parse(time.RFC3339, v.SuccessTime)
		if e != nil {
			return Result{}, ErrVerification
		}
	}
	return r, nil
}
func wechatNotFound(b []byte, status int, code string) bool {
	var v struct {
		Code string `json:"code"`
	}
	return status == 404 && strictJSON(b, &v) == nil && v.Code == code
}
func (w *WeChat) Query(ctx context.Context, order string) (Result, error) {
	if !identifier.MatchString(order) {
		return Result{}, ErrInvalid
	}
	b, status, e := w.call(ctx, "GET", "/v3/pay/transactions/out-trade-no/"+order+"?mchid="+url.QueryEscape(w.cfg.MerchantID), nil)
	if e != nil {
		return Result{}, e
	}
	if wechatNotFound(b, status, "ORDER_NOT_EXIST") {
		return Result{OrderID: order, Status: "NOT_FOUND"}, nil
	}
	if status != 200 {
		return Result{}, ErrUnavailable
	}
	return w.trade(b, order)
}
func (w *WeChat) Close(ctx context.Context, order string) error {
	if !identifier.MatchString(order) {
		return ErrInvalid
	}
	_, status, e := w.call(ctx, "POST", "/v3/pay/transactions/out-trade-no/"+order+"/close", map[string]string{"mchid": w.cfg.MerchantID})
	if e != nil {
		return e
	}
	if status != 204 {
		return ErrUnavailable
	}
	return nil
}
func (w *WeChat) VerifyNotification(h http.Header, b []byte) (Result, error) {
	if len(b) > maxBody {
		return Result{}, ErrVerification
	}
	if e := w.verify(h, b); e != nil {
		return Result{}, e
	}
	var n struct {
		Event    string `json:"event_type"`
		Type     string `json:"resource_type"`
		Resource struct {
			Algorithm      string `json:"algorithm"`
			OriginalType   string `json:"original_type"`
			Ciphertext     string `json:"ciphertext"`
			Nonce          string `json:"nonce"`
			AssociatedData string `json:"associated_data"`
		} `json:"resource"`
	}
	if e := strictJSON(b, &n); e != nil {
		return Result{}, e
	}
	if n.Event != "TRANSACTION.SUCCESS" || n.Type != "encrypt-resource" || n.Resource.Algorithm != "AEAD_AES_256_GCM" || n.Resource.OriginalType != "transaction" {
		return Result{}, ErrVerification
	}
	encrypted, e := base64.StdEncoding.DecodeString(n.Resource.Ciphertext)
	if e != nil {
		return Result{}, ErrVerification
	}
	block, e := aes.NewCipher(w.cfg.APIv3Key)
	if e != nil {
		return Result{}, ErrVerification
	}
	gcm, e := cipher.NewGCM(block)
	if e != nil || len(n.Resource.Nonce) != gcm.NonceSize() {
		return Result{}, ErrVerification
	}
	plain, e := gcm.Open(nil, []byte(n.Resource.Nonce), encrypted, []byte(n.Resource.AssociatedData))
	if e != nil {
		return Result{}, ErrVerification
	}
	r, e := w.trade(plain, "")
	if e != nil {
		return Result{}, e
	}
	if r.Status != "PAID" {
		return Result{}, ErrVerification
	}
	return r, nil
}
func (w *WeChat) refundResult(b []byte, r RefundRequest, submission bool) (RefundResult, error) {
	var v struct {
		OrderID          string `json:"out_trade_no"`
		TransactionID    string `json:"transaction_id"`
		RefundID         string `json:"out_refund_no"`
		ProviderRefundID string `json:"refund_id"`
		Channel          string `json:"channel"`
		Status           string `json:"status"`
		Amount           struct {
			Refund   int64  `json:"refund"`
			Total    int64  `json:"total"`
			Currency string `json:"currency"`
		} `json:"amount"`
	}
	if e := strictJSON(b, &v); e != nil {
		return RefundResult{}, e
	}
	amount, e := fenAmount(v.Amount.Refund)
	total, f := fenAmount(v.Amount.Total)
	if e != nil || f != nil || v.OrderID != r.OrderID || v.TransactionID != r.TransactionID || v.RefundID != r.RefundID || v.ProviderRefundID == "" || v.Channel != "ORIGINAL" || v.Amount.Currency != "CNY" || amount != r.Amount || total != r.TotalAmount {
		return RefundResult{}, ErrVerification
	}
	result := RefundResult{RefundID: r.RefundID, TransactionID: r.TransactionID, Amount: amount}
	switch v.Status {
	case "SUCCESS":
		result.Status = "SUCCEEDED"
	case "PROCESSING":
		result.Status = "PENDING"
	case "CLOSED":
		result.Status = "CLOSED"
	default:
		return RefundResult{}, ErrUnavailable
	}
	if submission && result.Status == "SUCCEEDED" {
		result.Status = "PENDING"
	}
	return result, nil
}
func (w *WeChat) Refund(ctx context.Context, r RefundRequest) (RefundResult, error) {
	if e := validRefund(r); e != nil {
		return RefundResult{}, e
	}
	amount, _ := toFen(r.Amount)
	total, _ := toFen(r.TotalAmount)
	if total > math.MaxInt32 {
		return RefundResult{}, ErrInvalid
	}
	payload := map[string]any{"transaction_id": r.TransactionID, "out_refund_no": r.RefundID, "amount": map[string]any{"refund": amount, "total": total, "currency": "CNY"}}
	if r.Reason != "" {
		payload["reason"] = r.Reason
	}
	b, status, e := w.call(ctx, "POST", "/v3/refund/domestic/refunds", payload)
	if e != nil {
		return RefundResult{}, e
	}
	if status != 200 {
		return RefundResult{}, ErrUnavailable
	}
	return w.refundResult(b, r, true)
}
func (w *WeChat) QueryRefund(ctx context.Context, r RefundRequest) (RefundResult, error) {
	if e := validRefund(r); e != nil {
		return RefundResult{}, e
	}
	b, status, e := w.call(ctx, "GET", "/v3/refund/domestic/refunds/"+r.RefundID, nil)
	if e != nil {
		return RefundResult{}, e
	}
	if wechatNotFound(b, status, "RESOURCE_NOT_EXISTS") {
		return RefundResult{RefundID: r.RefundID, TransactionID: r.TransactionID, Status: "NOT_FOUND"}, nil
	}
	if status != 200 {
		return RefundResult{}, ErrUnavailable
	}
	return w.refundResult(b, r, false)
}
