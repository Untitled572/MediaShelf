package server

import (
	"net/url"
	"strings"
)

// Avatar metadata is never fetched by the server. The browser loads it only
// after the user disables image privacy mode.
func avatarURL(raw string) string {
	raw = strings.TrimSpace(raw)
	u, err := url.Parse(raw)
	if err != nil || u.User != nil || strings.ContainsAny(raw, "\r\n") {
		return ""
	}
	if u.Scheme == "" && u.Host == "" && strings.HasPrefix(u.Path, "/covers/") && !strings.Contains(u.Path, "..") {
		return raw
	}
	if (u.Scheme == "http" || u.Scheme == "https") && u.Host != "" {
		return raw
	}
	return ""
}
func mergeActorThumbs(existing, incoming Object) {
	thumbs := toObject(existing["actor_thumbs"])
	names := map[string]bool{}
	for _, n := range stringSlice(existing["actors"]) {
		names[n] = true
	}
	for _, n := range stringSlice(incoming["actors"]) {
		names[n] = true
	}
	for name, raw := range toObject(incoming["actor_thumbs"]) {
		if image := avatarURL(str(raw)); names[name] && image != "" {
			thumbs[name] = image
		}
	}
	if len(thumbs) > 0 {
		existing["actor_thumbs"] = thumbs
	}
}
func actorAvatars(works []Object, policies []Object) map[string]string {
	policyMap := map[string]Object{}
	for _, p := range policies {
		policyMap[actorKey(str(p["name"]))] = p
	}
	aliases := aliasMap(policyMap)
	avatars := map[string]string{}
	for _, work := range works {
		for name, raw := range toObject(work["actor_thumbs"]) {
			name = canonicalActor(name, aliases)
			if image := avatarURL(str(raw)); image != "" && avatars[name] == "" {
				avatars[name] = image
			}
		}
	}
	for _, p := range policies {
		if image := avatarURL(str(p["avatar_url"])); image != "" {
			avatars[str(p["name"])] = image
		}
	}
	return avatars
}
