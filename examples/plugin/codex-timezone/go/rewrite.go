package main

import (
	"encoding/xml"
	"io"
	"regexp"
	"strconv"
	"strings"
	"time"
	_ "time/tzdata"

	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

var environmentRoot = regexp.MustCompile(`(?s)^\s*<environment_context>.*</environment_context>\s*$`)
var timezoneField = regexp.MustCompile(`<timezone>([ \t\r\n]*)([^<>]*?)([ \t\r\n]*)</timezone>`)

func validTimezone(zone string) bool {
	if zone == "" || zone == "Local" || strings.Contains(zone, "..") || strings.HasPrefix(zone, "/") {
		return false
	}
	_, err := time.LoadLocation(zone)
	return err == nil
}

// Only an entire environment block is eligible; ordinary prompts are untouched.
func rewriteEnvironment(text, zone string) string {
	if !environmentRoot.MatchString(text) || strings.Count(text, "<environment_context>") != 1 || strings.Count(text, "<timezone>") != 1 {
		return text
	}
	// Require a direct child timezone, not a quoted/nested field in another value.
	decoder := xml.NewDecoder(strings.NewReader(text))
	depth, fields := 0, 0
	for {
		token, err := decoder.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			return text
		}
		switch value := token.(type) {
		case xml.StartElement:
			depth++
			if value.Name.Local == "timezone" {
				if depth != 2 || value.Name.Space != "" {
					return text
				}
				fields++
			}
		case xml.EndElement:
			depth--
		}
	}
	if fields != 1 {
		return text
	}
	match := timezoneField.FindStringSubmatchIndex(text)
	if len(match) != 8 || strings.TrimSpace(text[match[4]:match[5]]) == "" {
		return text
	}
	return text[:match[4]] + zone + text[match[5]:]
}

func rewriteBody(body []byte, zone string) []byte {
	// Settings and geolocation responses validate zones before publishing them.
	// Do not call LoadLocation here: it may read system zone files on every request.
	if zone == "" || !gjson.ValidBytes(body) {
		return body
	}
	patch := func(path string, value gjson.Result) {
		if value.Type != gjson.String {
			return
		}
		next := rewriteEnvironment(value.String(), zone)
		if next != value.String() {
			if changed, err := sjson.SetBytes(body, path, next); err == nil {
				body = changed
			}
		}
	}
	// The after-auth hook runs before translation: cover both Responses and Chat.
	for _, field := range []string{"input", "messages"} {
		for i, item := range gjson.GetBytes(body, field).Array() {
			if item.Get("role").String() != "user" {
				continue
			}
			path := field + "." + strconv.Itoa(i) + ".content"
			content := item.Get("content")
			if content.Type == gjson.String {
				patch(path, content)
			} else if content.IsArray() {
				for j, part := range content.Array() {
					kind := part.Get("type").String()
					if kind == "input_text" || (field == "messages" && kind == "text") {
						patch(path+"."+strconv.Itoa(j)+".text", part.Get("text"))
					}
				}
			}
		}
	}
	for i, tool := range gjson.GetBytes(body, "tools").Array() {
		kind := tool.Get("type").String()
		if (kind == "web_search" || strings.HasPrefix(kind, "web_search_")) && tool.Get("user_location.timezone").Type == gjson.String {
			if changed, err := sjson.SetBytes(body, "tools."+strconv.Itoa(i)+".user_location.timezone", zone); err == nil {
				body = changed
			}
		}
	}
	return body
}
