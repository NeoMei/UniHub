package httpapi

import (
	"net/http"
	"unihub/internal/accounts"
	"unihub/internal/billing"
)

func (s *api) productRuntimeRefundRoutes(m *http.ServeMux) {
	if len(s.options.ProductRuntime) == 0 {
		return
	}
	m.HandleFunc("POST /internal/v1/product-runtime/calls/{call}/refund", s.runtimeAuthenticated("calls:refund", func(w http.ResponseWriter, r *http.Request, service RuntimeService) error {
		if s.options.Billing == nil {
			return accounts.ErrForbidden
		}
		var in billing.RuntimeRefundRequest
		if err := decode(w, r, &in); err != nil {
			return err
		}
		out, err := s.options.Billing.RuntimeRefund(r.Context(), billing.RuntimeScope{ProductID: service.ProductID, InstanceID: service.InstanceID}, r.PathValue("call"), in)
		if err != nil {
			return err
		}
		return respond(w, 200, out)
	}))
}
