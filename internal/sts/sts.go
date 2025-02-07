// Package sts implements the MTA-STS (Strict Transport Security), RFC 8461.
//
// Note that "report" mode is not supported.
//
// Reference: https://tools.ietf.org/html/rfc8461
package sts

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"blitiri.com.ar/go/chasquid/internal/expvarom"
	"blitiri.com.ar/go/chasquid/internal/safeio"
	"blitiri.com.ar/go/chasquid/internal/trace"

	"golang.org/x/net/context/ctxhttp"
	"golang.org/x/net/idna"
)

// Exported variables.
var (
	cacheFetches = expvarom.NewInt("chasquid/sts/cache/fetches",
		"count of total fetches in the STS cache")
	cacheHits = expvarom.NewInt("chasquid/sts/cache/hits",
		"count of hits in the STS cache")
	cacheExpired = expvarom.NewInt("chasquid/sts/cache/expired",
		"count of expired entries in the STS cache")

	cacheIOErrors = expvarom.NewInt("chasquid/sts/cache/ioErrors",
		"count of I/O errors when maintaining STS cache")
	cacheFailedFetch = expvarom.NewInt("chasquid/sts/cache/failedFetch",
		"count of failed fetches in the STS cache")
	cacheInvalid = expvarom.NewInt("chasquid/sts/cache/invalid",
		"count of invalid policies in the STS cache")

	cacheMarshalErrors = expvarom.NewInt("chasquid/sts/cache/marshalErrors",
		"count of marshalling errors when maintaining STS cache")
	cacheUnmarshalErrors = expvarom.NewInt("chasquid/sts/cache/unmarshalErrors",
		"count of unmarshalling errors in STS cache")

	cacheRefreshCycles = expvarom.NewInt("chasquid/sts/cache/refreshCycles",
		"count of STS cache refresh cycles")
	cacheRefreshes = expvarom.NewInt("chasquid/sts/cache/refreshes",
		"count of STS cache refreshes")
	cacheRefreshErrors = expvarom.NewInt("chasquid/sts/cache/refreshErrors",
		"count of STS cache refresh errors")
)

// Policy represents a parsed policy.
// https://tools.ietf.org/html/rfc8461#section-3.2
// The json annotations are used for serializing for caching purposes.
type Policy struct {
	Version string        `json:"version"`
	Mode    Mode          `json:"mode"`
	MXs     []string      `json:"mx"`
	MaxAge  time.Duration `json:"max_age"`
}

// The Mode of a policy. Valid values (according to the standard) are
// constants below.
type Mode string

// Valid modes.
const (
	Enforce = Mode("enforce")
	Testing = Mode("testing")
	None    = Mode("none")
)

// parsePolicy parses a text representation of the policy (as specified in the
// RFC), and returns the corresponding Policy structure.
func parsePolicy(raw []byte) (*Policy, error) {
	p := &Policy{}

	scanner := bufio.NewScanner(bytes.NewReader(raw))
	for scanner.Scan() {
		sp := strings.SplitN(scanner.Text(), ":", 2)
		if len(sp) != 2 {
			continue
		}

		key := strings.TrimSpace(sp[0])
		value := strings.TrimSpace(sp[1])

		// Only care for the keys we recognize.
		switch key {
		case "version":
			p.Version = value
		case "mode":
			p.Mode = Mode(value)
		case "max_age":
			// On error, p.MaxAge will be 0 which is invalid.
			maxAge, _ := strconv.Atoi(value)
			p.MaxAge = time.Duration(maxAge) * time.Second
		case "mx":
			p.MXs = append(p.MXs, value)
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}

	return p, nil
}

// Check errors.
var (
	ErrUnknownVersion = errors.New("unknown policy version")
	ErrInvalidMaxAge  = errors.New("invalid max_age")
	ErrInvalidMode    = errors.New("invalid mode")
	ErrInvalidMX      = errors.New("invalid mx")
)

// Fetch errors.
var (
	ErrInvalidMediaType = errors.New("invalid HTTP media type")
)

// Check that the policy contents are valid.
func (p *Policy) Check() error {
	if p.Version != "STSv1" {
		return ErrUnknownVersion
	}

	// A 0 max age is invalid (could also represent an Atoi error), and so is
	// one greater than 31557600 (1 year), as per
	// https://tools.ietf.org/html/rfc8461#section-3.2.
	if p.MaxAge <= 0 || p.MaxAge > 31557600*time.Second {
		return ErrInvalidMaxAge
	}

	if p.Mode != Enforce && p.Mode != Testing && p.Mode != None {
		return ErrInvalidMode
	}

	// "mx" field is required, and the policy is invalid if it's not present.
	// https://mailarchive.ietf.org/arch/msg/uta/Omqo1Bw6rJbrTMl2Zo69IJr35Qo
	if len(p.MXs) == 0 {
		return ErrInvalidMX
	}

	return nil
}

// MXIsAllowed checks if the given MX is allowed, according to the policy.
// https://tools.ietf.org/html/rfc8461#section-4.1
func (p *Policy) MXIsAllowed(mx string) bool {
	if p.Mode != Enforce {
		return true
	}

	for _, pattern := range p.MXs {
		if matchDomain(mx, pattern) {
			return true
		}
	}

	return false
}

// UncheckedFetch fetches and parses the policy, but does NOT check it.
// This can be useful for debugging and troubleshooting, but you should always
// call Check on the policy before using it.
func UncheckedFetch(ctx context.Context, domain string) (*Policy, error) {
	// Convert the domain to ascii form, as httpGet does not support IDNs in
	// any other way.
	domain, err := idna.ToASCII(domain)
	if err != nil {
		return nil, err
	}

	ok, err := hasSTSRecord(domain)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, fmt.Errorf("MTA-STS TXT record missing")
	}

	url := urlForDomain(domain)
	rawPolicy, err := httpGet(ctx, url)
	if err != nil {
		return nil, err
	}

	return parsePolicy(rawPolicy)
}

// Fake URL for testing purposes, so we can do more end-to-end tests,
// including the HTTP fetching code.
var fakeURLForTesting string

func urlForDomain(domain string) string {
	if fakeURLForTesting != "" {
		return fakeURLForTesting + "/" + domain
	}

	// URL composed from the domain, as explained in:
	// https://tools.ietf.org/html/rfc8461#section-3.3
	// https://tools.ietf.org/html/rfc8461#section-3.2
	return "https://mta-sts." + domain + "/.well-known/mta-sts.txt"
}

// Fetch a policy for the given domain. Note this results in various network
// lookups and HTTPS GETs, so it can be slow.
// The returned policy is parsed and sanity-checked (using Policy.Check), so
// it should be safe to use.
func Fetch(ctx context.Context, domain string) (*Policy, error) {
	p, err := UncheckedFetch(ctx, domain)
	if err != nil {
		return nil, err
	}

	err = p.Check()
	if err != nil {
		return nil, err
	}

	return p, nil
}

// httpGet performs an HTTP GET of the given URL, using the context and
// rejecting redirects, as per the standard.
func httpGet(ctx context.Context, url string) ([]byte, error) {
	client := &http.Client{
		// We MUST NOT follow redirects, see
		// https://tools.ietf.org/html/rfc8461#section-3.3
		CheckRedirect: rejectRedirect,
	}

	resp, err := ctxhttp.Get(ctx, client, url)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("HTTP response status code: %v", resp.StatusCode)
	}

	// Media type must be "text/plain" to guard against cases where webservers
	// allow untrusted users to host non-text content (like HTML or images) at
	// a user-defined path.
	// https://tools.ietf.org/html/rfc8461#section-3.2
	mt, _, err := mime.ParseMediaType(resp.Header.Get("Content-type"))
	if err != nil {
		return nil, fmt.Errorf("HTTP media type error: %v", err)
	}
	if mt != "text/plain" {
		return nil, ErrInvalidMediaType
	}

	// Read but up to 10k; policies should be way smaller than that, and
	// having a limit prevents abuse/accidents with very large replies.
	return io.ReadAll(&io.LimitedReader{R: resp.Body, N: 10 * 1024})
}

var errRejectRedirect = errors.New("redirects not allowed in MTA-STS")

func rejectRedirect(req *http.Request, via []*http.Request) error {
	return errRejectRedirect
}

// matchDomain checks if the domain matches the given pattern, according to
// from https://tools.ietf.org/html/rfc8461#section-4.1
// (based on https://tools.ietf.org/html/rfc6125#section-6.4).
func matchDomain(domain, pattern string) bool {
	domain, dErr := domainToASCII(domain)
	pattern, pErr := domainToASCII(pattern)
	if dErr != nil || pErr != nil {
		// Domains should already have been checked and normalized by the
		// caller, exposing this is not worth the API complexity in this case.
		return false
	}

	// Simplify the case of a literal match.
	if domain == pattern {
		return true
	}

	// For wildcards, skip the first part of the domain and match the rest.
	// Note that if the pattern is malformed this might fail, but we are ok
	// with that.
	if strings.HasPrefix(pattern, "*.") {
		parts := strings.SplitN(domain, ".", 2)
		if len(parts) > 1 && parts[1] == pattern[2:] {
			return true
		}
	}

	return false
}

// domainToASCII converts the domain to ASCII form, similar to idna.ToASCII
// but with some preprocessing convenient for our use cases.
func domainToASCII(domain string) (string, error) {
	domain = strings.TrimSuffix(domain, ".")
	domain = strings.ToLower(domain)
	return idna.ToASCII(domain)
}

// Function that we override for testing purposes.
// In the future we will override net.DefaultResolver, but we don't do that
// yet for backwards compatibility.
var lookupTXT = net.LookupTXT

// hasSTSRecord checks if there is a valid MTA-STS TXT record for the domain.
// We don't do full parsing and don't care about the "id=" field, as it is
// unused in this implementation.
func hasSTSRecord(domain string) (bool, error) {
	txts, err := lookupTXT("_mta-sts." + domain)
	if err != nil {
		return false, err
	}

	for _, txt := range txts {
		if strings.HasPrefix(txt, "v=STSv1;") {
			return true, nil
		}
	}

	return false, nil
}

// PolicyCache is a caching layer for fetching policies.
//
// Policies are cached by domain, and stored in a single directory.
// The files will have as mtime the time when the policy expires, this makes
// the store simpler, as it can avoid keeping additional metadata.
//
// There is no in-memory caching. This may be added in the future, but for
// now disk is good enough for our purposes.
type PolicyCache struct {
	dir string

	sync.Mutex
}

// NewCache creates an instance of PolicyCache using the given directory as
// backing storage. The directory will be created if it does not exist.
func NewCache(dir string) (*PolicyCache, error) {
	c := &PolicyCache{
		dir: dir,
	}
	err := os.MkdirAll(dir, 0770)
	return c, err
}

const pathPrefix = "pol:"

func (c *PolicyCache) domainPath(domain string) string {
	// We assume the domain is well formed, sanity check just in case.
	if strings.Contains(domain, "/") {
		panic("domain contains slash")
	}

	return c.dir + "/" + pathPrefix + domain
}

var errExpired = errors.New("cache entry expired")

func (c *PolicyCache) load(domain string) (*Policy, error) {
	fname := c.domainPath(domain)

	fi, err := os.Stat(fname)
	if err != nil {
		return nil, err
	}
	if time.Since(fi.ModTime()) > 0 {
		cacheExpired.Add(1)
		return nil, errExpired
	}

	data, err := os.ReadFile(fname)
	if err != nil {
		cacheIOErrors.Add(1)
		return nil, err
	}

	p := &Policy{}
	err = json.Unmarshal(data, p)
	if err != nil {
		cacheUnmarshalErrors.Add(1)
		return nil, err
	}

	// The policy should always be valid, as we marshalled it ourselves;
	// however, check it just to be safe.
	if err := p.Check(); err != nil {
		cacheInvalid.Add(1)
		return nil, fmt.Errorf(
			"%s unmarshalled invalid policy %v: %v", domain, p, err)
	}

	return p, nil
}

func (c *PolicyCache) store(domain string, p *Policy) error {
	data, err := json.Marshal(p)
	if err != nil {
		cacheMarshalErrors.Add(1)
		return fmt.Errorf("%s failed to marshal policy %v, error: %v",
			domain, p, err)
	}

	// Change the modification time to the future, when the policy expires.
	// load will check for this to detect expired cache entries, see above for
	// the details.
	expires := time.Now().Add(p.MaxAge)
	chTime := func(fname string) error {
		return os.Chtimes(fname, expires, expires)
	}

	fname := c.domainPath(domain)
	err = safeio.WriteFile(fname, data, 0640, chTime)
	if err != nil {
		cacheIOErrors.Add(1)
	}
	return err
}

// Fetch a policy for the given domain, using the cache.
func (c *PolicyCache) Fetch(ctx context.Context, domain string) (*Policy, error) {
	cacheFetches.Add(1)
	tr := trace.New("STSCache.Fetch", domain)
	defer tr.Finish()

	p, err := c.load(domain)
	if err == nil {
		tr.Debugf("cache hit: %v", p)
		cacheHits.Add(1)
		return p, nil
	}

	p, err = Fetch(ctx, domain)
	if err != nil {
		tr.Debugf("failed to fetch: %v", err)
		cacheFailedFetch.Add(1)
		return nil, err
	}
	tr.Debugf("fetched: %v", p)

	// We could do this asynchronously, as we got the policy to give to the
	// caller. However, to make troubleshooting easier and the cost of storing
	// entries easier to track down, we store synchronously.
	// Note that even if the store returns an error, we pass on the policy: at
	// this point we rather use the policy even if we couldn't store it in the
	// cache.
	err = c.store(domain, p)
	if err != nil {
		tr.Errorf("failed to store: %v", err)
	} else {
		tr.Debugf("stored")
	}

	return p, nil
}

// PeriodicallyRefresh the cache, by re-fetching all entries.
func (c *PolicyCache) PeriodicallyRefresh(ctx context.Context) {
	for ctx.Err() == nil {
		c.refresh(ctx)
		cacheRefreshCycles.Add(1)

		// Wait 10 minutes between passes; this is a background refresh and
		// there's no need to poke the servers very often.
		time.Sleep(10 * time.Minute)
	}
}

func (c *PolicyCache) refresh(ctx context.Context) {
	tr := trace.New("STSCache.Refresh", c.dir)
	defer tr.Finish()

	entries, err := os.ReadDir(c.dir)
	if err != nil {
		tr.Errorf("failed to list directory %q: %v", c.dir, err)
		return
	}
	tr.Debugf("%d entries", len(entries))

	for _, e := range entries {
		if !strings.HasPrefix(e.Name(), pathPrefix) {
			continue
		}
		domain := e.Name()[len(pathPrefix):]
		cacheRefreshes.Add(1)
		tr.Debugf("%v: refreshing", domain)

		fetchCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
		p, err := Fetch(fetchCtx, domain)
		cancel()
		if err != nil {
			tr.Debugf("%v: failed to fetch: %v", domain, err)
			cacheRefreshErrors.Add(1)
			continue
		}
		tr.Debugf("%v: fetched", domain)

		err = c.store(domain, p)
		if err != nil {
			tr.Errorf("%v: failed to store: %v", domain, err)
		} else {
			tr.Debugf("%v: stored", domain)
		}
	}

	tr.Debugf("refresh done")
}
