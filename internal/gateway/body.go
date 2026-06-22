package gateway

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"os"
)

type BufferedBody struct {
	data   []byte
	file   string
	size   int64
	sha256 string
}

func BufferBody(body io.ReadCloser, maxMemory int64) (*BufferedBody, error) {
	if body == nil {
		return &BufferedBody{sha256: emptySHA256()}, nil
	}
	defer body.Close()

	hash := sha256.New()
	var buffer bytes.Buffer
	var temp *os.File
	chunk := make([]byte, 64*1024)
	var size int64

	for {
		n, readErr := body.Read(chunk)
		if n > 0 {
			part := chunk[:n]
			size += int64(n)
			_, _ = hash.Write(part)
			if temp == nil && int64(buffer.Len()+n) <= maxMemory {
				_, _ = buffer.Write(part)
			} else {
				if temp == nil {
					var err error
					temp, err = os.CreateTemp("", "dogecloud-webdav-gateway-*")
					if err != nil {
						return nil, err
					}
					if _, err := temp.Write(buffer.Bytes()); err != nil {
						_ = temp.Close()
						_ = os.Remove(temp.Name())
						return nil, err
					}
					buffer.Reset()
				}
				if _, err := temp.Write(part); err != nil {
					_ = temp.Close()
					_ = os.Remove(temp.Name())
					return nil, err
				}
			}
		}
		if readErr == io.EOF {
			break
		}
		if readErr != nil {
			if temp != nil {
				_ = temp.Close()
				_ = os.Remove(temp.Name())
			}
			return nil, readErr
		}
	}

	out := &BufferedBody{
		size:   size,
		sha256: hex.EncodeToString(hash.Sum(nil)),
	}
	if temp != nil {
		if err := temp.Close(); err != nil {
			_ = os.Remove(temp.Name())
			return nil, err
		}
		out.file = temp.Name()
	} else {
		out.data = buffer.Bytes()
	}
	return out, nil
}

func (b *BufferedBody) Open() (io.ReadCloser, error) {
	if b.file != "" {
		return os.Open(b.file)
	}
	return io.NopCloser(bytes.NewReader(b.data)), nil
}

func (b *BufferedBody) Cleanup() {
	if b.file != "" {
		_ = os.Remove(b.file)
	}
}

func (b *BufferedBody) Size() int64 {
	return b.size
}

func (b *BufferedBody) SHA256() string {
	return b.sha256
}

func emptySHA256() string {
	sum := sha256.Sum256(nil)
	return hex.EncodeToString(sum[:])
}
