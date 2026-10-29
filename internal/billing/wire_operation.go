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

func RateMatchesWire(rate Rate, wire string) bool {
	// Managed video is temporarily unavailable until provider and settlement
	// recovery are certified. Historical receipt verification remains available.
	if wire == "videos.create" {
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
