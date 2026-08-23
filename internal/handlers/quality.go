package handlers

import (
	"context"
	"regexp"
	"strings"
)

// Dieselben Kuerzel, die Radarr/Sonarr in den Custom Formats "Line Dubbed"
// und "Mic Dubbed" verwenden.
var dubMarker = regexp.MustCompile(`(?i)\b(LD|AC3LD|MD|AC3MD|Line[ .-]?Dubbed|Mic[ .-]?Dubbed)\b`)

// Qualitätsstufen für die WhatsApp-Nachricht. Bewusst drei Stufen in
// Alltagssprache statt Release-Jargon — Empfänger sind keine Admins.
//
// Die Einstufung folgt der Logik des Qualitätsprofils "[German] HD Bluray +
// WEB": Auflösung bestimmt die Grundstufe, und eine inoffizielle deutsche
// Tonspur (Line-Dub) zieht sie herunter. Line-Dub ist seit 2026-08-21 erlaubt
// (Score -50 statt -35000), damit ueberhaupt etwas kommt, wenn es nichts
// Besseres gibt — genau solche Faelle sollen in der Nachricht erkennbar sein.
const (
	tierVeryHigh = "sehr hoch"
	tierHigh     = "hoch"
	tierLimited  = "eingeschränkt"
)

type qualityInfo struct {
	Tier      string // sehr hoch | hoch | eingeschränkt
	Detail    string // z.B. "1080p WEB · Deutsch"
	Limited   bool   // true => Hinweis auf spaeteres automatisches Ersetzen
	Available bool   // false => Qualitaetszeile ganz weglassen
}

// classify uebersetzt Radarr-/Sonarr-Rohwerte in eine Stufe plus Kurzdetail.
func classify(qualityName string, customFormats, languages []string, sceneName string) qualityInfo {
	if qualityName == "" {
		return qualityInfo{}
	}
	q := strings.ToLower(qualityName)

	lineDub := false
	for _, cf := range customFormats {
		switch strings.ToLower(cf) {
		case "line dubbed", "line/mic dubbed", "mic dubbed":
			lineDub = true
		}
	}
	// Netz fuer den Fall, dass die Formate fehlen: der Release-Name ueberlebt
	// das Umbenennen in sceneName, und dort steht das LD/MD-Kuerzel noch drin.
	if !lineDub && sceneName != "" {
		lineDub = dubMarker.MatchString(sceneName)
	}

	// Auflösung -> Grundstufe
	tier := tierHigh
	res := ""
	switch {
	case strings.Contains(q, "2160") || strings.Contains(q, "4k"):
		tier, res = tierVeryHigh, "2160p"
	case strings.Contains(q, "1080"):
		tier, res = tierHigh, "1080p"
	case strings.Contains(q, "720"):
		tier, res = tierLimited, "720p"
	default:
		tier, res = tierLimited, qualityName
	}
	// Inoffizielle Tonspur wiegt schwerer als die Auflösung: das Bild mag 1080p
	// sein, gehört wird eine Laiensynchro.
	if lineDub {
		tier = tierLimited
	}

	// Quelle knapp benennen (Bluray/WEB), Jargon wie "WEBDL"/"WEBRip" vermeiden.
	src := ""
	switch {
	case strings.Contains(q, "bluray") || strings.Contains(q, "remux"):
		src = "Bluray"
	case strings.Contains(q, "web"):
		src = "WEB"
	case strings.Contains(q, "hdtv"):
		src = "TV"
	}

	parts := []string{}
	if res != "" {
		parts = append(parts, res)
	}
	if src != "" {
		parts = append(parts, src)
	}
	if hasLanguage(languages, "german") {
		if lineDub {
			parts = append(parts, "deutsche Tonspur inoffiziell")
		} else {
			parts = append(parts, "Deutsch")
		}
	} else if len(languages) > 0 {
		parts = append(parts, languages[0])
	}

	return qualityInfo{
		Tier:      tier,
		Detail:    strings.Join(parts, " · "),
		Limited:   tier == tierLimited,
		Available: true,
	}
}

func hasLanguage(langs []string, want string) bool {
	for _, l := range langs {
		if strings.EqualFold(l, want) {
			return true
		}
	}
	return false
}

// lookupQuality holt die Qualität der tatsächlich importierten Datei. Fehler
// sind bewusst nicht fatal: die Nachricht geht dann ohne Qualitätszeile raus,
// statt gar nicht.
func (b *Bot) lookupQuality(ctx context.Context, mediaType string, tmdbID, season, episode int) qualityInfo {
	if tmdbID == 0 {
		return qualityInfo{}
	}
	if mediaType == "tv" {
		if b.Sonarr == nil {
			return qualityInfo{}
		}
		q, err := b.Sonarr.QualityByTMDB(ctx, tmdbID, season, episode)
		if err != nil || q == nil {
			return qualityInfo{}
		}
		return classify(q.QualityName, q.CustomFormats, q.Languages, q.SceneName)
	}
	if b.Radarr == nil {
		return qualityInfo{}
	}
	q, err := b.Radarr.QualityByTMDB(ctx, tmdbID)
	if err != nil || q == nil {
		return qualityInfo{}
	}
	return classify(q.QualityName, q.CustomFormats, q.Languages, q.SceneName)
}
