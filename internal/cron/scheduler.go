package cron

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/go-co-op/gocron/v2"
	"github.com/rs/zerolog/log"

	"github.com/csullivan/yaypi/internal/config"
	"github.com/csullivan/yaypi/internal/db"
)

// Scheduler manages background cron jobs.
type Scheduler struct {
	s    gocron.Scheduler
	jobs []config.JobDef
	db   *db.Manager
}

// New creates a Scheduler from a list of job definitions.
func New(jobs []config.JobDef, dbManager *db.Manager) (*Scheduler, error) {
	s, err := gocron.NewScheduler()
	if err != nil {
		return nil, fmt.Errorf("creating scheduler: %w", err)
	}

	sched := &Scheduler{
		s:    s,
		jobs: jobs,
		db:   dbManager,
	}

	for _, job := range jobs {
		if err := sched.registerJob(job, dbManager); err != nil {
			return nil, fmt.Errorf("registering job %q: %w", job.Name, err)
		}
	}

	return sched, nil
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
	var taskFn func(ctx context.Context) error

	switch strings.ToLower(job.Handler) {
	case "sql":
		taskFn = sqlHandler(job, dbManager)
	case "http":
		taskFn = httpHandler(job)
	default:
		return fmt.Errorf("unknown job handler %q (supported: sql, http)", job.Handler)
	}

	jobDef, err := s.buildJobDefinition(job)
	if err != nil {
		return err
	}

	name := job.Name
	_, err = s.s.NewJob(
		jobDef,
		gocron.NewTask(func() {
			ctx := context.Background()
			if err := taskFn(ctx); err != nil {
				log.Error().Str("job", name).Err(err).Msg("cron job failed")
			} else {
				log.Info().Str("job", name).Msg("cron job completed")
			}
		}),
		gocron.WithName(job.Name),
	)
	return err
}

// buildJobDefinition converts a job schedule string to a gocron.JobDefinition.
func (s *Scheduler) buildJobDefinition(job config.JobDef) (gocron.JobDefinition, error) {
	schedule := job.Schedule
	if schedule == "" {
		return nil, fmt.Errorf("schedule is required")
	}

	// Named shortcuts
	switch schedule {
	case "@yearly", "@annually":
		return gocron.CronJob("0 0 1 1 *", false), nil
	case "@monthly":
		return gocron.CronJob("0 0 1 * *", false), nil
	case "@weekly":
		return gocron.CronJob("0 0 * * 0", false), nil
	case "@daily", "@midnight":
		return gocron.CronJob("0 0 * * *", false), nil
	case "@hourly":
		return gocron.CronJob("0 * * * *", false), nil
	case "@minutely":
		return gocron.CronJob("* * * * *", false), nil
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

	// Standard 5-field cron or 6-field cron (with seconds)
	fields := strings.Fields(schedule)
	switch len(fields) {
	case 5:
		return gocron.CronJob(schedule, false), nil
	case 6:
		return gocron.CronJob(schedule, true), nil // withSeconds=true
	default:
		return nil, fmt.Errorf("invalid cron expression %q (expected 5 or 6 fields)", schedule)
	}
}
