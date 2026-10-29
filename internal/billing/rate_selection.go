package billing

// ModelRatesUnambiguous permits one published tariff per operation for a
// model and asset. A model may offer several operations, such as generation
// and editing, but two tariffs for the same operation are ambiguous.
func ModelRatesUnambiguous(rates []Rate, model, asset string) bool {
	operations := map[string]bool{}
	for _, rate := range rates {
		if rate.Status != "published" || rate.Model != model || normalizedAsset(rate.Asset) != asset {
			continue
		}
		operation := "chat"
		if rate.Semantics == "metered_v2" {
			if rate.Metered == nil || rate.Metered.Operation == "" {
				return false
			}
			operation = rate.Metered.Operation
		} else if rate.Semantics != "exclusive_v1" && rate.Semantics != "" {
			return false
		}
		if operations[operation] {
			return false
		}
		operations[operation] = true
	}
	return len(operations) > 0
}
