package api

import (
	"bytes"
	"context"
	"encoding/json"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/video-site/backend/internal/catalog"
	"github.com/video-site/backend/internal/proxy"
)

func TestVideoSourceUsesTranscodeForAvi(t *testing.T) {
	v := &catalog.Video{
		ID:      "video-1",
		DriveID: "drive-1",
		FileID:  "file-1",
		Ext:     "avi",
	}

	got := videoSource(v)

	if got != "/p/transcode/video-1" {
		t.Fatalf("video source = %q, want transcode route", got)
	}
}

func TestVideoSourceKeepsDirectStreamForMp4(t *testing.T) {
	v := &catalog.Video{
		ID:      "video-1",
		DriveID: "drive-1",
		FileID:  "file-1",
		Ext:     "mp4",
	}

	got := videoSource(v)

	if got != "/p/stream/drive-1/file-1" {
		t.Fatalf("video source = %q, want direct stream route", got)
	}
}

func TestVideoSourceUsesLocalUploadRoute(t *testing.T) {
	v := &catalog.Video{
		ID:      "video-1",
		DriveID: localUploadDriveID,
		FileID:  "upload-1.mp4",
		Ext:     "mp4",
	}

	got := videoSource(v)

	if got != "/p/upload/video-1" {
		t.Fatalf("video source = %q, want local upload route", got)
	}
}

func TestPreviewURLIncludesUpdatedAtVersion(t *testing.T) {
	got := previewURL(&catalog.Video{
		ID:        "video-1",
		UpdatedAt: time.UnixMilli(1778863000123),
	})

	if got != "/p/preview/video-1?v=1778863000123" {
		t.Fatalf("preview URL = %q, want versioned URL", got)
	}
}

func TestPreviewURLFallsBackWithoutUpdatedAt(t *testing.T) {
	got := previewURL(&catalog.Video{ID: "video-1"})

	if got != "/p/preview/video-1" {
		t.Fatalf("preview URL = %q, want unversioned URL", got)
	}
}

func TestHomeWindowPageChangesEveryTwoHours(t *testing.T) {
	total := homePageSize * 3
	if got := homeWindowPage(time.Unix(0, 0), total, homePageSize); got != 1 {
		t.Fatalf("window page at epoch = %d, want 1", got)
	}
	if got := homeWindowPage(time.Unix(int64(homeWindowDuration/time.Second)-1, 0), total, homePageSize); got != 1 {
		t.Fatalf("window page before boundary = %d, want 1", got)
	}
	if got := homeWindowPage(time.Unix(int64(homeWindowDuration/time.Second), 0), total, homePageSize); got != 2 {
		t.Fatalf("window page at first boundary = %d, want 2", got)
	}
	if got := homeWindowPage(time.Unix(int64(3*homeWindowDuration/time.Second), 0), total, homePageSize); got != 1 {
		t.Fatalf("window page after cycle = %d, want 1", got)
	}
}

func TestHandleHomeRotatesHotVideosByTwoHourWindow(t *testing.T) {
	ctx := context.Background()
	cat, err := catalog.Open(t.TempDir() + "/catalog.db")
	if err != nil {
		t.Fatalf("open catalog: %v", err)
	}
	t.Cleanup(func() {
		if err := cat.Close(); err != nil {
			t.Fatalf("close catalog: %v", err)
		}
	})

	base := time.Unix(1_700_000_000, 0)
	for i := 0; i < homePageSize+1; i++ {
		id := "video-" + strconv.Itoa(i)
		if err := cat.UpsertVideo(ctx, &catalog.Video{
			ID:          id,
			DriveID:     "drive",
			FileID:      "file-" + strconv.Itoa(i),
			Title:       "Video " + strconv.Itoa(i),
			Likes:       homePageSize + 1 - i,
			PublishedAt: base.Add(time.Duration(i) * time.Second),
			CreatedAt:   base,
			UpdatedAt:   base,
		}); err != nil {
			t.Fatalf("seed video %s: %v", id, err)
		}
	}

	firstWindow := requestHomeIDs(t, &Server{
		Catalog: cat,
		Now:     func() time.Time { return time.Unix(0, 0) },
	})
	sameWindow := requestHomeIDs(t, &Server{
		Catalog: cat,
		Now:     func() time.Time { return time.Unix(int64(homeWindowDuration/time.Second)-1, 0) },
	})
	nextWindow := requestHomeIDs(t, &Server{
		Catalog: cat,
		Now:     func() time.Time { return time.Unix(int64(homeWindowDuration/time.Second), 0) },
	})

	if strings.Join(firstWindow, ",") != strings.Join(sameWindow, ",") {
		t.Fatalf("same two-hour window changed videos: first=%v same=%v", firstWindow, sameWindow)
	}
	if strings.Join(firstWindow, ",") == strings.Join(nextWindow, ",") {
		t.Fatalf("next two-hour window did not change videos: %v", nextWindow)
	}
	if len(firstWindow) != homePageSize {
		t.Fatalf("first window item count = %d, want %d", len(firstWindow), homePageSize)
	}
	if len(nextWindow) != 1 {
		t.Fatalf("next window item count = %d, want final page with 1 item", len(nextWindow))
	}
}

func TestHandleUploadVideoSavesFileVideoTagsAndQueuesPreview(t *testing.T) {
	ctx := context.Background()
	cat, err := catalog.Open(t.TempDir() + "/catalog.db")
	if err != nil {
		t.Fatalf("open catalog: %v", err)
	}
	t.Cleanup(func() {
		if err := cat.Close(); err != nil {
			t.Fatalf("close catalog: %v", err)
		}
	})

	var queued *catalog.Video
	server := &Server{
		Catalog:  cat,
		LocalDir: t.TempDir(),
		OnVideoUploaded: func(v *catalog.Video) {
			queued = v
		},
	}
	req := multipartUploadRequest(t, map[string]string{
		"title": "用户上传标题",
		"tags":  "奶子,AV,女大",
	}, "clip.mp4", "video-bytes")
	rr := httptest.NewRecorder()

	server.handleUploadVideo(rr, req)

	if rr.Code != http.StatusCreated {
		t.Fatalf("status = %d, body = %s", rr.Code, rr.Body.String())
	}
	var dto VideoDTO
	if err := json.NewDecoder(rr.Body).Decode(&dto); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if dto.ID == "" {
		t.Fatal("response video id is empty")
	}
	got, err := cat.GetVideo(ctx, dto.ID)
	if err != nil {
		t.Fatalf("get uploaded video: %v", err)
	}
	if got.DriveID != localUploadDriveID {
		t.Fatalf("drive id = %q, want %q", got.DriveID, localUploadDriveID)
	}
	if got.Title != "用户上传标题" {
		t.Fatalf("title = %q, want submitted title", got.Title)
	}
	if !sameStringSet(got.Tags, []string{"奶子", "AV", "女大"}) {
		t.Fatalf("tags = %#v, want selected tags", got.Tags)
	}
	if got.PreviewStatus != "pending" {
		t.Fatalf("preview status = %q, want pending", got.PreviewStatus)
	}
	path := filepath.Join(server.localUploadDir(), got.FileID)
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read uploaded file: %v", err)
	}
	if string(data) != "video-bytes" {
		t.Fatalf("uploaded file content = %q, want original bytes", string(data))
	}
	if queued == nil || queued.ID != got.ID {
		t.Fatalf("queued video = %#v, want uploaded video", queued)
	}
}

func TestHandleUploadVideoDefaultsBlankTitleToOriginalFileName(t *testing.T) {
	ctx := context.Background()
	cat, err := catalog.Open(t.TempDir() + "/catalog.db")
	if err != nil {
		t.Fatalf("open catalog: %v", err)
	}
	t.Cleanup(func() {
		if err := cat.Close(); err != nil {
			t.Fatalf("close catalog: %v", err)
		}
	})
	server := &Server{Catalog: cat, LocalDir: t.TempDir()}
	req := multipartUploadRequest(t, map[string]string{"title": "  "}, "holiday.clip.final.mp4", "video-bytes")
	rr := httptest.NewRecorder()

	server.handleUploadVideo(rr, req)

	if rr.Code != http.StatusCreated {
		t.Fatalf("status = %d, body = %s", rr.Code, rr.Body.String())
	}
	var dto VideoDTO
	if err := json.NewDecoder(rr.Body).Decode(&dto); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	got, err := cat.GetVideo(ctx, dto.ID)
	if err != nil {
		t.Fatalf("get uploaded video: %v", err)
	}
	if got.Title != "holiday.clip.final" {
		t.Fatalf("title = %q, want original file name without extension", got.Title)
	}
}

func TestHandleUploadVideoRejectsUnsupportedTag(t *testing.T) {
	cat, err := catalog.Open(t.TempDir() + "/catalog.db")
	if err != nil {
		t.Fatalf("open catalog: %v", err)
	}
	t.Cleanup(func() {
		if err := cat.Close(); err != nil {
			t.Fatalf("close catalog: %v", err)
		}
	})
	server := &Server{Catalog: cat, LocalDir: t.TempDir()}
	req := multipartUploadRequest(t, map[string]string{"tags": "奶子,后入"}, "clip.mp4", "video-bytes")
	rr := httptest.NewRecorder()

	server.handleUploadVideo(rr, req)

	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body = %s", rr.Code, rr.Body.String())
	}
}

func TestHandleUploadedVideoServesLocalUploadFile(t *testing.T) {
	ctx := context.Background()
	cat, err := catalog.Open(t.TempDir() + "/catalog.db")
	if err != nil {
		t.Fatalf("open catalog: %v", err)
	}
	t.Cleanup(func() {
		if err := cat.Close(); err != nil {
			t.Fatalf("close catalog: %v", err)
		}
	})
	root := t.TempDir()
	localDir := filepath.Join(root, "previews")
	uploadDir := filepath.Join(root, "uploads")
	if err := os.MkdirAll(uploadDir, 0o755); err != nil {
		t.Fatalf("mkdir uploads: %v", err)
	}
	if err := os.WriteFile(filepath.Join(uploadDir, "upload-1.mp4"), []byte("video-bytes"), 0o644); err != nil {
		t.Fatalf("write upload: %v", err)
	}
	now := time.Now()
	if err := cat.UpsertVideo(ctx, &catalog.Video{
		ID:          "video-1",
		DriveID:     localUploadDriveID,
		FileID:      "upload-1.mp4",
		Title:       "Uploaded",
		PublishedAt: now,
		CreatedAt:   now,
		UpdatedAt:   now,
	}); err != nil {
		t.Fatalf("seed video: %v", err)
	}
	server := &Server{Catalog: cat, LocalDir: localDir}
	req := requestWithRouteParam(http.MethodGet, "/p/upload/video-1", "videoID", "video-1", strings.NewReader(``))
	rr := httptest.NewRecorder()

	server.handleUploadedVideo(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rr.Code, rr.Body.String())
	}
	if rr.Body.String() != "video-bytes" {
		t.Fatalf("body = %q, want uploaded bytes", rr.Body.String())
	}
}

func TestHandlePreviewIgnoresRemotePreviewFileIDAndServesLocalFile(t *testing.T) {
	ctx := context.Background()
	cat, err := catalog.Open(t.TempDir() + "/catalog.db")
	if err != nil {
		t.Fatalf("open catalog: %v", err)
	}
	t.Cleanup(func() {
		if err := cat.Close(); err != nil {
			t.Fatalf("close catalog: %v", err)
		}
	})
	localDir := t.TempDir()
	localPreview := filepath.Join(localDir, "video-1.mp4")
	if err := os.WriteFile(localPreview, []byte("local teaser"), 0o644); err != nil {
		t.Fatalf("write local preview: %v", err)
	}
	now := time.Now()
	if err := cat.UpsertVideo(ctx, &catalog.Video{
		ID:            "video-1",
		DriveID:       "drive-1",
		FileID:        "file-1",
		Title:         "Video",
		PreviewStatus: "ready",
		PreviewFileID: "remote-preview-file",
		PreviewLocal:  localPreview,
		PublishedAt:   now,
		CreatedAt:     now,
		UpdatedAt:     now,
	}); err != nil {
		t.Fatalf("seed video: %v", err)
	}
	server := &Server{
		Catalog:  cat,
		LocalDir: localDir,
		Proxy:    proxy.New(proxy.NewRegistry()),
	}
	req := requestWithRouteParam(http.MethodGet, "/p/preview/video-1", "videoID", "video-1", strings.NewReader(``))
	rr := httptest.NewRecorder()

	server.handlePreview(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rr.Code, rr.Body.String())
	}
	if rr.Body.String() != "local teaser" {
		t.Fatalf("body = %q, want local teaser bytes", rr.Body.String())
	}
	if got := rr.Header().Get("Cache-Control"); got != "no-store" {
		t.Fatalf("Cache-Control = %q, want no-store", got)
	}
}

func TestTranscodeStatusReadyWhenCachedFileExists(t *testing.T) {
	s := &Server{LocalDir: t.TempDir()}
	videoID := "video-1"
	path := s.transcodePath(videoID)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir cache dir: %v", err)
	}
	if err := os.WriteFile(path, []byte("mp4"), 0o644); err != nil {
		t.Fatalf("write cache file: %v", err)
	}

	if got := s.transcodeStatus(videoID); got != "ready" {
		t.Fatalf("status = %q, want ready", got)
	}
}

func TestTranscodeStatusProcessingWhenJobActive(t *testing.T) {
	s := &Server{LocalDir: t.TempDir()}
	videoID := "video-1"
	s.setTranscoding(videoID, true)

	if got := s.transcodeStatus(videoID); got != "processing" {
		t.Fatalf("status = %q, want processing", got)
	}
}

func TestTranscodeTempPathKeepsMp4Extension(t *testing.T) {
	s := &Server{LocalDir: t.TempDir()}

	if got := s.transcodeTempPath("video-1"); !strings.HasSuffix(got, ".mp4") {
		t.Fatalf("temp transcode path = %q, want .mp4 suffix for ffmpeg muxer detection", got)
	}
}

func TestHandleTagsReturnsUnifiedTagPool(t *testing.T) {
	ctx := context.Background()
	cat, err := catalog.Open(t.TempDir() + "/catalog.db")
	if err != nil {
		t.Fatalf("open catalog: %v", err)
	}
	t.Cleanup(func() {
		if err := cat.Close(); err != nil {
			t.Fatalf("close catalog: %v", err)
		}
	})
	now := time.Now()
	if err := cat.UpsertVideo(ctx, &catalog.Video{
		ID:          "video-1",
		DriveID:     "drive",
		FileID:      "file-1",
		Title:       "清纯女大后入",
		Tags:        []string{"后入", "女大"},
		Category:    "random-category",
		PublishedAt: now,
		CreatedAt:   now,
		UpdatedAt:   now,
	}); err != nil {
		t.Fatalf("seed video: %v", err)
	}
	if _, err := cat.CreateTagAndClassify(ctx, "清纯", nil, "user"); err != nil {
		t.Fatalf("create tag: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/api/tags", nil)
	rr := httptest.NewRecorder()
	(&Server{Catalog: cat}).handleTags(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rr.Code, rr.Body.String())
	}
	var got []struct {
		ID    string `json:"id"`
		Label string `json:"label"`
		Count int    `json:"count"`
	}
	if err := json.NewDecoder(rr.Body).Decode(&got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	labels := make([]string, 0, len(got))
	for _, tag := range got {
		labels = append(labels, tag.Label)
	}
	if !containsString(labels, "清纯") {
		t.Fatalf("labels = %#v, want user tag 清纯", labels)
	}
	if !containsString(labels, "后入") {
		t.Fatalf("labels = %#v, want system tag 后入", labels)
	}
	var qingchunCount int
	for _, tag := range got {
		if tag.Label == "清纯" {
			qingchunCount = tag.Count
		}
	}
	if qingchunCount != 1 {
		t.Fatalf("清纯 count = %d, want 1; tags = %#v", qingchunCount, got)
	}
}

func TestHandleUpdateVideoTagsRejectsUnknownTags(t *testing.T) {
	ctx := context.Background()
	cat, err := catalog.Open(t.TempDir() + "/catalog.db")
	if err != nil {
		t.Fatalf("open catalog: %v", err)
	}
	t.Cleanup(func() {
		if err := cat.Close(); err != nil {
			t.Fatalf("close catalog: %v", err)
		}
	})
	now := time.Now()
	if err := cat.UpsertVideo(ctx, &catalog.Video{
		ID:          "video-1",
		DriveID:     "drive",
		FileID:      "file-1",
		Title:       "普通标题",
		PublishedAt: now,
		CreatedAt:   now,
		UpdatedAt:   now,
	}); err != nil {
		t.Fatalf("seed video: %v", err)
	}

	req := requestWithVideoID(http.MethodPut, "/api/video/video-1/tags", "video-1", strings.NewReader(`{"tags":["不存在"]}`))
	rr := httptest.NewRecorder()
	(&Server{Catalog: cat}).handleUpdateVideoTags(rr, req)

	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body = %s", rr.Code, rr.Body.String())
	}
}

func TestHandleUpdateVideoTagsSavesExistingTags(t *testing.T) {
	ctx := context.Background()
	cat, err := catalog.Open(t.TempDir() + "/catalog.db")
	if err != nil {
		t.Fatalf("open catalog: %v", err)
	}
	t.Cleanup(func() {
		if err := cat.Close(); err != nil {
			t.Fatalf("close catalog: %v", err)
		}
	})
	now := time.Now()
	if err := cat.UpsertVideo(ctx, &catalog.Video{
		ID:          "video-1",
		DriveID:     "drive",
		FileID:      "file-1",
		Title:       "清纯标题",
		PublishedAt: now,
		CreatedAt:   now,
		UpdatedAt:   now,
	}); err != nil {
		t.Fatalf("seed video: %v", err)
	}
	if _, err := cat.CreateTagAndClassify(ctx, "清纯", nil, "user"); err != nil {
		t.Fatalf("create tag: %v", err)
	}

	req := requestWithVideoID(http.MethodPut, "/api/video/video-1/tags", "video-1", strings.NewReader(`{"tags":["清纯"]}`))
	rr := httptest.NewRecorder()
	(&Server{Catalog: cat}).handleUpdateVideoTags(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rr.Code, rr.Body.String())
	}
	got, err := cat.GetVideo(ctx, "video-1")
	if err != nil {
		t.Fatalf("get video: %v", err)
	}
	if !sameStrings(got.Tags, []string{"清纯"}) {
		t.Fatalf("tags = %#v, want 清纯", got.Tags)
	}
}

func TestHandleVideoDetailIncludesDriveKindLabel(t *testing.T) {
	ctx := context.Background()
	cat, err := catalog.Open(t.TempDir() + "/catalog.db")
	if err != nil {
		t.Fatalf("open catalog: %v", err)
	}
	t.Cleanup(func() {
		if err := cat.Close(); err != nil {
			t.Fatalf("close catalog: %v", err)
		}
	})
	now := time.Now()
	if err := cat.UpsertDrive(ctx, &catalog.Drive{
		ID:        "drive-onedrive",
		Kind:      "onedrive",
		Name:      "Personal Drive",
		RootID:    "root",
		Status:    "ok",
		CreatedAt: now,
		UpdatedAt: now,
	}); err != nil {
		t.Fatalf("seed drive: %v", err)
	}
	if err := cat.UpsertVideo(ctx, &catalog.Video{
		ID:          "video-1",
		DriveID:     "drive-onedrive",
		FileID:      "file-1",
		Title:       "Video",
		PublishedAt: now,
		CreatedAt:   now,
		UpdatedAt:   now,
	}); err != nil {
		t.Fatalf("seed video: %v", err)
	}

	req := requestWithVideoID(http.MethodGet, "/api/video/video-1", "video-1", strings.NewReader(``))
	rr := httptest.NewRecorder()
	(&Server{Catalog: cat}).handleVideoDetail(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rr.Code, rr.Body.String())
	}
	var got VideoDetailDTO
	if err := json.NewDecoder(rr.Body).Decode(&got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.SourceLabel != "OneDrive" {
		t.Fatalf("sourceLabel = %q, want OneDrive", got.SourceLabel)
	}
}

func TestHandleHideVideoRemovesVideoFromPublicListAndDetail(t *testing.T) {
	ctx := context.Background()
	cat, err := catalog.Open(t.TempDir() + "/catalog.db")
	if err != nil {
		t.Fatalf("open catalog: %v", err)
	}
	t.Cleanup(func() {
		if err := cat.Close(); err != nil {
			t.Fatalf("close catalog: %v", err)
		}
	})

	now := time.Now()
	for _, v := range []*catalog.Video{
		{
			ID:          "video-hidden",
			DriveID:     "drive",
			FileID:      "file-hidden",
			Title:       "Hide me",
			PublishedAt: now,
			CreatedAt:   now,
			UpdatedAt:   now,
		},
		{
			ID:          "video-visible",
			DriveID:     "drive",
			FileID:      "file-visible",
			Title:       "Keep me",
			PublishedAt: now.Add(-time.Minute),
			CreatedAt:   now,
			UpdatedAt:   now,
		},
	} {
		if err := cat.UpsertVideo(ctx, v); err != nil {
			t.Fatalf("seed video %s: %v", v.ID, err)
		}
	}

	server := &Server{Catalog: cat}
	hideReq := requestWithVideoID(http.MethodPost, "/api/video/video-hidden/hide", "video-hidden", strings.NewReader(``))
	hideRR := httptest.NewRecorder()
	server.handleHideVideo(hideRR, hideReq)

	if hideRR.Code != http.StatusOK {
		t.Fatalf("hide status = %d, body = %s", hideRR.Code, hideRR.Body.String())
	}

	listReq := httptest.NewRequest(http.MethodGet, "/api/list?page=1&size=24", nil)
	listRR := httptest.NewRecorder()
	server.handleList(listRR, listReq)

	if listRR.Code != http.StatusOK {
		t.Fatalf("list status = %d, body = %s", listRR.Code, listRR.Body.String())
	}
	var listed struct {
		Items []VideoDTO `json:"items"`
		Total int        `json:"total"`
	}
	if err := json.NewDecoder(listRR.Body).Decode(&listed); err != nil {
		t.Fatalf("decode list: %v", err)
	}
	if listed.Total != 1 || len(listed.Items) != 1 || listed.Items[0].ID != "video-visible" {
		t.Fatalf("listed = total:%d items:%#v, want only video-visible", listed.Total, listed.Items)
	}

	detailReq := requestWithVideoID(http.MethodGet, "/api/video/video-hidden", "video-hidden", strings.NewReader(``))
	detailRR := httptest.NewRecorder()
	server.handleVideoDetail(detailRR, detailReq)

	if detailRR.Code != http.StatusNotFound {
		t.Fatalf("detail status = %d, want 404; body = %s", detailRR.Code, detailRR.Body.String())
	}
}

func sameStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func containsString(list []string, value string) bool {
	for _, item := range list {
		if item == value {
			return true
		}
	}
	return false
}

func sameStringSet(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	seen := make(map[string]int, len(a))
	for _, value := range a {
		seen[value]++
	}
	for _, value := range b {
		if seen[value] == 0 {
			return false
		}
		seen[value]--
	}
	return true
}

func requestWithVideoID(method, target, videoID string, body *strings.Reader) *http.Request {
	return requestWithRouteParam(method, target, "id", videoID, body)
}

func requestWithRouteParam(method, target, key, value string, body *strings.Reader) *http.Request {
	req := httptest.NewRequest(method, target, body)
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add(key, value)
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))
	return req
}

func requestHomeIDs(t *testing.T, server *Server) []string {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/api/home", nil)
	rr := httptest.NewRecorder()
	server.handleHome(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("home status = %d, body = %s", rr.Code, rr.Body.String())
	}
	var videos []VideoDTO
	if err := json.NewDecoder(rr.Body).Decode(&videos); err != nil {
		t.Fatalf("decode home response: %v", err)
	}
	ids := make([]string, 0, len(videos))
	for _, v := range videos {
		ids = append(ids, v.ID)
	}
	return ids
}

func multipartUploadRequest(t *testing.T, fields map[string]string, fileName, fileContent string) *http.Request {
	t.Helper()
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	for key, value := range fields {
		if err := writer.WriteField(key, value); err != nil {
			t.Fatalf("write field %s: %v", key, err)
		}
	}
	part, err := writer.CreateFormFile("file", fileName)
	if err != nil {
		t.Fatalf("create file part: %v", err)
	}
	if _, err := part.Write([]byte(fileContent)); err != nil {
		t.Fatalf("write file part: %v", err)
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("close multipart writer: %v", err)
	}
	req := httptest.NewRequest(http.MethodPost, "/api/upload", &body)
	req.Header.Set("Content-Type", writer.FormDataContentType())
	return req
}
