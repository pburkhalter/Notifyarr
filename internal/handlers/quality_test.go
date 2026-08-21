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
	}{
		{
			name:    "deutsche 1080p WEB-DL ist die Normalerwartung",
			quality: "WEBDL-1080p", formats: []string{"German DL", "German Web Tier 01"},
			languages: []string{"German", "English"},
			wantTier:  tierHigh, wantLimited: false, wantDetail: "1080p · WEB · Deutsch",
		},
		{
			name:    "Bluray 2160p ist die hoechste Stufe",
			quality: "Bluray-2160p", formats: nil, languages: []string{"German"},
			wantTier: tierVeryHigh, wantLimited: false, wantDetail: "2160p · Bluray · Deutsch",
		},
		{
			// Der Fall, fuer den die Lockerung vom 2026-08-21 gemacht wurde:
			// Bild ist 1080p, aber die deutsche Tonspur ist eine Laiensynchro.
			name:    "Line-Dub zieht trotz 1080p auf eingeschraenkt",
			quality: "WEBDL-1080p", formats: []string{"German DL", "Line Dubbed"},
			languages: []string{"German"},
			wantTier:  tierLimited, wantLimited: true,
			wantDetail: "1080p · WEB · deutsche Tonspur inoffiziell",
		},
		{
			name:    "720p gilt als eingeschraenkt",
			quality: "WEBDL-720p", formats: nil, languages: []string{"German"},
			wantTier: tierLimited, wantLimited: true, wantDetail: "720p · WEB · Deutsch",
		},
		{
			name:    "altes Kombiformat wird weiterhin erkannt",
			quality: "WEBDL-1080p", formats: []string{"Line/Mic Dubbed"},
			languages: []string{"German"},
			wantTier:  tierLimited, wantLimited: true,
		},
		{
			name:    "ohne Qualitaetsangabe bleibt die Zeile weg",
			quality: "", wantTier: "", wantLimited: false,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := classify(c.quality, c.formats, c.languages)
			if c.quality == "" {
				if got.Available {
					t.Fatalf("ohne Qualitaet darf nichts ausgegeben werden, bekam %+v", got)
				}
				return
			}
			if !got.Available {
				t.Fatalf("Available=false fuer %q", c.quality)
			}
			if got.Tier != c.wantTier {
				t.Errorf("Tier = %q, erwartet %q", got.Tier, c.wantTier)
			}
			if got.Limited != c.wantLimited {
				t.Errorf("Limited = %v, erwartet %v", got.Limited, c.wantLimited)
			}
			if c.wantDetail != "" && got.Detail != c.wantDetail {
				t.Errorf("Detail = %q, erwartet %q", got.Detail, c.wantDetail)
			}
		})
	}
}

// Englische Releases sollen nicht faelschlich als deutsch beschrieben werden.
func TestClassifyNonGerman(t *testing.T) {
	got := classify("Bluray-1080p", nil, []string{"English"})
	if got.Tier != tierHigh {
		t.Errorf("Tier = %q", got.Tier)
	}
	if got.Detail != "1080p · Bluray · English" {
		t.Errorf("Detail = %q", got.Detail)
	}
}
