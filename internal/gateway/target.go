package gateway

import "github.com/greenhat616/dogecloud-webdav-gateway/internal/sigv4"

func UpstreamPath(bucket, key string) string {
	path := "/" + sigv4.URIEncode(bucket, true)
	if key != "" {
		path += "/" + sigv4.URIEncode(key, false)
	}
	return path
}
