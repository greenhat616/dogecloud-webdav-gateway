package gateway

import (
	"context"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"path"
	"strconv"
	"strings"
	"time"

	"github.com/greenhat616/dogecloud-webdav-gateway/internal/config"
	"github.com/greenhat616/dogecloud-webdav-gateway/internal/doge"
	"github.com/greenhat616/dogecloud-webdav-gateway/internal/sigv4"
)

const davNamespace = "DAV:"

type Gateway struct {
	dogeClient      *doge.Client
	tokenCache      *doge.TokenCache
	httpClient      *http.Client
	region          string
	service         string
	bodyMemoryLimit int64
	now             func() time.Time
}

type davIdentity struct {
	AccessKey string
	SecretKey string
}

type davTarget struct {
	Root       bool
	Bucket     string
	Key        string
	Collection bool
}

type s3Object struct {
	Key          string
	LastModified string
	ETag         string
	Size         int64
}

type s3CommonPrefix struct {
	Prefix string
}

type resolvedBucket struct {
	bucket      doge.Bucket
	credentials doge.TemporaryCredentials
}

type s3ListBucketResult struct {
	Name                  string           `xml:"Name"`
	Prefix                string           `xml:"Prefix"`
	KeyCount              int              `xml:"KeyCount"`
	MaxKeys               int              `xml:"MaxKeys"`
	IsTruncated           bool             `xml:"IsTruncated"`
	NextContinuationToken string           `xml:"NextContinuationToken"`
	Contents              []s3Object       `xml:"Contents"`
	CommonPrefixes        []s3CommonPrefix `xml:"CommonPrefixes"`
}

func New(cfg config.Config, dogeClient *doge.Client, httpClient *http.Client) *Gateway {
	if dogeClient == nil {
		dogeClient = doge.NewClient(cfg.DogeAPIBaseURL, httpClient)
	}
	if httpClient == nil {
		httpClient = http.DefaultClient
	}
	tokenCache := doge.NewTokenCache(dogeClient, cfg.TmpTokenTTL, cfg.TokenRefreshSkew, cfg.TokenCleanupInterval)
	return &Gateway{
		dogeClient:      dogeClient,
		tokenCache:      tokenCache,
		httpClient:      httpClient,
		region:          cfg.Region,
		service:         cfg.Service,
		bodyMemoryLimit: cfg.BodyMemoryLimit,
		now:             time.Now,
	}
}

func (g *Gateway) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path == "/healthz" {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok\n"))
		return
	}

	switch r.Method {
	case "OPTIONS":
		g.handleOptions(w)
		return
	case "PROPFIND", "GET", "HEAD", "PUT", "DELETE", "MKCOL", "COPY", "MOVE", "LOCK", "UNLOCK":
	default:
		writeDAVError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}

	identity, ok := basicIdentity(r)
	if !ok {
		writeAuthRequired(w)
		return
	}

	target, err := parseDAVTarget(r.URL.EscapedPath())
	if err != nil {
		writeDAVError(w, http.StatusBadRequest, err.Error())
		return
	}

	switch r.Method {
	case "PROPFIND":
		g.handlePropfind(w, r, identity, target)
	case "GET":
		g.handleGetHead(w, r, identity, target, false)
	case "HEAD":
		g.handleGetHead(w, r, identity, target, true)
	case "PUT":
		g.handlePut(w, r, identity, target)
	case "DELETE":
		g.handleDelete(w, r, identity, target)
	case "MKCOL":
		g.handleMkcol(w, r, identity, target)
	case "COPY":
		g.handleCopyMove(w, r, identity, target, false)
	case "MOVE":
		g.handleCopyMove(w, r, identity, target, true)
	case "LOCK":
		g.handleLock(w, r, target)
	case "UNLOCK":
		w.WriteHeader(http.StatusNoContent)
	}
}

func (g *Gateway) handleOptions(w http.ResponseWriter) {
	w.Header().Set("DAV", "1, 2")
	w.Header().Set("Allow", "OPTIONS, PROPFIND, GET, HEAD, PUT, DELETE, MKCOL, COPY, MOVE, LOCK, UNLOCK")
	w.Header().Set("MS-Author-Via", "DAV")
	w.WriteHeader(http.StatusNoContent)
}

func (g *Gateway) handlePropfind(w http.ResponseWriter, r *http.Request, identity davIdentity, target davTarget) {
	depth := davDepth(r.Header.Get("Depth"))
	if target.Root {
		buckets, err := g.dogeClient.ListBuckets(r.Context(), identity.apiKey())
		if err != nil {
			writeDAVError(w, http.StatusBadGateway, err.Error())
			return
		}
		responses := []davResponse{collectionResponse("/", time.Time{})}
		if depth != "0" {
			for _, bucket := range buckets.Buckets {
				if bucket.Name == "" {
					continue
				}
				responses = append(responses, collectionResponse("/"+escapePathSegment(bucket.Name)+"/", time.Time{}))
			}
		}
		writeMultiStatus(w, responses)
		return
	}

	if !target.Collection && depth == "0" {
		g.handleObjectPropfind(w, r, identity, target)
		return
	}

	bucket, err := g.bucketFor(r.Context(), identity, target.Bucket)
	if err != nil {
		writeDAVError(w, http.StatusBadGateway, err.Error())
		return
	}
	prefix := target.Key
	if prefix != "" && !strings.HasSuffix(prefix, "/") {
		prefix += "/"
	}
	list, err := g.listObjects(r.Context(), bucket, prefix, "/", "")
	if err != nil {
		writeDAVError(w, http.StatusBadGateway, err.Error())
		return
	}

	baseHref := hrefFor(target.Bucket, prefix, true)
	responses := []davResponse{collectionResponse(baseHref, time.Time{})}
	if depth != "0" {
		responses = append(responses, listResponses(target.Bucket, prefix, list)...)
	}
	writeMultiStatus(w, responses)
}

func (g *Gateway) handleObjectPropfind(w http.ResponseWriter, r *http.Request, identity davIdentity, target davTarget) {
	bucket, err := g.bucketFor(r.Context(), identity, target.Bucket)
	if err != nil {
		writeDAVError(w, http.StatusBadGateway, err.Error())
		return
	}
	resp, err := g.s3Request(r.Context(), http.MethodHead, bucket, target.Key, "", nil, 0, nil)
	if err != nil {
		writeDAVError(w, http.StatusBadGateway, err.Error())
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		writeDAVError(w, http.StatusNotFound, "not found")
		return
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		copyResponse(w, resp, true)
		return
	}
	size, _ := strconv.ParseInt(resp.Header.Get("Content-Length"), 10, 64)
	writeMultiStatus(w, []davResponse{fileResponse(hrefFor(target.Bucket, target.Key, false), size, resp.Header.Get("ETag"), parseHTTPTime(resp.Header.Get("Last-Modified")))})
}

func (g *Gateway) handleGetHead(w http.ResponseWriter, r *http.Request, identity davIdentity, target davTarget, head bool) {
	if target.Root || target.Key == "" || target.Collection {
		writeDAVError(w, http.StatusMethodNotAllowed, "GET/HEAD requires an object path")
		return
	}
	bucket, err := g.bucketFor(r.Context(), identity, target.Bucket)
	if err != nil {
		writeDAVError(w, http.StatusBadGateway, err.Error())
		return
	}
	method := http.MethodGet
	if head {
		method = http.MethodHead
	}
	resp, err := g.s3Request(r.Context(), method, bucket, target.Key, "", nil, 0, nil)
	if err != nil {
		writeDAVError(w, http.StatusBadGateway, err.Error())
		return
	}
	defer resp.Body.Close()
	copyResponse(w, resp, head)
}

func (g *Gateway) handlePut(w http.ResponseWriter, r *http.Request, identity davIdentity, target davTarget) {
	if target.Root || target.Key == "" || target.Collection {
		writeDAVError(w, http.StatusConflict, "PUT requires an object path")
		return
	}
	bucket, err := g.bucketFor(r.Context(), identity, target.Bucket)
	if err != nil {
		writeDAVError(w, http.StatusBadGateway, err.Error())
		return
	}

	body, err := BufferBody(r.Body, g.bodyMemoryLimit)
	if err != nil {
		writeDAVError(w, http.StatusBadRequest, "failed to read request body")
		return
	}
	defer body.Cleanup()
	bodyReader, err := body.Open()
	if err != nil {
		writeDAVError(w, http.StatusInternalServerError, "failed to reopen request body")
		return
	}
	defer bodyReader.Close()

	headers := http.Header{}
	if contentType := r.Header.Get("Content-Type"); contentType != "" {
		headers.Set("Content-Type", contentType)
	}
	resp, err := g.s3Request(r.Context(), http.MethodPut, bucket, target.Key, "", bodyReader, body.Size(), headers)
	if err != nil {
		writeDAVError(w, http.StatusBadGateway, err.Error())
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		copyResponse(w, resp, true)
		return
	}
	w.WriteHeader(http.StatusCreated)
}

func (g *Gateway) handleDelete(w http.ResponseWriter, r *http.Request, identity davIdentity, target davTarget) {
	if target.Root {
		writeDAVError(w, http.StatusForbidden, "cannot delete WebDAV root")
		return
	}
	bucket, err := g.bucketFor(r.Context(), identity, target.Bucket)
	if err != nil {
		writeDAVError(w, http.StatusBadGateway, err.Error())
		return
	}
	if target.Collection || target.Key == "" {
		prefix := target.Key
		if prefix != "" && !strings.HasSuffix(prefix, "/") {
			prefix += "/"
		}
		if err := g.deletePrefix(r.Context(), bucket, prefix); err != nil {
			writeDAVError(w, http.StatusBadGateway, err.Error())
			return
		}
		w.WriteHeader(http.StatusNoContent)
		return
	}
	resp, err := g.s3Request(r.Context(), http.MethodDelete, bucket, target.Key, "", nil, 0, nil)
	if err != nil {
		writeDAVError(w, http.StatusBadGateway, err.Error())
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		copyResponse(w, resp, true)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (g *Gateway) handleMkcol(w http.ResponseWriter, r *http.Request, identity davIdentity, target davTarget) {
	if target.Root || target.Key == "" {
		writeDAVError(w, http.StatusMethodNotAllowed, "MKCOL requires a collection path")
		return
	}
	bucket, err := g.bucketFor(r.Context(), identity, target.Bucket)
	if err != nil {
		writeDAVError(w, http.StatusBadGateway, err.Error())
		return
	}
	key := target.Key
	if !strings.HasSuffix(key, "/") {
		key += "/"
	}
	resp, err := g.s3Request(r.Context(), http.MethodPut, bucket, key, "", http.NoBody, 0, nil)
	if err != nil {
		writeDAVError(w, http.StatusBadGateway, err.Error())
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		copyResponse(w, resp, true)
		return
	}
	w.WriteHeader(http.StatusCreated)
}

func (g *Gateway) handleCopyMove(w http.ResponseWriter, r *http.Request, identity davIdentity, source davTarget, move bool) {
	if source.Root || source.Key == "" || source.Collection {
		writeDAVError(w, http.StatusMethodNotAllowed, "COPY/MOVE requires an object source")
		return
	}
	destination, err := parseDestination(r)
	if err != nil {
		writeDAVError(w, http.StatusBadRequest, err.Error())
		return
	}
	if destination.Root || destination.Key == "" || destination.Collection {
		writeDAVError(w, http.StatusBadRequest, "destination must be an object path")
		return
	}
	buckets, err := g.bucketsFor(r.Context(), identity, []string{source.Bucket, destination.Bucket})
	if err != nil {
		writeDAVError(w, http.StatusBadGateway, err.Error())
		return
	}
	sourceBucket, ok := buckets[source.Bucket]
	if !ok {
		writeDAVError(w, http.StatusNotFound, "source bucket not found")
		return
	}
	destinationBucket, ok := buckets[destination.Bucket]
	if !ok {
		writeDAVError(w, http.StatusNotFound, "destination bucket not found")
		return
	}

	copySource := "/" + sigv4.URIEncode(sourceBucket.bucket.S3Bucket, true) + "/" + sigv4.URIEncode(source.Key, false)
	headers := http.Header{"X-Amz-Copy-Source": []string{copySource}}
	resp, err := g.s3Request(r.Context(), http.MethodPut, destinationBucket, destination.Key, "", http.NoBody, 0, headers)
	if err != nil {
		writeDAVError(w, http.StatusBadGateway, err.Error())
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		copyResponse(w, resp, true)
		return
	}

	if move {
		delResp, err := g.s3Request(r.Context(), http.MethodDelete, sourceBucket, source.Key, "", nil, 0, nil)
		if err != nil {
			writeDAVError(w, http.StatusBadGateway, err.Error())
			return
		}
		defer delResp.Body.Close()
		if delResp.StatusCode < 200 || delResp.StatusCode >= 300 {
			copyResponse(w, delResp, true)
			return
		}
	}
	w.WriteHeader(http.StatusCreated)
}

func (g *Gateway) handleLock(w http.ResponseWriter, r *http.Request, target davTarget) {
	token := "opaquelocktoken:" + sigv4.HashString(r.URL.Path+"|"+g.now().Format(time.RFC3339Nano))
	w.Header().Set("Lock-Token", "<"+token+">")
	w.Header().Set("Content-Type", "application/xml; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_, _ = fmt.Fprintf(w, `<?xml version="1.0" encoding="utf-8"?><D:prop xmlns:D="DAV:"><D:lockdiscovery><D:activelock><D:locktype><D:write/></D:locktype><D:lockscope><D:exclusive/></D:lockscope><D:depth>infinity</D:depth><D:owner></D:owner><D:timeout>Second-3600</D:timeout><D:locktoken><D:href>%s</D:href></D:locktoken><D:lockroot><D:href>%s</D:href></D:lockroot></D:activelock></D:lockdiscovery></D:prop>`, xmlEscape(token), xmlEscape(target.href()))
}

func (g *Gateway) bucketFor(ctx context.Context, identity davIdentity, bucketName string) (resolvedBucket, error) {
	buckets, err := g.bucketsFor(ctx, identity, []string{bucketName})
	if err != nil {
		return resolvedBucket{}, err
	}
	bucket, ok := buckets[bucketName]
	if !ok {
		return resolvedBucket{}, fmt.Errorf("bucket %q is not available in DogeCloud temporary credentials", bucketName)
	}
	return bucket, nil
}

func (g *Gateway) bucketsFor(ctx context.Context, identity davIdentity, scopes []string) (map[string]resolvedBucket, error) {
	token, err := g.tokenCache.Get(ctx, identity.apiKey(), scopes)
	if err != nil {
		return nil, err
	}
	out := map[string]resolvedBucket{}
	for _, bucket := range token.Buckets {
		resolved := resolvedBucket{bucket: bucket, credentials: token.Credentials}
		if bucket.Name != "" {
			out[bucket.Name] = resolved
		}
		if bucket.S3Bucket != "" {
			out[bucket.S3Bucket] = resolved
		}
	}
	return out, nil
}

func (g *Gateway) listObjects(ctx context.Context, bucket resolvedBucket, prefix, delimiter, continuationToken string) (s3ListBucketResult, error) {
	values := url.Values{}
	values.Set("list-type", "2")
	if prefix != "" {
		values.Set("prefix", prefix)
	}
	if delimiter != "" {
		values.Set("delimiter", delimiter)
	}
	if continuationToken != "" {
		values.Set("continuation-token", continuationToken)
	}
	resp, err := g.s3Request(ctx, http.MethodGet, bucket, "", values.Encode(), nil, 0, nil)
	if err != nil {
		return s3ListBucketResult{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		raw, _ := io.ReadAll(resp.Body)
		return s3ListBucketResult{}, fmt.Errorf("s3 list failed: status=%d body=%s", resp.StatusCode, string(raw))
	}
	var out s3ListBucketResult
	if err := xml.NewDecoder(resp.Body).Decode(&out); err != nil {
		return s3ListBucketResult{}, err
	}
	return out, nil
}

func (g *Gateway) deletePrefix(ctx context.Context, bucket resolvedBucket, prefix string) error {
	continuation := ""
	for {
		list, err := g.listObjects(ctx, bucket, prefix, "", continuation)
		if err != nil {
			return err
		}
		for _, object := range list.Contents {
			resp, err := g.s3Request(ctx, http.MethodDelete, bucket, object.Key, "", nil, 0, nil)
			if err != nil {
				return err
			}
			_ = resp.Body.Close()
			if resp.StatusCode < 200 || resp.StatusCode >= 300 {
				return fmt.Errorf("s3 delete %q failed with status %d", object.Key, resp.StatusCode)
			}
		}
		if !list.IsTruncated || list.NextContinuationToken == "" {
			return nil
		}
		continuation = list.NextContinuationToken
	}
}

func (g *Gateway) s3Request(ctx context.Context, method string, bucket resolvedBucket, key, rawQuery string, body io.ReadCloser, contentLength int64, headers http.Header) (*http.Response, error) {
	if body == nil {
		body = http.NoBody
	}
	upstreamURL, err := buildUpstreamURL(bucket.bucket, key, rawQuery)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, method, upstreamURL, body)
	if err != nil {
		return nil, err
	}
	req.ContentLength = contentLength
	for name, values := range headers {
		for _, value := range values {
			req.Header.Add(name, value)
		}
	}
	req.Host = req.URL.Host

	payloadHash := sigv4.EmptyPayloadSHA256
	if method == http.MethodPut && body != http.NoBody {
		if seeker, ok := body.(interface {
			io.Seeker
		}); ok {
			current, err := seeker.Seek(0, io.SeekCurrent)
			if err != nil {
				return nil, err
			}
			raw, err := io.ReadAll(body)
			if err != nil {
				return nil, err
			}
			payloadHash = sigv4.HashBytes(raw)
			if _, err := seeker.Seek(current, io.SeekStart); err != nil {
				return nil, err
			}
		} else {
			payloadHash = sigv4.UnsignedPayload
		}
	}

	if err := sigv4.Sign(req, sigv4.Credentials{
		AccessKeyID:     bucket.credentials.AccessKeyID,
		SecretAccessKey: bucket.credentials.SecretAccessKey,
		SessionToken:    bucket.credentials.SessionToken,
	}, g.region, g.service, payloadHash, g.now()); err != nil {
		return nil, err
	}
	return g.httpClient.Do(req)
}

func (identity davIdentity) apiKey() doge.APIKey {
	return doge.APIKey{AccessKey: identity.AccessKey, SecretKey: identity.SecretKey}
}

func basicIdentity(r *http.Request) (davIdentity, bool) {
	username, password, ok := r.BasicAuth()
	if !ok || strings.TrimSpace(username) == "" || password == "" {
		return davIdentity{}, false
	}
	return davIdentity{AccessKey: username, SecretKey: password}, true
}

func parseDAVTarget(escapedPath string) (davTarget, error) {
	if escapedPath == "" || escapedPath == "/" {
		return davTarget{Root: true, Collection: true}, nil
	}
	decoded, err := url.PathUnescape(escapedPath)
	if err != nil {
		return davTarget{}, err
	}
	cleaned := path.Clean("/" + decoded)
	collection := strings.HasSuffix(decoded, "/")
	trimmed := strings.TrimPrefix(cleaned, "/")
	if trimmed == "" || trimmed == "." {
		return davTarget{Root: true, Collection: true}, nil
	}
	bucket, key, _ := strings.Cut(trimmed, "/")
	if bucket == "" {
		return davTarget{}, errors.New("missing bucket")
	}
	if key == "." {
		key = ""
	}
	if collection && key != "" && !strings.HasSuffix(key, "/") {
		key += "/"
	}
	return davTarget{
		Bucket:     bucket,
		Key:        key,
		Collection: collection || key == "",
	}, nil
}

func parseDestination(r *http.Request) (davTarget, error) {
	value := strings.TrimSpace(r.Header.Get("Destination"))
	if value == "" {
		return davTarget{}, errors.New("missing Destination header")
	}
	if u, err := url.Parse(value); err == nil && u.Scheme != "" {
		return parseDAVTarget(u.EscapedPath())
	}
	return parseDAVTarget(value)
}

func (t davTarget) href() string {
	if t.Root {
		return "/"
	}
	return hrefFor(t.Bucket, t.Key, t.Collection)
}

func davDepth(value string) string {
	value = strings.TrimSpace(strings.ToLower(value))
	if value == "0" {
		return "0"
	}
	return "1"
}

func buildUpstreamURL(bucket doge.Bucket, key, rawQuery string) (string, error) {
	if bucket.S3Endpoint == "" || bucket.S3Bucket == "" {
		return "", errors.New("temporary token response is missing s3 endpoint or bucket")
	}
	u, err := url.Parse(bucket.S3Endpoint)
	if err != nil {
		return "", err
	}
	u.Path = strings.TrimRight(u.EscapedPath(), "/") + UpstreamPath(bucket.S3Bucket, key)
	u.RawQuery = rawQuery
	return u.String(), nil
}

func copyResponse(w http.ResponseWriter, resp *http.Response, head bool) {
	for key, values := range resp.Header {
		if isHopByHopHeader(strings.ToLower(key)) {
			continue
		}
		for _, value := range values {
			w.Header().Add(key, value)
		}
	}
	w.WriteHeader(resp.StatusCode)
	if !head {
		_, _ = io.Copy(w, resp.Body)
	}
}

func isHopByHopHeader(lower string) bool {
	switch lower {
	case "connection", "keep-alive", "proxy-authenticate", "proxy-authorization", "te", "trailer", "transfer-encoding", "upgrade", "expect":
		return true
	default:
		return false
	}
}

func writeAuthRequired(w http.ResponseWriter) {
	w.Header().Set("WWW-Authenticate", `Basic realm="DogeCloud WebDAV"`)
	writeDAVError(w, http.StatusUnauthorized, "authentication required")
}

func writeDAVError(w http.ResponseWriter, status int, message string) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	http.Error(w, message, status)
}

func xmlEscape(value string) string {
	var builder strings.Builder
	_ = xml.EscapeText(&builder, []byte(value))
	return builder.String()
}

func parseHTTPTime(value string) time.Time {
	if value == "" {
		return time.Time{}
	}
	t, err := http.ParseTime(value)
	if err != nil {
		return time.Time{}
	}
	return t
}

func (g *Gateway) Shutdown(ctx context.Context) error {
	if g.tokenCache != nil {
		g.tokenCache.Close()
	}
	if closer, ok := g.httpClient.Transport.(interface{ CloseIdleConnections() }); ok {
		closer.CloseIdleConnections()
	}
	return nil
}
