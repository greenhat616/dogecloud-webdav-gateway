package gateway

import (
	"bytes"
	"encoding/json"
	"encoding/xml"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/greenhat616/dogecloud-webdav-gateway/internal/config"
	"github.com/greenhat616/dogecloud-webdav-gateway/internal/doge"
	"github.com/greenhat616/dogecloud-webdav-gateway/internal/sigv4"
)

const (
	dogeAccessKey   = "doge-ak"
	dogeSecretKey   = "doge-sk"
	tmpAccessKey    = "tmp-ak"
	tmpSecretKey    = "tmp-sk"
	tmpSessionToken = "tmp-session"
	publicBucket    = "photos"
	realBucket      = "real-photos"
)

func TestWebDAVFullScenario(t *testing.T) {
	fakeS3 := newFakeS3(t)
	s3Server := httptest.NewServer(fakeS3)
	defer s3Server.Close()

	dogeServer := httptest.NewServer(newFakeDoge(t, s3Server.URL))
	defer dogeServer.Close()

	handler := New(config.Config{
		Addr:                 ":0",
		DogeAPIBaseURL:       dogeServer.URL,
		Region:               "automatic",
		Service:              "s3",
		TmpTokenTTL:          7200,
		TokenRefreshSkew:     time.Second,
		TokenCleanupInterval: 0,
		BodyMemoryLimit:      32,
	}, doge.NewClient(dogeServer.URL, http.DefaultClient), http.DefaultClient)
	server := httptest.NewServer(handler)
	defer server.Close()

	client := server.Client()

	resp := doDAV(t, client, http.MethodOptions, server.URL+"/", nil, nil)
	assertStatus(t, resp, http.StatusNoContent)
	if dav := resp.Header.Get("DAV"); !strings.Contains(dav, "1") {
		t.Fatalf("DAV header = %q", dav)
	}
	_ = resp.Body.Close()

	resp = doDAVNoAuth(t, client, "PROPFIND", server.URL+"/", nil, map[string]string{"Depth": "1"})
	assertStatus(t, resp, http.StatusUnauthorized)
	_ = resp.Body.Close()

	resp = doDAV(t, client, "PROPFIND", server.URL+"/", nil, map[string]string{"Depth": "1"})
	assertStatus(t, resp, 207)
	rootHrefs := readDAVHrefs(t, resp)
	assertContains(t, rootHrefs, "/")
	assertContains(t, rootHrefs, "/photos/")

	resp = doDAV(t, client, "MKCOL", server.URL+"/photos/docs/", nil, nil)
	assertStatus(t, resp, http.StatusCreated)
	_ = resp.Body.Close()
	if !fakeS3.has(realBucket, "docs/") {
		t.Fatal("MKCOL did not create directory marker")
	}

	resp = doDAV(t, client, http.MethodPut, server.URL+"/photos/docs/readme.txt", strings.NewReader("hello"), map[string]string{"Content-Type": "text/plain"})
	assertStatus(t, resp, http.StatusCreated)
	_ = resp.Body.Close()
	if got := fakeS3.get(realBucket, "docs/readme.txt"); string(got) != "hello" {
		t.Fatalf("uploaded readme = %q", got)
	}

	streamPayload := strings.Repeat("stream-", 2048)
	resp = doStreamPUT(t, client, server.URL+"/photos/docs/stream.bin", streamPayload)
	assertStatus(t, resp, http.StatusCreated)
	_ = resp.Body.Close()
	if got := fakeS3.get(realBucket, "docs/stream.bin"); string(got) != streamPayload {
		t.Fatalf("stream upload length = %d, want %d", len(got), len(streamPayload))
	}

	resp = doDAV(t, client, "PROPFIND", server.URL+"/photos/docs/", nil, map[string]string{"Depth": "1"})
	assertStatus(t, resp, 207)
	docsHrefs := readDAVHrefs(t, resp)
	assertContains(t, docsHrefs, "/photos/docs/")
	assertContains(t, docsHrefs, "/photos/docs/readme.txt")
	assertContains(t, docsHrefs, "/photos/docs/stream.bin")

	resp = doDAV(t, client, http.MethodGet, server.URL+"/photos/docs/readme.txt", nil, nil)
	assertStatus(t, resp, http.StatusOK)
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if string(raw) != "hello" {
		t.Fatalf("GET body = %q", raw)
	}

	resp = doDAV(t, client, http.MethodHead, server.URL+"/photos/docs/readme.txt", nil, nil)
	assertStatus(t, resp, http.StatusOK)
	if got := resp.Header.Get("Content-Length"); got != "5" {
		t.Fatalf("HEAD Content-Length = %q", got)
	}
	_ = resp.Body.Close()

	resp = doDAV(t, client, "COPY", server.URL+"/photos/docs/readme.txt", nil, map[string]string{"Destination": server.URL + "/photos/docs/copy.txt"})
	assertStatus(t, resp, http.StatusCreated)
	_ = resp.Body.Close()
	if got := fakeS3.get(realBucket, "docs/copy.txt"); string(got) != "hello" {
		t.Fatalf("copied body = %q", got)
	}

	resp = doDAV(t, client, "MOVE", server.URL+"/photos/docs/copy.txt", nil, map[string]string{"Destination": server.URL + "/photos/docs/moved.txt"})
	assertStatus(t, resp, http.StatusCreated)
	_ = resp.Body.Close()
	if fakeS3.has(realBucket, "docs/copy.txt") {
		t.Fatal("MOVE kept source object")
	}
	if got := fakeS3.get(realBucket, "docs/moved.txt"); string(got) != "hello" {
		t.Fatalf("moved body = %q", got)
	}

	resp = doDAV(t, client, "LOCK", server.URL+"/photos/docs/moved.txt", strings.NewReader(lockRequestXML), nil)
	assertStatus(t, resp, http.StatusOK)
	lockToken := resp.Header.Get("Lock-Token")
	if lockToken == "" {
		t.Fatal("LOCK did not return Lock-Token")
	}
	_ = resp.Body.Close()

	resp = doDAV(t, client, "UNLOCK", server.URL+"/photos/docs/moved.txt", nil, map[string]string{"Lock-Token": lockToken})
	assertStatus(t, resp, http.StatusNoContent)
	_ = resp.Body.Close()

	resp = doDAV(t, client, http.MethodDelete, server.URL+"/photos/docs/readme.txt", nil, nil)
	assertStatus(t, resp, http.StatusNoContent)
	_ = resp.Body.Close()
	if fakeS3.has(realBucket, "docs/readme.txt") {
		t.Fatal("DELETE kept readme object")
	}

	resp = doDAV(t, client, http.MethodDelete, server.URL+"/photos/docs/", nil, nil)
	assertStatus(t, resp, http.StatusNoContent)
	_ = resp.Body.Close()
	for _, key := range []string{"docs/", "docs/stream.bin", "docs/moved.txt"} {
		if fakeS3.has(realBucket, key) {
			t.Fatalf("collection DELETE kept %q", key)
		}
	}

	if got := atomic.LoadInt32(&fakeS3.putCalls); got < 4 {
		t.Fatalf("expected put/copy calls, got %d", got)
	}
	if got := atomic.LoadInt32(&fakeS3.deleteCalls); got < 3 {
		t.Fatalf("expected delete calls, got %d", got)
	}
}

func TestWebDAVObjectPropfindAndMissingObject(t *testing.T) {
	fakeS3 := newFakeS3(t)
	fakeS3.put(realBucket, "cat.jpg", []byte("meow"), "image/jpeg")
	s3Server := httptest.NewServer(fakeS3)
	defer s3Server.Close()

	dogeServer := httptest.NewServer(newFakeDoge(t, s3Server.URL))
	defer dogeServer.Close()
	handler := New(testConfig(dogeServer.URL), doge.NewClient(dogeServer.URL, http.DefaultClient), http.DefaultClient)
	server := httptest.NewServer(handler)
	defer server.Close()

	resp := doDAV(t, server.Client(), "PROPFIND", server.URL+"/photos/cat.jpg", nil, map[string]string{"Depth": "0"})
	assertStatus(t, resp, 207)
	hrefs := readDAVHrefs(t, resp)
	assertContains(t, hrefs, "/photos/cat.jpg")

	resp = doDAV(t, server.Client(), http.MethodGet, server.URL+"/photos/missing.jpg", nil, nil)
	assertStatus(t, resp, http.StatusNotFound)
	_ = resp.Body.Close()
}

func testConfig(dogeURL string) config.Config {
	return config.Config{
		Addr:                 ":0",
		DogeAPIBaseURL:       dogeURL,
		Region:               "automatic",
		Service:              "s3",
		TmpTokenTTL:          7200,
		TokenRefreshSkew:     time.Second,
		TokenCleanupInterval: 0,
		BodyMemoryLimit:      1024,
	}
}

func doDAV(t *testing.T, client *http.Client, method, target string, body io.Reader, headers map[string]string) *http.Response {
	t.Helper()
	req, err := http.NewRequest(method, target, body)
	if err != nil {
		t.Fatal(err)
	}
	req.SetBasicAuth(dogeAccessKey, dogeSecretKey)
	for key, value := range headers {
		req.Header.Set(key, value)
	}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	return resp
}

func doDAVNoAuth(t *testing.T, client *http.Client, method, target string, body io.Reader, headers map[string]string) *http.Response {
	t.Helper()
	req, err := http.NewRequest(method, target, body)
	if err != nil {
		t.Fatal(err)
	}
	for key, value := range headers {
		req.Header.Set(key, value)
	}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	return resp
}

func doStreamPUT(t *testing.T, client *http.Client, target, payload string) *http.Response {
	t.Helper()
	reader, writer := io.Pipe()
	req, err := http.NewRequest(http.MethodPut, target, reader)
	if err != nil {
		t.Fatal(err)
	}
	req.SetBasicAuth(dogeAccessKey, dogeSecretKey)
	req.Header.Set("Content-Type", "application/octet-stream")
	req.ContentLength = -1

	errCh := make(chan error, 1)
	go func() {
		defer writer.Close()
		for _, chunk := range chunkString(payload, 257) {
			if _, err := io.WriteString(writer, chunk); err != nil {
				errCh <- err
				return
			}
		}
		errCh <- nil
	}()

	resp, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	if err := <-errCh; err != nil {
		t.Fatal(err)
	}
	return resp
}

func chunkString(value string, size int) []string {
	out := []string{}
	for len(value) > 0 {
		if len(value) <= size {
			out = append(out, value)
			break
		}
		out = append(out, value[:size])
		value = value[size:]
	}
	return out
}

func assertStatus(t *testing.T, resp *http.Response, want int) {
	t.Helper()
	if resp.StatusCode != want {
		raw, _ := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		t.Fatalf("status = %d, want %d, body = %s", resp.StatusCode, want, raw)
	}
}

func readDAVHrefs(t *testing.T, resp *http.Response) []string {
	t.Helper()
	defer resp.Body.Close()
	var parsed struct {
		Responses []struct {
			Href string `xml:"href"`
		} `xml:"response"`
	}
	if err := xml.NewDecoder(resp.Body).Decode(&parsed); err != nil {
		t.Fatal(err)
	}
	hrefs := make([]string, 0, len(parsed.Responses))
	for _, response := range parsed.Responses {
		hrefs = append(hrefs, response.Href)
	}
	sort.Strings(hrefs)
	return hrefs
}

func assertContains(t *testing.T, values []string, want string) {
	t.Helper()
	for _, value := range values {
		if value == want {
			return
		}
	}
	t.Fatalf("%q not found in %#v", want, values)
}

type fakeDoge struct {
	t     *testing.T
	s3URL string
}

func newFakeDoge(t *testing.T, s3URL string) http.Handler {
	return &fakeDoge{t: t, s3URL: s3URL}
}

func (f *fakeDoge) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	raw, err := io.ReadAll(r.Body)
	if err != nil {
		f.t.Fatal(err)
	}
	wantAuth := doge.SignAuthorization(r.URL.RequestURI(), raw, doge.APIKey{
		AccessKey: dogeAccessKey,
		SecretKey: dogeSecretKey,
	})
	if got := r.Header.Get("Authorization"); got != wantAuth {
		f.t.Fatalf("doge authorization = %q, want %q", got, wantAuth)
	}

	switch r.URL.Path {
	case "/oss/bucket/list.json":
		writeJSON(f.t, w, map[string]any{
			"code": 200,
			"msg":  "OK",
			"data": map[string]any{
				"buckets": []map[string]string{{
					"name":       publicBucket,
					"s3Bucket":   realBucket,
					"s3Endpoint": f.s3URL,
				}},
			},
		})
	case "/auth/tmp_token.json":
		var payload doge.TmpTokenRequest
		if err := json.Unmarshal(raw, &payload); err != nil {
			f.t.Fatal(err)
		}
		if payload.Channel != "OSS_FULL" {
			f.t.Fatalf("tmp token channel = %q", payload.Channel)
		}
		if len(payload.Scopes) == 0 {
			f.t.Fatal("tmp token request has no scopes")
		}
		writeJSON(f.t, w, map[string]any{
			"code": 200,
			"msg":  "OK",
			"data": map[string]any{
				"Credentials": map[string]string{
					"accessKeyId":     tmpAccessKey,
					"secretAccessKey": tmpSecretKey,
					"sessionToken":    tmpSessionToken,
				},
				"ExpiredAt": time.Now().Add(time.Hour).Unix(),
				"Buckets": []map[string]string{{
					"name":       publicBucket,
					"s3Bucket":   realBucket,
					"s3Endpoint": f.s3URL,
				}},
			},
		})
	default:
		http.NotFound(w, r)
	}
}

func writeJSON(t *testing.T, w http.ResponseWriter, value any) {
	t.Helper()
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(value); err != nil {
		t.Fatal(err)
	}
}

type fakeS3 struct {
	t           *testing.T
	mu          sync.Mutex
	objects     map[string]map[string]fakeObject
	putCalls    int32
	deleteCalls int32
}

type fakeObject struct {
	body        []byte
	contentType string
	modified    time.Time
	etag        string
}

func newFakeS3(t *testing.T) *fakeS3 {
	return &fakeS3{
		t:       t,
		objects: map[string]map[string]fakeObject{realBucket: {}},
	}
}

func (f *fakeS3) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	bucket, key := splitS3Path(r.URL.EscapedPath())
	if bucket == "" {
		http.NotFound(w, r)
		return
	}
	raw, err := io.ReadAll(r.Body)
	if err != nil {
		f.t.Fatal(err)
	}
	verifyS3Signature(f.t, r, raw)

	if r.Method == http.MethodGet && r.URL.Query().Get("list-type") == "2" {
		f.handleList(w, bucket, r.URL.Query())
		return
	}
	switch r.Method {
	case http.MethodPut:
		atomic.AddInt32(&f.putCalls, 1)
		if copySource := r.Header.Get("X-Amz-Copy-Source"); copySource != "" {
			f.handleCopy(w, bucket, key, copySource)
			return
		}
		f.put(bucket, key, raw, r.Header.Get("Content-Type"))
		w.Header().Set("ETag", quote(sigv4.HashBytes(raw)))
		w.WriteHeader(http.StatusOK)
	case http.MethodGet:
		obj, ok := f.object(bucket, key)
		if !ok {
			http.NotFound(w, r)
			return
		}
		writeObjectHeaders(w, obj)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(obj.body)
	case http.MethodHead:
		obj, ok := f.object(bucket, key)
		if !ok {
			http.NotFound(w, r)
			return
		}
		writeObjectHeaders(w, obj)
		w.WriteHeader(http.StatusOK)
	case http.MethodDelete:
		atomic.AddInt32(&f.deleteCalls, 1)
		f.mu.Lock()
		delete(f.objects[bucket], key)
		f.mu.Unlock()
		w.WriteHeader(http.StatusNoContent)
	default:
		http.Error(w, "unsupported s3 method", http.StatusMethodNotAllowed)
	}
}

func (f *fakeS3) handleList(w http.ResponseWriter, bucket string, query url.Values) {
	prefix := query.Get("prefix")
	delimiter := query.Get("delimiter")
	f.mu.Lock()
	defer f.mu.Unlock()
	seenPrefixes := map[string]struct{}{}
	result := struct {
		XMLName     xml.Name `xml:"ListBucketResult"`
		Name        string   `xml:"Name"`
		Prefix      string   `xml:"Prefix"`
		KeyCount    int      `xml:"KeyCount"`
		MaxKeys     int      `xml:"MaxKeys"`
		IsTruncated bool     `xml:"IsTruncated"`
		Contents    []struct {
			Key          string `xml:"Key"`
			LastModified string `xml:"LastModified"`
			ETag         string `xml:"ETag"`
			Size         int64  `xml:"Size"`
		} `xml:"Contents"`
		CommonPrefixes []struct {
			Prefix string `xml:"Prefix"`
		} `xml:"CommonPrefixes"`
	}{
		Name:    bucket,
		Prefix:  prefix,
		MaxKeys: 1000,
	}
	for key, object := range f.objects[bucket] {
		if !strings.HasPrefix(key, prefix) {
			continue
		}
		rest := strings.TrimPrefix(key, prefix)
		if delimiter != "" {
			if idx := strings.Index(rest, delimiter); idx >= 0 {
				commonPrefix := prefix + rest[:idx+len(delimiter)]
				if _, ok := seenPrefixes[commonPrefix]; !ok {
					seenPrefixes[commonPrefix] = struct{}{}
					result.CommonPrefixes = append(result.CommonPrefixes, struct {
						Prefix string `xml:"Prefix"`
					}{Prefix: commonPrefix})
				}
				continue
			}
		}
		result.Contents = append(result.Contents, struct {
			Key          string `xml:"Key"`
			LastModified string `xml:"LastModified"`
			ETag         string `xml:"ETag"`
			Size         int64  `xml:"Size"`
		}{
			Key:          key,
			LastModified: object.modified.UTC().Format(time.RFC3339),
			ETag:         object.etag,
			Size:         int64(len(object.body)),
		})
	}
	result.KeyCount = len(result.Contents) + len(result.CommonPrefixes)
	w.Header().Set("Content-Type", "application/xml")
	if err := xml.NewEncoder(w).Encode(result); err != nil {
		f.t.Fatal(err)
	}
}

func (f *fakeS3) handleCopy(w http.ResponseWriter, bucket, key, copySource string) {
	sourceBucket, sourceKey := splitCopySource(copySource)
	obj, ok := f.object(sourceBucket, sourceKey)
	if !ok {
		http.Error(w, "copy source not found", http.StatusNotFound)
		return
	}
	f.put(bucket, key, obj.body, obj.contentType)
	w.WriteHeader(http.StatusOK)
}

func (f *fakeS3) put(bucket, key string, body []byte, contentType string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.objects[bucket] == nil {
		f.objects[bucket] = map[string]fakeObject{}
	}
	copied := append([]byte(nil), body...)
	f.objects[bucket][key] = fakeObject{
		body:        copied,
		contentType: contentType,
		modified:    time.Now().UTC(),
		etag:        quote(sigv4.HashBytes(copied)),
	}
}

func (f *fakeS3) has(bucket, key string) bool {
	_, ok := f.object(bucket, key)
	return ok
}

func (f *fakeS3) get(bucket, key string) []byte {
	obj, ok := f.object(bucket, key)
	if !ok {
		return nil
	}
	return append([]byte(nil), obj.body...)
}

func (f *fakeS3) object(bucket, key string) (fakeObject, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	obj, ok := f.objects[bucket][key]
	return obj, ok
}

func verifyS3Signature(t *testing.T, r *http.Request, raw []byte) {
	t.Helper()
	if got := r.Header.Get("X-Amz-Security-Token"); got != tmpSessionToken {
		t.Fatalf("session token = %q", got)
	}
	r.Body = io.NopCloser(bytes.NewReader(raw))
	r.ContentLength = int64(len(raw))
	auth, err := sigv4.ParseAuth(r)
	if err != nil {
		t.Fatal(err)
	}
	bodyHash := sigv4.HashBytes(raw)
	if r.Header.Get("X-Amz-Content-Sha256") == sigv4.UnsignedPayload {
		bodyHash = sigv4.UnsignedPayload
	}
	if err := sigv4.Verify(r, auth, tmpSecretKey, bodyHash, time.Now()); err != nil {
		t.Fatal(err)
	}
	if auth.AccessKeyID != tmpAccessKey {
		t.Fatalf("access key = %q", auth.AccessKeyID)
	}
}

func writeObjectHeaders(w http.ResponseWriter, obj fakeObject) {
	w.Header().Set("ETag", obj.etag)
	w.Header().Set("Last-Modified", obj.modified.UTC().Format(http.TimeFormat))
	w.Header().Set("Content-Length", fmt.Sprintf("%d", len(obj.body)))
	if obj.contentType != "" {
		w.Header().Set("Content-Type", obj.contentType)
	}
}

func splitS3Path(escapedPath string) (string, string) {
	trimmed := strings.TrimPrefix(escapedPath, "/")
	bucketPart, keyPart, _ := strings.Cut(trimmed, "/")
	bucket, _ := url.PathUnescape(bucketPart)
	key, _ := url.PathUnescape(keyPart)
	return bucket, key
}

func splitCopySource(value string) (string, string) {
	value = strings.Trim(value, "<>")
	if strings.HasPrefix(value, "http://") || strings.HasPrefix(value, "https://") {
		if u, err := url.Parse(value); err == nil {
			value = u.EscapedPath()
		}
	}
	if idx := strings.IndexByte(value, '?'); idx >= 0 {
		value = value[:idx]
	}
	return splitS3Path(value)
}

func quote(value string) string {
	return `"` + value + `"`
}

const lockRequestXML = `<?xml version="1.0" encoding="utf-8" ?>
<D:lockinfo xmlns:D="DAV:">
  <D:lockscope><D:exclusive/></D:lockscope>
  <D:locktype><D:write/></D:locktype>
</D:lockinfo>`
