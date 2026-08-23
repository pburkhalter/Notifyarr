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
			got := classify(c.quality, c.formats, c.languages, c.scene)
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
	got := classify("Bluray-1080p", nil, []string{"English"}, "")
	if got.Tier != tierHigh {
		t.Errorf("Tier = %q", got.Tier)
	}
	if got.Detail != "1080p · Bluray · English" {
		t.Errorf("Detail = %q", got.Detail)
	}
}

// Radarr liefert customFormats im eingebetteten movieFile LEER — die Erkennung
// darf deshalb nicht allein davon abhaengen. Der Release-Name ueberlebt das
// Umbenennen und traegt das LD-Kuerzel weiter. Realfall Backrooms, 2026-08-23:
// die Datei heisst "Backrooms (2026) WEBDL-1080p.mkv", der sceneName aber
// "Backrooms.2026.German.5.1.LD.DL.1080p.WEB.h264-LiNEUP".
func TestClassifyFallsBackToSceneName(t *testing.T) {
	got := classify("WEBDL-1080p", nil, []string{"German", "English"},
		"Backrooms.2026.German.5.1.LD.DL.1080p.WEB.h264-LiNEUP")
	if got.Tier != tierLimited {
		t.Errorf("Tier = %q, erwartet %q", got.Tier, tierLimited)
	}
	if !got.Limited {
		t.Error("Limited muss gesetzt sein, damit der Ersetzungs-Hinweis erscheint")
	}
}

// "DL" (Dual Language) darf NICHT als Line-Dub durchgehen — sonst waere fast
// jedes deutsche Release faelschlich eingeschraenkt.
func TestClassifyDoesNotConfuseDLWithLD(t *testing.T) {
	got := classify("WEBDL-1080p", []string{"German DL"}, []string{"German"},
		"Toy.Story.5.2026.German.DL.1080p.WEB.h264-WvF")
	if got.Tier != tierHigh {
		t.Errorf("Tier = %q, erwartet %q — DL ist kein Line-Dub", got.Tier, tierHigh)
	}
	if got.Limited {
		t.Error("Limited darf bei regulaerem German DL nicht gesetzt sein")
	}
}

// Mic-Dub bleibt gesperrt, wird aber falls doch importiert als eingeschraenkt
// gemeldet.
func TestClassifyDetectsMicDubInSceneName(t *testing.T) {
	got := classify("WEBDL-1080p", nil, []string{"German"},
		"Irgendwas.2026.German.MD.1080p.WEB.h264-XY")
	if got.Tier != tierLimited {
		t.Errorf("Tier = %q, erwartet %q", got.Tier, tierLimited)
	}
}
