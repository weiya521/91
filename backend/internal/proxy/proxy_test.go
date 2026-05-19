package proxy

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/video-site/backend/internal/drives"
)

func TestServeStreamRedirectsP115WithRequestUserAgent(t *testing.T) {
	reg := NewRegistry()
	drv := &proxyFakeDrive{kind: "p115"}
	reg.Set("115", drv)

	p := New(reg)
	req := httptest.NewRequest(http.MethodGet, "/p/stream/115/file-1", nil)
	req.Header.Set("User-Agent", "Browser-A")
	rr := httptest.NewRecorder()

	p.ServeStream(rr, req, "115", "file-1")

	if rr.Code != http.StatusFound {
		t.Fatalf("status = %d, want %d", rr.Code, http.StatusFound)
	}
	if got := rr.Header().Get("Location"); got != "https://cdn.example/file-1?ua=Browser-A" {
		t.Fatalf("Location = %q", got)
	}
	if got := drv.calls[0].ua; got != "Browser-A" {
		t.Fatalf("link UA = %q, want request UA", got)
	}
	if got := rr.Header().Get("Referrer-Policy"); got != "no-referrer" {
		t.Fatalf("Referrer-Policy = %q", got)
	}
}

func TestServeStreamP115CacheIsUserAgentScoped(t *testing.T) {
	reg := NewRegistry()
	drv := &proxyFakeDrive{kind: "p115"}
	reg.Set("115", drv)

	p := New(reg)

	requestP115(t, p, "115", "file-1", "Browser-A")
	requestP115(t, p, "115", "file-1", "Browser-B")
	requestP115(t, p, "115", "file-1", "Browser-A")

	if len(drv.calls) != 2 {
		t.Fatalf("link calls = %d, want 2", len(drv.calls))
	}
	if drv.calls[0].ua != "Browser-A" || drv.calls[1].ua != "Browser-B" {
		t.Fatalf("link UAs = %#v", drv.calls)
	}
}

func requestP115(t *testing.T, p *Proxy, driveID, fileID, ua string) {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/p/stream/"+driveID+"/"+fileID, nil)
	req.Header.Set("User-Agent", ua)
	rr := httptest.NewRecorder()
	p.ServeStream(rr, req, driveID, fileID)
	if rr.Code != http.StatusFound {
		t.Fatalf("status = %d, want %d", rr.Code, http.StatusFound)
	}
}

type proxyFakeDrive struct {
	kind  string
	calls []proxyFakeCall
}

type proxyFakeCall struct {
	fileID string
	ua     string
}

func (d *proxyFakeDrive) Kind() string { return d.kind }
func (d *proxyFakeDrive) ID() string   { return "fake" }
func (d *proxyFakeDrive) Init(context.Context) error {
	return nil
}
func (d *proxyFakeDrive) List(context.Context, string) ([]drives.Entry, error) {
	return nil, drives.ErrNotSupported
}
func (d *proxyFakeDrive) Stat(context.Context, string) (*drives.Entry, error) {
	return nil, drives.ErrNotSupported
}
func (d *proxyFakeDrive) StreamURL(ctx context.Context, fileID string) (*drives.StreamLink, error) {
	return d.StreamURLWithHeader(ctx, fileID, nil)
}
func (d *proxyFakeDrive) StreamURLWithHeader(_ context.Context, fileID string, header http.Header) (*drives.StreamLink, error) {
	ua := header.Get("User-Agent")
	d.calls = append(d.calls, proxyFakeCall{fileID: fileID, ua: ua})
	return &drives.StreamLink{
		URL:     "https://cdn.example/" + fileID + "?ua=" + ua,
		Headers: http.Header{"User-Agent": {ua}},
		Expires: time.Now().Add(time.Minute),
	}, nil
}
func (d *proxyFakeDrive) Upload(context.Context, string, string, io.Reader, int64) (string, error) {
	return "", drives.ErrNotSupported
}
func (d *proxyFakeDrive) EnsureDir(context.Context, string) (string, error) {
	return "", drives.ErrNotSupported
}
func (d *proxyFakeDrive) RootID() string { return "0" }
