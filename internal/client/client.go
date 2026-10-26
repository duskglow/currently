// Package client talks to a running nutng daemon, over its local control
// socket or over HTTP.
package client

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/nut-ng/nut-ng/pkg/sdk"
)

// Client is a nutng API client.
type Client struct {
	base  string
	where string // for error messages
	token string
	http  *http.Client
}

// NewSocket returns a client for the control socket at path.
func NewSocket(path string) *Client {
	tr := &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			var d net.Dialer
			return d.DialContext(ctx, "unix", path)
		},
	}
	return &Client{base: "http://nutng", where: "control socket " + path, http: &http.Client{Transport: tr}}
}

// NewURL returns a client for a daemon's HTTP address, such as
// http://ups-host:9300. token is needed only for changes.
func NewURL(rawURL, token string) (*Client, error) {
	if !strings.Contains(rawURL, "://") {
		rawURL = "http://" + rawURL
	}
	u, err := url.Parse(rawURL)
	if err != nil || u.Host == "" {
		return nil, fmt.Errorf("invalid URL %q", rawURL)
	}
	return &Client{base: strings.TrimSuffix(u.String(), "/"), where: u.Host, token: token, http: &http.Client{}}, nil
}

// ListUPS returns every UPS.
func (c *Client) ListUPS(ctx context.Context) ([]sdk.UPS, error) {
	var out struct {
		UPS []sdk.UPS `json:"ups"`
	}
	err := c.do(ctx, "GET", "/api/v1/ups", nil, &out)
	return out.UPS, err
}

// GetUPS returns one UPS.
func (c *Client) GetUPS(ctx context.Context, name string) (sdk.UPS, error) {
	var out sdk.UPS
	err := c.do(ctx, "GET", "/api/v1/ups/"+url.PathEscape(name), nil, &out)
	return out, err
}

// RunCommand runs an instant command.
func (c *Client) RunCommand(ctx context.Context, ups, cmd, param string) error {
	var body any
	if param != "" {
		body = map[string]string{"param": param}
	}
	return c.do(ctx, "POST", "/api/v1/ups/"+url.PathEscape(ups)+"/commands/"+url.PathEscape(cmd), body, nil)
}

// SetVar changes a writable variable.
func (c *Client) SetVar(ctx context.Context, ups, name, value string) error {
	return c.do(ctx, "PUT", "/api/v1/ups/"+url.PathEscape(ups)+"/vars/"+url.PathEscape(name),
		map[string]string{"value": value}, nil)
}

// Events calls fn for each event until ctx is cancelled or the stream ends.
// recent replays that many recent notable events first.
func (c *Client) Events(ctx context.Context, ups string, recent int, fn func(sdk.Event)) error {
	q := url.Values{}
	if ups != "" {
		q.Set("ups", ups)
	}
	if recent > 0 {
		q.Set("recent", strconv.Itoa(recent))
	}
	req, err := c.request(ctx, "GET", "/api/v1/events?"+q.Encode(), nil)
	if err != nil {
		return err
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return c.connError(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return apiError(resp)
	}
	sc := bufio.NewScanner(resp.Body)
	sc.Buffer(make([]byte, 64<<10), 1<<20)
	for sc.Scan() {
		data, ok := strings.CutPrefix(sc.Text(), "data: ")
		if !ok {
			continue
		}
		var e sdk.Event
		if err := json.Unmarshal([]byte(data), &e); err == nil {
			fn(e)
		}
	}
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if err := sc.Err(); err != nil {
		return c.connError(err)
	}
	return fmt.Errorf("the nutng daemon closed the event stream")
}

func (c *Client) request(ctx context.Context, method, path string, body any) (*http.Request, error) {
	var r io.Reader
	if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			return nil, err
		}
		r = bytes.NewReader(data)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.base+path, r)
	if err != nil {
		return nil, err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}
	return req, nil
}

func (c *Client) do(ctx context.Context, method, path string, body, out any) error {
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	req, err := c.request(ctx, method, path, body)
	if err != nil {
		return err
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return c.connError(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		return apiError(resp)
	}
	if out == nil {
		return nil
	}
	return json.NewDecoder(resp.Body).Decode(out)
}

// ErrNotRunning means the daemon could not be reached.
var ErrNotRunning = errors.New("nutng daemon is not reachable")

func (c *Client) connError(err error) error {
	if errors.Is(err, context.Canceled) {
		return err
	}
	if errors.Is(err, os.ErrNotExist) || errors.Is(err, syscall.ECONNREFUSED) || isNoSocket(err) {
		return fmt.Errorf("%w at %s: is it running? Start it with `nutng serve` (or `nutng serve --demo` to try a simulated UPS)", ErrNotRunning, c.where)
	}
	if errors.Is(err, os.ErrPermission) {
		return fmt.Errorf("permission denied on %s: run as root/Administrator or as a member of the nutng group", c.where)
	}
	return fmt.Errorf("cannot reach nutng at %s: %w", c.where, err)
}

// isNoSocket catches the Windows errors for a missing or dead AF_UNIX
// socket, which do not map to the errno values above.
func isNoSocket(err error) bool {
	var op *net.OpError
	if !errors.As(err, &op) {
		return false
	}
	msg := strings.ToLower(op.Err.Error())
	return strings.Contains(msg, "no connection could be made") ||
		strings.Contains(msg, "cannot find the file") ||
		strings.Contains(msg, "no such file")
}

// APIError is an error response from the daemon.
type APIError struct {
	Status  int
	Message string
}

func (e *APIError) Error() string { return e.Message }

func apiError(resp *http.Response) error {
	var body struct {
		Error string `json:"error"`
	}
	data, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	if json.Unmarshal(data, &body) != nil || body.Error == "" {
		body.Error = fmt.Sprintf("%s: %s", resp.Status, strings.TrimSpace(string(data)))
	}
	return &APIError{Status: resp.StatusCode, Message: body.Error}
}
