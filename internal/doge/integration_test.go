package doge

import (
	"context"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/joho/godotenv"
)

func TestRealDogeCloudConnection(t *testing.T) {
	_ = godotenv.Load("../../.env", ".env")

	accessKey := firstNonEmptyEnv("DOGE_ACCESS_KEY", "DOGE_API_KEY", "DOGE_API_ID")
	secretKey := firstNonEmptyEnv("DOGE_SECRET_KEY", "DOGE_API_SECRET", "DOGE_API_TOKEN")
	if accessKey == "" || secretKey == "" {
		t.Skip("set DOGE_ACCESS_KEY/DOGE_SECRET_KEY or DOGE_API_KEY/DOGE_API_SECRET to run the real DogeCloud integration test")
	}

	baseURL := firstNonEmptyEnv("DOGE_API_BASE_URL")
	if baseURL == "" {
		baseURL = "https://api.dogecloud.com"
	}

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	client := NewClient(baseURL, &http.Client{Timeout: 20 * time.Second})
	key := APIKey{AccessKey: accessKey, SecretKey: secretKey}

	buckets, err := client.ListBuckets(ctx, key)
	if err != nil {
		t.Fatalf("ListBuckets failed: %v", err)
	}
	t.Logf("DogeCloud returned %d bucket(s)", len(buckets.Buckets))

	bucketName := firstNonEmptyEnv("DOGE_TEST_BUCKET", "DOGE_REAL_TEST_BUCKET")
	if bucketName == "" && len(buckets.Buckets) > 0 {
		bucketName = buckets.Buckets[0].Name
	}
	if bucketName == "" {
		t.Skip("no bucket available for temporary token test; set DOGE_TEST_BUCKET to exercise /auth/tmp_token.json")
	}

	token, err := client.TemporaryToken(ctx, key, TmpTokenRequest{
		Channel: "OSS_FULL",
		Scopes:  []string{bucketName},
		TTL:     600,
	})
	if err != nil {
		t.Fatalf("TemporaryToken(%q) failed: %v", bucketName, err)
	}
	if token.Credentials.AccessKeyID == "" || token.Credentials.SecretAccessKey == "" || token.Credentials.SessionToken == "" {
		t.Fatalf("temporary credentials are incomplete: %#v", token.Credentials)
	}
	if token.ExpiredAt <= time.Now().Unix() {
		t.Fatalf("temporary token is already expired: %d", token.ExpiredAt)
	}

	var matched Bucket
	for _, bucket := range token.Buckets {
		if bucket.Name == bucketName || bucket.S3Bucket == bucketName {
			matched = bucket
			break
		}
	}
	if matched.S3Bucket == "" || matched.S3Endpoint == "" {
		t.Fatalf("temporary token response did not include s3Bucket/s3Endpoint for %q: %#v", bucketName, token.Buckets)
	}
	t.Logf("temporary token bucket mapping: %s -> %s @ %s", matched.Name, matched.S3Bucket, matched.S3Endpoint)
}

func firstNonEmptyEnv(keys ...string) string {
	for _, key := range keys {
		if value := strings.TrimSpace(os.Getenv(key)); value != "" {
			return value
		}
	}
	return ""
}
