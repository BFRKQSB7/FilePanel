// Package nodes implements optional, explicitly trusted storage and relay peers.
package nodes

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"filepanel/internal/files"
	"filepanel/internal/usage"
)

const prefix = "/_node/v1"
const TTL = 30 * time.Second
const Renew = 10 * time.Second
const Skew = 5 * time.Second

var ErrDenied = errors.New("节点或文件授权不可用")
var ErrNotFound = errors.New("已授权操作没有已提交对象")

func random() string {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return base64.RawURLEncoding.EncodeToString(b)
}
func hash(s string) string { h := sha256.Sum256([]byte(s)); return hex.EncodeToString(h[:]) }
func raw(v any) string     { b, _ := json.Marshal(v); return string(b) }
func send(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
func fail(w http.ResponseWriter) {
	for _, k := range []string{"Content-Length", "Content-Range", "Content-Disposition", "ETag", "Accept-Ranges"} {
		w.Header().Del(k)
	}
	send(w, 503, map[string]string{"error": "节点或文件暂不可用，请返回面板重试"})
}
func decode(r *http.Request, v any) error {
	d := json.NewDecoder(io.LimitReader(r.Body, 128<<10))
	d.DisallowUnknownFields()
	if err := d.Decode(v); err != nil {
		return err
	}
	if d.Decode(new(any)) != io.EOF {
		return ErrDenied
	}
	return nil
}
func bearer(r *http.Request) string {
	return strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
}

// Endpoint never permits cleartext, URL credentials, path prefixes or redirects.
func Endpoint(s string) error {
	u, e := url.Parse(s)
	if e != nil || u.Scheme != "https" || u.Host == "" || u.User != nil || (u.Path != "" && u.Path != "/") || u.RawQuery != "" || u.Fragment != "" {
		return errors.New("节点与面板端点必须是 HTTPS 源地址")
	}
	return nil
}
func Client(ca string) (*http.Client, error) {
	roots, err := x509.SystemCertPool()
	if err != nil {
		roots = x509.NewCertPool()
	}
	if ca != "" && !roots.AppendCertsFromPEM([]byte(ca)) {
		return nil, errors.New("CA 证书无效")
	}
	return &http.Client{Transport: &http.Transport{TLSClientConfig: &tls.Config{MinVersion: tls.VersionTLS12, RootCAs: roots}, ResponseHeaderTimeout: 8 * time.Second, IdleConnTimeout: 60 * time.Second}, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}, nil
}
func call(ctx context.Context, c *http.Client, method, endpoint, path, credential string, in, out any) error {
	var body io.Reader
	if in != nil {
		body = strings.NewReader(raw(in))
	}
	req, err := http.NewRequestWithContext(ctx, method, strings.TrimRight(endpoint, "/")+path, body)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+credential)
	req.Header.Set("Content-Type", "application/json")
	res, err := c.Do(req)
	if err != nil {
		return errors.New("TLS 或节点连接失败")
	}
	defer res.Body.Close()
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		if res.StatusCode == http.StatusNotFound {
			return ErrNotFound
		}
		return ErrDenied
	}
	if out != nil {
		return json.NewDecoder(io.LimitReader(res.Body, 128<<10)).Decode(out)
	}
	return nil
}

type Envelope struct {
	Body      json.RawMessage `json:"body"`
	Signature string          `json:"signature"`
}

func seal(key ed25519.PrivateKey, v any) Envelope {
	b := []byte(raw(v))
	return Envelope{b, base64.RawURLEncoding.EncodeToString(ed25519.Sign(key, b))}
}
func unseal(key string, e Envelope, v any) error {
	k, err := base64.RawURLEncoding.DecodeString(key)
	sig, se := base64.RawURLEncoding.DecodeString(e.Signature)
	if err != nil || se != nil || len(k) != ed25519.PublicKeySize || !ed25519.Verify(k, e.Body, sig) {
		return errors.New("面板身份不匹配")
	}
	return json.Unmarshal(e.Body, v)
}

type JoinRequest struct {
	Ticket   string `json:"ticket"`
	Endpoint string `json:"endpoint"`
	CA       string `json:"ca"`
}
type Identity struct {
	Panel      string `json:"panel"`
	PanelID    string `json:"panel_id"`
	CA         string `json:"ca"`
	ID         string `json:"id"`
	Credential string `json:"credential"`
}
type Node struct {
	ID            string       `json:"id"`
	Endpoint      string       `json:"endpoint"`
	CA            string       `json:"ca,omitempty"`
	Storage       bool         `json:"storage"`
	Relay         bool         `json:"relay"`
	Revoked       bool         `json:"revoked"`
	Version       int64        `json:"version"`
	Note          string       `json:"note"`
	Region        string       `json:"region"`
	ManualRegion  bool         `json:"manual_region"`
	PublicIP      string       `json:"public_ip"`
	LastSeen      int64        `json:"last_seen"`
	LastProbe     int64        `json:"last_probe"`
	ProbeError    string       `json:"probe_error"`
	Policy        usage.Config `json:"policy"`
	Credential    string       `json:"-"`
	Online        bool         `json:"online"`
	DataReady     bool         `json:"data_ready"`
	Report        *Report      `json:"report,omitempty"`
	RebindPending bool         `json:"rebind_pending"`
}
type Report struct {
	Sequence int64        `json:"sequence"`
	Period   usage.Period `json:"period"`
	Today    usage.Period `json:"today"`
	Total    uint64       `json:"total"`
	Free     uint64       `json:"free"`
}
type Control struct {
	PanelID string       `json:"panel_id"`
	NodeID  string       `json:"node_id"`
	Nonce   string       `json:"nonce"`
	Until   int64        `json:"until"`
	Version int64        `json:"version"`
	Storage bool         `json:"storage"`
	Relay   bool         `json:"relay"`
	Policy  usage.Config `json:"policy"`
}
type Heartbeat struct {
	Nonce  string `json:"nonce"`
	Report Report `json:"report"`
}
type Check struct {
	Nonce    string `json:"nonce"`
	Transfer string `json:"transfer"`
	Role     string `json:"role"`
	IP       string `json:"ip"`
	Start    bool   `json:"start"`
}
type Permit struct {
	Control
	ObjectID  string     `json:"object_id,omitempty"`
	Action    string     `json:"action"`
	File      files.Item `json:"file"`
	Source    string     `json:"source"`
	SourceCA  string     `json:"source_ca"`
	Ticket    string     `json:"ticket"`
	VisitorIP string     `json:"visitor_ip"`
}
type Transfer struct {
	ID, Action, File, Source, Relay, SHA, Name, IP string
	Version                                        int64
	Session                                        string
	Expires                                        int64
	State                                          string
}

// Signed tickets contain only a random transfer ID and a target role. Internal
// endpoints, credentials and object paths are obtained over the control channel.
type Ticket struct {
	ID   string `json:"id"`
	Role string `json:"role"`
}

func makeTicket(key ed25519.PrivateKey, id, role string) string {
	return base64.RawURLEncoding.EncodeToString([]byte(raw(seal(key, Ticket{id, role}))))
}
func readTicket(key, t string) (Ticket, error) {
	var e Envelope
	var v Ticket
	b, err := base64.RawURLEncoding.DecodeString(t)
	if err != nil || len(b) > 2048 {
		return v, ErrDenied
	}
	if json.Unmarshal(b, &e) != nil {
		return v, ErrDenied
	}
	return v, unseal(key, e, &v)
}

// checkedReader defers the final EOF until the entire enclosing multipart
// request is validated. The remote object cannot commit before that EOF.
type checkedReader struct {
	source io.Reader
	check  func() error
	done   bool
}

func (r *checkedReader) Read(p []byte) (int, error) {
	n, e := r.source.Read(p)
	if e == io.EOF && !r.done {
		r.done = true
		if x := r.check(); x != nil {
			return n, x
		}
	}
	return n, e
}

type remoteReader struct {
	ctx              context.Context
	client           *http.Client
	endpoint, ticket string
	size             int64
	body             io.ReadCloser
	meter            *usage.Meter
	ip, category     string
	stream           *usage.Stream
}

func (r *remoteReader) Seek(n int64, whence int) (int64, error) {
	if whence != io.SeekStart || n < 0 || n > r.size {
		return 0, ErrDenied
	}
	if r.body != nil {
		_ = r.body.Close()
	}
	req, err := http.NewRequestWithContext(r.ctx, "GET", r.endpoint+prefix+"/source", nil)
	if err != nil {
		return 0, err
	}
	req.Header.Set("Authorization", "Bearer "+r.ticket)
	if n > 0 {
		req.Header.Set("Range", "bytes="+itoa(n)+"-")
	}
	res, err := r.client.Do(req)
	if err != nil {
		return 0, err
	}
	if (n == 0 && res.StatusCode != 200) || (n > 0 && res.StatusCode != 206) {
		res.Body.Close()
		return 0, ErrDenied
	}
	if res.ContentLength != r.size-n {
		res.Body.Close()
		return 0, ErrDenied
	}
	if r.meter != nil {
		r.stream, err = r.meter.BeginReceived("ingress:"+r.ip, r.category, r.size-n)
		if err != nil {
			res.Body.Close()
			return 0, err
		}
	}
	r.body = res.Body
	return n, nil
}
func (r *remoteReader) Read(p []byte) (int, error) {
	if r.body == nil {
		return 0, io.EOF
	}
	if r.stream != nil {
		g, e := r.stream.Grant(len(p))
		if e != nil {
			return 0, e
		}
		p = p[:g]
	}
	n, e := r.body.Read(p)
	if r.stream != nil {
		r.stream.Confirm(n)
	}
	return n, e
}
func (r *remoteReader) Close() error {
	if r.stream != nil {
		_ = r.stream.Close(false)
	}
	if r.body != nil {
		return r.body.Close()
	}
	return nil
}
