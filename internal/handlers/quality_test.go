package handlers

import "testing"

func TestClassify(t *testing.T) {
	cases := []struct {
		name        string
		quality     string
		formats     []string
		languages   []string
		wantTier    string
		wantLimited bool
		wantDetail  string
		scene       string
	}{
		{
			name:    "German 1080p WEB-DL is the normal case",
			quality: "WEBDL-1080p", formats: []string{"German DL", "German Web Tier 01"},
			languages: []string{"German", "English"},
			wantTier:  tierHigh, wantLimited: false, wantDetail: "1080p · WEB · Deutsch",
		},
		{
			name:    "Bluray 2160p is the top tier",
			quality: "Bluray-2160p", formats: nil, languages: []string{"German"},
			wantTier: tierVeryHigh, wantLimited: false, wantDetail: "2160p · Bluray · Deutsch",
		},
		{
			// The case the relaxation of 2026-08-21 was made for: the picture
			// is 1080p, but the German track is an amateur dub.
			name:    "a line dub is limited despite 1080p",
			quality: "WEBDL-1080p", formats: []string{"German DL", "Line Dubbed"},
			languages: []string{"German"},
			wantTier:  tierLimited, wantLimited: true,
			wantDetail: "1080p · WEB · deutsche Tonspur inoffiziell",
		},
		{
			name:    "720p is limited",
			quality: "WEBDL-720p", formats: nil, languages: []string{"German"},
			wantTier: tierLimited, wantLimited: true, wantDetail: "720p · WEB · Deutsch",
		},
		{
			name:    "the old combined format is still recognised",
			quality: "WEBDL-1080p", formats: []string{"Line/Mic Dubbed"},
			languages: []string{"German"},
			wantTier:  tierLimited, wantLimited: true,
		},
		{
			name:    "without a quality the line stays out",
			quality: "", wantTier: "", wantLimited: false,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := classify(c.quality, c.formats, c.languages, c.scene)
			if c.quality == "" {
				if got.Available {
					t.Fatalf("without a quality nothing may be shown, got %+v", got)
				}
				return
			}
			if !got.Available {
				t.Fatalf("Available=false for %q", c.quality)
			}
			if got.Tier != c.wantTier {
				t.Errorf("Tier = %q, want %q", got.Tier, c.wantTier)
			}
			if got.Limited != c.wantLimited {
				t.Errorf("Limited = %v, want %v", got.Limited, c.wantLimited)
			}
			if c.wantDetail != "" && got.Detail != c.wantDetail {
				t.Errorf("Detail = %q, want %q", got.Detail, c.wantDetail)
			}
		})
	}
}

// An English file (the fallback for titles without a German release) is named
// in German and flagged, so the message says the German version replaces it.
func TestClassifyNonGerman(t *testing.T) {
	got := classify("Bluray-1080p", nil, []string{"English"}, "")
	if got.Tier != tierHigh {
		t.Errorf("Tier = %q", got.Tier)
	}
	if got.Detail != "1080p · Bluray · Englisch" {
		t.Errorf("Detail = %q", got.Detail)
	}
	if !got.NotGerman {
		t.Error("NotGerman must be set for an English file")
	}
	// A German file with an English second track is German.
	if g := classify("WEBDL-1080p", nil, []string{"German", "English"}, ""); g.NotGerman {
		t.Error("NotGerman set for a German file")
	}
	// Unknown languages keep Sonarr's/Radarr's name.
	if g := classify("WEBDL-1080p", nil, []string{"Klingon"}, ""); g.Detail != "1080p · WEB · Klingon" || !g.NotGerman {
		t.Errorf("unknown language: %+v", g)
	}
	// No language reported: nothing is claimed.
	if g := classify("WEBDL-1080p", nil, nil, ""); g.NotGerman {
		t.Error("NotGerman set without languages")
	}
}

// Radarr returns customFormats EMPTY in the embedded movieFile, so the
// detection must not depend on them alone. The release name survives the
// rename and keeps the LD marker. Real case Backrooms, 2026-08-23: the file is
// "Backrooms (2026) WEBDL-1080p.mkv", the sceneName
// "Backrooms.2026.German.5.1.LD.DL.1080p.WEB.h264-LiNEUP".
func TestClassifyFallsBackToSceneName(t *testing.T) {
	got := classify("WEBDL-1080p", nil, []string{"German", "English"},
		"Backrooms.2026.German.5.1.LD.DL.1080p.WEB.h264-LiNEUP")
	if got.Tier != tierLimited {
		t.Errorf("Tier = %q, want %q", got.Tier, tierLimited)
	}
	if !got.Limited {
		t.Error("Limited must be set so the replacement note shows")
	}
}

// "DL" (dual language) must NOT count as a line dub — otherwise almost every
// German release would be marked limited.
func TestClassifyDoesNotConfuseDLWithLD(t *testing.T) {
	got := classify("WEBDL-1080p", []string{"German DL"}, []string{"German"},
		"Toy.Story.5.2026.German.DL.1080p.WEB.h264-WvF")
	if got.Tier != tierHigh {
		t.Errorf("Tier = %q, want %q — DL is not a line dub", got.Tier, tierHigh)
	}
	if got.Limited {
		t.Error("Limited must not be set for a regular German DL")
	}
}

// Mic dubs stay blocked, but one that gets imported anyway is reported as
// limited.
func TestClassifyDetectsMicDubInSceneName(t *testing.T) {
	got := classify("WEBDL-1080p", nil, []string{"German"},
		"Something.2026.German.MD.1080p.WEB.h264-XY")
	if got.Tier != tierLimited {
		t.Errorf("Tier = %q, want %q", got.Tier, tierLimited)
	}
}
