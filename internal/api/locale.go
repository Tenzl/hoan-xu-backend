package api

import (
	_ "embed"
	"encoding/json"
	"net/http"
	"strings"
)

//go:embed en.json
var englishJSON []byte
var englishMessages = func() map[string]string {
	v := map[string]string{}
	if e := json.Unmarshal(englishJSON, &v); e != nil {
		panic(e)
	}
	return v
}()

func localMessage(r *http.Request, message string) string {
	language := strings.ToLower(strings.TrimSpace(strings.Split(strings.Split(r.Header.Get("Accept-Language"), ",")[0], ";")[0]))
	if language != "en" && !strings.HasPrefix(language, "en-") {
		return message
	}
	if value, ok := englishMessages[message]; ok {
		return value
	}
	if strings.HasPrefix(message, "Thiếu cột ") {
		return "Missing column " + strings.TrimPrefix(message, "Thiếu cột ")
	}
	return message
}
