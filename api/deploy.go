package api

import (
	"context"
)

type deployJob struct {
	State   string `json:"state"`
	Message string `json:"message"`
}

// Keep the latest job so a refreshed dashboard can resume watching it.
// Repeated clicks while a job runs share the same deployment.
func (a *App) reserveDeployJob() (*deployJob, bool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.deployJob != nil && a.deployJob.State == "running" {
		return a.deployJob, false
	}
	a.deployJob = &deployJob{State: "running", Message: "Updating Traefik. The connection may briefly reconnect while the container restarts."}
	return a.deployJob, true
}

func (a *App) deployStatus() deployJob {
	a.mu.RLock()
	defer a.mu.RUnlock()
	if a.deployJob == nil {
		return deployJob{State: "idle"}
	}
	return *a.deployJob
}

func (a *App) runDeployJob(job *deployJob) {
	err := a.redeploy(context.Background())
	a.mu.Lock()
	defer a.mu.Unlock()
	if err != nil {
		job.State = "error"
		job.Message = "Deploy failed: " + err.Error()
		return
	}
	job.State = "done"
	job.Message = "Traefik redeployed successfully."
}
