package router

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/teleology-io/yayPI/internal/config"
	"github.com/teleology-io/yayPI/internal/openapi"
	"github.com/teleology-io/yayPI/internal/plugin"
	"github.com/teleology-io/yayPI/internal/schema"
	"github.com/teleology-io/yayPI/pkg/sdk"
)

const contractEntity = `
name: Event
table: events
fields:
  - {name: id, type: uuid, primary_key: true, default: uuid}
  - {name: title, type: string}
`

const contractEndpoints = `
endpoints:
  - path: /calendar
    method: GET
    entity: Event
    handler: cal.view
    operation_id: calendarView
    params:
      date: {type: date, required: true}
      limit: {type: integer, validate: {min: 1, max: 50}}
      busy: boolean
    response: {list: Event}
  - path: /events/{id}/rsvp
    method: POST
    entity: Event
    handler: cal.rsvp
    body:
      going: {type: boolean, required: true}
      at: timestamptz
      note: {type: string, validate: {max_length: 10}}
    response: {one: {fields: {ok: boolean, count: integer}}}
  - path: /days/{date}
    method: GET
    entity: Event
    handler: cal.day
    response:
      one:
        fields:
          date: date
          events: {list: Event}
          doses: {list: {fields: {at: timestamptz, taken: boolean}}}
          totals: {one: {fields: {count: integer}}, nullable: true}
`

func contractRegistry(t *testing.T) *schema.Registry {
	t.Helper()
	var ent config.EntityDef
	if err := yaml.Unmarshal([]byte(contractEntity), &ent); err != nil {
		t.Fatal(err)
	}
	var eps config.EndpointFileConfig
	if err := yaml.Unmarshal([]byte(contractEndpoints), &eps); err != nil {
		t.Fatal(err)
	}
	reg, err := schema.Build(&config.RootConfig{
		Entities:  []*config.EntityConfig{{Entity: ent}},
		Endpoints: []*config.EndpointFileConfig{&eps},
		Specs:     []config.SpecConfig{{Name: "api", Title: "API", Version: "1"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	return reg
}

func TestCustomRouteInputIsCheckedAndTyped(t *testing.T) {
	var got sdk.RouteContext
	d := plugin.NewDispatcher()
	d.RegisterRoutePlugin(fakeRoutePlugin{name: "cal", handlers: map[string]sdk.RouteHandlerFunc{
		"view": func(c sdk.RouteContext) { got = c; c.Data([]any{}) },
		"day":  func(c sdk.RouteContext) { c.Data(map[string]any{}) },
		"rsvp": func(c sdk.RouteContext) {
			got = c
			if c.Body["going"] == false {
				c.Error(http.StatusUnprocessableEntity, "say yes")
				return
			}
			c.Created(map[string]any{"ok": true, "count": 1})
		},
	}})
	h := Build(contractRegistry(t), nil, Config{Dispatcher: d, BaseURL: "/api"})
	do := func(method, path, body string) (*httptest.ResponseRecorder, map[string]any) {
		var r *http.Request
		if body == "" {
			r = httptest.NewRequest(method, path, nil)
		} else {
			r = httptest.NewRequest(method, path, strings.NewReader(body))
			r.Header.Set("Content-Type", "application/json")
		}
		w := httptest.NewRecorder()
		got = sdk.RouteContext{}
		h.ServeHTTP(w, r)
		var m map[string]any
		_ = json.Unmarshal(w.Body.Bytes(), &m)
		return w, m
	}

	w, _ := do("GET", "/api/calendar?date=2026-03-04&limit=5&busy=true&cachebust=1", "")
	if w.Code != 200 || got.Params["date"] != "2026-03-04" || got.Params["limit"] != int64(5) || got.Params["busy"] != true {
		t.Fatalf("%d %#v", w.Code, got.Params)
	}
	if _, ok := got.Params["cachebust"]; ok {
		t.Error("undeclared query parameter passed through")
	}
	w, m := do("GET", "/api/calendar?limit=99", "")
	errs, _ := m["errors"].(map[string]any)
	if w.Code != 400 || errs["date"] == nil || errs["limit"] == nil || got.Request != nil {
		t.Fatalf("missing/out-of-range params: %d %v (handler ran: %v)", w.Code, m, got.Request != nil)
	}

	w, _ = do("POST", "/api/events/x/rsvp", `{"going": true, "at": "2026-03-04T10:00:00Z"}`)
	if w.Code != 201 || got.Body["going"] != true {
		t.Fatalf("%d %#v", w.Code, got.Body)
	}
	if at, ok := got.Body["at"].(time.Time); !ok || at.Hour() != 10 {
		t.Errorf("at: %#v", got.Body["at"])
	}
	w, m = do("POST", "/api/events/x/rsvp", `{"going": "yes", "extra": 1, "note": "far too long"}`)
	errs, _ = m["errors"].(map[string]any)
	if w.Code != 400 || errs["going"] == nil || errs["extra"] == nil || errs["note"] == nil {
		t.Fatalf("bad body: %d %v", w.Code, m)
	}
	w, m = do("POST", "/api/events/x/rsvp", `{"going": false}`)
	if w.Code != 422 || m["error"] != "say yes" || m["code"] == "" {
		t.Fatalf("c.Error envelope: %d %v", w.Code, m)
	}
}

func TestCustomRoutesAreInTheSpec(t *testing.T) {
	spec := openapi.Build(contractRegistry(t), "p", false)["api"]
	cal := spec.Paths["/calendar"]
	if cal == nil || cal.Get == nil || cal.Get.OperationID != "calendarView" {
		t.Fatalf("calendar: %+v", cal)
	}
	params := map[string]openapi.Parameter{}
	for _, p := range cal.Get.Parameters {
		params[p.Name] = p
	}
	if p := params["date"]; !p.Required || p.In != "query" || p.Schema.Format != "date" {
		t.Errorf("date param: %+v", p)
	}
	if p := params["limit"]; p.Required || p.Schema.Type != "integer" {
		t.Errorf("limit param: %+v", p)
	}
	data := cal.Get.Responses["200"].Content["application/json"].Schema.Properties["data"]
	if data.Type != "array" || data.Items.Ref != "#/components/schemas/Event" {
		t.Errorf("calendar response: %+v", data)
	}

	rsvp := spec.Paths["/events/{id}/rsvp"].Post
	if rsvp.OperationID != "rsvp" || rsvp.Parameters[0].Name != "id" || rsvp.Parameters[0].In != "path" {
		t.Fatalf("rsvp: %+v", rsvp)
	}
	body := rsvp.RequestBody.Content["application/json"].Schema
	if body.Properties["at"].Format != "date-time" || len(body.Required) != 1 || body.Required[0] != "going" {
		t.Errorf("rsvp body: %+v", body)
	}
	one := rsvp.Responses["200"].Content["application/json"].Schema.Properties["data"]
	if one.Type != "object" || one.Properties["count"].Type != "integer" {
		t.Errorf("rsvp response: %+v", one)
	}
}

func TestNestedResponsesAreInTheSpec(t *testing.T) {
	spec := openapi.Build(contractRegistry(t), "p", false)["api"]
	data := spec.Paths["/days/{date}"].Get.Responses["200"].Content["application/json"].Schema.Properties["data"]
	if data.Type != "object" || data.Properties["date"].Format != "date" {
		t.Fatalf("day: %+v", data)
	}
	if ev := data.Properties["events"]; ev.Type != "array" || ev.Items.Ref != "#/components/schemas/Event" {
		t.Errorf("events: %+v", ev)
	}
	if d := data.Properties["doses"]; d.Type != "array" || d.Items.Properties["taken"].Type != "boolean" {
		t.Errorf("doses: %+v", d)
	}
	if tot := data.Properties["totals"]; tot.Type != "object" || !tot.Nullable || tot.Properties["count"].Type != "integer" {
		t.Errorf("totals: %+v", tot)
	}
	if len(data.Required) != 3 {
		t.Errorf("required members: %v", data.Required)
	}
}

func TestContractErrorsAreReported(t *testing.T) {
	bad := map[string]string{
		"crud with params":   "  - {path: /x, entity: Event, crud: [list], params: {a: string}}",
		"get with body":      "  - {path: /x, method: GET, entity: Event, handler: a.b, body: {a: string}}",
		"unknown type":       "  - {path: /x, method: GET, entity: Event, handler: a.b, params: {a: strng}}",
		"unknown entity":     "  - {path: /x, method: GET, entity: Event, handler: a.b, response: {list: Nope}}",
		"error status":       "  - {path: /x, method: GET, entity: Event, handler: a.b, response: {status: 404}}",
		"one and list":       "  - {path: /x, method: GET, entity: Event, handler: a.b, response: {one: Event, list: Event}}",
		"204 with a payload": "  - {path: /x, method: POST, entity: Event, handler: a.b, response: {status: 204, one: Event}}",
		"nested input":       "  - {path: /x, method: POST, entity: Event, handler: a.b, body: {a: {list: Event}}}",
		"nested unknown":     "  - {path: /x, method: GET, entity: Event, handler: a.b, response: {one: {fields: {a: {list: Nope}}}}}",
	}
	var ent config.EntityDef
	_ = yaml.Unmarshal([]byte(contractEntity), &ent)
	for name, ep := range bad {
		var eps config.EndpointFileConfig
		err := yaml.Unmarshal([]byte("endpoints:\n"+ep), &eps)
		if err == nil {
			_, err = schema.Build(&config.RootConfig{Entities: []*config.EntityConfig{{Entity: ent}}, Endpoints: []*config.EndpointFileConfig{&eps}})
		}
		if err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}
