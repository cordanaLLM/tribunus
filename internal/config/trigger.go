package config

import (
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/adhocore/gronx"
	"github.com/tekmora/epitrop/internal/eventlog"
)

var (
	fixedCronReference  = time.Date(2000, 1, 1, 0, 0, 0, 0, time.UTC)
	recordActionPattern = regexp.MustCompile(`^[a-z][a-z0-9_]*$`)
)

// JobEventTrigger configures an event trigger for a job with schedule "event".
type JobEventTrigger struct {
	Type   string `koanf:"type" json:"type"`
	TaskID string `koanf:"task_id" json:"task_id,omitempty"`
	Status string `koanf:"status" json:"status,omitempty"`
}

// ValidateJobTrigger validates the trigger configuration of a job, checking that its
// schedule is one of the supported forms ("always", "once", "event", or a 5-field
// cron expression evaluated in UTC), that on_event is present if and only if schedule
// is "event", that its trigger cannot match the job's own records, and that restart
// policies other than "never" are rejected on triggered jobs.
func ValidateJobTrigger(job JobConfig) error {
	if job.Name == "" {
		return errors.New("config: job name is required")
	}
	sched := job.Schedule
	if sched == "" {
		sched = defaultJobSchedule
	}
	if err := validateJobOnEvent(job, sched); err != nil {
		return err
	}
	if err := validateJobRestartPolicy(job, sched); err != nil {
		return err
	}
	return validateJobSchedule(job, sched)
}

func validateJobOnEvent(job JobConfig, sched string) error {
	if sched != "event" {
		if job.OnEvent != nil {
			return fmt.Errorf("config: job %s: on_event is forbidden unless schedule is %q", job.Name, "event")
		}
		return nil
	}
	if job.OnEvent == nil {
		return fmt.Errorf("config: job %s: schedule %q requires on_event", job.Name, "event")
	}
	return validateEventTrigger(job.Name, *job.OnEvent)
}

func validateEventTrigger(jobName string, t JobEventTrigger) error {
	if t.Type == "" {
		return fmt.Errorf("config: job %s: on_event.type is required", jobName)
	}
	if !eventlog.KnownDomain(t.Type) || !validRecordType(t.Type) {
		return fmt.Errorf("config: job %s: on_event.type %q is invalid", jobName, t.Type)
	}
	if t.Status != "" && t.Type != "unit.recorded" {
		return fmt.Errorf("config: job %s: on_event.status is only supported for type %q", jobName, "unit.recorded")
	}
	if strings.HasPrefix(t.Type, "job.") {
		if t.TaskID == "" {
			return fmt.Errorf("config: job %s: on_event.task_id is required for job.* events", jobName)
		}
		if t.TaskID == jobName {
			return fmt.Errorf("config: job %s: on_event.task_id cannot match the job's own name %q", jobName, jobName)
		}
	}
	return nil
}

func validRecordType(t string) bool {
	parts := strings.Split(t, ".")
	if len(parts) != 2 {
		return false
	}
	return parts[0] != "" && recordActionPattern.MatchString(parts[1])
}

func validateJobRestartPolicy(job JobConfig, sched string) error {
	if sched == "always" || sched == "once" {
		return nil
	}
	policy := job.Restart.Policy
	if policy != "" && policy != "never" {
		return fmt.Errorf("config: job %s: restart.policy %q forbidden on triggered jobs; only \"never\" is supported", job.Name, policy)
	}
	return nil
}

func validateJobSchedule(job JobConfig, sched string) error {
	if sched == "always" || sched == "once" || sched == "event" {
		return nil
	}
	return validateCronExpression(job.Name, sched)
}

func validateCronExpression(jobName string, expr string) error {
	fields := strings.Fields(expr)
	if len(fields) != 5 {
		return invalidScheduleError(jobName, expr)
	}
	for i := 0; i < len(fields); i++ {
		for _, c := range fields[i] {
			if !isCronChar(c) {
				return invalidScheduleError(jobName, expr)
			}
		}
	}
	if !gronx.IsValid(expr) {
		return invalidScheduleError(jobName, expr)
	}
	if _, err := gronx.NextTickAfter(expr, fixedCronReference, false); err != nil {
		return fmt.Errorf("config: job %s: schedule %q unreachable: %w", jobName, expr, err)
	}
	return nil
}

func invalidScheduleError(jobName string, expr string) error {
	return fmt.Errorf("config: job %s: schedule %q is invalid: accepted forms are \"always\", \"once\", \"event\", or a 5-field cron expression using only digits and */,- characters", jobName, expr)
}

func isCronChar(c rune) bool {
	return (c >= '0' && c <= '9') || c == '*' || c == '/' || c == ',' || c == '-'
}
