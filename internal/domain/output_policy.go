package domain

import (
	"errors"
	"io"
	"net/url"
	"regexp"
	"strings"

	"golang.org/x/net/html"
)

// OutputPolicy restricts presentation only; zero values preserve existing clients.
type OutputPolicy struct {
	DisallowCustomEmoji bool `json:"disallow_custom_emoji,omitempty"`
}

var emojiMarkdownLink = regexp.MustCompile(`!?\[[^\]\n]*\]\(\s*<?([^\s)>]+)`)

// InvalidText recognizes executable custom-emoji markup, not ordinary emoji or prose.
func (p OutputPolicy) InvalidText(text string) bool {
	if !p.DisallowCustomEmoji {
		return false
	}
	tokens := html.NewTokenizer(strings.NewReader(text))
	for {
		kind := tokens.Next()
		if kind == html.ErrorToken {
			if errors.Is(tokens.Err(), io.EOF) {
				break
			}
			return true
		}
		if kind != html.StartTagToken && kind != html.SelfClosingTagToken {
			continue
		}
		token := tokens.Token()
		if customEmojiTag(token) {
			return true
		}
	}
	for _, match := range emojiMarkdownLink.FindAllStringSubmatch(text, -1) {
		if customEmojiURL(html.UnescapeString(match[1])) {
			return true
		}
	}
	return false
}

func customEmojiURL(raw string) bool {
	link, err := url.Parse(strings.TrimSpace(raw))
	return err == nil && strings.EqualFold(link.Scheme, "tg") && strings.EqualFold(link.Host, "emoji")
}

func customEmojiTag(token html.Token) bool {
	if strings.EqualFold(token.Data, "tg-emoji") {
		return true
	}
	if !strings.EqualFold(token.Data, "a") {
		return false
	}
	for _, attr := range token.Attr {
		if strings.EqualFold(attr.Key, "href") && customEmojiURL(attr.Val) {
			return true
		}
	}
	return false
}
