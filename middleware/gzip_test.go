package middleware

import (
	"bytes"
	"encoding/binary"
	"io"
	"net/http"
	"net/http/httptest"
	"runtime"
	"sync/atomic"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/gin-gonic/gin"
	"github.com/klauspost/compress/zstd"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

var zstdMagic = []byte{0x28, 0xb5, 0x2f, 0xfd}

// zstdRLEBlock is a last block that repeats 'a' once.
var zstdRLEBlock = []byte{0x0b, 0x00, 0x00, 'a'}

func readZstdBody(t *testing.T, body []byte) ([]byte, error) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	router := gin.New()
	var data []byte
	var readErr error
	router.POST("/v1/chat/completions", DecompressRequestMiddleware(), func(c *gin.Context) {
		data, readErr = io.ReadAll(c.Request.Body)
	})
	request := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewReader(body))
	request.Header.Set("Content-Encoding", "zstd")
	t.Cleanup(func() {
		assert.NoError(t, request.Body.Close())
	})
	router.ServeHTTP(httptest.NewRecorder(), request)
	return data, readErr
}

func TestDecompressRequestZstd(t *testing.T) {
	previousMaxBodyMB := constant.MaxRequestBodyMB
	constant.MaxRequestBodyMB = 16
	t.Cleanup(func() {
		constant.MaxRequestBodyMB = previousMaxBodyMB
	})

	encoder, err := zstd.NewWriter(nil, zstd.WithEncoderConcurrency(1))
	require.NoError(t, err)
	payload := bytes.Repeat([]byte(`{"role":"user","content":"hello"},`), 1<<15)
	compressed := encoder.EncodeAll(payload, nil)
	require.NoError(t, encoder.Close())

	// Window descriptors 0x50, 0x68, 0x69, and 0x98 declare 1MiB, 8MiB, 9MiB, and 512MiB windows.
	smallWindowFrame := append(append(append([]byte{}, zstdMagic...), 0x00, 0x50), zstdRLEBlock...)
	boundaryWindowFrame := append(append(append([]byte{}, zstdMagic...), 0x00, 0x68), zstdRLEBlock...)
	aboveWindowFrame := append(append(append([]byte{}, zstdMagic...), 0x00, 0x69), zstdRLEBlock...)
	largeWindowFrame := append(append(append([]byte{}, zstdMagic...), 0x00, 0x98), zstdRLEBlock...)
	// A single-segment frame uses its declared content size, here 100MiB, as the window.
	largeContentFrame := binary.LittleEndian.AppendUint64(append(append([]byte{}, zstdMagic...), 0xe0), 100<<20)
	largeContentFrame = append(largeContentFrame, zstdRLEBlock...)

	largePayload := bytes.Repeat([]byte("a"), 9<<20)
	boundedEncoder, err := zstd.NewWriter(nil, zstd.WithWindowSize(1<<20), zstd.WithSingleSegment(false), zstd.WithEncoderConcurrency(1))
	require.NoError(t, err)
	largeBody := boundedEncoder.EncodeAll(largePayload, nil)
	require.NoError(t, boundedEncoder.Close())
	var header zstd.Header
	require.NoError(t, header.Decode(largeBody))
	require.False(t, header.SingleSegment)
	require.LessOrEqual(t, header.WindowSize, uint64(8<<20))

	singleEncoder, err := zstd.NewWriter(nil, zstd.WithSingleSegment(true), zstd.WithEncoderConcurrency(1))
	require.NoError(t, err)
	boundaryContentFrame := singleEncoder.EncodeAll(largePayload[:8<<20], nil)
	aboveContentFrame := singleEncoder.EncodeAll(largePayload[:(8<<20)+1], nil)
	require.NoError(t, singleEncoder.Close())
	for _, frame := range [][]byte{boundaryContentFrame, aboveContentFrame} {
		require.NoError(t, header.Decode(frame))
		require.True(t, header.SingleSegment)
	}

	cases := []struct {
		name           string
		body           []byte
		wantBody       []byte
		wantErr        error
		maxBodyMB      int
		wantLimitError bool
	}{
		{name: "regular body is decompressed", body: compressed, wantBody: payload},
		{name: "512MiB window is rejected", body: largeWindowFrame, wantErr: zstd.ErrWindowSizeExceeded},
		{name: "100MiB single-segment window is rejected", body: largeContentFrame, wantErr: zstd.ErrDecoderSizeExceeded},
		{name: "8MiB window is accepted", body: boundaryWindowFrame, wantBody: []byte("a")},
		{name: "9MiB window is rejected", body: aboveWindowFrame, wantErr: zstd.ErrWindowSizeExceeded},
		{name: "8MiB single-segment body is accepted", body: boundaryContentFrame, wantBody: largePayload[:8<<20]},
		{name: "single-segment body one byte above 8MiB is rejected", body: aboveContentFrame, wantErr: zstd.ErrDecoderSizeExceeded},
		{name: "9MiB body with a bounded window is decompressed", body: largeBody, wantBody: largePayload},
		{name: "bounded window does not bypass the decoded body limit", body: largeBody, wantBody: largePayload[:1<<20], maxBodyMB: 1, wantLimitError: true},
		{name: "window equal to a smaller request limit is accepted", body: smallWindowFrame, wantBody: []byte("a"), maxBodyMB: 1},
		{name: "window above a smaller request limit is rejected", body: boundaryWindowFrame, wantErr: zstd.ErrWindowSizeExceeded, maxBodyMB: 1},
		{name: "single-segment window above a smaller request limit is rejected", body: boundaryContentFrame, wantErr: zstd.ErrDecoderSizeExceeded, maxBodyMB: 1},
		{name: "oversized window in a later frame is rejected", body: append(append([]byte{}, compressed...), largeWindowFrame...), wantBody: payload, wantErr: zstd.ErrWindowSizeExceeded},
		{name: "oversized single-segment window in a later frame is rejected", body: append(append([]byte{}, compressed...), aboveContentFrame...), wantBody: payload, wantErr: zstd.ErrDecoderSizeExceeded},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if tc.maxBodyMB > 0 {
				constant.MaxRequestBodyMB = tc.maxBodyMB
				t.Cleanup(func() {
					constant.MaxRequestBodyMB = 16
				})
			}
			data, err := readZstdBody(t, tc.body)

			switch {
			case tc.wantLimitError:
				var sizeErr *http.MaxBytesError
				require.ErrorAs(t, err, &sizeErr)
				assert.Equal(t, int64(tc.maxBodyMB)<<20, sizeErr.Limit)
			case tc.wantErr != nil:
				require.ErrorIs(t, err, tc.wantErr)
			default:
				require.NoError(t, err)
			}
			if tc.wantBody == nil {
				assert.Empty(t, data)
				return
			}
			assert.Equal(t, tc.wantBody, data)
		})
	}
}

type zstdBodyReadCounter struct {
	reads  atomic.Int32
	closes atomic.Int32
	reader io.Reader
}

func (b *zstdBodyReadCounter) Read(p []byte) (int, error) {
	b.reads.Add(1)
	if b.reader != nil {
		return b.reader.Read(p)
	}
	return 0, io.EOF
}

func (b *zstdBodyReadCounter) Close() error {
	b.closes.Add(1)
	return nil
}

func TestDecompressRequestZstdEarlyRejectionDoesNotReadBody(t *testing.T) {
	// Keep this regression meaningful even on hosts where the default decoder
	// would otherwise be synchronous because GOMAXPROCS is 1.
	previousProcs := runtime.GOMAXPROCS(2)
	t.Cleanup(func() {
		runtime.GOMAXPROCS(previousProcs)
	})

	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.POST("/v1/chat/completions", DecompressRequestMiddleware(), func(c *gin.Context) {
		_, stored := c.Get(common.KeyOriginalBodyStorage)
		assert.False(t, stored, "rejected requests must not allocate wire storage")
		c.AbortWithStatus(http.StatusUnauthorized)
	})
	body := &zstdBodyReadCounter{}
	request := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", body)
	request.Header.Set("Content-Encoding", "zstd")
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)

	// Closing also joins any asynchronous decoder before checking the read count.
	require.NoError(t, request.Body.Close())
	assert.Equal(t, http.StatusUnauthorized, response.Code)
	assert.Zero(t, body.reads.Load(), "an early rejection must not consume the request body")
	assert.EqualValues(t, 1, body.closes.Load(), "early rejection must close the wire body once")
}

func TestDecompressRequestZstdPreservesOriginalBody(t *testing.T) {
	encoder, err := zstd.NewWriter(nil, zstd.WithEncoderConcurrency(1))
	require.NoError(t, err)
	payload := []byte(`{"model":"test","messages":[{"role":"user","content":"hello"}]}`)
	compressed := encoder.EncodeAll(payload, nil)
	require.NoError(t, encoder.Close())

	for _, knownLength := range []bool{true, false} {
		name := "unknown length"
		if knownLength {
			name = "known length"
		}
		t.Run(name, func(t *testing.T) {
			gin.SetMode(gin.TestMode)
			router := gin.New()
			body := &zstdBodyReadCounter{reader: bytes.NewReader(compressed)}
			var original common.BodyStorage
			router.POST("/", DecompressRequestMiddleware(), func(c *gin.Context) {
				assert.Zero(t, body.reads.Load())
				assert.EqualValues(t, -1, c.Request.ContentLength)
				assert.Empty(t, c.GetHeader("Content-Encoding"))
				assert.Empty(t, c.GetHeader("Content-Length"))
				defer common.CleanupBodyStorage(c)
				// Transparent relay first obtains the decoded storage, then
				// selects the retained wire storage for the outbound request.
				decoded, err := common.GetBodyStorage(c)
				require.NoError(t, err)
				data, err := decoded.Bytes()
				require.NoError(t, err)
				assert.Equal(t, payload, data)
				raw, exists := c.Get(common.KeyOriginalBodyStorage)
				require.True(t, exists)
				var ok bool
				original, ok = raw.(common.BodyStorage)
				require.True(t, ok)
				assert.Equal(t, "zstd", c.GetString(common.KeyOriginalContentEncoding))
				assert.EqualValues(t, len(compressed), original.Size())
				for range 2 {
					replay, err := original.NewReader()
					require.NoError(t, err)
					wire, err := io.ReadAll(replay)
					require.NoError(t, err)
					assert.Equal(t, compressed, wire)
					require.NoError(t, replay.Close())
				}
			})
			request := httptest.NewRequest(http.MethodPost, "/", body)
			if knownLength {
				request.ContentLength = int64(len(compressed))
				request.Header.Set("Content-Length", "123")
			} else {
				request.ContentLength = -1
			}
			request.Header.Set("Content-Encoding", "zstd")
			response := httptest.NewRecorder()
			router.ServeHTTP(response, request)
			assert.Equal(t, http.StatusOK, response.Code)
			require.NotNil(t, original)
			_, err := original.NewReader()
			assert.ErrorIs(t, err, common.ErrStorageClosed)
			require.NoError(t, request.Body.Close())
			assert.EqualValues(t, 1, body.closes.Load())
		})
	}
}

func TestDecompressRequestZstdLimitsCompressedBody(t *testing.T) {
	previousMaxBodyMB := constant.MaxRequestBodyMB
	constant.MaxRequestBodyMB = 1
	t.Cleanup(func() {
		constant.MaxRequestBodyMB = previousMaxBodyMB
	})

	gin.SetMode(gin.TestMode)
	router := gin.New()
	body := &zstdBodyReadCounter{reader: bytes.NewReader(make([]byte, (1<<20)+1))}
	router.POST("/", DecompressRequestMiddleware(), func(c *gin.Context) {
		assert.Zero(t, body.reads.Load())
		_, err := common.GetBodyStorage(c)
		require.Error(t, err)
		assert.True(t, common.IsRequestBodyTooLargeError(err))
		_, stored := c.Get(common.KeyOriginalBodyStorage)
		assert.False(t, stored, "failed capture must not expose partial wire storage")
		c.AbortWithStatus(http.StatusRequestEntityTooLarge)
	})
	request := httptest.NewRequest(http.MethodPost, "/", body)
	request.ContentLength = -1
	request.Header.Set("Content-Encoding", "zstd")
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	assert.Equal(t, http.StatusRequestEntityTooLarge, response.Code)
	require.NoError(t, request.Body.Close())
	assert.EqualValues(t, 1, body.closes.Load())
}
