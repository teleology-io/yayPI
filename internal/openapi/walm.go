package openapi

import (
	"sort"

	"github.com/teleology-io/yayPI/internal/query"
)

// WalmProfile is what walm's OpenAPI support needs beyond the spec:
// yayPI's conventions that OpenAPI can't express (response envelopes, the
// error message path, filter and sort syntax) and the base URL.
// `yaypi spec generate --walm` writes it next to the spec.
type WalmProfile struct {
	Profile  string       `yaml:"profile"`
	Base     string       `yaml:"base,omitempty"`
	Envelope walmEnvelope `yaml:"envelope"`
	Errors   walmErrors   `yaml:"errors"`
	Query    walmQuery    `yaml:"query"`
}

type walmEnvelope struct {
	List struct {
		Items string `yaml:"items"`
		Meta  string `yaml:"meta"`
	} `yaml:"list"`
	One struct {
		Item string `yaml:"item"`
	} `yaml:"one"`
}

type walmErrors struct {
	Message string `yaml:"message"`
}

type walmQuery struct {
	Filter struct {
		Ops  []string `yaml:"ops"`
		List string   `yaml:"list"`
	} `yaml:"filter"`
	Sort struct {
		Param      string `yaml:"param"`
		Separator  string `yaml:"separator"`
		DescPrefix string `yaml:"desc_prefix"`
	} `yaml:"sort"`
}

// BuildWalmProfile describes yayPI's wire conventions (see docs/endpoints.md
// "Response format" and the list query syntax).
func BuildWalmProfile(baseURL string) WalmProfile {
	p := WalmProfile{Profile: "yaypi", Base: baseURL}
	p.Envelope.List.Items = "data"
	p.Envelope.List.Meta = "meta"
	p.Envelope.One.Item = "data"
	p.Errors.Message = "error"
	for op := range query.FilterOps {
		p.Query.Filter.Ops = append(p.Query.Filter.Ops, op)
	}
	sort.Strings(p.Query.Filter.Ops)
	p.Query.Filter.List = ","
	p.Query.Sort.Param = "sort"
	p.Query.Sort.Separator = ":"
	p.Query.Sort.DescPrefix = "-"
	return p
}
