package worker

import (
	"context"
	"sync"
	"time"

	"github.com/content-services/content-sources-backend/pkg/config"
	m "github.com/content-services/content-sources-backend/pkg/instrumentation"
	"github.com/content-services/content-sources-backend/pkg/models"
	"github.com/content-services/content-sources-backend/pkg/tasks"
	"github.com/content-services/content-sources-backend/pkg/tasks/queue"
	"github.com/google/uuid"
	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"
)

type worker struct {
	queue       queue.Queue
	workerWg    *sync.WaitGroup // wait for worker loop to exit
	handlers    map[string]TaskHandler
	taskTypes   []string
	metrics     *m.Metrics
	readyChan   chan struct{} // receives value when worker is ready for new task
	stopChan    chan struct{} // receives value when worker should exit gracefully
	runningTask *runningTask  // holds information about the in-progress task
}

type workerConfig struct {
	queue     queue.Queue
	workerWg  *sync.WaitGroup
	handlers  map[string]TaskHandler
	taskTypes []string
}

type runningTask struct {
	id        uuid.UUID
	token     uuid.UUID
	typename  string
	requestID string
}

func (t *runningTask) set(info *models.TaskInfo) {
	t.id = info.Id
	t.token = info.Token
	t.typename = info.Typename
	t.requestID = info.RequestID
}

func (t *runningTask) clear() {
	t.id = uuid.Nil
	t.token = uuid.Nil
	t.typename = ""
	t.requestID = ""
}

func newWorker(config workerConfig, metrics *m.Metrics) worker {
	return worker{
		queue:       config.queue,
		workerWg:    config.workerWg,
		handlers:    config.handlers,
		taskTypes:   config.taskTypes,
		readyChan:   make(chan struct{}, 1),
		stopChan:    make(chan struct{}, 1),
		metrics:     metrics,
		runningTask: &runningTask{},
	}
}

func (w *worker) start(ctx context.Context) {
	log.Logger.Info().Msg("Starting worker")
	defer w.workerWg.Done()
	defer recoverOnPanic(log.Logger)

	w.readyChan <- struct{}{}

	beat := time.NewTimer(config.Get().Tasking.Heartbeat / 3)
	defer beat.Stop()

	for {
		select {
		case <-w.stopChan:
			if w.runningTask.id != uuid.Nil {
				err := w.requeue(w.runningTask.id)
				if err != nil {
					log.Logger.Error().Err(err).Msgf("error requeuing task with task_id: %v", w.runningTask.id)
				}
			}
			return
		case <-w.readyChan:
			taskInfo, err := w.dequeue(ctx)
			if err != nil {
				if err == queue.ErrContextCanceled {
					continue
				}
				continue
			}
			if taskInfo != nil {
				go w.process(ctx, taskInfo)
			}
		case <-beat.C:
			if w.runningTask.token != uuid.Nil {
				err := w.queue.RefreshHeartbeat(w.runningTask.token)
				if err != nil {
					if err == queue.ErrRowsNotAffected {
						log.Logger.Error().Err(nil).Msg("No rows affected when refreshing heartbeat")
						continue
					}
					log.Logger.Error().Err(err).Msg("Error refreshing heartbeat")
				}
			}
			beat.Reset(config.Get().Tasking.Heartbeat / 3)
		}
	}
}

func (w *worker) dequeue(ctx context.Context) (*models.TaskInfo, error) {
	logger := logForTask(w.runningTask)
	defer recoverOnPanic(*logger)

	info, err := w.queue.Dequeue(ctx, w.taskTypes)
	if err != nil {
		if err == queue.ErrContextCanceled {
			return nil, err
		}
		log.Logger.Error().Err(err).Msg("error dequeuing task")
		w.readyChan <- struct{}{}
		return nil, err
	}

	w.metrics.RecordMessageLatency(*info.Queued)
	w.runningTask.set(info)
	logForTask(w.runningTask).Info().Msg("[Dequeued Task]")

	return info, nil
}

func (w *worker) requeue(id uuid.UUID) error {
	logger := logForTask(w.runningTask)
	defer recoverOnPanic(*logger)

	err := w.queue.Requeue(id)
	if err != nil {
		return err
	}
	logger.Info().Msg("[Requeued Task]")
	return nil
}

// process calls the handler for the task specified by taskInfo, finishes the task, then marks worker as ready for new task
func (w *worker) process(ctx context.Context, taskInfo *models.TaskInfo) {
	logger := logForTask(w.runningTask)
	defer recoverOnPanic(*logger)

	if handler, ok := w.handlers[taskInfo.Typename]; ok {
		err := handler(ctx, taskInfo, &w.queue)
		if err != nil {
			w.metrics.RecordMessageResult(false)
		} else {
			w.metrics.RecordMessageResult(true)
		}

		err = w.queue.Finish(taskInfo.Id, err)
		if err != nil {
			logger.Error().Msgf("error finishing task: %v", err)
		}
		logger.Info().Msg("[Finished Task]")
		w.runningTask.clear()
	} else {
		logger.Warn().Msg("handler not found for task type")
	}
	w.readyChan <- struct{}{}
}

func (w *worker) stop() {
	w.stopChan <- struct{}{}
}

// Catches a panic so that only the surrounding function is exited
func recoverOnPanic(logger zerolog.Logger) {
	var err error
	if r := recover(); r != nil {
		err, _ = r.(error)
		logger.Error().Err(err).Stack().Msgf("recovered panic in worker with error: %v", err)
	}
}

func logForTask(task *runningTask) *zerolog.Logger {
	logger := tasks.LogForTask(task.id.String(), task.typename, task.requestID)
	return logger
}
