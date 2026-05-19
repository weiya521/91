package preview

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/video-site/backend/internal/catalog"
	"github.com/video-site/backend/internal/drives"
)

func TestThumbWorkerUpdatesThumbnailWithoutChangingPreviewStatus(t *testing.T) {
	ctx := context.Background()
	cat, video := seedPreviewTestVideo(t, "thumb-worker-video")

	gen := &fakeThumbGenerator{}
	drv := &previewFakeDrive{}
	worker := NewThumbWorker(gen, cat, drv)

	worker.process(ctx, video)

	got, err := cat.GetVideo(ctx, video.ID)
	if err != nil {
		t.Fatalf("get video: %v", err)
	}
	if got.ThumbnailURL != "/p/thumb/"+video.ID {
		t.Fatalf("thumbnail = %q, want generated thumb URL", got.ThumbnailURL)
	}
	if got.PreviewStatus != "pending" {
		t.Fatalf("preview status = %q, want pending", got.PreviewStatus)
	}
	if got.DurationSeconds != 0 {
		t.Fatalf("duration = %d, want unchanged", got.DurationSeconds)
	}
	if gen.thumbnailVideoID != video.ID {
		t.Fatalf("thumbnail video id = %q, want %q", gen.thumbnailVideoID, video.ID)
	}
	if gen.thumbnailDuration != 0 {
		t.Fatalf("thumbnail duration = %.1f, want fixed-offset thumbnail generation", gen.thumbnailDuration)
	}
	if gen.probeCalls != 0 {
		t.Fatalf("probe calls = %d, want 0 for thumbnail generation", gen.probeCalls)
	}
	if drv.streamFileID != video.FileID {
		t.Fatalf("stream file id = %q, want %q", drv.streamFileID, video.FileID)
	}
}

func TestThumbWorkerFallsBackToLocalPreviewWhenDriveStreamFails(t *testing.T) {
	ctx := context.Background()
	cat, video := seedPreviewTestVideo(t, "thumb-worker-local-preview")
	localPreview := filepath.Join(t.TempDir(), "preview.mp4")
	if err := os.WriteFile(localPreview, []byte("preview"), 0o644); err != nil {
		t.Fatalf("write local preview: %v", err)
	}
	video.PreviewLocal = localPreview
	if err := cat.UpsertVideo(ctx, video); err != nil {
		t.Fatalf("update video: %v", err)
	}

	gen := &fakeThumbGenerator{}
	drv := &previewFakeDrive{streamErr: errors.New("remote unavailable")}
	worker := NewThumbWorker(gen, cat, drv)

	worker.process(ctx, video)

	got, err := cat.GetVideo(ctx, video.ID)
	if err != nil {
		t.Fatalf("get video: %v", err)
	}
	if got.ThumbnailURL != "/p/thumb/"+video.ID {
		t.Fatalf("thumbnail = %q, want generated thumb URL", got.ThumbnailURL)
	}
	if gen.thumbnailURL != localPreview {
		t.Fatalf("thumbnail source = %q, want local preview %q", gen.thumbnailURL, localPreview)
	}
}

func TestPreviewWorkerGeneratesTeaserWithoutReplacingExistingThumbnail(t *testing.T) {
	ctx := context.Background()
	cat, video := seedPreviewTestVideo(t, "preview-worker-video")
	video.ThumbnailURL = "https://thumbnail.example/original.jpg"
	if err := cat.UpsertVideo(ctx, video); err != nil {
		t.Fatalf("update video: %v", err)
	}

	gen := &fakeTeaserGenerator{}
	drv := &previewFakeDrive{}
	worker := NewWorker(gen, cat, drv, "")

	worker.process(ctx, video)

	got, err := cat.GetVideo(ctx, video.ID)
	if err != nil {
		t.Fatalf("get video: %v", err)
	}
	if got.ThumbnailURL != "https://thumbnail.example/original.jpg" {
		t.Fatalf("thumbnail = %q, want existing thumbnail unchanged", got.ThumbnailURL)
	}
	if got.PreviewStatus != "ready" {
		t.Fatalf("preview status = %q, want ready", got.PreviewStatus)
	}
	if got.PreviewLocal != "/tmp/"+video.ID+".mp4" {
		t.Fatalf("preview local = %q, want moved teaser path", got.PreviewLocal)
	}
}

func TestPreviewWorkerDeduplicatesQueuedVideos(t *testing.T) {
	ctx := context.Background()
	cat, video := seedPreviewTestVideo(t, "preview-dedupe-video")

	gen := &fakeTeaserGenerator{}
	drv := &previewFakeDrive{}
	worker := NewWorker(gen, cat, drv, "")

	if !worker.EnqueueBlocking(ctx, video) {
		t.Fatal("first enqueue returned false, want true")
	}
	if !worker.EnqueueBlocking(ctx, video) {
		t.Fatal("duplicate enqueue returned false, want idempotent success")
	}
	if got := worker.Status().QueueLength; got != 1 {
		t.Fatalf("queue length = %d, want 1 unique video", got)
	}

	queued := <-worker.ch
	if !worker.Enqueue(video) {
		t.Fatal("enqueue while the same video is reserved returned false, want idempotent success")
	}
	select {
	case <-worker.ch:
		t.Fatal("duplicate enqueue added another queued video")
	default:
	}

	worker.processQueued(ctx, queued)
	if !worker.Enqueue(video) {
		t.Fatal("enqueue after processing returned false, want true")
	}
}

func TestThumbWorkerDeduplicatesQueuedVideos(t *testing.T) {
	ctx := context.Background()
	cat, video := seedPreviewTestVideo(t, "thumb-dedupe-video")

	gen := &fakeThumbGenerator{}
	drv := &previewFakeDrive{}
	worker := NewThumbWorker(gen, cat, drv)

	if !worker.Enqueue(video) {
		t.Fatal("first enqueue returned false, want true")
	}
	if !worker.Enqueue(video) {
		t.Fatal("duplicate enqueue returned false, want idempotent success")
	}
	if got := worker.Status().QueueLength; got != 1 {
		t.Fatalf("queue length = %d, want 1 unique video", got)
	}

	queued := <-worker.ch
	if !worker.Enqueue(video) {
		t.Fatal("enqueue while the same thumbnail is reserved returned false, want idempotent success")
	}
	select {
	case <-worker.ch:
		t.Fatal("duplicate enqueue added another queued thumbnail")
	default:
	}

	worker.processQueued(ctx, queued)
	if !worker.Enqueue(video) {
		t.Fatal("enqueue after release returned false, want true")
	}
}

func TestPreviewWorkerRemovesPreviousLocalTeaserAfterNewTeaserIsReady(t *testing.T) {
	ctx := context.Background()
	cat, video := seedPreviewTestVideo(t, "preview-cleanup-video")
	oldPath := filepath.Join(t.TempDir(), "old-teaser.mp4")
	if err := os.WriteFile(oldPath, []byte("old teaser"), 0o644); err != nil {
		t.Fatalf("write old teaser: %v", err)
	}
	video.PreviewLocal = oldPath
	video.PreviewStatus = "ready"
	if err := cat.UpsertVideo(ctx, video); err != nil {
		t.Fatalf("update video: %v", err)
	}

	gen := &fakeTeaserGenerator{
		localPath: filepath.Join(t.TempDir(), "new-teaser.mp4"),
	}
	drv := &previewFakeDrive{}
	worker := NewWorker(gen, cat, drv, "")

	worker.process(ctx, video)

	if _, err := os.Stat(oldPath); !os.IsNotExist(err) {
		t.Fatalf("old teaser still exists or stat failed with unexpected error: %v", err)
	}
	got, err := cat.GetVideo(ctx, video.ID)
	if err != nil {
		t.Fatalf("get video: %v", err)
	}
	if got.PreviewLocal != gen.localPath {
		t.Fatalf("preview local = %q, want %q", got.PreviewLocal, gen.localPath)
	}
}

func TestPreviewWorkerDoesNotUploadTeaserWhenRemoteDirIsConfigured(t *testing.T) {
	ctx := context.Background()
	cat, video := seedPreviewTestVideo(t, "preview-local-only-video")
	localPath := filepath.Join(t.TempDir(), "local-only-teaser.mp4")
	gen := &fakeTeaserGenerator{localPath: localPath}
	drv := &previewFakeDrive{}
	worker := NewWorker(gen, cat, drv, "/previews")

	worker.process(ctx, video)

	got, err := cat.GetVideo(ctx, video.ID)
	if err != nil {
		t.Fatalf("get video: %v", err)
	}
	if got.PreviewStatus != "ready" {
		t.Fatalf("preview status = %q, want ready", got.PreviewStatus)
	}
	if got.PreviewLocal != localPath {
		t.Fatalf("preview local = %q, want %q", got.PreviewLocal, localPath)
	}
	if got.PreviewFileID != "" {
		t.Fatalf("preview file id = %q, want empty for local-only teaser", got.PreviewFileID)
	}
	if drv.ensureDirCalls != 0 {
		t.Fatalf("ensure dir calls = %d, want 0", drv.ensureDirCalls)
	}
	if drv.uploadCalls != 0 {
		t.Fatalf("upload calls = %d, want 0", drv.uploadCalls)
	}
}

func TestPreviewWorkerSkipsTeaserForVideoLargerThanFiveGiB(t *testing.T) {
	ctx := context.Background()
	cat, video := seedPreviewTestVideo(t, "preview-large-video")
	video.Size = maxPreviewTeaserSizeBytes + 1
	if err := cat.UpsertVideo(ctx, video); err != nil {
		t.Fatalf("update video: %v", err)
	}

	gen := &fakeTeaserGenerator{}
	drv := &previewFakeDrive{}
	worker := NewWorker(gen, cat, drv, "")

	worker.process(ctx, video)

	got, err := cat.GetVideo(ctx, video.ID)
	if err != nil {
		t.Fatalf("get video: %v", err)
	}
	if got.PreviewStatus != previewStatusSkipped {
		t.Fatalf("preview status = %q, want skipped", got.PreviewStatus)
	}
	if got.PreviewLocal != "" {
		t.Fatalf("preview local = %q, want empty", got.PreviewLocal)
	}
	if drv.streamCalls != 0 {
		t.Fatalf("stream calls = %d, want 0", drv.streamCalls)
	}
	if gen.generateCalls != 0 {
		t.Fatalf("generate calls = %d, want 0", gen.generateCalls)
	}
}

func TestPreviewWorkerGeneratesTeaserAtFiveGiBBoundary(t *testing.T) {
	ctx := context.Background()
	cat, video := seedPreviewTestVideo(t, "preview-five-gib-video")
	video.Size = maxPreviewTeaserSizeBytes
	if err := cat.UpsertVideo(ctx, video); err != nil {
		t.Fatalf("update video: %v", err)
	}

	gen := &fakeTeaserGenerator{}
	drv := &previewFakeDrive{}
	worker := NewWorker(gen, cat, drv, "")

	worker.process(ctx, video)

	got, err := cat.GetVideo(ctx, video.ID)
	if err != nil {
		t.Fatalf("get video: %v", err)
	}
	if got.PreviewStatus != "ready" {
		t.Fatalf("preview status = %q, want ready", got.PreviewStatus)
	}
	if drv.streamCalls != 1 {
		t.Fatalf("stream calls = %d, want 1", drv.streamCalls)
	}
	if gen.generateCalls != 1 {
		t.Fatalf("generate calls = %d, want 1", gen.generateCalls)
	}
}

func TestPreviewWorkerRateLimitLeavesCurrentPendingAndSkipsNextVideo(t *testing.T) {
	ctx := context.Background()
	cat, first := seedPreviewTestVideo(t, "preview-rate-limit-1")
	second := *first
	second.ID = "preview-rate-limit-2"
	second.FileID = "file-id-2"
	if err := cat.UpsertVideo(ctx, &second); err != nil {
		t.Fatalf("seed second video: %v", err)
	}

	gen := &fakeTeaserGenerator{
		generateErr: &drives.RateLimitError{
			Provider:   "onedrive",
			RetryAfter: 2 * time.Hour,
			Err:        errors.New("429 Too Many Requests"),
		},
	}
	drv := &previewFakeDrive{}
	worker := NewWorker(gen, cat, drv, "")

	before := time.Now()
	worker.process(ctx, first)
	gotFirst, err := cat.GetVideo(ctx, first.ID)
	if err != nil {
		t.Fatalf("get first video: %v", err)
	}
	if gotFirst.PreviewStatus != "pending" {
		t.Fatalf("first preview status = %q, want pending after rate limit", gotFirst.PreviewStatus)
	}
	if gen.generateCalls != 1 {
		t.Fatalf("generate calls = %d, want 1", gen.generateCalls)
	}
	assertCooldownAround(t, worker.Status().CooldownUntil, before, 5*time.Minute)

	gen.generateErr = nil
	worker.process(ctx, &second)
	gotSecond, err := cat.GetVideo(ctx, second.ID)
	if err != nil {
		t.Fatalf("get second video: %v", err)
	}
	if gotSecond.PreviewStatus != "pending" {
		t.Fatalf("second preview status = %q, want pending while drive is cooling down", gotSecond.PreviewStatus)
	}
	if gen.generateCalls != 1 {
		t.Fatalf("generate calls = %d, want second video skipped during cooldown", gen.generateCalls)
	}
}

func TestThumbWorkerRateLimitCoolsDownFiveMinutes(t *testing.T) {
	ctx := context.Background()
	cat, video := seedPreviewTestVideo(t, "thumb-rate-limit")

	gen := &fakeThumbGenerator{
		generateErr: &drives.RateLimitError{
			Provider:   "media source",
			RetryAfter: 2 * time.Hour,
			Err:        errors.New("429 Too Many Requests"),
		},
	}
	drv := &previewFakeDrive{}
	worker := NewThumbWorker(gen, cat, drv)

	before := time.Now()
	worker.process(ctx, video)

	got, err := cat.GetVideo(ctx, video.ID)
	if err != nil {
		t.Fatalf("get video: %v", err)
	}
	if got.ThumbnailURL != "" {
		t.Fatalf("thumbnail = %q, want unchanged after rate limit", got.ThumbnailURL)
	}
	assertCooldownAround(t, worker.Status().CooldownUntil, before, 5*time.Minute)
}

func TestPreviewWorkerP115TransientErrorKeepsVideoPending(t *testing.T) {
	ctx := context.Background()
	cat, video := seedPreviewTestVideo(t, "preview-p115-transient")

	gen := &fakeTeaserGenerator{
		generateErr: errors.New("ffmpeg: exit status 1, stderr: Server returned 403 Forbidden"),
	}
	drv := &previewFakeDrive{kind: "p115"}
	worker := NewWorker(gen, cat, drv, "")

	worker.process(ctx, video)

	got, err := cat.GetVideo(ctx, video.ID)
	if err != nil {
		t.Fatalf("get video: %v", err)
	}
	if got.PreviewStatus != "pending" {
		t.Fatalf("preview status = %q, want pending for transient 115 media error", got.PreviewStatus)
	}
	if gen.generateCalls != 1 {
		t.Fatalf("generate calls = %d, want 1", gen.generateCalls)
	}
}

func assertCooldownAround(t *testing.T, until time.Time, before time.Time, want time.Duration) {
	t.Helper()
	if until.IsZero() {
		t.Fatal("cooldown is zero, want active cooldown")
	}
	min := before.Add(want - time.Second)
	max := time.Now().Add(want + time.Second)
	if until.Before(min) || until.After(max) {
		t.Fatalf("cooldown until = %s, want around %s from now", until.Format(time.RFC3339Nano), want)
	}
}

func TestPreviewWorkerRefreshesP115LinksPerTeaserInput(t *testing.T) {
	ctx := context.Background()
	cat, video := seedPreviewTestVideo(t, "preview-p115-refresh")
	video.DurationSeconds = 81
	if err := cat.UpsertVideo(ctx, video); err != nil {
		t.Fatalf("update video: %v", err)
	}

	gen := &fakeTeaserGenerator{}
	drv := &previewFakeDrive{kind: "p115"}
	worker := NewWorker(gen, cat, drv, "")

	worker.process(ctx, video)

	if gen.refreshCalls != 3 {
		t.Fatalf("refresh calls = %d, want 3 extra links for a four-input p115 teaser", gen.refreshCalls)
	}
	if drv.streamCalls != 4 {
		t.Fatalf("stream calls = %d, want initial link plus 3 refreshed links", drv.streamCalls)
	}
}

func seedPreviewTestVideo(t *testing.T, id string) (*catalog.Catalog, *catalog.Video) {
	t.Helper()
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

	video := &catalog.Video{
		ID:            id,
		DriveID:       "drive-id",
		FileID:        "file-id",
		Title:         "Clip",
		PreviewStatus: "pending",
		PublishedAt:   time.Now(),
		CreatedAt:     time.Now(),
		UpdatedAt:     time.Now(),
	}
	if err := cat.UpsertVideo(ctx, video); err != nil {
		t.Fatalf("seed video: %v", err)
	}
	return cat, video
}

type fakeThumbGenerator struct {
	thumbnailVideoID  string
	thumbnailDuration float64
	thumbnailURL      string
	probeCalls        int
	generateErr       error
}

func (g *fakeThumbGenerator) Probe(context.Context, *drives.StreamLink) (float64, error) {
	g.probeCalls++
	return 42, nil
}

func (g *fakeThumbGenerator) GenerateThumbnail(_ context.Context, link *drives.StreamLink, videoID string, duration float64) (string, error) {
	g.thumbnailVideoID = videoID
	g.thumbnailDuration = duration
	if link != nil {
		g.thumbnailURL = link.URL
	}
	if g.generateErr != nil {
		return "", g.generateErr
	}
	return "/tmp/" + videoID + ".jpg", nil
}

type fakeTeaserGenerator struct {
	localPath     string
	generateErr   error
	generateCalls int
	refreshCalls  int
}

func (g *fakeTeaserGenerator) Probe(context.Context, *drives.StreamLink) (float64, error) {
	return 0, nil
}

func (g *fakeTeaserGenerator) Generate(context.Context, *drives.StreamLink, float64) (string, error) {
	g.generateCalls++
	if g.generateErr != nil {
		return "", g.generateErr
	}
	return "/tmp/source-teaser.mp4", nil
}

func (g *fakeTeaserGenerator) GenerateWithLinkProvider(ctx context.Context, first *drives.StreamLink, duration float64, refresh func(context.Context) (*drives.StreamLink, error)) (string, error) {
	for i := 0; i < 3; i++ {
		if _, err := refresh(ctx); err != nil {
			return "", err
		}
		g.refreshCalls++
	}
	return g.Generate(ctx, first, duration)
}

func (g *fakeTeaserGenerator) MoveToLocal(_ string, videoID string) (string, error) {
	if g.localPath != "" {
		return g.localPath, nil
	}
	return "/tmp/" + videoID + ".mp4", nil
}

type previewFakeDrive struct {
	kind           string
	streamFileID   string
	streamCalls    int
	streamErr      error
	ensureDirCalls int
	uploadCalls    int
}

func (d *previewFakeDrive) Kind() string {
	if d.kind != "" {
		return d.kind
	}
	return "fake"
}
func (d *previewFakeDrive) ID() string { return "drive-id" }
func (d *previewFakeDrive) Init(context.Context) error {
	return nil
}
func (d *previewFakeDrive) List(context.Context, string) ([]drives.Entry, error) {
	return nil, nil
}
func (d *previewFakeDrive) Stat(context.Context, string) (*drives.Entry, error) {
	return nil, drives.ErrNotSupported
}
func (d *previewFakeDrive) StreamURL(_ context.Context, fileID string) (*drives.StreamLink, error) {
	d.streamCalls++
	d.streamFileID = fileID
	if d.streamErr != nil {
		return nil, d.streamErr
	}
	return &drives.StreamLink{URL: "https://video.example/clip.mp4"}, nil
}
func (d *previewFakeDrive) Upload(context.Context, string, string, io.Reader, int64) (string, error) {
	d.uploadCalls++
	return "", drives.ErrNotSupported
}
func (d *previewFakeDrive) EnsureDir(context.Context, string) (string, error) {
	d.ensureDirCalls++
	return "", drives.ErrNotSupported
}
func (d *previewFakeDrive) RootID() string { return "root" }
