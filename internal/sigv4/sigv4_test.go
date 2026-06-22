package sigv4

import (
	"net/http"
	"strings"
	"testing"
	"time"
)

func TestSignAndVerifyHeaderAuth(t *testing.T) {
	now := time.Date(2026, 6, 22, 10, 30, 0, 0, time.UTC)
	body := "hello"
	req, err := http.NewRequest(http.MethodPut, "https://gateway.example.com/photos/cat.jpg?partNumber=1&uploadId=abc", strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "text/plain")
	req.ContentLength = int64(len(body))
	if err := Sign(req, Credentials{
		AccessKeyID:     "AKID",
		SecretAccessKey: "SECRET",
	}, "us-east-1", "s3", HashString(body), now); err != nil {
		t.Fatal(err)
	}

	auth, err := ParseAuth(req)
	if err != nil {
		t.Fatal(err)
	}
	if auth.AccessKeyID != "AKID" {
		t.Fatalf("access key = %q", auth.AccessKeyID)
	}
	if err := Verify(req, auth, "SECRET", HashString(body), now); err != nil {
		t.Fatal(err)
	}
}

func TestVerifyRejectsChangedPayload(t *testing.T) {
	now := time.Date(2026, 6, 22, 10, 30, 0, 0, time.UTC)
	req, err := http.NewRequest(http.MethodPut, "https://gateway.example.com/photos/cat.jpg", strings.NewReader("hello"))
	if err != nil {
		t.Fatal(err)
	}
	req.ContentLength = int64(len("hello"))
	if err := Sign(req, Credentials{AccessKeyID: "AKID", SecretAccessKey: "SECRET"}, "us-east-1", "s3", HashString("hello"), now); err != nil {
		t.Fatal(err)
	}
	auth, err := ParseAuth(req)
	if err != nil {
		t.Fatal(err)
	}
	if err := Verify(req, auth, "SECRET", HashString("changed"), now); err == nil {
		t.Fatal("Verify succeeded for changed payload")
	}
}
