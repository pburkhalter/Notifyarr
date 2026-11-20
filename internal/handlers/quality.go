package handlers

import (
	"context"
	"regexp"
	"strings"
)

// The same markers Radarr/Sonarr use in the custom formats "Line Dubbed" and
// "Mic Dubbed".
var dubMarker = regexp.MustCompile(`(?i)\b(LD|AC3LD|MD|AC3MD|Line[ .-]?Dubbed|Mic[ .-]?Dubbed)\b`)

// Quality tiers for the WhatsApp message: three on purpose, in everyday
// German instead of release jargon — the recipients are not admins.
//
// The tiers follow the quality profile "[German] HD Bluray + WEB": the
// resolution sets the base tier, an unofficial German audio track (line dub)
// pulls it down. Line dubs are allowed since 2026-08-21 (score -50 instead of
// -35000) so that something arrives when nothing better exists — exactly
// those cases must show in the message. The same holds for an English file
// (fallback since 2026-11-20 for titles without a German release): the
// profile replaces it once a German one appears, and the message says so.
const (
	tierVeryHigh = "sehr hoch"
	tierHigh     = "hoch"
	tierLimited  = "eingeschränkt"
)

type qualityInfo struct {
	Tier      string // sehr hoch | hoch | eingeschränkt
	Detail    string // e.g. "1080p · WEB · Deutsch"
	Limited   bool   // true => note that a better version replaces it later
	NotGerman bool   // true => note that the German version replaces it later
	Available bool   // false => leave the quality line out
}

// classify turns raw Radarr/Sonarr values into a tier plus a short detail.
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
	// Safety net for missing formats: the release name survives the rename in
	// sceneName, and the LD/MD marker is still in it.
	if !lineDub && sceneName != "" {
		lineDub = dubMarker.MatchString(sceneName)
	}

	// resolution -> base tier
	var tier string
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
	// An unofficial audio track weighs more than the resolution: the picture
	// may be 1080p, but what you hear is an amateur dub.
	if lineDub {
		tier = tierLimited
	}

	// Name the source briefly (Bluray/WEB), no jargon like "WEBDL"/"WEBRip".
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
		parts = append(parts, languageName(languages[0]))
	}

	return qualityInfo{
		Tier:      tier,
		Detail:    strings.Join(parts, " · "),
		Limited:   tier == tierLimited,
		NotGerman: len(languages) > 0 && !hasLanguage(languages, "german"),
		Available: true,
	}
}

// languageNames translates the languages the profile lets through (German or
// English; the rest scores -35000) plus the usual originals.
var languageNames = map[string]string{
	"english": "Englisch", "japanese": "Japanisch", "french": "Französisch",
	"spanish": "Spanisch", "italian": "Italienisch", "korean": "Koreanisch",
}

func languageName(l string) string {
	if n, ok := languageNames[strings.ToLower(l)]; ok {
		return n
	}
	return l
}

func hasLanguage(langs []string, want string) bool {
	for _, l := range langs {
		if strings.EqualFold(l, want) {
			return true
		}
	}
	return false
}

// lookupQuality fetches the quality of the file actually imported. Errors are
// not fatal on purpose: the message then goes out without the quality line
// instead of not at all.
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
