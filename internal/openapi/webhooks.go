package openapi

import (
	"net/http"
	"strings"

	"github.com/teleology-io/yayPI/internal/config"
)

// addInboundPaths documents inbound webhook receivers; it reports whether any was added.
func addInboundPaths(s *Spec, defs []config.WebhookDef, authConfigured bool) bool {
	added := false
	for _, w := range defs {
		if w.Spec != nil && !*w.Spec {
			continue
		}
		item := s.Paths[w.Path]
		if item == nil {
			item = &PathItem{}
			s.Paths[w.Path] = item
		}
		authed := w.Auth != nil && (w.Auth.Require || len(w.Auth.Roles) > 0 || len(w.Auth.Conditions) > 0)
		var guards []string
		if scheme := config.EffectiveSignatureScheme(w); scheme != "none" {
			guards = append(guards, "signature ("+scheme+")")
		}
		if w.Token != nil {
			guards = append(guards, "token")
		}
		if len(w.AllowedIPs) > 0 {
			guards = append(guards, "IP allowlist")
		}
		if authed {
			guards = append(guards, "auth")
		}
		post := &Operation{
			OperationID: "webhook_" + w.Name,
			Summary:     "Inbound webhook: " + w.Name,
			Description: "Verified (" + strings.Join(guards, ", ") + "), deduplicated and stored; processed asynchronously with retries. " +
				"Always answers 200 once stored, including for a repeat delivery.",
			Tags: []string{"Webhooks"},
			RequestBody: &RequestBody{Required: true, Content: map[string]MediaType{
				"application/json": {Schema: &Schema{Type: "object", Description: "Provider payload"}},
			}},
			Responses: map[string]Response{
				"200": {Description: "Accepted (or duplicate)", Content: map[string]MediaType{"application/json": {Schema: &Schema{
					Type: "object", Properties: map[string]*Schema{"status": {Type: "string", Enum: []string{"accepted", "duplicate"}}},
				}}}},
			},
		}
		if authed && authConfigured {
			post.Security = []SecurityRequirement{{"bearerAuth": []string{}}}
		}
		addErrorResponses(post, "webhook", true)
		post.Responses["413"] = Response{Description: "Payload Too Large", Content: post.Responses["401"].Content}
		post.Responses["503"] = Response{Description: "Could not store the request; the sender should retry", Content: post.Responses["401"].Content}
		item.Post = post

		if c := w.Challenge; c != nil && (c.Method == "" || strings.EqualFold(c.Method, http.MethodGet)) {
			item.Get = &Operation{
				OperationID: "webhook_" + w.Name + "_challenge",
				Summary:     "Inbound webhook handshake: " + w.Name,
				Description: "Echoes the " + c.QueryParam + " query parameter as text/plain (provider endpoint verification).",
				Tags:        []string{"Webhooks"},
				Parameters:  []Parameter{{Name: c.QueryParam, In: "query", Required: true, Schema: &Schema{Type: "string"}}},
				Responses: map[string]Response{
					"200": {Description: "The challenge value", Content: map[string]MediaType{"text/plain": {Schema: &Schema{Type: "string"}}}},
				},
			}
		}
		added = true
	}
	return added
}
