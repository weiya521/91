package p115

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"path"
	"strings"
	"sync"
	"time"

	sdk "github.com/SheltonZhu/115driver/pkg/driver"
	"github.com/video-site/backend/internal/drives"
)

type Driver struct {
	id     string
	cookie string
	rootID string
	client *sdk.Pan115Client
	ua     string

	listMu       sync.Mutex
	lastListAt   time.Time
	listInterval time.Duration
}

type Config struct {
	ID     string
	Cookie string // 形如 "UID=xxx; CID=xxx; SEID=xxx; KID=xxx"
	RootID string // 默认 "0"
	UA     string // 默认 UA115Browser
}

func New(c Config) *Driver {
	rootID := c.RootID
	if rootID == "" {
		rootID = "0"
	}
	ua := c.UA
	if ua == "" {
		ua = sdk.UA115Browser
	}
	return &Driver{
		id:           c.ID,
		cookie:       c.Cookie,
		rootID:       rootID,
		ua:           ua,
		listInterval: 2 * time.Second,
	}
}

func (d *Driver) Kind() string   { return "p115" }
func (d *Driver) ID() string     { return d.id }
func (d *Driver) RootID() string { return d.rootID }

func (d *Driver) Init(ctx context.Context) error {
	cr := &sdk.Credential{}
	if err := cr.FromCookie(d.cookie); err != nil {
		return fmt.Errorf("parse cookie: %w", err)
	}
	d.client = sdk.New(sdk.UA(d.ua)).ImportCredential(cr)
	return d.client.LoginCheck()
}

func (d *Driver) List(ctx context.Context, dirID string) ([]drives.Entry, error) {
	files, err := d.listWithRetry(ctx, dirID)
	if err != nil {
		return nil, fmt.Errorf("115 list: %w", err)
	}
	if files == nil {
		return nil, nil
	}
	out := make([]drives.Entry, 0, len(*files))
	for _, f := range *files {
		out = append(out, fileToEntry(&f, dirID))
	}
	return out, nil
}

func (d *Driver) listWithRetry(ctx context.Context, dirID string) (*[]sdk.File, error) {
	d.listMu.Lock()
	defer d.listMu.Unlock()

	cooldowns := []time.Duration{30 * time.Minute, 30 * time.Minute, 30 * time.Minute}
	var lastErr error
	for attempt := 0; ; attempt++ {
		if err := d.waitForListSlotLocked(ctx); err != nil {
			return nil, err
		}

		files, err := d.client.ListWithLimit(dirID, sdk.MaxDirPageLimit)
		if err == nil {
			return files, nil
		}
		lastErr = err
		if !isTransient115ListError(err) || attempt >= len(cooldowns) {
			break
		}
		cooldown := cooldowns[attempt]
		log.Printf("[p115] list cooling down drive=%s dir=%s cooldown=%s attempt=%d/%d", d.id, dirID, cooldown, attempt+1, len(cooldowns))
		if err := sleepContext(ctx, cooldown); err != nil {
			return nil, err
		}
	}
	return nil, lastErr
}

func (d *Driver) waitForListSlotLocked(ctx context.Context) error {
	if d.listInterval <= 0 || d.lastListAt.IsZero() {
		d.lastListAt = time.Now()
		return ctx.Err()
	}

	next := d.lastListAt.Add(d.listInterval)
	now := time.Now()
	if now.Before(next) {
		if err := sleepContext(ctx, next.Sub(now)); err != nil {
			return err
		}
	}
	d.lastListAt = time.Now()
	return ctx.Err()
}

func sleepContext(ctx context.Context, d time.Duration) error {
	if d <= 0 {
		return ctx.Err()
	}
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func isTransient115ListError(err error) bool {
	if err == nil {
		return false
	}
	text := strings.ToLower(err.Error())
	return strings.Contains(text, "405") ||
		strings.Contains(text, "429") ||
		strings.Contains(text, "too many request") ||
		strings.Contains(text, "too many requests") ||
		strings.Contains(text, "blocked") ||
		strings.Contains(text, "security") ||
		strings.Contains(text, "waf") ||
		strings.Contains(text, "unexpected error") ||
		strings.Contains(text, "访问被阻断") ||
		strings.Contains(text, "安全威胁")
}

func (d *Driver) Stat(ctx context.Context, fileID string) (*drives.Entry, error) {
	f, err := d.client.GetFile(fileID)
	if err != nil {
		return nil, fmt.Errorf("115 stat: %w", err)
	}
	if f == nil {
		return nil, errors.New("115 stat: not found")
	}
	e := fileToEntry(f, f.ParentID)
	return &e, nil
}

func (d *Driver) StreamURL(ctx context.Context, fileID string) (*drives.StreamLink, error) {
	return d.streamURLWithUA(ctx, fileID, d.ua)
}

func (d *Driver) StreamURLWithHeader(ctx context.Context, fileID string, header http.Header) (*drives.StreamLink, error) {
	return d.streamURLWithUA(ctx, fileID, header.Get("User-Agent"))
}

func (d *Driver) streamURLWithUA(ctx context.Context, fileID string, ua string) (*drives.StreamLink, error) {
	// 需要先拿到 pickCode
	f, err := d.client.GetFile(fileID)
	if err != nil {
		return nil, fmt.Errorf("115 get file: %w", err)
	}
	info, ua, err := d.downloadInfo(f.PickCode, ua)
	if err != nil {
		return nil, fmt.Errorf("115 download url: %w", err)
	}
	if info == nil || info.Url.Url == "" {
		return nil, errors.New("115 download url: empty")
	}

	headers := http.Header{}
	// 115 直链会返回一组 Cookie / Referer，info.Header 里带了
	for k, vs := range info.Header {
		for _, v := range vs {
			headers.Add(k, v)
		}
	}
	if headers.Get("User-Agent") == "" {
		headers.Set("User-Agent", ua)
	}

	return &drives.StreamLink{
		URL:     info.Url.Url,
		Headers: headers,
		Expires: time.Now().Add(25 * time.Minute), // 115 直链 30 分钟过期，留余量
	}, nil
}

func (d *Driver) downloadInfo(pickCode string, ua string) (*sdk.DownloadInfo, string, error) {
	ua = strings.TrimSpace(ua)
	if ua == "" {
		ua = d.ua
	}
	info, err := d.client.DownloadWithUA(pickCode, ua)
	if err != nil {
		return nil, "", err
	}
	return info, ua, nil
}

func (d *Driver) Upload(ctx context.Context, parentID, name string, r io.Reader, size int64) (string, error) {
	// 115 上传流程比较复杂：RapidUpload -> OSS 分片
	// 第一版 teaser 文件小（<2MB），直接读全量写 seeker，走 RapidUploadOrByOSS
	buf, err := io.ReadAll(r)
	if err != nil {
		return "", err
	}
	rs := strings.NewReader(string(buf))
	if err := d.client.RapidUploadOrByOSS(parentID, name, size, rs); err != nil {
		return "", fmt.Errorf("115 upload: %w", err)
	}
	// RapidUploadOrByOSS 目前没返回 fileID，需要回查
	files, err := d.client.ListWithLimit(parentID, sdk.FileListLimit)
	if err != nil {
		return "", fmt.Errorf("115 upload verify: %w", err)
	}
	if files != nil {
		for _, f := range *files {
			if !f.IsDirectory && f.Name == name {
				return f.FileID, nil
			}
		}
	}
	return "", errors.New("115 upload: file not found after upload")
}

func (d *Driver) EnsureDir(ctx context.Context, pathFromRoot string) (string, error) {
	parts := splitPath(pathFromRoot)
	currentID := d.rootID
	for _, name := range parts {
		childID, err := d.findChildDir(ctx, currentID, name)
		if err != nil {
			return "", err
		}
		if childID == "" {
			id, err := d.client.Mkdir(currentID, name)
			if err != nil {
				return "", fmt.Errorf("115 mkdir %s: %w", name, err)
			}
			childID = id
		}
		currentID = childID
	}
	return currentID, nil
}

func (d *Driver) findChildDir(ctx context.Context, parent, name string) (string, error) {
	entries, err := d.List(ctx, parent)
	if err != nil {
		return "", err
	}
	for _, e := range entries {
		if e.IsDir && e.Name == name {
			return e.ID, nil
		}
	}
	return "", nil
}

func splitPath(p string) []string {
	p = strings.Trim(p, "/")
	if p == "" {
		return nil
	}
	return strings.Split(p, "/")
}

func fileToEntry(f *sdk.File, parentID string) drives.Entry {
	return drives.Entry{
		ID:           f.FileID,
		Name:         f.Name,
		Size:         f.Size,
		Hash:         f.Sha1,
		IsDir:        f.IsDirectory,
		ParentID:     parentID,
		MimeType:     guessMime(f.Name),
		ModTime:      f.UpdateTime,
		ThumbnailURL: f.ThumbURL,
	}
}

func guessMime(name string) string {
	ext := strings.ToLower(path.Ext(name))
	switch ext {
	case ".mp4":
		return "video/mp4"
	case ".mkv":
		return "video/x-matroska"
	case ".mov":
		return "video/quicktime"
	case ".webm":
		return "video/webm"
	case ".jpg", ".jpeg":
		return "image/jpeg"
	case ".png":
		return "image/png"
	}
	return "application/octet-stream"
}

var _ drives.Drive = (*Driver)(nil)
