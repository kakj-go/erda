// Copyright (c) 2021 Terminus, Inc.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//      http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package reconciler

import (
	"context"
	"fmt"
	"runtime/debug"
	"strconv"
	"sync"
	"time"

	"github.com/pkg/errors"
	"github.com/sirupsen/logrus"

	"github.com/erda-project/erda/apistructs"
	"github.com/erda-project/erda/modules/pipeline/commonutil/statusutil"
	"github.com/erda-project/erda/modules/pipeline/events"
	"github.com/erda-project/erda/modules/pipeline/pipengine/actionexecutor"
	"github.com/erda-project/erda/modules/pipeline/pipengine/actionexecutor/types"
	"github.com/erda-project/erda/modules/pipeline/pipengine/reconciler/rlog"
	"github.com/erda-project/erda/modules/pipeline/pipengine/reconciler/taskrun"
	"github.com/erda-project/erda/modules/pipeline/spec"
)

func (r *Reconciler) MergeDBTasks(pipeline *spec.Pipeline) ([]*spec.PipelineTask, error) {
	// get pipeline tasks from db
	tasks, err := r.dbClient.ListPipelineTasksByPipelineID(pipeline.ID)
	if err != nil {
		return nil, err
	}

	// get or set stages from caches
	stages, err := getOrSetStagesFromContext(r.dbClient, pipeline.ID)
	if err != nil {
		return nil, err
	}

	// get or set pipelineYml from caches
	pipelineYml, err := getOrSetPipelineYmlFromContext(r.dbClient, pipeline.ID)
	if err != nil {
		return nil, err
	}

	passedDataWhenCreate, err := getOrSetPassedDataWhenCreateFromContext(r.bdl, pipelineYml, pipeline.ID)
	if err != nil {
		return nil, err
	}

	tasks, err = r.pipelineSvcFunc.MergePipelineYmlTasks(pipelineYml, tasks, pipeline, stages, passedDataWhenCreate)
	if err != nil {
		return nil, err
	}

	var newTasks []*spec.PipelineTask
	for index := range tasks {
		task := tasks[index]
		newTasks = append(newTasks, &task)
	}

	return newTasks, err
}

func (r *Reconciler) saveTask(task *spec.PipelineTask, pipeline *spec.Pipeline) (*spec.PipelineTask, error) {
	if task.ID > 0 {
		return task, nil
	}

	lastSuccessTaskMap, err := getOrSetPipelineRerunSuccessTasksFromContext(r.dbClient, pipeline.ID)
	if err != nil {
		return nil, err
	}

	stages, err := getOrSetStagesFromContext(r.dbClient, pipeline.ID)
	if err != nil {
		return nil, err
	}

	var pt *spec.PipelineTask
	lastSuccessTask, ok := lastSuccessTaskMap[task.Name]
	if ok {
		pt = lastSuccessTask
		pt.ID = 0
		pt.PipelineID = pipeline.ID
		pt.StageID = stages[task.Extra.StageOrder].ID
	} else {
		pt = task
	}

	// save action
	if err := r.dbClient.CreatePipelineTask(pt); err != nil {
		logrus.Errorf("[alert] failed to create pipeline task when create pipeline graph: %v", err)
		return nil, err
	}

	return pt, nil
}

func (r *Reconciler) createSnippetPipeline(task *spec.PipelineTask, p *spec.Pipeline) (snippetPipeline *spec.Pipeline, resultTask *spec.PipelineTask, err error) {
	var failedError error
	defer func() {
		if failedError != nil {
			err = failedError
			task.Result.Errors = append(task.Result.Errors, &apistructs.PipelineTaskErrResponse{
				Msg: err.Error(),
			})
			task.Status = apistructs.PipelineStatusFailed
			if updateErr := r.dbClient.UpdatePipelineTask(task.ID, task); updateErr != nil {
				err = updateErr
				return
			}
			snippetPipeline = nil
			resultTask = nil
		}
	}()
	var taskSnippetConfig = apistructs.SnippetConfig{
		Source: task.Extra.Action.SnippetConfig.Source,
		Name:   task.Extra.Action.SnippetConfig.Name,
		Labels: task.Extra.Action.SnippetConfig.Labels,
	}
	var sourceSnippetConfigs []apistructs.SnippetConfig
	sourceSnippetConfigs = append(sourceSnippetConfigs, taskSnippetConfig)
	sourceSnippetConfigYamls, err := r.pipelineSvcFunc.HandleQueryPipelineYamlBySnippetConfigs(sourceSnippetConfigs)
	if err != nil {
		failedError = err
		return nil, nil, failedError
	}
	if len(sourceSnippetConfigYamls) <= 0 {
		return nil, nil, fmt.Errorf("not find snippet %v yml", taskSnippetConfig.ToString())
	}

	snippetPipeline, err = r.pipelineSvcFunc.MakeSnippetPipeline4Create(p, task, sourceSnippetConfigYamls[taskSnippetConfig.ToString()])
	if err != nil {
		return nil, nil, err
	}
	if err := r.pipelineSvcFunc.CreatePipelineGraph(snippetPipeline); err != nil {
		return nil, nil, err
	}

	task.SnippetPipelineID = &snippetPipeline.ID
	task.Extra.AppliedResources = snippetPipeline.Snapshot.AppliedResources
	if err := r.dbClient.UpdatePipelineTask(task.ID, task); err != nil {
		return nil, nil, err
	}
	return snippetPipeline, task, nil
}

func (r *Reconciler) reconcileSnippetTask(task *spec.PipelineTask, p *spec.Pipeline) (resultTask *spec.PipelineTask, err error) {
	var snippetPipeline *spec.Pipeline
	if task.SnippetPipelineID != nil && *task.SnippetPipelineID > 0 {
		snippetPipelineValue, err := r.dbClient.GetPipeline(task.SnippetPipelineID)
		if err != nil {
			return nil, err
		}
		snippetPipeline = &snippetPipelineValue
	} else {
		snippetPipeline, task, err = r.createSnippetPipeline(task, p)
		if err != nil {
			return nil, err
		}
	}

	if snippetPipeline == nil {
		task.Status = apistructs.PipelineStatusAnalyzeFailed
		task.Result.Errors = append(task.Result.Errors, &apistructs.PipelineTaskErrResponse{
			Msg: "not find task bind pipeline",
		})
		if updateErr := r.dbClient.UpdatePipelineTask(task.ID, task); updateErr != nil {
			err = updateErr
			return nil, updateErr
		}
		return nil, fmt.Errorf("not find task bind pipeline")
	}

	sp := snippetPipeline
	// make context for snippet
	snippetCtx := makeContextForPipelineReconcile(sp.ID)
	// snippet pipeline first run
	if sp.Status == apistructs.PipelineStatusAnalyzed {
		// copy pipeline level run info from root pipeline
		if err = r.copyParentPipelineRunInfo(sp); err != nil {
			return nil, err
		}
		// set begin time
		now := time.Now()
		sp.TimeBegin = &now
		if err = r.dbClient.UpdatePipelineBase(sp.ID, &sp.PipelineBase); err != nil {
			return nil, err
		}

		var snippetPipelineTasks []*spec.PipelineTask
		snippetPipelineTasks, err = r.MergeDBTasks(sp)
		if err != nil {
			return nil, err
		}
		snippetDetail := apistructs.PipelineTaskSnippetDetail{
			DirectSnippetTasksNum:    len(snippetPipelineTasks),
			RecursiveSnippetTasksNum: -1,
		}
		if err := r.dbClient.UpdatePipelineTaskSnippetDetail(task.ID, snippetDetail); err != nil {
			return nil, err
		}

		if err := r.dbClient.UpdatePipelineTaskStatus(task.ID, apistructs.PipelineStatusRunning); err != nil {
			return nil, err
		}
	}

	if err := r.updateStatusBeforeReconcile(*sp); err != nil {
		rlog.PErrorf(p.ID, "Failed to update pipeline status before reconcile, err: %v", err)
		return nil, err
	}
	err = r.reconcile(snippetCtx, sp.ID)
	defer func() {
		r.teardownCurrentReconcile(snippetCtx, sp.ID)
		if err := r.updateStatusAfterReconcile(snippetCtx, sp.ID); err != nil {
			logrus.Errorf("snippet pipeline: %d, failed to update status after reconcile, err: %v", sp.ID, err)
		}
	}()
	if err != nil {
		return nil, err
	}
	// 查询最新 task
	latestTask, err := r.dbClient.GetPipelineTask(task.ID)
	if err != nil {
		return nil, err
	}
	*task = *(&latestTask)
	return task, nil
}

// reconcile do pipeline reconcile.
func (r *Reconciler) reconcile(ctx context.Context, pipelineID uint64) error {
	// judge if dlock lost
	if ctx.Err() != nil {
		rlog.PWarnf(pipelineID, "no need reconcile, dlock already lost, err: %v", ctx.Err())
		return nil
	}
	// init caches and get stages
	defer clearPipelineContextCaches(pipelineID)

	// get latest pipeline before reconcile
	p, err := r.dbClient.GetPipeline(pipelineID)
	if err != nil {
		return err
	}

	tasks, err := r.MergeDBTasks(&p)
	if err != nil {
		return err
	}

	// delay gc if have
	r.delayGC(p.Extra.Namespace, p.ID)

	// calculate pipeline status by tasks
	calcPStatus := statusutil.CalculatePipelineStatusV2(tasks)
	logrus.Infof("reconciler: pipelineID: %d, pipeline is not completed, continue reconcile, currentStatus: %s",
		p.ID, p.Status)

	schedulableTasks, err := r.getSchedulableTasks(&p, tasks)
	if err != nil {
		return rlog.PErrorAndReturn(p.ID, err)
	}

	var wg sync.WaitGroup
	for i := range schedulableTasks {
		wg.Add(1)
		go func(i int) {
			var err error
			var task *spec.PipelineTask
			task, err = r.saveTask(schedulableTasks[i], &p)
			if err != nil {
				logrus.Errorf("[alert] reconciler: pipelineID: %d, task %v reconcile occurred an error: %v", p.ID, schedulableTasks[i].Name, err)
				return
			}

			defer func() {
				if r := recover(); r != nil {
					debug.PrintStack()
					err = errors.Errorf("%v", r)
				}
				if err != nil {
					logrus.Errorf("[alert] reconciler: pipelineID: %d, task %v reconcile occurred an error: %v", p.ID, schedulableTasks[i].Name, err)
				}
				r.processingTasks.Delete(strconv.FormatUint(p.ID, 10) + "_" + schedulableTasks[i].Name)
				err = r.reconcile(ctx, pipelineID)
				wg.Done()
			}()

			if task.IsSnippet {
				task, err = r.reconcileSnippetTask(task, &p)
				return
			}

			executor, err := actionexecutor.GetManager().Get(types.Name(task.Extra.ExecutorName))
			if err != nil {
				return
			}

			tr := taskrun.New(ctx, task,
				ctx.Value(ctxKeyPipelineExitCh).(chan struct{}), ctx.Value(ctxKeyPipelineExitChCancelFunc).(context.CancelFunc),
				r.TaskThrottler, executor, &p, r.bdl, r.dbClient, r.js,
				r.actionAgentSvc, r.extMarketSvc)

			// tear down task
			defer func() {
				if tr.Task.Status.IsEndStatus() {
					// 同步 teardown
					tr.Teardown()
				}
			}()

			// 从 executor 获取最新任务状态，防止重复创建、启动的情况发生
			latestStatusFromExecutor, err := tr.Executor.Status(tr.Ctx, tr.Task)
			if err == nil && tr.Task.Status != latestStatusFromExecutor.Status {
				if latestStatusFromExecutor.Status.IsAbnormalFailedStatus() {
					logrus.Errorf("[alert] reconciler: pipelineID: %d, task %q, not correct task status from executor: %s -> %s (abnormal), continue reconcile task",
						p.ID, tr.Task.Name, tr.Task.Status, latestStatusFromExecutor.Status)
				} else {
					logrus.Errorf("[alert] reconciler: pipelineID: %d, task %q, correct task status from executor: %s -> %s",
						p.ID, tr.Task.Name, tr.Task.Status, latestStatusFromExecutor.Status)
					tr.Task.Status = latestStatusFromExecutor.Status
					tr.Update()
				}
			}

			// 之前的节点有失败的, 然后 action 中没有 if 表达式，直接更新状态为失败
			if calcPStatus == apistructs.PipelineStatusFailed && tr.Task.Extra.Action.If == "" {
				tr.Task.Status = apistructs.PipelineStatusNoNeedBySystem
				tr.Task.Extra.AllowFailure = true
				tr.Update()
				return
			}

			err = reconcileTask(tr)
			return
		}(i)
	}
	wg.Wait()

	return nil
}

// updatePipeline update db, publish websocket event
func (r *Reconciler) updatePipelineStatus(p *spec.Pipeline) error {
	// db
	if err := r.dbClient.UpdatePipelineBaseStatus(p.ID, p.Status); err != nil {
		return err
	}

	// event
	events.EmitPipelineInstanceEvent(p, p.GetRunUserID())

	return nil
}
