package cron

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/go-co-op/gocron/v2"
	"github.com/rs/zerolog/log"

	"github.com/teleology-io/yayPI/internal/config"
	"github.com/teleology-io/yayPI/internal/db"
	"github.com/teleology-io/yayPI/pkg/sdk"
)

// Scheduler manages background cron jobs.
type Scheduler struct {
	s       gocron.Scheduler
	jobs    []config.JobDef
	db      *db.Manager
	plugins Plugins
	locker  gocron.Locker // nil = run on every instance
	// OnRun, when set, is called after every run (metrics).
	OnRun func(job string, d time.Duration, err error)
}

// Plugins resolves `handler: plugin` jobs and on_failure handlers, and supplies what
// their JobContext carries.
type Plugins struct {
	Lookup func(name string) (sdk.JobFunc, bool) // nil = no plugin jobs registered
	Store  sdk.Store
	Logger func(name string) sdk.Logger
	HTTP   func(sdk.HTTPOptions) *http.Client
}

func (p Plugins) job(name string) (sdk.JobFunc, error) {
	if p.Lookup != nil {
		if fn, ok := p.Lookup(name); ok {
			return fn, nil
		}
	}
	return nil, fmt.Errorf("no plugin job %q is registered (Server.RegisterJobs)", name)
}

func (p Plugins) context(ctx context.Context, job config.JobDef, attempt int) sdk.JobContext {
	jc := sdk.JobContext{Ctx: ctx, Job: job.Name, Config: job.Config, Attempt: attempt, Store: p.Store, HTTP: p.HTTP}
	if p.Logger != nil {
		jc.Logger = p.Logger(job.Name)
	}
	return jc
}

// New creates a Scheduler from a list of job definitions. With a database and distributed
// set, runs are coordinated through a lock table so each tick executes on one replica only.
func New(jobs []config.JobDef, dbManager *db.Manager, distributed bool, plugins Plugins) (*Scheduler, error) {
	s, err := gocron.NewScheduler()
	if err != nil {
		return nil, fmt.Errorf("creating scheduler: %w", err)
	}

	sched := &Scheduler{
		s:       s,
		jobs:    jobs,
		db:      dbManager,
		plugins: plugins,
	}
	if dbManager != nil && distributed {
		sched.locker = newDBLocker(dbManager.Default())
	}

	for _, job := range jobs {
		if err := sched.registerJob(job, dbManager); err != nil {
			return nil, fmt.Errorf("registering job %q: %w", job.Name, err)
		}
	}

	return sched, nil
}

// jobTimeout is the job's timeout: block (default 5m).
func jobTimeout(job config.JobDef) time.Duration {
	if d, err := time.ParseDuration(job.Timeout); job.Timeout != "" && err == nil && d > 0 {
		return d
	}
	return 5 * time.Minute
}

// runWithRetry runs fn under the job's timeout, retrying per its retry: block (an
// sdk.Permanent error stops early). It returns the attempts made and the last error.
func runWithRetry(job config.JobDef, fn func(ctx context.Context, attempt int) error) (int, error) {
	timeout := jobTimeout(job)
	attempts, delay, maxDelay, exponential := 1, time.Second, time.Minute, true
	if r := job.Retry; r != nil {
		if r.MaxAttempts > 1 {
			attempts = r.MaxAttempts
		}
		if d, err := time.ParseDuration(r.InitialDelay); r.InitialDelay != "" && err == nil {
			delay = d
		}
		if d, err := time.ParseDuration(r.MaxDelay); r.MaxDelay != "" && err == nil {
			maxDelay = d
		}
		exponential = r.Backoff != "fixed"
	}
	var err error
	for i := 0; i < attempts; i++ {
		if i > 0 {
			log.Warn().Str("job", job.Name).Int("attempt", i+1).Err(err).Msg("cron job retrying")
			time.Sleep(delay)
			if exponential {
				delay = min(delay*2, maxDelay)
			}
		}
		ctx, cancel := context.WithTimeout(context.Background(), timeout)
		err = fn(ctx, i+1)
		cancel()
		if err == nil || sdk.IsPermanent(err) {
			return i + 1, err
		}
	}
	return attempts, err
}

func (s *Scheduler) recordRun(name string, start time.Time, err error) {
	d := time.Since(start)
	if err != nil {
		log.Error().Str("job", name).Dur("duration", d).Err(err).Msg("cron job failed")
	} else {
		log.Info().Str("job", name).Dur("duration", d).Msg("cron job completed")
	}
	if s.OnRun != nil {
		s.OnRun(name, d, err)
	}
}

// runOnFailure hands a failed run to the job's on_failure handler (once, under the job
// timeout). Its own failure is only logged.
func (s *Scheduler) runOnFailure(job config.JobDef, fn sdk.JobFunc, attempts int, runErr error) {
	ctx, cancel := context.WithTimeout(context.Background(), jobTimeout(job))
	defer cancel()
	jc := s.plugins.context(ctx, job, 1)
	jc.Failure = &sdk.JobFailure{Job: job.Name, Err: runErr, Attempts: attempts}
	if err := fn(jc); err != nil {
		log.Error().Str("job", job.Name).Str("on_failure", job.OnFailure).Err(err).Msg("cron on_failure handler failed")
	}
}

// Start begins executing scheduled jobs.
func (s *Scheduler) Start() {
	s.s.Start()
}

// Stop gracefully shuts down the scheduler.
func (s *Scheduler) Stop() {
	if err := s.s.Shutdown(); err != nil {
		log.Error().Err(err).Msg("error shutting down cron scheduler")
	}
}

// registerJob registers a single job with the scheduler.
func (s *Scheduler) registerJob(job config.JobDef, dbManager *db.Manager) error {
	var taskFn func(ctx context.Context, attempt int) error

	switch strings.ToLower(job.Handler) {
	case "sql":
		fn := sqlHandler(job, dbManager)
		taskFn = func(ctx context.Context, _ int) error { return fn(ctx) }
	case "http":
		fn, err := httpHandler(job)
		if err != nil {
			return err
		}
		taskFn = func(ctx context.Context, _ int) error { return fn(ctx) }
	case "plugin":
		fn, err := s.plugins.job(job.Plugin)
		if err != nil {
			return err
		}
		taskFn = func(ctx context.Context, attempt int) error { return fn(s.plugins.context(ctx, job, attempt)) }
	default:
		return fmt.Errorf("unknown job handler %q (supported: sql, http, plugin)", job.Handler)
	}
	var onFailure sdk.JobFunc
	if job.OnFailure != "" && job.OnFailure != "log" { // log: the default (failures are always logged)
		fn, err := s.plugins.job(job.OnFailure)
		if err != nil {
			return fmt.Errorf("on_failure: %w", err)
		}
		onFailure = fn
	}

	jobDef, err := s.buildJobDefinition(job)
	if err != nil {
		return err
	}

	name := job.Name
	opts := []gocron.JobOption{
		gocron.WithName(job.Name),
		// Never overlap a run of the same job with itself on this instance.
		gocron.WithSingletonMode(gocron.LimitModeReschedule),
	}
	if s.locker != nil {
		opts = append(opts, gocron.WithDistributedJobLocker(s.locker))
	}
	_, err = s.s.NewJob(
		jobDef,
		gocron.NewTask(func() {
			start := time.Now()
			attempts, err := runWithRetry(job, taskFn)
			s.recordRun(name, start, err)
			if err != nil && onFailure != nil {
				s.runOnFailure(job, onFailure, attempts, err)
			}
		}),
		opts...,
	)
	return err
}

// buildJobDefinition converts a job schedule string to a gocron.JobDefinition.
func (s *Scheduler) buildJobDefinition(job config.JobDef) (gocron.JobDefinition, error) {
	schedule := job.Schedule
	if schedule == "" {
		return nil, fmt.Errorf("schedule is required")
	}

	prefix := ""
	if job.Timezone != "" {
		if _, err := time.LoadLocation(job.Timezone); err != nil {
			return nil, fmt.Errorf("invalid timezone %q: %w", job.Timezone, err)
		}
		prefix = "CRON_TZ=" + job.Timezone + " "
	}

	// Named shortcuts
	switch schedule {
	case "@yearly", "@annually":
		schedule = "0 0 1 1 *"
	case "@monthly":
		schedule = "0 0 1 * *"
	case "@weekly":
		schedule = "0 0 * * 0"
	case "@daily", "@midnight":
		schedule = "0 0 * * *"
	case "@hourly":
		schedule = "0 * * * *"
	case "@minutely":
		schedule = "* * * * *"
	}

	// @every duration
	if strings.HasPrefix(schedule, "@every ") {
		durationStr := strings.TrimPrefix(schedule, "@every ")
		d, err := time.ParseDuration(durationStr)
		if err != nil {
			return nil, fmt.Errorf("invalid @every duration %q: %w", durationStr, err)
		}
		return gocron.DurationJob(d), nil
	}

	// Standard 5-field cron or 6-field cron (with seconds); timezone via CRON_TZ.
	fields := strings.Fields(schedule)
	switch len(fields) {
	case 5:
		return gocron.CronJob(prefix+schedule, false), nil
	case 6:
		return gocron.CronJob(prefix+schedule, true), nil // withSeconds=true
	default:
		return nil, fmt.Errorf("invalid cron expression %q (expected 5 or 6 fields)", schedule)
	}
}
