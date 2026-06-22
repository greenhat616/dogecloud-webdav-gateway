package doge

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
)

type APIKey struct {
	AccessKey string
	SecretKey string
}

type Client struct {
	BaseURL    string
	HTTPClient *http.Client
}

type APIError struct {
	Code int
	Msg  string
}

func (e APIError) Error() string {
	return fmt.Sprintf("dogecloud api error: code=%d msg=%s", e.Code, e.Msg)
}

type TmpTokenRequest struct {
	Channel      string   `json:"channel"`
	Scopes       []string `json:"scopes"`
	TTL          int      `json:"ttl,omitempty"`
	AllowActions []string `json:"allowActions,omitempty"`
}

type TmpTokenData struct {
	Credentials TemporaryCredentials `json:"Credentials"`
	ExpiredAt   int64                `json:"ExpiredAt"`
	Buckets     []Bucket             `json:"Buckets"`
}

type TemporaryCredentials struct {
	AccessKeyID     string `json:"accessKeyId"`
	SecretAccessKey string `json:"secretAccessKey"`
	SessionToken    string `json:"sessionToken"`
}

type Bucket struct {
	Name       string `json:"name"`
	S3Bucket   string `json:"s3Bucket"`
	S3Endpoint string `json:"s3Endpoint"`
}

type ListBucketsData struct {
	Buckets []Bucket `json:"buckets"`
}

type apiResponse[T any] struct {
	Code int     `json:"code"`
	Msg  string  `json:"msg"`
	Data T       `json:"data"`
	Cost float64 `json:"cost"`
	ST   int64   `json:"st"`
}

func NewClient(baseURL string, httpClient *http.Client) *Client {
	if httpClient == nil {
		httpClient = http.DefaultClient
	}
	return &Client{
		BaseURL:    strings.TrimRight(baseURL, "/"),
		HTTPClient: httpClient,
	}
}

func SignAuthorization(apiPath string, body []byte, key APIKey) string {
	mac := hmac.New(sha1.New, []byte(key.SecretKey))
	_, _ = mac.Write([]byte(apiPath))
	_, _ = mac.Write([]byte("\n"))
	_, _ = mac.Write(body)
	signature := hex.EncodeToString(mac.Sum(nil))
	return "TOKEN " + key.AccessKey + ":" + signature
}

func (c *Client) TemporaryToken(ctx context.Context, key APIKey, request TmpTokenRequest) (TmpTokenData, error) {
	var out TmpTokenData
	err := c.postJSON(ctx, key, "/auth/tmp_token.json", request, &out)
	return out, err
}

func (c *Client) ListBuckets(ctx context.Context, key APIKey) (ListBucketsData, error) {
	var out ListBucketsData
	err := c.postForm(ctx, key, "/oss/bucket/list.json", url.Values{}, &out)
	return out, err
}

func (c *Client) postJSON(ctx context.Context, key APIKey, apiPath string, payload any, out any) error {
	body, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	return c.do(ctx, key, apiPath, "application/json", body, out)
}

func (c *Client) postForm(ctx context.Context, key APIKey, apiPath string, form url.Values, out any) error {
	body := []byte(form.Encode())
	return c.do(ctx, key, apiPath, "application/x-www-form-urlencoded", body, out)
}

func (c *Client) do(ctx context.Context, key APIKey, apiPath, contentType string, body []byte, out any) error {
	if c.BaseURL == "" {
		return errors.New("dogecloud api base url is empty")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.BaseURL+apiPath, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", contentType)
	req.Header.Set("Authorization", SignAuthorization(apiPath, body, key))
	req.ContentLength = int64(len(body))

	resp, err := c.HTTPClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("dogecloud api http status %d: %s", resp.StatusCode, string(raw))
	}

	var envelope apiResponse[json.RawMessage]
	if err := json.Unmarshal(raw, &envelope); err != nil {
		return err
	}
	if envelope.Code != 200 {
		return APIError{Code: envelope.Code, Msg: envelope.Msg}
	}
	if out == nil {
		return nil
	}
	if len(envelope.Data) == 0 || string(envelope.Data) == "null" {
		return nil
	}
	return json.Unmarshal(envelope.Data, out)
}
