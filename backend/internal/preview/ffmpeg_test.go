package preview

import (
	"context"
	"errors"
	"math"
	"net/http"
	"strings"
	"testing"

	"github.com/video-site/backend/internal/drives"
)

func TestNewDefaultsToThreeSecondTeaserSegments(t *testing.T) {
	gen := New(Config{})
	if gen.cfg.DurationSeconds != 3 {
		t.Fatalf("DurationSeconds = %d, want 3", gen.cfg.DurationSeconds)
	}
}

func TestMediumVideoPreviewPlanUsesFourThreeSecondSegments(t *testing.T) {
	plan := buildTeaserPlan(Config{DurationSeconds: 3, Segments: 3}, 300)
	if len(plan.starts) != 4 {
		t.Fatalf("segments = %d, want 4", len(plan.starts))
	}
	if plan.eachSec != 3 {
		t.Fatalf("eachSec = %.2f, want 3", plan.eachSec)
	}
	want := []float64{15, 95, 175, 255}
	for i := range want {
		if math.Abs(plan.starts[i]-want[i]) > 0.001 {
			t.Fatalf("start[%d] = %.2f, want %.2f", i, plan.starts[i], want[i])
		}
	}
}

func TestLongVideoPreviewPlanUsesFourThreeSecondSegments(t *testing.T) {
	plan := buildTeaserPlan(Config{DurationSeconds: 15, Segments: 3}, 601)
	if len(plan.starts) != 4 {
		t.Fatalf("segments = %d, want 4", len(plan.starts))
	}
	if plan.eachSec != 3 {
		t.Fatalf("eachSec = %.2f, want 3", plan.eachSec)
	}
	want := []float64{120.2, 240.4, 360.6, 480.8}
	for i := range want {
		if math.Abs(plan.starts[i]-want[i]) > 0.001 {
			t.Fatalf("start[%d] = %.2f, want %.2f", i, plan.starts[i], want[i])
		}
	}
}

func TestShortVideoPreviewPlanUsesUpToThreeThreeSecondSegments(t *testing.T) {
	plan := buildTeaserPlan(Config{DurationSeconds: 15, Segments: 3}, 20)
	if len(plan.starts) != 3 {
		t.Fatalf("segments = %d, want 3", len(plan.starts))
	}
	if plan.eachSec != 3 {
		t.Fatalf("eachSec = %.2f, want 3", plan.eachSec)
	}
	want := []float64{2, 9.5, 17}
	for i := range want {
		if math.Abs(plan.starts[i]-want[i]) > 0.001 {
			t.Fatalf("start[%d] = %.2f, want %.2f", i, plan.starts[i], want[i])
		}
	}
}

func TestShortVideoPreviewPlanDropsSegmentsThatDoNotFit(t *testing.T) {
	plan := buildTeaserPlan(Config{DurationSeconds: 15, Segments: 3}, 8)
	if len(plan.starts) != 2 {
		t.Fatalf("segments = %d, want 2", len(plan.starts))
	}
	if plan.eachSec != 3 {
		t.Fatalf("eachSec = %.2f, want 3", plan.eachSec)
	}
	want := []float64{0.8, 5}
	for i := range want {
		if math.Abs(plan.starts[i]-want[i]) > 0.001 {
			t.Fatalf("start[%d] = %.2f, want %.2f", i, plan.starts[i], want[i])
		}
	}
}

func TestTinyVideoPreviewPlanUsesWholeVideoAsSingleSegment(t *testing.T) {
	plan := buildTeaserPlan(Config{DurationSeconds: 15, Segments: 3}, 2.5)
	if len(plan.starts) != 1 {
		t.Fatalf("segments = %d, want 1", len(plan.starts))
	}
	if plan.eachSec != 2.5 {
		t.Fatalf("eachSec = %.2f, want 2.5", plan.eachSec)
	}
	if plan.starts[0] != 0 {
		t.Fatalf("start[0] = %.2f, want 0", plan.starts[0])
	}
}

func TestTeaserCandidateStartsKeepPrimaryAndAddFallbacks(t *testing.T) {
	primary := []float64{10.2, 64.65, 119.1, 173.55}
	got := teaserCandidateStarts(204, primary, 3)
	if len(got) <= len(primary) {
		t.Fatalf("candidate starts = %#v, want fallback starts after primary", got)
	}
	for i, want := range primary {
		if math.Abs(got[i]-want) > 0.001 {
			t.Fatalf("candidate[%d] = %.2f, want primary %.2f first", i, got[i], want)
		}
	}
}

func TestTeaserSegmentFallbackAllowedForBadSegmentOutput(t *testing.T) {
	for _, err := range []error{
		errors.New("generated teaser has no video stream"),
		errors.New("ffmpeg segment: signal: killed, stderr: "),
		errors.New("ffmpeg segment produced empty file, stderr: "),
	} {
		if !teaserSegmentFallbackAllowed(err) {
			t.Fatalf("teaserSegmentFallbackAllowed(%v) = false, want true", err)
		}
	}
	if teaserSegmentFallbackAllowed(errors.New("server returned 403 forbidden")) {
		t.Fatal("403 errors should not trigger teaser segment fallback")
	}
}

func TestTeaserSegmentFallbackRequiresPlannedSegmentCount(t *testing.T) {
	err := errors.New("only generated 2/4 teaser segments: generated teaser has no video stream")
	if !strings.Contains(err.Error(), "2/4") {
		t.Fatalf("error = %v, want generated/planned segment count", err)
	}
}

func TestThumbnailOffsetsUseFiveSecondsWithEarlyFallbacks(t *testing.T) {
	got := thumbnailOffsets()
	want := []float64{5, 1, 0}
	if len(got) != len(want) {
		t.Fatalf("offsets = %#v, want %#v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("offset[%d] = %.2f, want %.2f", i, got[i], want[i])
		}
	}
}

func TestThumbnailOffsetFallbackAllowedForEmptyOutputAndTimeouts(t *testing.T) {
	for _, err := range []error{
		errors.New("ffmpeg thumb produced empty file, stderr: "),
		errors.New("ffmpeg thumb: signal: killed, stderr: "),
		context.DeadlineExceeded,
	} {
		if !thumbnailOffsetFallbackAllowed(err) {
			t.Fatalf("thumbnailOffsetFallbackAllowed(%v) = false, want true", err)
		}
	}
	if thumbnailOffsetFallbackAllowed(errors.New("server returned 403 forbidden")) {
		t.Fatal("403 errors should not trigger thumbnail offset fallback")
	}
}

func TestFFmpeg429OutputBecomesRateLimitError(t *testing.T) {
	err := ffmpegCommandError("ffmpeg", errors.New("exit status 8"), []byte("Server returned 429 Too Many Requests"))
	var rateLimit *drives.RateLimitError
	if !errors.As(err, &rateLimit) {
		t.Fatalf("error = %T %[1]v, want RateLimitError", err)
	}
	if rateLimit.RetryAfter != 0 {
		t.Fatalf("retry after = %v, want none from ffmpeg stderr", rateLimit.RetryAfter)
	}
}

func TestFFmpegCommandErrorRedactsSignedURLs(t *testing.T) {
	err := ffmpegCommandError("ffmpeg", errors.New("exit status 8"), []byte("Error opening input file https://download.example/file.mp4?tempauth=secret."))
	got := err.Error()
	if strings.Contains(got, "tempauth=secret") {
		t.Fatalf("error leaked signed URL: %s", got)
	}
	if !strings.Contains(got, "https://<redacted>.") {
		t.Fatalf("error = %q, want redacted URL with punctuation preserved", got)
	}
}

func TestFFmpegHTTPInputOptionsUsesDedicatedUserAgent(t *testing.T) {
	link := &drives.StreamLink{
		URL: "https://download.example/video.mp4",
		Headers: http.Header{
			"User-Agent": {"Mozilla/5.0 115Browser/27.0.5.7"},
			"Cookie":     {"UID=redacted"},
		},
	}

	args := ffmpegHTTPInputOptions(link)
	joined := strings.Join(args, "\n")
	if !strings.Contains(joined, "-user_agent\nMozilla/5.0 115Browser/27.0.5.7") {
		t.Fatalf("args = %#v, want dedicated ffmpeg user_agent option", args)
	}
	if strings.Contains(joined, "User-Agent:") {
		t.Fatalf("args = %#v, user agent should not be duplicated in raw headers", args)
	}
	if !strings.Contains(joined, "Cookie: UID=redacted") {
		t.Fatalf("args = %#v, want cookie preserved in raw headers", args)
	}
}

func TestShouldProxy115FFmpegLinks(t *testing.T) {
	if !shouldProxyFFmpegLink(&drives.StreamLink{URL: "https://cdnfhnfile.115cdn.net/file.mp4"}) {
		t.Fatal("115 CDN link should use local ffmpeg proxy")
	}
	if shouldProxyFFmpegLink(&drives.StreamLink{URL: "https://download.example/file.mp4"}) {
		t.Fatal("generic link should not use local ffmpeg proxy")
	}
}
