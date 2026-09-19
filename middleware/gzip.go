package middleware

import (
	"compress/gzip"
	"io"
	"net/http"
	"sync"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/andybalholm/brotli"
	"github.com/gin-gonic/gin"
	"github.com/klauspost/compress/zstd"
)

type readCloser struct {
	io.Reader
	closeFn   func() error
	closeOnce sync.Once
	closeErr  error
}

func (rc *readCloser) Read(p []byte) (int, error) {
	if rc.Reader == nil {
		return 0, io.ErrClosedPipe
	}
	return rc.Reader.Read(p)
}

func (rc *readCloser) Close() error {
	rc.closeOnce.Do(func() {
		if rc.closeFn != nil {
			rc.closeErr = rc.closeFn()
		}
		// The deferred Close retains this wrapper until the request ends. Do not
		// retain decoder buffers or the original compressed reader with it.
		rc.Reader = nil
		rc.closeFn = nil
	})
	return rc.closeErr
}

func DecompressRequestMiddleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		if c.Request.Body == nil || c.Request.Method == http.MethodGet {
			c.Next()
			return
		}
		maxMB := constant.MaxRequestBodyMB
		if maxMB <= 0 {
			maxMB = 32
		}
		maxBytes := int64(maxMB) << 20

		origBody := c.Request.Body
		wrapMaxBytes := func(body io.ReadCloser) io.ReadCloser {
			return http.MaxBytesReader(c.Writer, body, maxBytes)
		}

		encoding := c.GetHeader("Content-Encoding")
		switch encoding {
		case "gzip", "br", "zstd":
			storage, err := common.CreateBodyStorageFromReader(origBody, c.Request.ContentLength, maxBytes)
			_ = origBody.Close()
			if err != nil {
				status := http.StatusBadRequest
				if common.IsRequestBodyTooLargeError(err) {
					status = http.StatusRequestEntityTooLarge
				}
				c.AbortWithStatus(status)
				return
			}
			defer common.ReleaseOriginalBodyStorage(c)
			c.Set(common.KeyOriginalBodyStorage, storage)
			c.Set(common.KeyOriginalContentEncoding, encoding)
			origBody, err = storage.NewReader()
			if err != nil {
				c.AbortWithStatus(http.StatusInternalServerError)
				return
			}
		default:
			// Keep uncompressed requests streaming without an extra storage copy.
			c.Request.Body = wrapMaxBytes(origBody)
			c.Next()
			return
		}

		switch encoding {
		case "gzip":
			gzipReader, err := gzip.NewReader(origBody)
			if err != nil {
				_ = origBody.Close()
				c.AbortWithStatus(http.StatusBadRequest)
				return
			}
			// Replace the request body with the decompressed data, and enforce a max size (post-decompression).
			c.Request.Body = wrapMaxBytes(&readCloser{
				Reader: gzipReader,
				closeFn: func() error {
					_ = gzipReader.Close()
					return origBody.Close()
				},
			})
		case "br":
			reader := brotli.NewReader(origBody)
			c.Request.Body = wrapMaxBytes(&readCloser{
				Reader: reader,
				closeFn: func() error {
					return origBody.Close()
				},
			})
		case "zstd":
			reader, err := zstd.NewReader(origBody)
			if err != nil {
				_ = origBody.Close()
				c.AbortWithStatus(http.StatusBadRequest)
				return
			}
			c.Request.Body = wrapMaxBytes(&readCloser{
				Reader: reader,
				closeFn: func() error {
					reader.Close()
					return origBody.Close()
				},
			})
		}

		// The wire length describes compressed bytes, not the decoded body.
		c.Request.ContentLength = -1
		c.Request.Header.Del("Content-Length")
		c.Request.Header.Del("Content-Encoding")
		// Downstream may abort before consuming or closing the decoded body.
		defer c.Request.Body.Close()

		// Continue processing the request
		c.Next()
	}
}
