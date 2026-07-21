// Package intents turns raw WhatsApp messages into Command structs the
// handlers can dispatch on.
//
// Two parse paths:
//
//  1. @-mention path. The message starts with the bot's mention token
//     (e.g. "@41791112233"). The verb that follows picks a Command.Kind,
//     and remaining words become the argument.
//
//  2. Numeric-reply path. The message is just "1", "2", or "3". The
//     handler matches it to the sender's most recent Suche result (kept
//     in store with a 60s TTL) and turns it into a Request command.
//
// Aliases are intentional: "suche", "search", "find" all map to KindSuche
// so the group can talk in either language.
package intents

import (
	"strconv"
	"strings"
)

// Kind enumerates the bot's command vocabulary.
type Kind int

const (
	KindNone Kind = iota
	KindHelp
	KindSuche
	KindRequest
	KindStatus
	KindNeu
	KindLibrary
	KindStats
	KindWartet
	KindNumericReply
)

// String renders Kind for log fields.
func (k Kind) String() string {
	switch k {
	case KindHelp:
		return "help"
	case KindSuche:
		return "suche"
	case KindRequest:
		return "request"
	case KindStatus:
		return "status"
	case KindNeu:
		return "neu"
	case KindLibrary:
		return "library"
	case KindStats:
		return "stats"
	case KindWartet:
		return "wartet"
	case KindNumericReply:
		return "numeric_reply"
	}
	return "none"
}

// Command is what the parser produces.
type Command struct {
	Kind Kind
	// Arg is the free-form argument after the verb, trimmed of whitespace.
	// For KindNumericReply, Arg is "1", "2", or "3".
	Arg string
	// Mention is the bot mention token the message led with, if any. Used
	// by handlers that want to echo it back to keep the thread tidy.
	Mention string
}

// Parse turns one message body into a Command. Returns Kind=KindNone when
// the message isn't addressed to the bot.
//
// mentionTokens is the slice of "@<digits>" forms the bot answers to —
// typically @<phone> and @<lid>. mentionedSelf is true when the WAHA
// payload's mentions[] array contained the bot's jid; this catches the
// case where the user picked the bot from WhatsApp's contact chooser
// and the body shows "@Bot" rather than the raw digits.
func Parse(body string, mentionTokens []string, mentionedSelf bool) Command {
	t := strings.TrimSpace(body)
	if t == "" {
		return Command{}
	}

	// Numeric-reply path. Accept "1", "1.", "1)" and similar trivial forms.
	if n := numericReply(t); n != "" {
		return Command{Kind: KindNumericReply, Arg: n}
	}

	// Detect the mention. Two ways:
	//   1. Body literally starts with one of mentionTokens (typed manually
	//      or picked from a chooser that inserted the raw "@<digits>").
	//   2. WAHA's mentions[] said we were mentioned (contact-picker path) —
	//      in that case the body usually still has an "@<something>" prefix
	//      (the contact display name), which we strip.
	matchedToken := ""
	for _, tok := range mentionTokens {
		if tok == "" {
			continue
		}
		if strings.HasPrefix(strings.ToLower(t), strings.ToLower(tok)) {
			matchedToken = tok
			break
		}
	}
	if matchedToken == "" && !mentionedSelf {
		return Command{}
	}

	primaryMention := ""
	if len(mentionTokens) > 0 {
		primaryMention = mentionTokens[0]
	}
	var rest string
	switch {
	case matchedToken != "":
		rest = strings.TrimSpace(t[len(matchedToken):])
	case strings.HasPrefix(t, "@"):
		// Strip the leading "@<token>" — could be "@Bot", "@Notifyarr",
		// "@~Bot", etc. The token ends at the first whitespace.
		_, after := splitVerb(t)
		rest = strings.TrimSpace(after)
	default:
		// Mentioned via side channel without an explicit "@..." prefix —
		// treat the whole body as the command body.
		rest = t
	}

	if rest == "" {
		return Command{Kind: KindHelp, Mention: primaryMention}
	}

	verb, arg := splitVerb(rest)
	cmd := Command{Arg: arg, Mention: primaryMention}
	switch strings.ToLower(verb) {
	case "help", "hilfe", "?", "commands":
		cmd.Kind = KindHelp
	case "suche", "search", "find", "such":
		cmd.Kind = KindSuche
	case "request", "req", "anfragen":
		cmd.Kind = KindRequest
	case "status", "queue", "downloads":
		cmd.Kind = KindStatus
	case "neu", "new", "recent", "kürzlich", "kuerzlich":
		cmd.Kind = KindNeu
	case "library", "lib", "jellyfin":
		cmd.Kind = KindLibrary
	case "stats", "statistik":
		cmd.Kind = KindStats
	case "wartet", "pending", "waiting", "queue-pending":
		cmd.Kind = KindWartet
	default:
		// Unknown verb — treat the whole thing as a search.
		cmd.Kind = KindSuche
		cmd.Arg = rest
	}
	return cmd
}

// splitVerb peels the first word off as the verb, returns the rest.
func splitVerb(s string) (verb, arg string) {
	for i, r := range s {
		if r == ' ' || r == '\t' || r == '\n' {
			return s[:i], strings.TrimSpace(s[i+1:])
		}
	}
	return s, ""
}

// numericReply returns "1"/"2"/"3" when s is a tolerant numeric reply
// ("1", "1.", "(2)", "3)"), or "" otherwise. Capped at 9 — beyond that the
// payload is almost certainly not a reply to a suche.
func numericReply(s string) string {
	t := strings.Trim(s, "().: \t\n")
	if t == "" {
		return ""
	}
	n, err := strconv.Atoi(t)
	if err != nil || n < 1 || n > 9 {
		return ""
	}
	return strconv.Itoa(n)
}
