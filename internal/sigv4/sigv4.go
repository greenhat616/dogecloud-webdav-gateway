package sigv4

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"
)

const (
	Algorithm          = "AWS4-HMAC-SHA256"
	ScopeTerminator    = "aws4_request"
	EmptyPayloadSHA256 = "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855"
	UnsignedPayload    = "UNSIGNED-PAYLOAD"
)

type Credentials struct {
	AccessKeyID     string
	SecretAccessKey string
	SessionToken    string
}

type AuthType int

const (
	AuthHeader AuthType = iota + 1
	AuthPresigned
)

type Auth struct {
	Type          AuthType
	AccessKeyID   string
	Date          string
	Region        string
	Service       string
	SignedHeaders []string
	Signature     string
	AmzDate       string
	Expires       time.Duration
}

func ParseAuth(r *http.Request) (Auth, error) {
	if authorization := strings.TrimSpace(r.Header.Get("Authorization")); authorization != "" {
		return parseHeaderAuth(authorization)
	}
	return parsePresignedAuth(r.URL.Query())
}

func Verify(r *http.Request, auth Auth, secretKey, bodyHash string, now time.Time) error {
	if secretKey == "" {
		return errors.New("empty secret key")
	}
	if auth.Signature == "" || auth.AccessKeyID == "" {
		return errors.New("missing signature")
	}
	if auth.Service != "s3" {
		return fmt.Errorf("unsupported service %q", auth.Service)
	}
	if auth.Type == AuthPresigned {
		if err := verifyPresignedTime(auth, now); err != nil {
			return err
		}
	}
	amzDate := auth.AmzDate
	if amzDate == "" {
		amzDate = r.Header.Get("X-Amz-Date")
	}
	if amzDate == "" {
		return errors.New("missing x-amz-date")
	}

	payloadHash, err := canonicalPayloadHash(r, auth.Type, bodyHash)
	if err != nil {
		return err
	}
	canonicalRequest, err := CanonicalRequest(r, auth.SignedHeaders, payloadHash, auth.Type == AuthPresigned)
	if err != nil {
		return err
	}
	scope := credentialScope(auth.Date, auth.Region, auth.Service)
	stringToSign := strings.Join([]string{
		Algorithm,
		amzDate,
		scope,
		HashString(canonicalRequest),
	}, "\n")
	expected := signature(secretKey, auth.Date, auth.Region, auth.Service, stringToSign)
	if !hmac.Equal([]byte(strings.ToLower(auth.Signature)), []byte(expected)) {
		return errors.New("signature does not match")
	}
	return nil
}

func Sign(r *http.Request, creds Credentials, region, service, payloadHash string, now time.Time) error {
	if creds.AccessKeyID == "" || creds.SecretAccessKey == "" {
		return errors.New("missing signing credentials")
	}
	if region == "" {
		region = "automatic"
	}
	if service == "" {
		service = "s3"
	}
	if payloadHash == "" {
		payloadHash = EmptyPayloadSHA256
	}

	r.Header.Del("Authorization")
	r.Header.Set("X-Amz-Date", now.UTC().Format("20060102T150405Z"))
	r.Header.Set("X-Amz-Content-Sha256", payloadHash)
	if creds.SessionToken != "" {
		r.Header.Set("X-Amz-Security-Token", creds.SessionToken)
	} else {
		r.Header.Del("X-Amz-Security-Token")
	}

	signedHeaders := signedHeadersForRequest(r)
	canonicalRequest, err := CanonicalRequest(r, signedHeaders, payloadHash, false)
	if err != nil {
		return err
	}
	date := now.UTC().Format("20060102")
	scope := credentialScope(date, region, service)
	stringToSign := strings.Join([]string{
		Algorithm,
		r.Header.Get("X-Amz-Date"),
		scope,
		HashString(canonicalRequest),
	}, "\n")
	sig := signature(creds.SecretAccessKey, date, region, service, stringToSign)
	r.Header.Set("Authorization", fmt.Sprintf("%s Credential=%s/%s, SignedHeaders=%s, Signature=%s",
		Algorithm,
		creds.AccessKeyID,
		scope,
		strings.Join(signedHeaders, ";"),
		sig,
	))
	return nil
}

func HashBytes(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func HashString(data string) string {
	return HashBytes([]byte(data))
}

func CanonicalRequest(r *http.Request, signedHeaders []string, payloadHash string, presigned bool) (string, error) {
	headers, signedHeadersValue, err := canonicalHeaders(r, signedHeaders)
	if err != nil {
		return "", err
	}
	query := canonicalQueryString(r.URL.RawQuery, presigned)
	return strings.Join([]string{
		r.Method,
		canonicalURI(r.URL),
		query,
		headers,
		signedHeadersValue,
		payloadHash,
	}, "\n"), nil
}

func parseHeaderAuth(authorization string) (Auth, error) {
	if !strings.HasPrefix(authorization, Algorithm+" ") {
		return Auth{}, errors.New("unsupported authorization algorithm")
	}
	params := map[string]string{}
	for _, part := range strings.Split(strings.TrimPrefix(authorization, Algorithm+" "), ",") {
		key, value, ok := strings.Cut(strings.TrimSpace(part), "=")
		if !ok {
			return Auth{}, errors.New("malformed authorization header")
		}
		params[key] = value
	}
	accessKey, date, region, service, err := parseCredential(params["Credential"])
	if err != nil {
		return Auth{}, err
	}
	signedHeaders := normalizeSignedHeaders(strings.Split(params["SignedHeaders"], ";"))
	if len(signedHeaders) == 0 {
		return Auth{}, errors.New("missing signed headers")
	}
	return Auth{
		Type:          AuthHeader,
		AccessKeyID:   accessKey,
		Date:          date,
		Region:        region,
		Service:       service,
		SignedHeaders: signedHeaders,
		Signature:     params["Signature"],
	}, nil
}

func parsePresignedAuth(query url.Values) (Auth, error) {
	if query.Get("X-Amz-Algorithm") != Algorithm {
		return Auth{}, errors.New("missing authorization")
	}
	accessKey, date, region, service, err := parseCredential(query.Get("X-Amz-Credential"))
	if err != nil {
		return Auth{}, err
	}
	expiresSeconds, err := strconv.Atoi(query.Get("X-Amz-Expires"))
	if err != nil || expiresSeconds < 0 {
		return Auth{}, errors.New("invalid presigned expiration")
	}
	signedHeaders := normalizeSignedHeaders(strings.Split(query.Get("X-Amz-SignedHeaders"), ";"))
	if len(signedHeaders) == 0 {
		return Auth{}, errors.New("missing signed headers")
	}
	return Auth{
		Type:          AuthPresigned,
		AccessKeyID:   accessKey,
		Date:          date,
		Region:        region,
		Service:       service,
		SignedHeaders: signedHeaders,
		Signature:     query.Get("X-Amz-Signature"),
		AmzDate:       query.Get("X-Amz-Date"),
		Expires:       time.Duration(expiresSeconds) * time.Second,
	}, nil
}

func parseCredential(value string) (accessKey, date, region, service string, err error) {
	parts := strings.Split(value, "/")
	if len(parts) != 5 || parts[4] != ScopeTerminator {
		return "", "", "", "", errors.New("invalid credential scope")
	}
	if parts[0] == "" || parts[1] == "" || parts[2] == "" || parts[3] == "" {
		return "", "", "", "", errors.New("invalid credential scope")
	}
	return parts[0], parts[1], parts[2], parts[3], nil
}

func verifyPresignedTime(auth Auth, now time.Time) error {
	signedAt, err := time.Parse("20060102T150405Z", auth.AmzDate)
	if err != nil {
		return errors.New("invalid x-amz-date")
	}
	if now.UTC().After(signedAt.Add(auth.Expires)) {
		return errors.New("presigned url expired")
	}
	return nil
}

func canonicalPayloadHash(r *http.Request, authType AuthType, bodyHash string) (string, error) {
	if authType == AuthPresigned {
		if value := r.URL.Query().Get("X-Amz-Content-Sha256"); value != "" {
			return verifyPayloadHeader(value, bodyHash)
		}
		if value := r.Header.Get("X-Amz-Content-Sha256"); value != "" {
			return verifyPayloadHeader(value, bodyHash)
		}
		return UnsignedPayload, nil
	}

	value := r.Header.Get("X-Amz-Content-Sha256")
	if value == "" {
		value = bodyHash
	}
	return verifyPayloadHeader(value, bodyHash)
}

func verifyPayloadHeader(value, bodyHash string) (string, error) {
	if value == UnsignedPayload || strings.HasPrefix(value, "STREAMING-") {
		return value, nil
	}
	if len(value) == 64 && strings.EqualFold(value, bodyHash) {
		return strings.ToLower(value), nil
	}
	if len(value) == 64 {
		return "", errors.New("payload hash does not match body")
	}
	return value, nil
}

func normalizeSignedHeaders(headers []string) []string {
	out := make([]string, 0, len(headers))
	seen := map[string]struct{}{}
	for _, header := range headers {
		header = strings.ToLower(strings.TrimSpace(header))
		if header == "" {
			continue
		}
		if _, ok := seen[header]; ok {
			continue
		}
		seen[header] = struct{}{}
		out = append(out, header)
	}
	sort.Strings(out)
	return out
}

func canonicalURI(u *url.URL) string {
	path := u.EscapedPath()
	if path == "" {
		path = "/"
	}
	decoded, err := url.PathUnescape(path)
	if err != nil {
		decoded = path
	}
	return URIEncode(decoded, false)
}

func canonicalQueryString(rawQuery string, presigned bool) string {
	type pair struct {
		key   string
		value string
	}
	if rawQuery == "" {
		return ""
	}
	pairs := []pair{}
	for _, part := range strings.Split(rawQuery, "&") {
		if part == "" {
			continue
		}
		rawKey, rawValue, _ := strings.Cut(part, "=")
		key, err := url.QueryUnescape(rawKey)
		if err != nil {
			key = rawKey
		}
		if presigned && strings.EqualFold(key, "X-Amz-Signature") {
			continue
		}
		value, err := url.QueryUnescape(rawValue)
		if err != nil {
			value = rawValue
		}
		pairs = append(pairs, pair{
			key:   URIEncode(key, true),
			value: URIEncode(value, true),
		})
	}
	sort.Slice(pairs, func(i, j int) bool {
		if pairs[i].key == pairs[j].key {
			return pairs[i].value < pairs[j].value
		}
		return pairs[i].key < pairs[j].key
	})
	parts := make([]string, len(pairs))
	for i, pair := range pairs {
		parts[i] = pair.key + "=" + pair.value
	}
	return strings.Join(parts, "&")
}

func canonicalHeaders(r *http.Request, signedHeaders []string) (string, string, error) {
	normalized := normalizeSignedHeaders(signedHeaders)
	lines := make([]string, 0, len(normalized))
	for _, header := range normalized {
		value, ok := headerValue(r, header)
		if !ok {
			return "", "", fmt.Errorf("signed header %q missing", header)
		}
		lines = append(lines, header+":"+value+"\n")
	}
	return strings.Join(lines, ""), strings.Join(normalized, ";"), nil
}

func headerValue(r *http.Request, name string) (string, bool) {
	switch name {
	case "host":
		if r.Host != "" {
			return cleanHeaderValue(r.Host), true
		}
		if r.URL.Host != "" {
			return cleanHeaderValue(r.URL.Host), true
		}
		return "", false
	case "content-length":
		if values, ok := r.Header[http.CanonicalHeaderKey(name)]; ok && len(values) > 0 {
			return cleanJoined(values), true
		}
		if r.ContentLength >= 0 {
			return strconv.FormatInt(r.ContentLength, 10), true
		}
		return "", false
	default:
		values, ok := headerValues(r.Header, name)
		if !ok || len(values) == 0 {
			return "", false
		}
		return cleanJoined(values), true
	}
}

func headerValues(headers http.Header, name string) ([]string, bool) {
	for key, values := range headers {
		if strings.EqualFold(key, name) {
			return values, true
		}
	}
	return nil, false
}

func cleanJoined(values []string) string {
	cleaned := make([]string, 0, len(values))
	for _, value := range values {
		cleaned = append(cleaned, cleanHeaderValue(value))
	}
	return strings.Join(cleaned, ",")
}

func cleanHeaderValue(value string) string {
	return strings.Join(strings.Fields(strings.TrimSpace(value)), " ")
}

func signedHeadersForRequest(r *http.Request) []string {
	headers := []string{"host"}
	seen := map[string]struct{}{"host": {}}
	for key := range r.Header {
		lower := strings.ToLower(key)
		if lower == "authorization" || isHopByHopHeader(lower) {
			continue
		}
		if lower == "content-length" {
			continue
		}
		if _, ok := seen[lower]; ok {
			continue
		}
		seen[lower] = struct{}{}
		headers = append(headers, lower)
	}
	sort.Strings(headers)
	return headers
}

func isHopByHopHeader(lower string) bool {
	switch lower {
	case "connection", "keep-alive", "proxy-authenticate", "proxy-authorization", "te", "trailer", "transfer-encoding", "upgrade", "expect":
		return true
	default:
		return false
	}
}

func credentialScope(date, region, service string) string {
	return date + "/" + region + "/" + service + "/" + ScopeTerminator
}

func signature(secretKey, date, region, service, stringToSign string) string {
	dateKey := hmacSHA256([]byte("AWS4"+secretKey), date)
	dateRegionKey := hmacSHA256(dateKey, region)
	dateRegionServiceKey := hmacSHA256(dateRegionKey, service)
	signingKey := hmacSHA256(dateRegionServiceKey, ScopeTerminator)
	return hex.EncodeToString(hmacSHA256(signingKey, stringToSign))
}

func hmacSHA256(key []byte, data string) []byte {
	mac := hmac.New(sha256.New, key)
	_, _ = mac.Write([]byte(data))
	return mac.Sum(nil)
}

func URIEncode(value string, encodeSlash bool) string {
	var builder strings.Builder
	for _, b := range []byte(value) {
		if isUnreserved(b) || (!encodeSlash && b == '/') {
			builder.WriteByte(b)
			continue
		}
		builder.WriteByte('%')
		builder.WriteString(strings.ToUpper(hex.EncodeToString([]byte{b})))
	}
	return builder.String()
}

func isUnreserved(b byte) bool {
	return (b >= 'A' && b <= 'Z') ||
		(b >= 'a' && b <= 'z') ||
		(b >= '0' && b <= '9') ||
		b == '-' || b == '_' || b == '.' || b == '~'
}
