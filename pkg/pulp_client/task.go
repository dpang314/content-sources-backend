package pulp_client

import (
	"errors"
	"fmt"
	"regexp"
	"time"

	zest "github.com/content-services/zest/release/v3"
	"github.com/rs/zerolog"
	"golang.org/x/exp/slices"
)

const (
	COMPLETED string = "completed"
	WAITING   string = "waiting"
	RUNNING   string = "running"
	SKIPPED   string = "skipped"
	CANCELED  string = "canceled"
	FAILED    string = "failed"
)

// GetTask Fetch a pulp task
func (r pulpDaoImpl) GetTask(taskHref string) (zest.TaskResponse, error) {
	task, httpResp, err := r.client.TasksApi.TasksRead(r.ctx, taskHref).Execute()

	if err != nil {
		return zest.TaskResponse{}, err
	}
	defer httpResp.Body.Close()

	return *task, nil
}

// PollTask Poll a task and return the final task object
func (r pulpDaoImpl) PollTask(taskHref string) (*zest.TaskResponse, error) {
	var task zest.TaskResponse
	var err error
	inProgress := true
	pollCount := 1
	logger := zerolog.Ctx(r.ctx)
	for inProgress {
		task, err = r.GetTask(taskHref)
		if err != nil {
			return nil, err
		}
		taskState := *task.State
		switch {
		case slices.Contains([]string{WAITING, RUNNING}, taskState):
			logger.Debug().Str("task_href", *task.PulpHref).Str("type", task.GetName()).Str("state", taskState).Msg("Running pulp task")
		case slices.Contains([]string{COMPLETED, SKIPPED, CANCELED}, taskState):
			logger.Debug().Str("task_href", *task.PulpHref).Str("type", task.GetName()).Str("state", taskState).Msg("Stopped pulp task")
			inProgress = false
		case taskState == FAILED:
			errorStr := TaskErrorString(task)
			logger.Warn().Str("Pulp error:", errorStr).Str("type", task.GetName()).Msg("Failed Pulp task")
			return &task, errors.New(errorStr)
		default:
			logger.Error().Str("task_href", *task.PulpHref).Str("type", task.GetName()).Str("state", taskState).Msg("Pulp task with unexpected state")
			inProgress = false
		}

		if inProgress {
			SleepWithBackoff(pollCount)
			pollCount += 1
		}
	}
	return &task, nil
}

func SleepWithBackoff(iteration int) {
	var secs int
	if iteration <= 5 {
		secs = 1
	} else if iteration > 5 && iteration <= 10 {
		secs = 5
	} else if iteration > 10 && iteration <= 20 {
		secs = 10
	} else {
		secs = 30
	}
	time.Sleep(time.Duration(secs) * time.Second)
}

func TaskErrorString(task zest.TaskResponse) string {
	str := ""
	for key, element := range task.Error {
		str = str + fmt.Sprintf("%v: %v.  ", key, element)
	}
	return str
}

func SelectVersionHref(task *zest.TaskResponse) *string {
	return SelectCreatedVersionHref(task, "/pulp/api/v3/repositories/rpm/rpm/.*/versions/[0-9]*/")
}

func SelectPublicationHref(task *zest.TaskResponse) *string {
	return SelectCreatedVersionHref(task, "/pulp/api/v3/publications/rpm/rpm/.*/")
}

func SelectRpmDistributionHref(task *zest.TaskResponse) *string {
	return SelectCreatedVersionHref(task, "/pulp/api/v3/distributions/rpm/rpm/.*/")
}

// SelectCreatedVersionHref scans a tasks CreatedResources and looks for a match to a regular expression
func SelectCreatedVersionHref(task *zest.TaskResponse, regex string) *string {
	if task != nil {
		for i := 0; i < len(task.CreatedResources); i++ {
			match, err := regexp.MatchString(regex, task.CreatedResources[i])
			if err == nil && match {
				return &task.CreatedResources[i]
			}
		}
	}
	return nil
}
