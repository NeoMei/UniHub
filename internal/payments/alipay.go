package payments

import (
	"context"
	"crypto/rsa"
	"encoding/json"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"
)

const alipayGateway = "https://openapi.alipay.com/gateway.do"

var chinaTime = time.FixedZone("CST", 8*3600)

const alipayTime = "2006-01-02 15:04:05"

type AlipayConfig struct {
	AppID, SellerID, NotifyURL, ReturnURL string
	PrivateKey                            *rsa.PrivateKey
	PublicKey                             *rsa.PublicKey
	HTTPClient                            *http.Client
}
type Alipay struct {
	cfg  AlipayConfig
	http *http.Client
}

var _ Provider = (*Alipay)(nil)

func NewAlipay(c AlipayConfig) (*Alipay, error) {
	if c.AppID == "" || c.SellerID == "" || c.NotifyURL == "" || c.ReturnURL == "" || c.PrivateKey == nil || c.PublicKey == nil {
		return nil, ErrUnconfigured
	}
	if !configID.MatchString(c.AppID) || !configID.MatchString(c.SellerID) || !trustedHTTPS(c.NotifyURL) || !trustedHTTPS(c.ReturnURL) || !validPrivateKey(c.PrivateKey) || !validPublicKey(c.PublicKey) {
		return nil, ErrInvalid
	}
	return &Alipay{cfg: c, http: client(c.HTTPClient)}, nil
}
func (a *Alipay) ID() string         { return "alipay" }
func (a *Alipay) MerchantID() string { return a.cfg.SellerID }
func canonical(v url.Values, notification bool) string {
	keys := make([]string, 0, len(v))
	for k := range v {
		if k != "sign" && (!notification || k != "sign_type") && v.Get(k) != "" {
			keys = append(keys, k)
		}
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		parts = append(parts, k+"="+v.Get(k))
	}
	return strings.Join(parts, "&")
}
func (a *Alipay) params(method string, biz any, page bool) (url.Values, error) {
	b, e := json.Marshal(biz)
	if e != nil {
		return nil, ErrInvalid
	}
	v := url.Values{"app_id": {a.cfg.AppID}, "method": {method}, "format": {"JSON"}, "charset": {"utf-8"}, "sign_type": {"RSA2"}, "timestamp": {time.Now().In(chinaTime).Format(alipayTime)}, "version": {"1.0"}, "biz_content": {string(b)}}
	if page {
		v.Set("notify_url", a.cfg.NotifyURL)
		v.Set("return_url", a.cfg.ReturnURL)
	}
	sig, e := signRSA(a.cfg.PrivateKey, []byte(canonical(v, false)))
	if e != nil {
		return nil, e
	}
	v.Set("sign", sig)
	return v, nil
}
func (a *Alipay) Create(ctx context.Context, r Request) (Session, error) {
	if e := ctx.Err(); e != nil {
		return Session{}, ErrUnavailable
	}
	if e := validRequest(r); e != nil {
		return Session{}, e
	}
	fen, _ := toFen(r.Amount)
	biz := map[string]string{"out_trade_no": r.OrderID, "total_amount": fenYuan(fen), "subject": r.Description, "product_code": "FAST_INSTANT_TRADE_PAY", "time_expire": r.ExpiresAt.In(chinaTime).Format(alipayTime)}
	v, e := a.params("alipay.trade.page.pay", biz, true)
	if e != nil {
		return Session{}, e
	}
	return Session{Kind: "redirect", URL: alipayGateway + "?" + v.Encode(), ExpiresAt: r.ExpiresAt}, nil
}

type alipayResponse struct {
	Code           string `json:"code"`
	SubCode        string `json:"sub_code"`
	AppID          string `json:"app_id"`
	SellerID       string `json:"seller_id"`
	OrderID        string `json:"out_trade_no"`
	TransactionID  string `json:"trade_no"`
	Status         string `json:"trade_status"`
	TotalAmount    string `json:"total_amount"`
	Currency       string `json:"trans_currency"`
	PaidAt         string `json:"send_pay_date"`
	RefundID       string `json:"out_request_no"`
	RefundAmount   string `json:"refund_amount"`
	RefundStatus   string `json:"refund_status"`
	RefundCurrency string `json:"refund_currency"`
}

func (a *Alipay) call(ctx context.Context, method string, biz any) (alipayResponse, error) {
	v, e := a.params(method, biz, false)
	if e != nil {
		return alipayResponse{}, e
	}
	// Alipay reads the request charset from the URL query string for POST
	// gateway calls. Keeping charset in the form body makes signatures fail
	// whenever biz_content contains non-ASCII text such as a Chinese refund
	// reason (isv.invalid-signature). It remains part of the signed parameter
	// set, but must not be sent a second time in the body.
	body := url.Values{}
	for key, values := range v {
		if key != "charset" {
			body[key] = values
		}
	}
	req, e := http.NewRequestWithContext(ctx, "POST", alipayGateway+"?charset=utf-8", strings.NewReader(body.Encode()))
	if e != nil {
		return alipayResponse{}, ErrInvalid
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, e := a.http.Do(req)
	if e != nil {
		return alipayResponse{}, ErrUnavailable
	}
	b, e := readBody(resp)
	if e != nil {
		return alipayResponse{}, e
	}
	if resp.StatusCode != 200 {
		return alipayResponse{}, ErrUnavailable
	}
	var envelope map[string]json.RawMessage
	if e = strictJSON(b, &envelope); e != nil {
		return alipayResponse{}, e
	}
	raw, ok := envelope[strings.ReplaceAll(method, ".", "_")+"_response"]
	if !ok {
		return alipayResponse{}, ErrVerification
	}
	var sig string
	if json.Unmarshal(envelope["sign"], &sig) != nil {
		return alipayResponse{}, ErrVerification
	}
	if e = verifyRSA(a.cfg.PublicKey, raw, sig); e != nil {
		return alipayResponse{}, e
	}
	var result alipayResponse
	if e = strictJSON(raw, &result); e != nil {
		return alipayResponse{}, e
	}
	if (result.AppID != "" && result.AppID != a.cfg.AppID) || (result.SellerID != "" && result.SellerID != a.cfg.SellerID) || (result.Currency != "" && result.Currency != "CNY") || (result.RefundCurrency != "" && result.RefundCurrency != "CNY") {
		return alipayResponse{}, ErrVerification
	}
	return result, nil
}
func alipayState(state string) (string, error) {
	switch state {
	case "WAIT_BUYER_PAY":
		return "PENDING", nil
	case "TRADE_SUCCESS", "TRADE_FINISHED":
		return "PAID", nil
	case "TRADE_CLOSED":
		return "CLOSED", nil
	default:
		return "", ErrUnavailable
	}
}
func (a *Alipay) Query(ctx context.Context, order string) (Result, error) {
	if !identifier.MatchString(order) {
		return Result{}, ErrInvalid
	}
	v, e := a.call(ctx, "alipay.trade.query", map[string]string{"out_trade_no": order})
	if e != nil {
		return Result{}, e
	}
	if v.Code == "40004" && v.SubCode == "ACQ.TRADE_NOT_EXIST" {
		return Result{OrderID: order, Status: "NOT_FOUND"}, nil
	}
	if v.Code != "10000" {
		return Result{}, ErrUnavailable
	}
	if v.OrderID != order {
		return Result{}, ErrVerification
	}
	status, e := alipayState(v.Status)
	if e != nil {
		return Result{}, e
	}
	amount, e := yuanAmount(v.TotalAmount)
	if e != nil {
		return Result{}, e
	}
	r := Result{OrderID: order, TransactionID: v.TransactionID, Amount: amount, Currency: "CNY", Status: status}
	if status == "PAID" && v.TransactionID == "" {
		return Result{}, ErrVerification
	}
	if v.PaidAt != "" {
		r.PaidAt, e = time.ParseInLocation(alipayTime, v.PaidAt, chinaTime)
		if e != nil {
			return Result{}, ErrVerification
		}
	}
	return r, nil
}
func (a *Alipay) Close(ctx context.Context, order string) error {
	if !identifier.MatchString(order) {
		return ErrInvalid
	}
	v, e := a.call(ctx, "alipay.trade.close", map[string]string{"out_trade_no": order})
	if e != nil {
		return e
	}
	if v.Code != "10000" {
		return ErrUnavailable
	}
	if v.OrderID != order {
		return ErrVerification
	}
	return nil
}
func (a *Alipay) VerifyNotification(_ http.Header, b []byte) (Result, error) {
	if len(b) > maxBody {
		return Result{}, ErrVerification
	}
	v, e := url.ParseQuery(string(b))
	if e != nil {
		return Result{}, ErrVerification
	}
	for _, values := range v {
		if len(values) != 1 {
			return Result{}, ErrVerification
		}
	}
	if v.Get("sign_type") != "RSA2" {
		return Result{}, ErrVerification
	}
	if e = verifyRSA(a.cfg.PublicKey, []byte(canonical(v, true)), v.Get("sign")); e != nil {
		return Result{}, e
	}
	if v.Get("app_id") != a.cfg.AppID || v.Get("seller_id") != a.cfg.SellerID || !identifier.MatchString(v.Get("out_trade_no")) || (v.Get("trans_currency") != "" && v.Get("trans_currency") != "CNY") {
		return Result{}, ErrVerification
	}
	state, e := alipayState(v.Get("trade_status"))
	if e != nil {
		return Result{}, e
	}
	amount, e := yuanAmount(v.Get("total_amount"))
	if e != nil {
		return Result{}, e
	}
	r := Result{OrderID: v.Get("out_trade_no"), TransactionID: v.Get("trade_no"), Amount: amount, Currency: "CNY", Status: state}
	if r.Status == "PAID" {
		if r.TransactionID == "" {
			return Result{}, ErrVerification
		}
		r.PaidAt, e = time.ParseInLocation(alipayTime, v.Get("gmt_payment"), chinaTime)
		if e != nil {
			return Result{}, ErrVerification
		}
	}
	return r, nil
}
func (a *Alipay) Refund(ctx context.Context, r RefundRequest) (RefundResult, error) {
	if e := validRefund(r); e != nil {
		return RefundResult{}, e
	}
	fen, _ := toFen(r.Amount)
	biz := map[string]string{"out_trade_no": r.OrderID, "trade_no": r.TransactionID, "out_request_no": r.RefundID, "refund_amount": fenYuan(fen)}
	if r.Reason != "" {
		biz["refund_reason"] = r.Reason
	}
	v, e := a.call(ctx, "alipay.trade.refund", biz)
	if e != nil {
		return RefundResult{}, e
	}
	if v.Code != "10000" {
		return RefundResult{}, ErrUnavailable
	}
	if v.OrderID != r.OrderID || v.TransactionID != r.TransactionID {
		return RefundResult{}, ErrVerification
	}
	// refund_fee is cumulative across the transaction, not this refund request.
	// An authenticated acceptance alone cannot confirm a particular refund.
	return RefundResult{RefundID: r.RefundID, TransactionID: r.TransactionID, Amount: r.Amount, Status: "PENDING"}, nil
}
func (a *Alipay) QueryRefund(ctx context.Context, r RefundRequest) (RefundResult, error) {
	if e := validRefund(r); e != nil {
		return RefundResult{}, e
	}
	v, e := a.call(ctx, "alipay.trade.fastpay.refund.query", map[string]string{"out_trade_no": r.OrderID, "trade_no": r.TransactionID, "out_request_no": r.RefundID})
	if e != nil {
		return RefundResult{}, e
	}
	if v.Code == "40004" && (v.SubCode == "ACQ.TRADE_NOT_EXIST" || v.SubCode == "TRADE_NOT_EXIST") {
		return RefundResult{RefundID: r.RefundID, TransactionID: r.TransactionID, Status: "NOT_FOUND"}, nil
	}
	if v.Code != "10000" {
		return RefundResult{}, ErrUnavailable
	}
	if v.TotalAmount != "" {
		total, err := yuanAmount(v.TotalAmount)
		if err != nil || total != r.TotalAmount {
			return RefundResult{}, ErrVerification
		}
	}
	if (v.OrderID != "" && v.OrderID != r.OrderID) || (v.TransactionID != "" && v.TransactionID != r.TransactionID) || (v.RefundID != "" && v.RefundID != r.RefundID) {
		return RefundResult{}, ErrVerification
	}
	if v.RefundAmount != "" {
		amount, err := yuanAmount(v.RefundAmount)
		if err != nil || amount != r.Amount {
			return RefundResult{}, ErrVerification
		}
	}
	if v.RefundStatus == "" {
		// Alipay documents absent refund_status as not received or failed, with
		// optional business fields. Retry only the same refund ID and amount;
		// treating this as PROCESSING would prevent the first submission forever.
		return RefundResult{RefundID: r.RefundID, TransactionID: r.TransactionID, Amount: r.Amount, Status: "NOT_FOUND"}, nil
	}
	if v.RefundStatus != "REFUND_SUCCESS" {
		return RefundResult{}, ErrUnavailable
	}
	if v.OrderID != r.OrderID || v.TransactionID != r.TransactionID || v.RefundID != r.RefundID || v.RefundAmount == "" {
		return RefundResult{}, ErrVerification
	}
	return RefundResult{RefundID: r.RefundID, TransactionID: r.TransactionID, Amount: r.Amount, Status: "SUCCEEDED"}, nil
}
