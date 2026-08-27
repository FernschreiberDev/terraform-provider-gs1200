package zyxel

import (
	"context"
	"crypto/sha256"
	"crypto/tls"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"time"
)

// Parsed out of zVLAN_1Q_List.html and zlogin.html.
var (
	pvidsRE     = regexp.MustCompile(`var\s+pvids\s*=\s*\[([0-9,\s]*)\]`)
	portMaxRE   = regexp.MustCompile(`var\s+portMaxNum\s*=\s*(\d+)`)
	vlanStateRE = regexp.MustCompile(`var\s+vlanState\s*=\s*(\d+)`)
	mgmtVlanRE  = regexp.MustCompile(`mgmt_vlan\s*:\s*\[\s*'(\d+)'\s*\]`)
	allowRE     = regexp.MustCompile(`var\s+allow\s*=\s*(\d+)`)
	// On the login page: "1" a rejected password, "2" the single session is taken.
	errTypeRE  = regexp.MustCompile(`var\s+errType\s*=\s*"(\d+)"`)
	modelRE    = regexp.MustCompile(`modelStr\s*:\s*\[\s*"([^"]*)"`)
	firmwareRE = regexp.MustCompile(`firmwareStr\s*:\s*\[\s*"([^"]*)"`)
)

// tlsConfig builds the TLS settings this hardware actually needs.
//
// The GS1200 offers exactly one cipher suite: TLS 1.2 with
// AES128-GCM-SHA256 over an RSA key exchange. Go 1.22 removed every
// RSA-key-exchange suite from the client's default list — they provide no
// forward secrecy — so a stock Go client and this switch share nothing and
// the handshake fails outright, with `remote error: tls: handshake failure`
// and no hint that the cause is a cipher list. curl still offers those
// suites, which is why curl reaches the switch and an unconfigured Go client
// does not.
//
// The firmware is the last of its line and takes no updates, so the choice is
// to name that suite explicitly or not to speak to the device at all. The
// modern suites stay first in the list: if a proxy with a real certificate
// ever fronts the switch, the better option is what gets negotiated.
func tlsConfig(verifyTLS bool) *tls.Config {
	return &tls.Config{
		// These switches ship a self-signed certificate that cannot be
		// replaced. Verification stays configurable for a fronting proxy.
		InsecureSkipVerify: !verifyTLS, //nolint:gosec // see VerifyTLS
		MinVersion:         tls.VersionTLS12,
		CipherSuites: []uint16{
			tls.TLS_ECDHE_RSA_WITH_AES_128_GCM_SHA256,
			tls.TLS_ECDHE_RSA_WITH_AES_256_GCM_SHA384,
			tls.TLS_ECDHE_ECDSA_WITH_AES_128_GCM_SHA256,
			tls.TLS_ECDHE_ECDSA_WITH_AES_256_GCM_SHA384,
			// The only suite a GS1200 will agree to.
			tls.TLS_RSA_WITH_AES_128_GCM_SHA256,
		},
	}
}

// deviceLocks serialises access to each physical switch.
//
// The GS1200 serves one web session at a time. Terraform evaluates resources
// concurrently, and two provider aliases can legitimately point at the same
// address, so the lock has to live with the device rather than with the
// client object — otherwise two goroutines each hold "their" lock and the
// second login is refused as busy.
var deviceLocks sync.Map // host -> *sync.Mutex

func lockFor(host string) *sync.Mutex {
	actual, _ := deviceLocks.LoadOrStore(host, &sync.Mutex{})
	return actual.(*sync.Mutex)
}

// Client talks to one GS1200 over its web interface.
type Client struct {
	Host     string
	Password string
	Scheme   string
	// VerifyTLS is off by default: these switches ship a self-signed
	// certificate that cannot be replaced. It stays configurable so a fronting
	// proxy with a real certificate can turn verification back on.
	VerifyTLS bool
	Timeout   time.Duration

	http     *http.Client
	loggedIn bool

	// cache holds the last authenticated read. See ReadConfig for why.
	cacheMu  sync.Mutex
	cached   *Config
	cachedAt time.Time
}

// configTTL bounds how long a cached authenticated read stays usable. Writes
// invalidate it outright; this only limits how stale a long refresh can get.
const configTTL = 60 * time.Second

func (c *Client) cachedConfig() (Config, bool) {
	c.cacheMu.Lock()
	defer c.cacheMu.Unlock()
	if c.cached == nil || time.Since(c.cachedAt) > configTTL {
		return Config{}, false
	}
	return *c.cached, true
}

func (c *Client) cacheConfig(config Config) {
	c.cacheMu.Lock()
	defer c.cacheMu.Unlock()
	c.cached, c.cachedAt = &config, time.Now()
}

// forget drops the cache, so the next read goes back to the device.
func (c *Client) forget() {
	c.cacheMu.Lock()
	defer c.cacheMu.Unlock()
	c.cached = nil
}

// NewClient builds a client. It opens no connection.
func NewClient(host, password, scheme string, verifyTLS bool, timeout time.Duration) (*Client, error) {
	if scheme == "" {
		scheme = "https"
	}
	if timeout == 0 {
		timeout = 10 * time.Second
	}
	jar, err := cookiejar.New(nil)
	if err != nil {
		return nil, fmt.Errorf("cannot create a cookie jar: %w", err)
	}
	return &Client{
		Host:      host,
		Password:  password,
		Scheme:    scheme,
		VerifyTLS: verifyTLS,
		Timeout:   timeout,
		http: &http.Client{
			Jar:     jar,
			Timeout: timeout,
			Transport: &http.Transport{
				TLSClientConfig: tlsConfig(verifyTLS),
			},
			// The CGI endpoints answer with redirect stubs; following them
			// tells us nothing and costs a round trip on a slow CPU.
			CheckRedirect: func(*http.Request, []*http.Request) error {
				return http.ErrUseLastResponse
			},
		},
	}, nil
}

func (c *Client) baseURL() string {
	return fmt.Sprintf("%s://%s", c.Scheme, c.Host)
}

// fetch performs one request and returns the body as text.
func (c *Client) fetch(ctx context.Context, method, path, body, contentType string) (string, error) {
	target := c.baseURL() + "/" + strings.TrimPrefix(path, "/")

	var reader io.Reader
	if method == http.MethodPost {
		reader = strings.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, target, reader)
	if err != nil {
		return "", fmt.Errorf("cannot build a request for %s: %w", path, err)
	}
	req.Header.Set("User-Agent", "terraform-provider-schaltwerk")
	if method == http.MethodPost {
		req.Header.Set("Content-Type", contentType)
	}

	resp, err := c.http.Do(req)
	if err != nil {
		return "", fmt.Errorf("cannot reach %s: %w", c.Host, err)
	}
	defer resp.Body.Close()

	payload, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("cannot read the answer from %s: %w", path, err)
	}
	// 3xx is the firmware's normal answer to a CGI write, so only 4xx/5xx is
	// a failure worth reporting.
	if resp.StatusCode >= 400 {
		return "", fmt.Errorf("%s returned HTTP %d", path, resp.StatusCode)
	}
	return string(payload), nil
}

func (c *Client) get(ctx context.Context, path string) (string, error) {
	return c.fetch(ctx, http.MethodGet, path, "", "")
}

// -- session ---------------------------------------------------------------

// failureReason says why the last login failed.
//
// logon.cgi answers with the same redirect stub whatever went wrong; the
// reason is on the login page as errType, where 1 means a bad password and 2
// means somebody already holds the single session.
func (c *Client) failureReason(ctx context.Context) string {
	page, err := c.get(ctx, "zlogin.html")
	if err != nil {
		return "unknown"
	}
	if match := errTypeRE.FindStringSubmatch(page); match != nil {
		return match[1]
	}
	return "unknown"
}

// Login claims the device's single web session.
func (c *Client) Login(ctx context.Context) error {
	if c.Password == "" {
		return fmt.Errorf("%w: no web password configured for this switch", ErrAuth)
	}

	// The firmware hashes in the browser before submitting; doing the same
	// keeps the plaintext off the wire.
	sum := sha256.Sum256([]byte(c.Password))
	form := url.Values{"password": {hex.EncodeToString(sum[:])}}

	body, err := c.fetch(ctx, http.MethodPost, "logon.cgi", form.Encode(),
		"application/x-www-form-urlencoded")
	if err != nil {
		return err
	}

	allow := allowRE.FindStringSubmatch(body)
	if allow == nil {
		return fmt.Errorf("unexpected answer from logon.cgi on %s", c.Host)
	}
	if allow[1] != "1" {
		if c.failureReason(ctx) == "2" {
			return fmt.Errorf("%w: this switch allows one web session at a time and "+
				"another one is open; close it, or wait about a minute for it to time out",
				ErrBusy)
		}
		return fmt.Errorf("%w: the switch rejected the password", ErrAuth)
	}
	c.loggedIn = true
	return nil
}

// sessionToken is the value of the Cookies= cookie the login handed us.
func (c *Client) sessionToken() string {
	parsed, err := url.Parse(c.baseURL())
	if err != nil {
		return ""
	}
	for _, cookie := range c.http.Jar.Cookies(parsed) {
		if cookie.Name == "Cookies" {
			return cookie.Value
		}
	}
	return ""
}

// Logout releases the single web session. Always call this.
//
// The firmware's logout is a form POST carrying the session token in the body,
// with enctype="text/plain" — its own logout() does
// document.cookie.substring(8) to strip the "Cookies=" prefix. Neither a GET
// nor an empty POST releases anything: the GET answers 200 and does nothing,
// the empty POST just hangs. Both look close enough to success to be believed,
// and both leave the owner locked out of their own switch until the session
// times out a couple of minutes later.
func (c *Client) Logout(ctx context.Context) error {
	if !c.loggedIn {
		return nil
	}
	// text/plain bodies are "name=value" pairs separated by CRLF.
	body := "Cookies=" + c.sessionToken() + "\r\n"
	_, err := c.fetch(ctx, http.MethodPost, "zlogout.cgi", body, "text/plain")

	c.loggedIn = false
	if jar, jarErr := cookiejar.New(nil); jarErr == nil {
		c.http.Jar = jar
	}
	if err != nil {
		return fmt.Errorf("logout from %s failed (%w); its web UI stays locked "+
			"until the session times out", c.Host, err)
	}
	return nil
}

// withSession logs in, runs work, and always logs out — holding the
// device-wide lock for the whole time.
//
// Every read-modify-verify cycle happens inside one session: opening a session
// per step would triple the login/logout cycles, and every cycle is another
// chance to leave the switch locked.
func (c *Client) withSession(ctx context.Context, work func(context.Context) error) error {
	lock := lockFor(c.Host)
	lock.Lock()
	defer lock.Unlock()

	if err := c.Login(ctx); err != nil {
		return err
	}
	workErr := work(ctx)

	// Log out even when the context is already cancelled, or a timed-out apply
	// leaves the switch locked for its owner. A short independent budget is
	// enough for one POST.
	logoutCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), c.Timeout)
	defer cancel()
	logoutErr := c.Logout(logoutCtx)

	if workErr != nil {
		return workErr
	}
	return logoutErr
}

// Identify reports model and firmware, readable without logging in.
func (c *Client) Identify(ctx context.Context) (model, firmware string, err error) {
	html, err := c.get(ctx, "zlogin.html")
	if err != nil {
		return "", "", err
	}
	if match := modelRE.FindStringSubmatch(html); match != nil {
		model = strings.TrimSpace(match[1])
	}
	if match := firmwareRE.FindStringSubmatch(html); match != nil {
		firmware = strings.TrimSpace(match[1])
	}
	return model, firmware, nil
}
