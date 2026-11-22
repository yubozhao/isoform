package main

import "wails-gui/models"

// Setup retains its saved choices and confirmed download ledger. Resumption
// and CLI discovery share the post-paint worker with other optional startup.
func (a *App) deferSetupStartup(manager *models.Manager) <-chan struct{} {
	if manager != nil {
		a.deferLaunchTask("setup_model_resume", func() {
			manager.MonitorNetwork()
			manager.Resume()
			a.watchDeferredModels(manager)
			a.watchWaitingSpeechModels(manager)
		})
	}
	return a.deferLaunchTask("setup_agent_probe", a.prepareAgentProbe)
}
