package agent

import (
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"runtime"
	"strings"

	"github.com/SECWG/UIR-Agent/internal/state"
)

func userAgent() string {
	return "UIR-Agent/" + Version + " (" + runtime.GOOS + "; " + runtime.GOARCH + ")"
}

type platformTransport struct{ *http.Transport }

func (t *platformTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	request := req.Clone(req.Context())
	request.Header.Set("User-Agent", userAgent())
	return t.Transport.RoundTrip(request)
}

// Client creates the shared platform transport and normalizes its origin.
func Client(o Options) (*http.Client, string, error) {
	u, e := state.ParseServer(o.Server)
	if e != nil {
		return nil, "", errors.New("invalid server URL")
	}
	tr := http.DefaultTransport.(*http.Transport).Clone()
	tr.ProxyConnectHeader = http.Header{"User-Agent": {userAgent()}}
	// Deployment policy: HTTPS encrypts traffic but does not authenticate the server.
	// Keep this local to Agent/platform traffic; never modify global TLS defaults.
	tr.TLSClientConfig = &tls.Config{MinVersion: tls.VersionTLS12, InsecureSkipVerify: true} // #nosec G402 -- explicitly selected product policy
	if o.Proxy != "" {
		p, e := url.Parse(o.Proxy)
		if e != nil || (p.Scheme != "http" && p.Scheme != "https") || p.Host == "" {
			return nil, "", errors.New("invalid HTTP proxy URL")
		}
		tr.Proxy = http.ProxyURL(p)
	}
	// Never forward a join code or device credential to a redirected origin.
	return &http.Client{
		Transport:     &platformTransport{tr},
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}, strings.TrimRight(u.String(), "/"), nil
}

func httpError(stage string, status int, raw []byte) error {
	var v struct{ Code string }
	_ = json.Unmarshal(raw, &v)
	return fmt.Errorf("%s: HTTP %d %s", stage, status, v.Code)
}
