package gateway

import (
	"encoding/xml"
	"net/http"
	"net/url"
	"path"
	"strings"
	"time"

	"github.com/greenhat616/dogecloud-webdav-gateway/internal/sigv4"
)

type davMultiStatus struct {
	XMLName   xml.Name      `xml:"D:multistatus"`
	XmlnsD    string        `xml:"xmlns:D,attr"`
	Responses []davResponse `xml:"D:response"`
}

type davResponse struct {
	Href     string        `xml:"D:href"`
	Propstat davPropStatus `xml:"D:propstat"`
}

type davPropStatus struct {
	Prop   davProp `xml:"D:prop"`
	Status string  `xml:"D:status"`
}

type davProp struct {
	ResourceType  *davResourceType  `xml:"D:resourcetype"`
	ContentLength *int64            `xml:"D:getcontentlength,omitempty"`
	LastModified  string            `xml:"D:getlastmodified,omitempty"`
	ETag          string            `xml:"D:getetag,omitempty"`
	SupportedLock *davSupportedLock `xml:"D:supportedlock"`
	LockDiscovery *davLockDiscovery `xml:"D:lockdiscovery"`
	DisplayName   string            `xml:"D:displayname,omitempty"`
	ContentType   string            `xml:"D:getcontenttype,omitempty"`
}

type davResourceType struct {
	Collection *struct{} `xml:"D:collection,omitempty"`
}

type davSupportedLock struct{}

type davLockDiscovery struct{}

func writeMultiStatus(w http.ResponseWriter, responses []davResponse) {
	raw, err := xml.MarshalIndent(davMultiStatus{
		XmlnsD:    davNamespace,
		Responses: responses,
	}, "", "  ")
	if err != nil {
		writeDAVError(w, 500, err.Error())
		return
	}
	w.Header().Set("Content-Type", "application/xml; charset=utf-8")
	w.WriteHeader(207)
	_, _ = w.Write([]byte(xml.Header))
	_, _ = w.Write(raw)
}

func collectionResponse(href string, modified time.Time) davResponse {
	prop := davProp{
		ResourceType:  &davResourceType{Collection: &struct{}{}},
		SupportedLock: &davSupportedLock{},
		LockDiscovery: &davLockDiscovery{},
		DisplayName:   displayName(href, true),
		ContentType:   "httpd/unix-directory",
	}
	if !modified.IsZero() {
		prop.LastModified = modified.UTC().Format(time.RFC1123)
	}
	return davResponse{
		Href: href,
		Propstat: davPropStatus{
			Prop:   prop,
			Status: "HTTP/1.1 200 OK",
		},
	}
}

func fileResponse(href string, size int64, etag string, modified time.Time) davResponse {
	prop := davProp{
		ResourceType:  &davResourceType{},
		ContentLength: &size,
		SupportedLock: &davSupportedLock{},
		LockDiscovery: &davLockDiscovery{},
		DisplayName:   displayName(href, false),
	}
	if etag != "" {
		prop.ETag = etag
	}
	if !modified.IsZero() {
		prop.LastModified = modified.UTC().Format(time.RFC1123)
	}
	return davResponse{
		Href: href,
		Propstat: davPropStatus{
			Prop:   prop,
			Status: "HTTP/1.1 200 OK",
		},
	}
}

func listResponses(bucket, prefix string, list s3ListBucketResult) []davResponse {
	out := []davResponse{}
	seenCollections := map[string]struct{}{}
	for _, commonPrefix := range list.CommonPrefixes {
		if commonPrefix.Prefix == "" || commonPrefix.Prefix == prefix {
			continue
		}
		if _, ok := seenCollections[commonPrefix.Prefix]; ok {
			continue
		}
		seenCollections[commonPrefix.Prefix] = struct{}{}
		out = append(out, collectionResponse(hrefFor(bucket, commonPrefix.Prefix, true), time.Time{}))
	}
	for _, object := range list.Contents {
		if object.Key == "" || object.Key == prefix {
			continue
		}
		if strings.HasSuffix(object.Key, "/") {
			if _, ok := seenCollections[object.Key]; ok {
				continue
			}
			seenCollections[object.Key] = struct{}{}
			out = append(out, collectionResponse(hrefFor(bucket, object.Key, true), parseS3Time(object.LastModified)))
			continue
		}
		out = append(out, fileResponse(hrefFor(bucket, object.Key, false), object.Size, object.ETag, parseS3Time(object.LastModified)))
	}
	return out
}

func hrefFor(bucket, key string, collection bool) string {
	segments := []string{escapePathSegment(bucket)}
	for _, segment := range strings.Split(key, "/") {
		if segment == "" {
			continue
		}
		segments = append(segments, escapePathSegment(segment))
	}
	href := "/" + strings.Join(segments, "/")
	if collection && !strings.HasSuffix(href, "/") {
		href += "/"
	}
	return href
}

func escapePathSegment(segment string) string {
	return sigv4.URIEncode(segment, true)
}

func displayName(href string, collection bool) string {
	trimmed := strings.TrimSuffix(href, "/")
	if trimmed == "" {
		return ""
	}
	name := path.Base(trimmed)
	decoded, err := url.PathUnescape(name)
	if err != nil {
		return name
	}
	return decoded
}

func parseS3Time(value string) time.Time {
	if value == "" {
		return time.Time{}
	}
	if t, err := time.Parse(time.RFC3339, value); err == nil {
		return t
	}
	if t, err := time.Parse("2006-01-02T15:04:05.000Z", value); err == nil {
		return t
	}
	if t, err := time.Parse(time.RFC1123, value); err == nil {
		return t
	}
	return time.Time{}
}
