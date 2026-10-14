package config

import (
	"strings"
	"testing"
)

func TestValidateJobTriggerTable_T8(t *testing.T) {
	refusals := []struct {
		name    string
		job     JobConfig
		wantErr string
	}{
		{
			name: "unreachable cron date",
			job: JobConfig{
				Name:     "unreachable",
				Schedule: "0 0 30 2 *",
			},
			wantErr: "config: job unreachable: schedule \"0 0 30 2 *\" unreachable",
		},
		{
			name: "4-field cron",
			job: JobConfig{
				Name:     "four-fields",
				Schedule: "0 2 * *",
			},
			wantErr: "config: job four-fields: schedule \"0 2 * *\" is invalid",
		},
		{
			name: "6-field cron",
			job: JobConfig{
				Name:     "six-fields",
				Schedule: "0 0 2 * * *",
			},
			wantErr: "config: job six-fields: schedule \"0 0 2 * * *\" is invalid",
		},
		{
			name: "@daily macro",
			job: JobConfig{
				Name:     "macro-daily",
				Schedule: "@daily",
			},
			wantErr: "config: job macro-daily: schedule \"@daily\" is invalid",
		},
		{
			name: "day name in cron",
			job: JobConfig{
				Name:     "day-name",
				Schedule: "0 2 * * MON",
			},
			wantErr: "config: job day-name: schedule \"0 2 * * MON\" is invalid",
		},
		{
			name: "hourly bare word",
			job: JobConfig{
				Name:     "bare-word",
				Schedule: "hourly",
			},
			wantErr: "config: job bare-word: schedule \"hourly\" is invalid",
		},
		{
			name: "on_event without schedule event",
			job: JobConfig{
				Name:     "bad-on-event",
				Schedule: "always",
				OnEvent: &JobEventTrigger{
					Type: "unit.recorded",
				},
			},
			wantErr: "config: job bad-on-event: on_event is forbidden unless schedule is \"event\"",
		},
		{
			name: "schedule event without on_event",
			job: JobConfig{
				Name:     "missing-on-event",
				Schedule: "event",
			},
			wantErr: "config: job missing-on-event: schedule \"event\" requires on_event",
		},
		{
			name: "job.exited without task_id",
			job: JobConfig{
				Name:     "missing-task-id",
				Schedule: "event",
				OnEvent: &JobEventTrigger{
					Type: "job.exited",
				},
			},
			wantErr: "config: job missing-task-id: on_event.task_id is required for job.* events",
		},
		{
			name: "trigger on own name",
			job: JobConfig{
				Name:     "self-trigger",
				Schedule: "event",
				OnEvent: &JobEventTrigger{
					Type:   "job.exited",
					TaskID: "self-trigger",
				},
			},
			wantErr: "config: job self-trigger: on_event.task_id cannot match the job's own name \"self-trigger\"",
		},
		{
			name: "status with non-unit type",
			job: JobConfig{
				Name:     "status-non-unit",
				Schedule: "event",
				OnEvent: &JobEventTrigger{
					Type:   "job.exited",
					TaskID: "other-job",
					Status: "success",
				},
			},
			wantErr: "config: job status-non-unit: on_event.status is only supported for type \"unit.recorded\"",
		},
		{
			name: "cron job with restart on-failure",
			job: JobConfig{
				Name:     "cron-restart",
				Schedule: "0 2 * * *",
				Restart:  JobRestartConfig{Policy: "on-failure"},
			},
			wantErr: "config: job cron-restart: restart.policy \"on-failure\" forbidden on triggered jobs; only \"never\" is supported",
		},
		{
			name: "event job with restart always",
			job: JobConfig{
				Name:     "event-restart",
				Schedule: "event",
				OnEvent: &JobEventTrigger{
					Type: "unit.recorded",
				},
				Restart: JobRestartConfig{Policy: "always"},
			},
			wantErr: "config: job event-restart: restart.policy \"always\" forbidden on triggered jobs; only \"never\" is supported",
		},
		{
			name: "unknown domain event type",
			job: JobConfig{
				Name:     "unknown-domain",
				Schedule: "event",
				OnEvent: &JobEventTrigger{
					Type: "unknown.recorded",
				},
			},
			wantErr: "config: job unknown-domain: on_event.type \"unknown.recorded\" is invalid",
		},
		{
			name: "empty on_event type",
			job: JobConfig{
				Name:     "empty-type",
				Schedule: "event",
				OnEvent:  &JobEventTrigger{},
			},
			wantErr: "config: job empty-type: on_event.type is required",
		},
		{
			name: "cron with question mark",
			job: JobConfig{
				Name:     "question-mark",
				Schedule: "0 0 ? * *",
			},
			wantErr: "config: job question-mark: schedule \"0 0 ? * *\" is invalid",
		},
	}

	for _, tt := range refusals {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateJobTrigger(tt.job)
			if err == nil {
				t.Fatalf("ValidateJobTrigger(%+v) = nil, want error containing %q", tt.job, tt.wantErr)
			}
			if !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("ValidateJobTrigger(%+v) = %v, want error containing %q", tt.job, err, tt.wantErr)
			}
			if !strings.Contains(err.Error(), tt.job.Name) {
				t.Fatalf("ValidateJobTrigger error %q does not name job %q", err.Error(), tt.job.Name)
			}
		})
	}
}

func TestValidateJobTriggerAccepted_T8(t *testing.T) {
	accepted := []struct {
		name string
		job  JobConfig
	}{
		{
			name: "schedule always",
			job: JobConfig{
				Name:     "job-always",
				Schedule: "always",
			},
		},
		{
			name: "schedule once",
			job: JobConfig{
				Name:     "job-once",
				Schedule: "once",
			},
		},
		{
			name: "schedule event with valid unit trigger",
			job: JobConfig{
				Name:     "job-event-unit",
				Schedule: "event",
				OnEvent: &JobEventTrigger{
					Type:   "unit.recorded",
					Status: "active",
				},
			},
		},
		{
			name: "schedule event with valid job exited trigger",
			job: JobConfig{
				Name:     "job-event-exit",
				Schedule: "event",
				OnEvent: &JobEventTrigger{
					Type:   "job.exited",
					TaskID: "producer-job",
				},
			},
		},
		{
			name: "cron interval 15 minutes",
			job: JobConfig{
				Name:     "job-cron-step",
				Schedule: "*/15 * * * *",
			},
		},
		{
			name: "cron leap year leap day",
			job: JobConfig{
				Name:     "job-leap-day",
				Schedule: "0 0 29 2 *",
			},
		},
		{
			name: "default empty schedule",
			job: JobConfig{
				Name: "job-default-schedule",
			},
		},
	}

	for _, tt := range accepted {
		t.Run(tt.name, func(t *testing.T) {
			if err := ValidateJobTrigger(tt.job); err != nil {
				t.Fatalf("ValidateJobTrigger(%+v) = %v, want nil", tt.job, err)
			}
		})
	}
}

func TestValidateJobTriggerBoundary_HISS15(t *testing.T) {
	t.Run("empty job name", func(t *testing.T) {
		err := ValidateJobTrigger(JobConfig{})
		if err == nil || !strings.Contains(err.Error(), "job name is required") {
			t.Fatalf("ValidateJobTrigger(empty name) = %v, want name required", err)
		}
	})

	t.Run("malformed record type single token", func(t *testing.T) {
		err := ValidateJobTrigger(JobConfig{
			Name:     "bad-token",
			Schedule: "event",
			OnEvent:  &JobEventTrigger{Type: "unit"},
		})
		if err == nil || !strings.Contains(err.Error(), "is invalid") {
			t.Fatalf("ValidateJobTrigger(single token) = %v, want invalid type", err)
		}
	})

	t.Run("boundary cron allowed characters", func(t *testing.T) {
		validExprs := []string{
			"0,15,30,45 * * * *",
			"1-5 * * * *",
			"*/5 * * * *",
			"0 0 1 1 *",
		}
		for _, expr := range validExprs {
			job := JobConfig{Name: "valid-boundary", Schedule: expr}
			if err := ValidateJobTrigger(job); err != nil {
				t.Fatalf("ValidateJobTrigger(%q) = %v, want nil", expr, err)
			}
		}
	})
}
