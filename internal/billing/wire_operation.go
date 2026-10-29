package billing

// WireBillingOperation maps a trusted gateway route to its billing operation.
func WireBillingOperation(wire string) string {
	switch wire {
	case "chat.completions.create", "claude.messages.create", "gemini.generate":
		return "chat"
	case "responses.create", "responses.compact":
		return "responses"
	case "rerank.create":
		return "rerank"
	case "multimodal.embeddings.create":
		return "multimodal_embedding"
	case "embeddings.create":
		return "embedding"
	case "audio.speech.create", "bailian.audio.speech.create", "elevenlabs.audio.speech.create":
		return "speech"
	case "audio.transcriptions.create", "dashscope.audio.transcriptions.create":
		return "transcription"
	case "images.generations.create", "dashscope.images.generate", "gemini.images.generate":
		return "image_generation"
	case "images.edits.create":
		return "image_edit"
	case "videos.create":
		return "video"
	case "ocr.jobs.create", "dashscope.ocr.recognitions.create":
		return "ocr"
	}
	return ""
}

// allowedForNewCalls keeps historical settlement contracts readable without
// allowing obsolete or ambiguous tariffs to authorize new supplier dispatches.
func allowedForNewCalls(rate Rate) bool {
	if rate.Semantics != "metered_v2" || rate.Metered == nil {
		return true
	}
	p := rate.Metered
	if p.Operation == "ocr" && p.Policy != "success_request" && p.Policy != "provider_tokens_v1" {
		return false
	}
	if p.Operation == "video" && p.Policy == "ali_wan26_t2v_billable_duration_v1" {
		return false
	}
	return true
}

func RateMatchesWire(rate Rate, wire string) bool {
	if !allowedForNewCalls(rate) {
		return false
	}
	operation := WireBillingOperation(wire)
	if operation == "" {
		return false
	}
	if rate.Semantics == "exclusive_v1" {
		return operation == "chat"
	}
	return rate.Semantics == "metered_v2" && rate.Metered != nil && rate.Metered.Operation == operation
}
