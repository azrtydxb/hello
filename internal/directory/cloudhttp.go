package directory

// The HTTPS plumbing the Google Workspace and Microsoft Entra drivers share
// (spec I-20 to I-22, I-41): the SSRF-guarded client, JSON GETs with a
// bounded retry on 429 and 503 that honours Retry-After within a per-run
// budget, and the classification of failures into scrubbed codes. A response
// body is never echoed: it may carry tenant data or a token.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/azrtydxb/hello/internal/safehttp"
)

// Retry limits of one run against a cloud API.
const (
	// cloudRetries is how many throttled or unavailable answers one run
	// waits out in total before it fails with CodeQuota.
	cloudRetries = 12
	// cloudMaxWait caps one Retry-After; a longer one fails the run, which
	// the next scheduled run retries.
	cloudMaxWait = 2 * time.Minute
	// cloudWaitBudget caps the sum of the waits of a run.
	cloudWaitBudget = 6 * time.Minute
	// cloudTimeout bounds one request.
	cloudTimeout = 30 * time.Second
)

// sleep waits d or until ctx ends; tests replace it.
var sleep = func(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

// errGone is a 410 (an expired Graph delta link).
var errGone = errors.New("directory: gone")

// cloudClient is the run's HTTP client and retry budget.
type cloudClient struct {
	hc      *http.Client
	retries int
	waited  time.Duration
	// token is the bearer token of the API calls.
	token string
}

func newCloudClient(o FetchOptions) *cloudClient {
	hc := o.HTTP
	if hc == nil {
		// Google and Microsoft are public services: no private range is
		// reachable, whatever HELLO_INTEGRATIONS_ALLOWED_CIDRS says.
		hc = safehttp.NewClient(safehttp.Options{Timeout: cloudTimeout})
	}
	return &cloudClient{hc: hc}
}

// retryAfter reads a Retry-After header (seconds or an HTTP date).
func retryAfter(h string, now time.Time) time.Duration {
	h = strings.TrimSpace(h)
	if h == "" {
		return 0
	}
	if n, err := strconv.Atoi(h); err == nil && n >= 0 {
		return time.Duration(n) * time.Second
	}
	if t, err := http.ParseTime(h); err == nil {
		return t.Sub(now)
	}
	return 0
}

// do sends req (rebuilt by mk for each attempt) and returns the body of a 2xx
// answer, waiting out 429, 503 and 504 within the run's budget.
func (c *cloudClient) do(ctx context.Context, mk func() (*http.Request, error)) ([]byte, http.Header, error) {
	backoff := time.Second
	for {
		req, err := mk()
		if err != nil {
			return nil, nil, &Error{Code: CodeInternal, Detail: "could not build the request"}
		}
		if c.token != "" {
			req.Header.Set("Authorization", "Bearer "+c.token)
		}
		req.Header.Set("Accept", "application/json")
		resp, err := c.hc.Do(req)
		if err != nil {
			return nil, nil, classifyHTTP(ctx, err)
		}
		body, rerr := safehttp.ReadBody(resp.Body, safehttp.DefaultMaxBody)
		_ = resp.Body.Close()
		switch {
		case resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode == http.StatusServiceUnavailable || resp.StatusCode == http.StatusGatewayTimeout:
			wait := retryAfter(resp.Header.Get("Retry-After"), time.Now())
			if wait <= 0 {
				wait = backoff
				backoff = min(backoff*2, 30*time.Second)
			}
			c.retries++
			if c.retries > cloudRetries || wait > cloudMaxWait || c.waited+wait > cloudWaitBudget {
				return nil, nil, &Error{Code: CodeQuota, Detail: "the API kept throttling (HTTP " + strconv.Itoa(resp.StatusCode) + "); the next run retries"}
			}
			c.waited += wait
			if err := sleep(ctx, wait); err != nil {
				return nil, nil, classifyHTTP(ctx, err)
			}
			continue
		case resp.StatusCode >= 200 && resp.StatusCode < 300:
			if rerr != nil {
				return nil, nil, &Error{Code: CodeQuota, Detail: "the answer was larger than 8 MiB"}
			}
			return body, resp.Header, nil
		}
		return nil, nil, statusError(resp.StatusCode)
	}
}

// getJSON GETs u into out.
func (c *cloudClient) getJSON(ctx context.Context, u string, out any) error {
	body, _, err := c.do(ctx, func() (*http.Request, error) {
		return http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	})
	if err != nil {
		return err
	}
	if err := json.Unmarshal(body, out); err != nil {
		return &Error{Code: CodeInternal, Detail: "the API answered something that is not the expected JSON"}
	}
	return nil
}

// postForm POSTs a form (a token request) and decodes the JSON answer. A 400
// or 401 from a token endpoint is a refused credential.
func (c *cloudClient) postForm(ctx context.Context, u string, form url.Values, out any) error {
	body, _, err := c.do(ctx, func() (*http.Request, error) {
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, u, strings.NewReader(form.Encode()))
		if err == nil {
			req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		}
		return req, err
	})
	if err != nil {
		var de *Error
		if errors.As(err, &de) && de.Code == CodeBadFilter {
			return &Error{Code: CodeAuthFailed, Detail: "the identity provider refused the credential or the consent"}
		}
		return err
	}
	if err := json.Unmarshal(body, out); err != nil {
		return &Error{Code: CodeAuthFailed, Detail: "the token endpoint answered something that is not a token"}
	}
	return nil
}

// statusError classifies an HTTP failure status.
func statusError(code int) error {
	switch code {
	case http.StatusUnauthorized, http.StatusForbidden:
		return &Error{Code: CodeAuthFailed, Detail: fmt.Sprintf("the API refused the credential or a permission is missing (HTTP %d)", code)}
	case http.StatusGone:
		return errGone
	case http.StatusNotFound, http.StatusBadRequest:
		return &Error{Code: CodeBadFilter, Detail: fmt.Sprintf("the API rejected the request; check the customer, domain, tenant or group ids (HTTP %d)", code)}
	}
	return &Error{Code: CodeUnreachable, Detail: fmt.Sprintf("the API failed (HTTP %d)", code)}
}

// classifyHTTP classifies a transport failure.
func classifyHTTP(ctx context.Context, err error) error {
	var ne net.Error
	switch {
	case errors.Is(err, safehttp.ErrBlockedAddress):
		return &Error{Code: CodeUnreachable, Detail: "the address is not allowed"}
	case errors.Is(err, context.DeadlineExceeded), ctx.Err() != nil, errors.As(err, &ne) && ne.Timeout():
		return &Error{Code: CodeTimeout, Detail: "the API did not answer in time"}
	}
	return &Error{Code: CodeUnreachable, Detail: "the API could not be reached"}
}

// sameOrigin reports whether next (a paging link the API returned) points at
// base's scheme and host, so a forged link cannot send the bearer token
// elsewhere.
func sameOrigin(base, next string) bool {
	b, err1 := url.Parse(base)
	n, err2 := url.Parse(next)
	return err1 == nil && err2 == nil && n.Scheme == b.Scheme && strings.EqualFold(n.Host, b.Host) && n.User == nil
}

// boolString writes a JSON boolean the way the mapping's bool modes read it.
func boolString(b bool) string {
	if b {
		return "true"
	}
	return "false"
}

// flatten writes a JSON object into a mapping's attribute bag: nested objects
// become dotted keys, arrays of scalars become multiple values, arrays of
// objects add each member's fields under the same key (so phones.value holds
// every phone) and, when an element has a type, under key[type] too
// (phones[work].value). Keys are lower case.
func flatten(prefix string, v any, out map[string][]string) {
	switch x := v.(type) {
	case map[string]any:
		for k, val := range x {
			key := strings.ToLower(k)
			if prefix != "" {
				key = prefix + "." + key
			}
			flatten(key, val, out)
		}
	case []any:
		for _, el := range x {
			if m, ok := el.(map[string]any); ok {
				if t, ok := m["type"].(string); ok && t != "" {
					flatten(prefix+"["+strings.ToLower(t)+"]", m, out)
				}
			}
			flatten(prefix, el, out)
		}
	case string:
		if prefix != "" && x != "" {
			out[prefix] = append(out[prefix], x)
		}
	case bool:
		if prefix != "" {
			out[prefix] = append(out[prefix], boolString(x))
		}
	case float64:
		if prefix != "" {
			out[prefix] = append(out[prefix], strconv.FormatFloat(x, 'f', -1, 64))
		}
	case json.Number:
		if prefix != "" {
			out[prefix] = append(out[prefix], x.String())
		}
	}
}
