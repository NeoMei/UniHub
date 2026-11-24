package billing

import (
	"context"
	"fmt"
	"regexp"
	"slices"

	"unihub/internal/accounts"
)

// ManagedModels lists public model IDs in preference order for each operation.
// Missing entries inherit legacy behavior; an empty list denies an operation.
type ManagedModels map[string][]string

var (
	ErrManagedModelNotAllowed = fmt.Errorf("%w: managed model not allowed", accounts.ErrForbidden)
	managedModelID            = regexp.MustCompile(`^[A-Za-z0-9._:/-]{1,128}$`)
)

func (m ManagedModels) valid() bool {
	for operation, models := range m {
		switch operation {
		case "chat", "vision", "speech", "transcription", "image-generation", "image-edit", "video", "knowledge-embedding", "knowledge-rerank", "ocr":
		default:
			return false
		}
		if models == nil || len(models) > 32 {
			return false
		}
		seen := make(map[string]bool, len(models))
		for _, id := range models {
			if !managedModelID.MatchString(id) || seen[id] {
				return false
			}
			seen[id] = true
		}
	}
	return true
}

func (m ManagedModels) allows(model, wire, operation string) bool {
	if len(m) == 0 {
		return true
	}
	wireOperation := WireBillingOperation(wire)
	switch wireOperation {
	case "chat", "responses":
		// Text and image inputs share these wires. The authenticated gateway
		// must classify the payload when either list has been configured.
		if operation == "" {
			_, chat := m["chat"]
			_, vision := m["vision"]
			return !chat && !vision
		}
		if operation != "chat" && operation != "vision" {
			return false
		}
		wireOperation = operation
	case "embedding", "multimodal_embedding":
		wireOperation = "knowledge-embedding"
	case "rerank":
		wireOperation = "knowledge-rerank"
	case "image_generation":
		wireOperation = "image-generation"
	case "image_edit":
		wireOperation = "image-edit"
	case "":
		switch wire {
		case "ocr.jobs.retrieve", "ocr.results.retrieve":
			wireOperation = "ocr"
		default:
			return false
		}
	}
	if operation != "" && operation != wireOperation {
		return false
	}
	models, configured := m[wireOperation]
	return !configured || slices.Contains(models, model)
}

// AuthorizeManagedModel is the gateway's preliminary check. ReserveNative
// reads the current profile again inside the transaction for a new hold.
func (s *Service) AuthorizeManagedModel(ctx context.Context, product, scope, model, wire, operation string) error {
	if product != "superwagie" || scope != ProductCreditScopeV1 || model == "" && wire == "" {
		return nil
	}
	profile, err := s.Profile(ctx, product)
	if err != nil {
		return err
	}
	return profile.authorizeManagedModel(scope, model, wire, operation)
}

func (p BillingProfile) authorizeManagedModel(scope, model, wire, operation string) error {
	if p.ProductID == "superwagie" && scope == ProductCreditScopeV1 && !p.Usage.ManagedModels.allows(model, wire, operation) {
		return ErrManagedModelNotAllowed
	}
	return nil
}
