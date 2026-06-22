package doge

import "testing"

func TestSignAuthorizationMatchesOfficialExample(t *testing.T) {
	got := SignAuthorization("/auth/upload.json?filename=a.mp4", nil, APIKey{
		AccessKey: "MY_ACCESS_KEY",
		SecretKey: "MY_SECRET_KEY",
	})
	want := "TOKEN MY_ACCESS_KEY:bf5ec167c882d6ffa8afa4a1d2c2ed8d622beadf"
	if got != want {
		t.Fatalf("authorization = %q, want %q", got, want)
	}
}
