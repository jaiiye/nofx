package nofxos

// ResolveClient returns a nofxos data client backed by the public nofxos.ai
// API. The claw402 x402 payment gateway has been removed from this build, so
// no paid routing is available and the direct client is always used.
func ResolveClient(_ string) *Client {
	return NewClient(DefaultBaseURL, DefaultAuthKey)
}
