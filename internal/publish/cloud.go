package publish

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"net/url"
	"os"
	"path"
	"regexp"
	"sort"
	"strings"
	"time"
)

// The orch-cloud contract (docs/CLOUD.md) is implemented twice: by the
// Worker in the orch-cloud repository and by the client below. The limits
// are the Worker's; the client checks them too so an oversized site fails
// here, with a sentence naming the file, rather than as a 413 after
// uploading ten megabytes.
const (
	// CloudAPIVersion is the contract version this client speaks.
	CloudAPIVersion = 1

	cloudMaxBodyBytes = 10 << 20
	cloudMaxFileBytes = 5 << 20
	cloudMaxFiles     = 200

	// cloudRequestTimeout bounds one request. A site upload is a single
	// JSON body of at most 10 MiB; a minute is generous for that on any
	// connection an operator would publish from, and short enough that a
	// hung Worker ends a `--watch` tick instead of the watch.
	cloudRequestTimeout = 60 * time.Second
)

// cloudProjectID is the contract's project-id shape.
var cloudProjectID = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,62}$`)

// CloudProjectID maps an orch project id onto the contract's shape: it is
// lowercased, and then either matches or is refused. It is not sanitised —
// an id quietly rewritten from `Billing_API` to `billing-api` would be a
// second project the first time someone else spelled it the other way.
func CloudProjectID(orchID string) (string, error) {
	id := strings.ToLower(orchID)
	if !cloudProjectID.MatchString(id) {
		return "", fmt.Errorf(
			"project id %q cannot be used with orch-cloud: it must be lowercase letters, "+
				"digits and hyphens, starting with a letter or digit, at most 63 characters "+
				"— pass --project-id to publish under a different id", orchID)
	}
	return id, nil
}

// TokenKind names which of the contract's three secrets a request carried,
// so an unauthorized error can say which one to fix.
type TokenKind string

const (
	TokenAdmin   TokenKind = "admin"
	TokenPublish TokenKind = "publish"
	TokenNone    TokenKind = ""
)

// CloudError is a non-2xx answer from the Worker, decoded from the
// contract's `{"error","message"}` body.
type CloudError struct {
	Status  int
	Code    string
	Message string
	// Token is the kind of token the failed request carried.
	Token TokenKind
	// Project is the project the request was about, when there was one.
	Project string
}

func (e *CloudError) Error() string {
	msg := e.Message
	if msg == "" {
		msg = http.StatusText(e.Status)
	}
	base := fmt.Sprintf("orch-cloud: %s (%d %s)", msg, e.Status, e.Code)
	if e.Code != "unauthorized" {
		return base
	}
	switch e.Token {
	case TokenAdmin:
		return base + " — the admin token was rejected; run `orch cloud login --url <worker>` " +
			"with the Worker's ADMIN_TOKEN piped on stdin"
	case TokenPublish:
		return base + fmt.Sprintf(" — the publish token for project %q was rejected; "+
			"`orch cloud rotate --publish` issues a new one with the admin token", e.Project)
	default:
		return base
	}
}

// IsCloudCode reports whether err is a CloudError with the given code.
func IsCloudCode(err error, code string) bool {
	var ce *CloudError
	return errors.As(err, &ce) && ce.Code == code
}

// CloudClient talks to one orch-cloud Worker.
type CloudClient struct {
	base *url.URL
	http *http.Client
}

// NewCloudClient validates baseURL and returns a client for it.
//
// https is required except for loopback hosts: every request carries a
// bearer token, and a token sent over plain http to anything but this
// machine is a token given away. `wrangler dev` serves http on localhost,
// which is the one case that needs the exception.
func NewCloudClient(baseURL string) (*CloudClient, error) {
	u, err := url.Parse(strings.TrimSpace(baseURL))
	if err != nil || u.Host == "" || (u.Scheme != "https" && u.Scheme != "http") {
		return nil, fmt.Errorf("orch-cloud URL %q is not an http(s) URL", baseURL)
	}
	if u.Scheme == "http" && !isLoopbackHost(u.Hostname()) {
		return nil, fmt.Errorf(
			"orch-cloud URL %q uses plain http: tokens would travel unencrypted — use https "+
				"(http is accepted only for localhost, e.g. `wrangler dev`)", baseURL)
	}
	if u.RawQuery != "" || u.Fragment != "" {
		return nil, fmt.Errorf("orch-cloud URL %q must not carry a query or fragment", baseURL)
	}
	u.Path = strings.TrimRight(u.Path, "/")
	return &CloudClient{base: u, http: &http.Client{Timeout: cloudRequestTimeout}}, nil
}

func isLoopbackHost(host string) bool {
	return host == "localhost" || host == "127.0.0.1" || host == "::1"
}

// URL is the Worker's base URL, normalised (no trailing slash).
func (c *CloudClient) URL() string { return c.base.String() }

// ViewerURL is the stakeholder link for a view token. It is the one place a
// view token is meant to be shown: the URL is what the operator shares.
func (c *CloudClient) ViewerURL(viewToken string) string {
	return c.base.String() + "/v/" + viewToken + "/"
}

// CloudHealth is `GET /api/v1/health`.
type CloudHealth struct {
	OK      bool   `json:"ok"`
	Version string `json:"version"`
	API     int    `json:"api"`
}

// Health checks the Worker is reachable and speaks this contract version.
func (c *CloudClient) Health(ctx context.Context) (CloudHealth, error) {
	var h CloudHealth
	if err := c.do(ctx, http.MethodGet, "/api/v1/health", "", TokenNone, "", nil, &h); err != nil {
		return h, err
	}
	if h.API != CloudAPIVersion {
		return h, fmt.Errorf("orch-cloud at %s speaks API %d, this orch speaks %d — "+
			"redeploy the Worker from a matching orch-cloud release", c.URL(), h.API, CloudAPIVersion)
	}
	return h, nil
}

// WhoAmI succeeds only if adminToken is the Worker's admin token.
func (c *CloudClient) WhoAmI(ctx context.Context, adminToken string) error {
	var out struct {
		Role string `json:"role"`
	}
	if err := c.do(ctx, http.MethodGet, "/api/v1/whoami", adminToken, TokenAdmin, "", nil, &out); err != nil {
		return err
	}
	if out.Role != "admin" {
		return fmt.Errorf("orch-cloud whoami answered role %q, want admin", out.Role)
	}
	return nil
}

// CloudProjectTokens are the two tokens a new project is created with. The
// Worker keeps only their hashes, so this is the one time they exist in
// the clear anywhere but the operator's credentials file.
type CloudProjectTokens struct {
	ID           string `json:"id"`
	PublishToken string `json:"publish_token"`
	ViewToken    string `json:"view_token"`
}

// CreateProject registers id and returns its tokens. A project that already
// exists is a CloudError with code "conflict".
func (c *CloudClient) CreateProject(ctx context.Context, adminToken, id string) (CloudProjectTokens, error) {
	var out CloudProjectTokens
	body := map[string]string{"id": id}
	if err := c.do(ctx, http.MethodPost, "/api/v1/projects", adminToken, TokenAdmin, id, body, &out); err != nil {
		return out, err
	}
	if out.PublishToken == "" || out.ViewToken == "" {
		return out, fmt.Errorf("orch-cloud created project %q but returned no tokens", id)
	}
	return out, nil
}

// DeleteProject removes id, its view link and its site.
func (c *CloudClient) DeleteProject(ctx context.Context, adminToken, id string) error {
	return c.do(ctx, http.MethodDelete, "/api/v1/projects/"+id, adminToken, TokenAdmin, id, nil, nil)
}

// RotatePublishToken issues a new publish token for id; the old one stops
// working.
func (c *CloudClient) RotatePublishToken(ctx context.Context, adminToken, id string) (string, error) {
	var out struct {
		PublishToken string `json:"publish_token"`
	}
	if err := c.do(ctx, http.MethodPost, "/api/v1/projects/"+id+"/publish-token",
		adminToken, TokenAdmin, id, nil, &out); err != nil {
		return "", err
	}
	if out.PublishToken == "" {
		return "", fmt.Errorf("orch-cloud rotated the publish token of %q but returned none", id)
	}
	return out.PublishToken, nil
}

// RotateViewToken issues a new view token for id; the old URL stops working.
func (c *CloudClient) RotateViewToken(ctx context.Context, publishToken, id string) (string, error) {
	var out struct {
		ViewToken string `json:"view_token"`
	}
	if err := c.do(ctx, http.MethodPost, "/api/v1/projects/"+id+"/view-token",
		publishToken, TokenPublish, id, nil, &out); err != nil {
		return "", err
	}
	if out.ViewToken == "" {
		return "", fmt.Errorf("orch-cloud rotated the view token of %q but returned none", id)
	}
	return out.ViewToken, nil
}

// CloudUpload is the Worker's answer to a site upload.
type CloudUpload struct {
	Changed bool `json:"changed"`
	Version int  `json:"version"`
}

// CloudSite is a directory read into the upload body's shape, with the
// digest that identifies it.
type CloudSite struct {
	// Files maps relative, /-separated paths to their bytes.
	Files map[string][]byte
	// Digest identifies the file set: see SiteDigest.
	Digest string

	// body is the encoded upload, built by ReadCloudSite so its size is
	// known before any request is made.
	body []byte
}

// encode builds the upload body and checks it against the contract's body
// limit. The check has to be on the ENCODED size: base64 grows every file by
// a third, so three files that each pass the per-file limit can still be a
// body the Worker refuses.
func (s *CloudSite) encode() error {
	files := make(map[string]string, len(s.Files))
	for p, b := range s.Files {
		files[p] = base64.StdEncoding.EncodeToString(b)
	}
	body, err := json.Marshal(struct {
		Digest string            `json:"digest"`
		Files  map[string]string `json:"files"`
	}{s.Digest, files})
	if err != nil {
		return fmt.Errorf("encoding the site upload: %w", err)
	}
	if len(body) > cloudMaxBodyBytes {
		return fmt.Errorf("the site upload is %d bytes once encoded; orch-cloud accepts at most %d",
			len(body), cloudMaxBodyBytes)
	}
	s.body = body
	return nil
}

// ReadCloudSite reads an export directory into a CloudSite, enforcing the
// contract's limits before anything is sent.
//
// It walks what is on disk rather than trusting a list: the directory is
// the one `Export` just wrote, and the Worker serves exactly what it
// receives, so reading it back is the only way the upload and the page can
// be the same thing.
//
// The walk goes through an os.Root: a symlink planted in the directory
// between the walk and the read cannot make the upload carry a file from
// outside it, and an export contains no symlinks to follow anyway.
func ReadCloudSite(dir string) (CloudSite, error) {
	root, err := os.OpenRoot(dir)
	if err != nil {
		return CloudSite{}, fmt.Errorf("opening the export in %s: %w", dir, err)
	}
	defer func() { _ = root.Close() }()
	fsys := root.FS()

	site := CloudSite{Files: map[string][]byte{}}
	err = fs.WalkDir(fsys, ".", func(rel string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		if !d.Type().IsRegular() {
			return fmt.Errorf("%s is not a regular file — an export contains only files", rel)
		}
		if err := validSitePath(rel); err != nil {
			return err
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		if info.Size() > cloudMaxFileBytes {
			return fmt.Errorf("%s is %d bytes; orch-cloud accepts at most %d per file",
				rel, info.Size(), cloudMaxFileBytes)
		}
		if len(site.Files) >= cloudMaxFiles {
			return fmt.Errorf("the export has more than %d files, the most orch-cloud accepts", cloudMaxFiles)
		}
		b, err := fs.ReadFile(fsys, rel)
		if err != nil {
			return err
		}
		site.Files[rel] = b
		return nil
	})
	if err != nil {
		return CloudSite{}, fmt.Errorf("reading the export in %s: %w", dir, err)
	}
	if _, ok := site.Files["index.html"]; !ok {
		return CloudSite{}, fmt.Errorf("the export in %s has no index.html at its root", dir)
	}
	site.Digest = SiteDigest(site.Files)
	if err := site.encode(); err != nil {
		return CloudSite{}, err
	}
	return site, nil
}

// SiteDigest fingerprints a file set: SHA-256 over every path and the
// SHA-256 of its bytes, in path order.
//
// Deliberately NOT the snapshot's Digest. That one ignores generated_at so
// `--watch` can skip a tick where nothing a viewer reads has changed — the
// watch loop already does that before this is ever called. What the Worker
// compares is different: whether the bytes it is being sent are the bytes it
// already serves. Two things only a file digest gets right: an orch upgrade
// that ships a new stakeholder bundle over an unchanged snapshot is a new
// site, and a one-off `orch publish` re-run moves the page's freshness stamp
// the same way `--to git` does, instead of being answered "unchanged" while
// the page keeps saying it was last updated days ago. A retry of a timed-out
// upload sends the same bytes and is still a no-op, which is what the
// contract's server-side check is for.
func SiteDigest(files map[string][]byte) string {
	paths := make([]string, 0, len(files))
	for p := range files {
		paths = append(paths, p)
	}
	sort.Strings(paths)
	h := sha256.New()
	for _, p := range paths {
		sum := sha256.Sum256(files[p])
		_, _ = io.WriteString(h, p)
		_, _ = h.Write([]byte{0})
		_, _ = h.Write(sum[:])
	}
	return hex.EncodeToString(h.Sum(nil))
}

// validSitePath applies the contract's path rules.
func validSitePath(p string) error {
	if p == "" || strings.HasPrefix(p, "/") || strings.Contains(p, "\\") {
		return fmt.Errorf("export path %q is not a relative /-separated path", p)
	}
	for _, seg := range strings.Split(p, "/") {
		if seg == "" || seg == "." || seg == ".." {
			return fmt.Errorf("export path %q has an empty, '.' or '..' segment", p)
		}
	}
	if path.Clean(p) != p {
		return fmt.Errorf("export path %q is not clean", p)
	}
	return nil
}

// UploadSite replaces project id's site with site.
//
// site normally comes from ReadCloudSite, which has already encoded it; a
// CloudSite built by hand is encoded (and size-checked) here.
func (c *CloudClient) UploadSite(ctx context.Context, publishToken, id string, site CloudSite) (CloudUpload, error) {
	if site.body == nil {
		if site.Digest == "" {
			site.Digest = SiteDigest(site.Files)
		}
		if err := site.encode(); err != nil {
			return CloudUpload{}, err
		}
	}
	var out CloudUpload
	if err := c.doRaw(ctx, http.MethodPut, "/api/v1/projects/"+id+"/site",
		publishToken, TokenPublish, id, site.body, &out); err != nil {
		return out, err
	}
	return out, nil
}

// do sends a JSON body (or none) and decodes a JSON answer into out (when
// non-nil).
func (c *CloudClient) do(ctx context.Context, method, p, token string, kind TokenKind,
	project string, body, out any) error {
	var raw []byte
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return fmt.Errorf("encoding the request to %s: %w", p, err)
		}
		raw = b
	}
	return c.doRaw(ctx, method, p, token, kind, project, raw, out)
}

func (c *CloudClient) doRaw(ctx context.Context, method, p, token string, kind TokenKind,
	project string, body []byte, out any) error {
	var reader io.Reader
	if body != nil {
		reader = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.base.String()+p, reader)
	if err != nil {
		return fmt.Errorf("building %s %s: %w", method, p, err)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	req.Header.Set("Accept", "application/json")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}

	resp, err := c.http.Do(req)
	if err != nil {
		// url.Error carries the URL, never the Authorization header, so
		// this message is safe to print.
		return fmt.Errorf("orch-cloud %s %s: %w", method, p, err)
	}
	defer func() { _ = resp.Body.Close() }()

	// An answer is small JSON; a limit keeps a misconfigured URL that
	// points at some large page from being read into memory whole.
	data, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return fmt.Errorf("orch-cloud %s %s: reading the response: %w", method, p, err)
	}

	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		ce := &CloudError{Status: resp.StatusCode, Token: kind, Project: project}
		var eb struct {
			Error   string `json:"error"`
			Message string `json:"message"`
		}
		if json.Unmarshal(data, &eb) == nil && eb.Error != "" {
			ce.Code, ce.Message = eb.Error, eb.Message
		} else {
			// Not the contract's error shape: most likely the URL is not an
			// orch-cloud Worker at all. Say that instead of echoing HTML.
			ce.Code = "unexpected_response"
			ce.Message = fmt.Sprintf("%s did not answer with an orch-cloud error body — "+
				"is this the Worker's URL?", c.URL())
		}
		return ce
	}
	if out == nil || resp.StatusCode == http.StatusNoContent {
		return nil
	}
	if err := json.Unmarshal(data, out); err != nil {
		return fmt.Errorf("orch-cloud %s %s: the answer is not the expected JSON "+
			"(is %s an orch-cloud Worker?): %w", method, p, c.URL(), err)
	}
	return nil
}
