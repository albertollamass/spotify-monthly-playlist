package handlers

import (
	"reflect"
	"testing"
	"time"

	"spotify-monthly-playlist/internal/db"
	"spotify-monthly-playlist/internal/spotify"
)

func TestMonthTimeRange(t *testing.T) {
	now := time.Date(2026, time.May, 31, 12, 0, 0, 0, time.UTC)

	cases := []struct {
		month string
		want  string
	}{
		{month: "2026-05", want: "short_term"},
		{month: "2026-04", want: "short_term"},
		{month: "2026-03", want: "medium_term"},
		{month: "2025-12", want: "medium_term"},
		{month: "2025-01", want: "long_term"},
		{month: "bad-input", want: "long_term"},
	}

	for _, tc := range cases {
		t.Run(tc.month, func(t *testing.T) {
			got := monthTimeRange(tc.month, now)
			if got != tc.want {
				t.Fatalf("monthTimeRange(%q) = %q, want %q", tc.month, got, tc.want)
			}
		})
	}
}

func TestOrderedTimeRanges(t *testing.T) {
	months := []string{"2026-05", "2026-03", "2024-01"}
	got := orderedTimeRanges(months)

	if len(got) != 3 {
		t.Fatalf("orderedTimeRanges should return 3 items, got %d (%v)", len(got), got)
	}

	seen := map[string]bool{}
	for _, v := range got {
		seen[v] = true
	}
	for _, expected := range []string{"short_term", "medium_term", "long_term"} {
		if !seen[expected] {
			t.Fatalf("orderedTimeRanges missing %s in %v", expected, got)
		}
	}
}

func TestTrackIDFromURI(t *testing.T) {
	validID := "1234567890ABCDEFGHIJKL"
	if got := trackIDFromURI("spotify:track:" + validID); got != validID {
		t.Fatalf("trackIDFromURI valid = %q, want %q", got, validID)
	}

	if got := trackIDFromURI("spotify:album:" + validID); got != "" {
		t.Fatalf("trackIDFromURI should reject non-track URI, got %q", got)
	}
	if got := trackIDFromURI("spotify:track:short"); got != "" {
		t.Fatalf("trackIDFromURI should reject invalid track id length, got %q", got)
	}
}

func TestFilterUnheardTrackURIs(t *testing.T) {
	heard := map[string]struct{}{
		"AAAAAAAAAAAAAAAAAAAAAA": {},
	}
	in := []string{
		"spotify:track:AAAAAAAAAAAAAAAAAAAAAA", // heard
		"spotify:track:BBBBBBBBBBBBBBBBBBBBBB", // keep
		"spotify:track:BBBBBBBBBBBBBBBBBBBBBB", // duplicate
		"spotify:album:CCCCCCCCCCCCCCCCCCCCCC", // invalid
		"spotify:track:CCCCCCCCCCCCCCCCCCCCCC", // keep
	}
	want := []string{
		"spotify:track:BBBBBBBBBBBBBBBBBBBBBB",
		"spotify:track:CCCCCCCCCCCCCCCCCCCCCC",
	}
	got := filterUnheardTrackURIs(in, heard)
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("filterUnheardTrackURIs() = %v, want %v", got, want)
	}
}

func TestResolveMonthsUsingAvailable(t *testing.T) {
	available := []string{"2026-05", "2026-04", "2023-12", "2022-02", "2021-10", "2020-05"}
	requested := []string{"2024-01", "2022-02", "2019-01"}

	got := resolveMonthsUsingAvailable(requested, available)
	want := []string{"2023-12", "2022-02", "2020-05"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("resolveMonthsUsingAvailable() = %v, want %v", got, want)
	}
}

func TestNearestAvailableMonth(t *testing.T) {
	candidates := []string{"2026-05", "2026-04", "2023-12", "2022-02"}
	if got := nearestAvailableMonth("2024-01", candidates); got != "2023-12" {
		t.Fatalf("nearestAvailableMonth() = %q, want %q", got, "2023-12")
	}
}

func TestRotateTopTracksByMonth(t *testing.T) {
	tracks := []spotify.TopTrackItem{
		{ID: "AAAAAAAAAAAAAAAAAAAAAA"},
		{ID: "BBBBBBBBBBBBBBBBBBBBBB"},
		{ID: "CCCCCCCCCCCCCCCCCCCCCC"},
	}
	r1 := rotateTopTracksByMonth(tracks, "2023-12")
	r2 := rotateTopTracksByMonth(tracks, "2021-10")
	if len(r1) != len(tracks) || len(r2) != len(tracks) {
		t.Fatalf("rotation length mismatch: %d %d", len(r1), len(r2))
	}
	if reflect.DeepEqual(r1, r2) {
		t.Fatalf("expected different month rotations, got equal results: %v", r1)
	}
}

func TestSyntheticPlayWeight(t *testing.T) {
	w1 := syntheticPlayWeight(0, "2023-12")
	w2 := syntheticPlayWeight(30, "2023-12")
	if w1 <= w2 {
		t.Fatalf("expected higher weight for top ranked track, got w1=%d w2=%d", w1, w2)
	}
	if w1 < 1 || w1 > 5 || w2 < 1 || w2 > 5 {
		t.Fatalf("weights out of expected range: w1=%d w2=%d", w1, w2)
	}
}

func TestPickSeedIDsFromMonthlyTracks(t *testing.T) {
	tracks := []db.MonthlyTrack{
		{SpotifyTrackID: "AAAAAAAAAAAAAAAAAAAAAA"},
		{SpotifyTrackID: "BBBBBBBBBBBBBBBBBBBBBB"},
		{SpotifyTrackID: "CCCCCCCCCCCCCCCCCCCCCC"},
		{SpotifyTrackID: "DDDDDDDDDDDDDDDDDDDDDD"},
	}
	got := pickSeedIDsFromMonthlyTracks(tracks, 3, "2023-12")
	if len(got) != 3 {
		t.Fatalf("pickSeedIDsFromMonthlyTracks len=%d, want 3 (%v)", len(got), got)
	}
}
