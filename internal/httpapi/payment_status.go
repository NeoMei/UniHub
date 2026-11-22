package httpapi

func publicPaymentState(sandbox bool, providers []string) (string, bool) {
	if sandbox {
		return "sandbox", false
	}
	if len(providers) > 0 {
		return "direct", true
	}
	return "unconfigured", false
}

func publicPaymentProviders(sandbox bool, providers []string) []string {
	if sandbox {
		return []string{}
	}
	return providers
}
