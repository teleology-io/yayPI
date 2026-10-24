package openapi

import (
	"github.com/teleology-io/yayPI/internal/config"
	"github.com/teleology-io/yayPI/internal/schema"
)

// addAuthPaths documents the built-in auth routes (`kind: auth`) exactly as
// auth.Handler.Mount registers them: the same base path and the same enable
// switches. Paths are relative to project.base_url, like entity paths.
func addAuthPaths(s *Spec, reg *schema.Registry, ae *config.AuthEndpointDef) {
	base := ae.BasePath
	if base == "" {
		base = "/auth"
	}
	const tag = "Auth"
	bearer := []SecurityRequirement{{"bearerAuth": []string{}}}
	errRef := map[string]MediaType{"application/json": {Schema: &Schema{Ref: "#/components/schemas/Error"}}}
	json := func(sc *Schema) map[string]MediaType { return map[string]MediaType{"application/json": {Schema: sc}} }
	errs := func(codes ...string) map[string]Response {
		desc := map[string]string{
			"400": "Bad Request", "401": "Unauthorized", "403": "Forbidden", "409": "Conflict (account exists)",
			"429": "Too Many Requests (login lockout; see Retry-After)",
		}
		out := map[string]Response{}
		for _, c := range codes {
			out[c] = Response{Description: desc[c], Content: errRef}
		}
		return out
	}
	with := func(rs map[string]Response, code string, r Response) map[string]Response {
		rs[code] = r
		return rs
	}
	body := func(props map[string]*Schema, required ...string) *RequestBody {
		return &RequestBody{Required: true, Content: json(&Schema{Type: "object", Properties: props, Required: required})}
	}
	str := func(desc string) *Schema { return &Schema{Type: "string", Description: desc} }
	status := json(&Schema{Type: "object", Properties: map[string]*Schema{"status": {Type: "string"}}, Required: []string{"status"}})
	add := func(method, path string, op *Operation) {
		op.Tags = []string{tag}
		p := base + path
		if s.Paths[p] == nil {
			s.Paths[p] = &PathItem{}
		}
		switch method {
		case "get":
			s.Paths[p].Get = op
		case "post":
			s.Paths[p].Post = op
		}
	}

	refreshInBody := ae.Refresh != nil && ae.Refresh.Store == "body"
	refreshOn := ae.Refresh != nil && ae.Refresh.Enabled

	userSchema := &Schema{Ref: "#/components/schemas/" + schema.BuiltinUserEntityName}
	session := &Schema{Type: "object", Required: []string{"token", "expires_in", "user"}, Properties: map[string]*Schema{
		"token":      str("Access token (JWT); send as Authorization: Bearer"),
		"expires_in": {Type: "integer", Description: "Access token lifetime in seconds"},
		"user":       userSchema,
	}}
	tokens := &Schema{Type: "object", Required: []string{"token", "expires_in"}, Properties: map[string]*Schema{
		"token":      str("Access token (JWT)"),
		"expires_in": {Type: "integer", Description: "Access token lifetime in seconds"},
	}}
	if refreshOn && refreshInBody {
		session.Properties["refresh_token"] = str("Refresh token (refresh.store: body)")
		tokens.Properties["refresh_token"] = str("Rotated refresh token")
	}
	s.Components.Schemas["AuthSession"] = session

	credentials := func(credField, passField string) map[string]*Schema {
		return map[string]*Schema{credField: {Type: "string"}, passField: {Type: "string", Format: "password"}}
	}

	if r := ae.Register; r != nil && r.Enabled {
		cred, pass := or(r.CredentialField, "email"), or(r.PasswordField, "password")
		props := credentials(cred, pass)
		if user, ok := reg.GetEntity(schema.BuiltinUserEntityName); ok {
			hash := or(r.HashField, "password_hash")
			for _, f := range user.Fields {
				if schema.IsBuiltinUserField(f.ColumnName) || f.ColumnName == hash || f.PrimaryKey || f.OmitResponse || len(f.WriteRoles) > 0 {
					continue
				}
				props[f.Name] = fieldSchemaFor(&f)
			}
		}
		add("post", "/register", &Operation{
			OperationID: "authRegister", Summary: "Create an account and sign in",
			Description: "The role is always the configured default_role.",
			RequestBody: body(props, cred, pass),
			Responses:   with(errs("400", "409", "429"), "201", Response{Description: "Created", Content: json(&Schema{Ref: "#/components/schemas/AuthSession"})}),
		})
	}
	if l := ae.Login; l != nil && l.Enabled {
		cred, pass := or(l.CredentialField, "email"), or(l.PasswordField, "password")
		codes := []string{"400", "401", "429"}
		if ae.EmailVerification != nil && ae.EmailVerification.Required {
			codes = append(codes, "403")
		}
		add("post", "/login", &Operation{
			OperationID: "authLogin", Summary: "Sign in",
			RequestBody: body(credentials(cred, pass), cred, pass),
			Responses:   with(errs(codes...), "200", Response{Description: "OK", Content: json(&Schema{Ref: "#/components/schemas/AuthSession"})}),
		})
	}
	if ae.Me != nil && ae.Me.Enabled {
		add("get", "/me", &Operation{
			OperationID: "authMe", Summary: "Current user", Security: bearer,
			Responses: with(errs("401"), "200", Response{Description: "OK", Content: json(&Schema{
				Type: "object", Required: []string{"user"}, Properties: map[string]*Schema{"user": userSchema}})}),
		})
	}
	if refreshOn {
		var rb *RequestBody
		desc := "The refresh token is read from its HttpOnly cookie (refresh.store: cookie)."
		if refreshInBody {
			rb = body(map[string]*Schema{"refresh_token": {Type: "string"}}, "refresh_token")
			desc = "Send the refresh token in the body (refresh.store: body)."
		}
		add("post", "/refresh", &Operation{
			OperationID: "authRefresh", Summary: "New access token (rotates the refresh token)", Description: desc,
			RequestBody: rb,
			Responses:   with(errs("401"), "200", Response{Description: "OK", Content: json(tokens)}),
		})
		add("post", "/logout", &Operation{
			OperationID: "authLogout", Summary: "End this session", Description: desc,
			RequestBody: rb,
			Responses:   map[string]Response{"204": {Description: "No Content (always)"}},
		})
		add("post", "/logout-all", &Operation{
			OperationID: "authLogoutAll", Summary: "End every session for the user", Security: bearer,
			Responses: with(errs("401"), "204", Response{Description: "No Content"}),
		})
	}
	if ae.PasswordReset != nil && ae.PasswordReset.Enabled {
		add("post", "/password/forgot", &Operation{
			OperationID: "authPasswordForgot", Summary: "Email a password reset link",
			RequestBody: body(map[string]*Schema{"email": {Type: "string"}}, "email"),
			Responses:   map[string]Response{"202": {Description: "Accepted (always, whether or not the account exists)", Content: status}},
		})
		add("post", "/password/reset", &Operation{
			OperationID: "authPasswordReset", Summary: "Set a new password with the emailed token",
			RequestBody: body(map[string]*Schema{"token": {Type: "string"}, "password": {Type: "string", Format: "password"}}, "token", "password"),
			Responses:   with(errs("400"), "200", Response{Description: "OK", Content: status}),
		})
	}
	if ae.EmailVerification != nil && ae.EmailVerification.Enabled {
		add("post", "/verify-email", &Operation{
			OperationID: "authVerifyEmail", Summary: "Confirm the email with the emailed token",
			RequestBody: body(map[string]*Schema{"token": {Type: "string"}}, "token"),
			Responses:   with(errs("400"), "200", Response{Description: "OK", Content: status}),
		})
		add("post", "/verify-email/resend", &Operation{
			OperationID: "authVerifyEmailResend", Summary: "Resend the verification email", Security: bearer,
			Responses: with(with(errs("401", "429"), "200", Response{Description: "Already verified", Content: status}),
				"202", Response{Description: "Accepted", Content: status}),
		})
	}
	if ae.OAuth2 != nil {
		for _, p := range ae.OAuth2.Providers {
			redirect := map[string]Response{"302": {Description: "Redirect"}}
			add("get", "/"+p.Name, &Operation{OperationID: "authOAuth_" + p.Name, Summary: "Start " + p.Name + " sign-in (browser redirect)", Responses: redirect})
			add("get", "/callback/"+p.Name, &Operation{OperationID: "authOAuthCallback_" + p.Name, Summary: p.Name + " sign-in callback (browser redirect)", Responses: redirect})
		}
	}
}

func or(s, def string) string {
	if s == "" {
		return def
	}
	return s
}
