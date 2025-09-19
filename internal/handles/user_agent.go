package handles

import (
	"math/rand"
)

const (
	// ChromeUserAgentMacOSSonoma141 identifies a recent Chrome build on macOS Sonoma.
	ChromeUserAgentMacOSSonoma141 = "Mozilla/5.0 (Macintosh; Intel Mac OS X 14_5_0) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/141.0.846.0 Safari/537.36"
	// ChromeUserAgentWindows141 identifies a recent Chrome build on Windows 10.
	ChromeUserAgentWindows141 = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/141.0.846.0 Safari/537.36"
	// ChromeUserAgentLinux141 identifies a recent Chrome build on Linux.
	ChromeUserAgentLinux141 = "Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/141.0.846.0 Safari/537.36"
	// ChromeUserAgentMacOSVentura141 identifies a recent Chrome build on macOS Ventura.
	ChromeUserAgentMacOSVentura141 = "Mozilla/5.0 (Macintosh; Intel Mac OS X 13_6_1) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/141.0.850.0 Safari/537.36"
)

var (
	defaultChromeUserAgentValues = []string{
		ChromeUserAgentMacOSSonoma141,
		ChromeUserAgentWindows141,
		ChromeUserAgentLinux141,
		ChromeUserAgentMacOSVentura141,
	}

	defaultChromeUserAgentProvider = NewChromeUserAgentProvider(defaultChromeUserAgentValues)
)

// ChromeUserAgentProvider selects Chrome user agent strings for outbound requests.
type ChromeUserAgentProvider struct {
	userAgents []string
}

// NewChromeUserAgentProvider constructs a ChromeUserAgentProvider with the supplied agent values.
func NewChromeUserAgentProvider(userAgents []string) ChromeUserAgentProvider {
	copiedUserAgents := make([]string, len(userAgents))
	copy(copiedUserAgents, userAgents)
	return ChromeUserAgentProvider{userAgents: copiedUserAgents}
}

// RandomAgent returns a user agent from the provider using the supplied random generator.
// When the random generator is nil, the package-level math/rand functions are used.
func (provider ChromeUserAgentProvider) RandomAgent(randomGenerator *rand.Rand) string {
	if len(provider.userAgents) == 0 {
		return ""
	}
	if randomGenerator != nil {
		selectedIndex := randomGenerator.Intn(len(provider.userAgents))
		return provider.userAgents[selectedIndex]
	}
	selectedIndex := rand.Intn(len(provider.userAgents))
	return provider.userAgents[selectedIndex]
}

// DefaultChromeUserAgent returns a modern Chrome user agent string.
func DefaultChromeUserAgent(randomGenerator *rand.Rand) string {
	return defaultChromeUserAgentProvider.RandomAgent(randomGenerator)
}

// DefaultChromeUserAgents exposes the list of modern Chrome user agent strings.
func DefaultChromeUserAgents() []string {
	return append([]string{}, defaultChromeUserAgentProvider.userAgents...)
}
