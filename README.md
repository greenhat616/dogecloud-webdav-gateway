# DogeCloud WebDAV Gateway

Target: expose DogeCloud OSS as a WebDAV endpoint while using DogeCloud temporary S3 credentials internally.

The design is different from an S3-to-S3 gateway. A standard S3 SigV4 request does not reveal the client secret key to the gateway, so a zero-server-secret S3 gateway cannot call DogeCloud's `/auth/tmp_token.json`. WebDAV commonly uses HTTP Basic authentication, so the gateway can treat:

- WebDAV username as DogeCloud `AccessKey`
- WebDAV password as DogeCloud `SecretKey`

With that pair, the gateway can request DogeCloud `OSS_FULL` temporary credentials for the target bucket and perform the real S3 operations against the returned `s3Bucket` and `s3Endpoint`.

## Scope

The goal is a practical WebDAV file gateway, not a complete semantic conversion between S3 and WebDAV.

Planned WebDAV support:

- `OPTIONS`
- `PROPFIND` for root, bucket, and prefix listing
- `GET` / `HEAD`
- `PUT`, including internal multipart upload for large request bodies
- `DELETE`
- `MKCOL` using prefix or directory marker semantics
- `COPY`
- `MOVE` using copy plus delete
- Basic `LOCK` / `UNLOCK` compatibility if required by common clients

Out of scope or limited:

- Full DAV ACL semantics
- Full DAV versioning semantics
- Durable distributed WebDAV locks without an external lock store
- Arbitrary WebDAV dead-property persistence unless backed by sidecar metadata
- Complete S3 feature exposure through WebDAV, such as bucket policy, object tagging, storage class, lifecycle, event notifications, S3 Select, object lock, legal hold, or presigned URL behavior

## Implementation Plan

1. Rename and reshape the project around WebDAV.
   - Module and binary become `dogecloud-webdav-gateway`.
   - Keep the DogeCloud API client and temporary credential cache.
   - Replace the original S3 proxy entrypoint with WebDAV request handling.

2. Add WebDAV authentication.
   - Parse HTTP Basic Auth.
   - Decode username/password as DogeCloud API credentials.
   - Never log credentials.
   - Cache temporary token results by DogeCloud access key and bucket scope.

3. Add DogeCloud bucket and path resolution.
   - WebDAV root lists DogeCloud buckets.
   - First path segment maps to DogeCloud bucket name.
   - Remaining path maps to object key prefix.
   - DogeCloud temporary token response maps public bucket name to real `s3Bucket` and `s3Endpoint`.

4. Implement WebDAV read/list operations.
   - `OPTIONS`
   - `PROPFIND /`
   - `PROPFIND /bucket/`
   - `GET /bucket/key`
   - `HEAD /bucket/key`
   - Generate WebDAV `207 Multi-Status` responses from S3 object listings.

5. Implement WebDAV write operations.
   - `PUT` maps to S3 upload.
   - Large `PUT` uses multipart upload internally when needed.
   - `DELETE` maps to object delete or prefix delete for collection-like paths.
   - `MKCOL` creates a directory marker only when needed for client compatibility.

6. Implement copy, move, and client compatibility behavior.
   - `COPY` maps to S3 copy.
   - `MOVE` maps to copy plus delete.
   - `LOCK` / `UNLOCK` starts with an in-memory compatibility layer, then can move to Redis or another external store if multi-instance deployments need durable locks.

7. Harden and test.
   - Unit-test WebDAV XML generation and path normalization.
   - Integration-test against fake DogeCloud API and fake S3 upstream.
   - Keep a skipped-by-default real DogeCloud API integration test driven by environment variables.
   - Add compatibility tests for common WebDAV clients.
   - Keep `go test ./...` green.

## Configuration

The final WebDAV target should not require DogeCloud account secrets in environment variables. DogeCloud credentials are supplied by each WebDAV request via Basic Auth.

Non-secret operational configuration remains environment-based and supports `.env`:

```env
GATEWAY_ADDR=:8080
DOGE_API_BASE_URL=https://api.dogecloud.com
DOGE_TMP_TOKEN_TTL=7200
DOGE_TOKEN_REFRESH_SKEW_SECONDS=300
DOGE_TOKEN_CACHE_CLEANUP_INTERVAL_SECONDS=60
MAX_BODY_MEMORY_BYTES=33554432
```

## Real DogeCloud Test

`go test ./...` includes a real DogeCloud API test, but it skips unless credentials are present.

Accepted credential variables:

```env
DOGE_ACCESS_KEY=dogecloud-api-access-key
DOGE_SECRET_KEY=dogecloud-api-secret-key
```

Aliases are also accepted:

```env
DOGE_API_KEY=dogecloud-api-access-key
DOGE_API_SECRET=dogecloud-api-secret-key
DOGE_API_ID=dogecloud-api-access-key
DOGE_API_TOKEN=dogecloud-api-secret-key
```

Optional:

```env
DOGE_TEST_BUCKET=mybucket
DOGE_API_BASE_URL=https://api.dogecloud.com
```

When credentials are set, the test calls DogeCloud `ListBuckets` and then requests an `OSS_FULL` temporary token for `DOGE_TEST_BUCKET`, or for the first returned bucket if `DOGE_TEST_BUCKET` is not set.

## Current State

The codebase now contains a WebDAV handler backed by DogeCloud temporary S3 credentials. The test suite includes fake DogeCloud API and fake S3 upstream services and covers:

- HTTP Basic Auth using DogeCloud API credentials
- `OPTIONS`
- `PROPFIND` root and bucket/prefix listings
- `MKCOL`
- normal `PUT`
- streamed `PUT` with an unknown request length
- `GET`
- `HEAD`
- `COPY`
- `MOVE`
- `LOCK` / `UNLOCK`
- object `DELETE`
- collection/prefix `DELETE`
- S3 upstream SigV4 signing with temporary credentials

The next hardening step is compatibility testing against real WebDAV clients and, where needed, replacing the current upload buffering path with true multipart upload for very large files.
