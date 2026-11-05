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
	openFn    func() (io.Reader, error)
	openErr   error
	closeFn   func() error
	closeOnce sync.Once
	closeErr  error
}

func (rc *readCloser) Read(p []byte) (int, error) {
	if rc.openFn != nil {
		rc.Reader, rc.openErr = rc.openFn()
		rc.openFn = nil
	}
	if rc.openErr != nil {
		return 0, rc.openErr
	}
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
		rc.openFn = nil
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
			defer common.ReleaseOriginalBodyStorage(c)
			wireBody := origBody
			wireLength := c.Request.ContentLength
			storageStatus := http.StatusBadRequest
			storedBody := &readCloser{closeFn: wireBody.Close}
			// Retain the exact wire representation on first consumption, not
			// before authentication has had a chance to reject a zstd request.
			storedBody.openFn = func() (io.Reader, error) {
				storage, err := common.CreateBodyStorageFromReader(wireBody, wireLength, maxBytes)
				_ = wireBody.Close()
				storedBody.closeFn = nil
				if err != nil {
					return nil, err
				}
				c.Set(common.KeyOriginalBodyStorage, storage)
				c.Set(common.KeyOriginalContentEncoding, encoding)
				reader, err := storage.NewReader()
				if err != nil {
					storageStatus = http.StatusInternalServerError
					return nil, err
				}
				storedBody.closeFn = reader.Close
				return reader, nil
			}
			origBody = storedBody
			if encoding != "zstd" {
				// Preserve the existing eager storage/error behavior for gzip
				// (which also validates its header eagerly) and brotli.
				storedBody.Reader, storedBody.openErr = storedBody.openFn()
				storedBody.openFn = nil
				if storedBody.openErr != nil {
					if common.IsRequestBodyTooLargeError(storedBody.openErr) {
						storageStatus = http.StatusRequestEntityTooLarge
					}
					c.AbortWithStatus(storageStatus)
					return
				}
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
			// The frame header sets the decoder's window, and the decoder allocates
			// it before any output exists, so the output limit below does not bound
			// it. 8MiB is the window RFC 8878 asks encoders to stay within.
			// In streaming mode MaxMemory also caps the window at the request limit.
			// Disable async decoding to avoid reading the body before downstream
			// middleware (including authentication) decides to consume it.
			reader, err := zstd.NewReader(origBody,
				zstd.WithDecoderMaxWindow(8<<20),
				zstd.WithDecoderMaxMemory(uint64(maxBytes)),
				zstd.WithDecoderConcurrency(1),
			)
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
