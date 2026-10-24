package cron

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/teleology-io/yayPI/internal/config"
	"github.com/teleology-io/yayPI/internal/signature"
	"github.com/teleology-io/yayPI/pkg/sdk"
)

func TestRetryStopsOnPermanentError(t *testing.T) {
	job := config.JobDef{Name: "j", Retry: &config.RetryConfig{MaxAttempts: 5, InitialDelay: "1ms", MaxDelay: "1ms"}}
	calls := 0
	attempts, err := runWithRetry(job, func(_ context.Context, attempt int) error {
		calls++
		if attempt != calls {
			t.Fatalf("attempt %d on call %d", attempt, calls)
		}
		if calls == 2 {
			return sdk.Permanent(errors.New("bad config"))
		}
		return errors.New("flaky")
	})
	if calls != 2 || attempts != 2 || !sdk.IsPermanent(err) {
		t.Fatalf("calls=%d attempts=%d err=%v", calls, attempts, err)
	}
}

func TestPluginJobsResolveAtBoot(t *testing.T) {
	ran := make(chan sdk.JobContext, 1)
	plugins := Plugins{Lookup: func(name string) (sdk.JobFunc, bool) {
		if name != "crm.Sync" {
			return nil, false
		}
		return func(c sdk.JobContext) error { ran <- c; return nil }, true
	}}
	if _, err := New([]config.JobDef{{Name: "sync", Schedule: "@hourly", Handler: "plugin", Plugin: "crm.Missing"}}, nil, false, plugins); err == nil {
		t.Fatal("unregistered plugin job accepted")
	}
	if _, err := New([]config.JobDef{{Name: "sync", Schedule: "@hourly", Handler: "plugin", Plugin: "crm.Sync", OnFailure: "x.Missing"}}, nil, false, plugins); err == nil {
		t.Fatal("unregistered on_failure handler accepted")
	}
	job := config.JobDef{Name: "sync", Schedule: "@hourly", Handler: "plugin", Plugin: "crm.Sync", Config: map[string]any{"since": "1d"}}
	fn, _ := plugins.job(job.Plugin)
	if err := fn(plugins.context(context.Background(), job, 1)); err != nil {
		t.Fatal(err)
	}
	if c := <-ran; c.Job != "sync" || c.Config["since"] != "1d" || c.Attempt != 1 {
		t.Fatalf("job context: %+v", c)
	}
}

func TestOnFailureGetsTheFailure(t *testing.T) {
	var got *sdk.JobFailure
	s := &Scheduler{plugins: Plugins{}}
	s.runOnFailure(config.JobDef{Name: "sync"}, func(c sdk.JobContext) error { got = c.Failure; return nil }, 3, errors.New("boom"))
	if got == nil || got.Job != "sync" || got.Attempts != 3 || got.Err.Error() != "boom" {
		t.Fatalf("failure = %+v", got)
	}
}

func TestHTTPJobSendsHeadersBodyAndSignature(t *testing.T) {
	var gotHeader, gotType, gotBody string
	var verifyErr error
	scheme, _ := signature.Resolve(nil, "s3cret")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		gotHeader, gotType, gotBody = r.Header.Get("X-Source"), r.Header.Get("Content-Type"), string(b)
		verifyErr = scheme.Verify("s3cret", r.Header, b, time.Now())
	}))
	defer srv.Close()

	fn, err := httpHandler(config.JobDef{Name: "ping", Config: map[string]any{
		"url": srv.URL, "method": "POST", "allow_private_network": true, "secret": "s3cret",
		"headers": map[string]any{"X-Source": "yaypi"},
		"body":    map[string]any{"job": "ping"},
	}})
	if err != nil {
		t.Fatal(err)
	}
	if err := fn(context.Background()); err != nil {
		t.Fatal(err)
	}
	if gotHeader != "yaypi" || gotType != "application/json" || !strings.Contains(gotBody, `"job":"ping"`) || verifyErr != nil {
		t.Fatalf("header=%q type=%q body=%q verify=%v", gotHeader, gotType, gotBody, verifyErr)
	}

	for name, cfg := range map[string]map[string]any{
		"host not allowed":     {"url": srv.URL, "allowed_hosts": []any{"example.com"}},
		"signature w/o secret": {"url": srv.URL, "signature": map[string]any{"scheme": "github"}},
		"bad headers":          {"url": srv.URL, "headers": "X: y"},
	} {
		if _, err := httpHandler(config.JobDef{Name: "x", Config: cfg}); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}
