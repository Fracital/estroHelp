package zai

import (
	"encoding/json"
	"fmt"
	"net/http"
	"sync"
	"time"
)

// Optional Mullvad SOCKS5 relay rotation for the browser pool (ZAI_BROWSER_PROXY=mullvad).

const mullvadRelaysURL = "https://api.mullvad.net/www/relays/all/"

type mullvadRelay struct {
	Hostname    string `json:"hostname"`
	CountryCode string `json:"country_code"`
	CityName    string `json:"city_name"`
	Active      bool   `json:"active"`
	Type        string `json:"type"`
	SocksName   string `json:"socks_name"`
	SocksPort   int    `json:"socks_port"`
}

var (
	mullvadCachedRelaysMu sync.RWMutex
	mullvadCachedRelays   []mullvadRelay
)

func fetchMullvadRelays() ([]mullvadRelay, error) {
	req, err := http.NewRequest(http.MethodGet, mullvadRelaysURL, nil)
	if err != nil {
		return nil, err
	}
	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("unexpected status from Mullvad relays API: %s", resp.Status)
	}
	var relays []mullvadRelay
	if err := json.NewDecoder(resp.Body).Decode(&relays); err != nil {
		return nil, err
	}
	filtered := make([]mullvadRelay, 0)
	for _, relay := range relays {
		if relay.Active && relay.Type == "wireguard" && relay.SocksName != "" && relay.SocksPort > 0 && relay.CountryCode == "us" {
			filtered = append(filtered, relay)
		}
	}
	if len(filtered) == 0 {
		return nil, fmt.Errorf("no active WireGuard SOCKS relays found in US")
	}
	logf("event=mullvad_relays_loaded count=%d", len(filtered))
	mullvadCachedRelaysMu.Lock()
	mullvadCachedRelays = filtered
	mullvadCachedRelaysMu.Unlock()
	return filtered, nil
}

// getLiveMullvadSocks5Proxy returns a random active Mullvad SOCKS5 proxy URL,
// fetching the relay list on first use.
func getLiveMullvadSocks5Proxy() string {
	mullvadCachedRelaysMu.RLock()
	relays := mullvadCachedRelays
	mullvadCachedRelaysMu.RUnlock()
	if len(relays) == 0 {
		fetched, err := fetchMullvadRelays()
		if err != nil || len(fetched) == 0 {
			logf("event=mullvad_live_fetch_error err=%v", err)
			return ""
		}
		relays = fetched
	}
	r := relays[time.Now().UnixNano()%int64(len(relays))]
	return fmt.Sprintf("socks5://%s:%d", r.SocksName, r.SocksPort)
}
