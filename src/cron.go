package main

import (
	"io"
	"log"
	"os"

	"github.com/robfig/cron"
)

type (
	CronTask struct {
		Name     string
		Schedule string
		Fn       func(io.Writer) error
	}
)

// TODO: Add daily cron task to cleanup SB build box release containers.

func (this *Server) GetCronTasks() []CronTask {
	cronTasks := []CronTask{
		// ZFS maintenance.
		CronTask{
			Name:     "ZfsMaintenance",
			Schedule: "1 30 7 * * *",
			Fn:       this.sysPerformZfsMaintenance,
		},
		// Orphaned snapshots removal.
		CronTask{
			Name:     "OrphanedSnapshots",
			Schedule: "1 45 * * * *",
			Fn:       this.sysRemoveOrphanedReleaseSnapshots,
		},
		// Hourly NTP sync.
		CronTask{
			Name:     "NtpSync",
			Schedule: "1 1 * * * *",
			Fn:       this.sysSyncNtp,
		},
	}
	return cronTasks
}

func (this *Server) startCrons() {
	c := cron.New()
	log.Printf("[cron] Configuring..\n")
	for _, cronTask := range this.GetCronTasks() {
		if cronTask.Name == "ZfsMaintenance" && lxcFs != "zfs" {
			log.Printf(`[cron] Refusing to add ZFS maintenance cron task because the lxcFs is actuallty "%v"\n`, lxcFs)
			continue
		}
		log.Printf("[cron] Adding cron task '%v'\n", cronTask.Name)
		c.AddFunc(cronTask.Schedule, func() {
			logger := NewLogger(os.Stdout, "["+cronTask.Name+"] ")
			err := cronTask.Fn(logger)
			if err != nil {
				log.Printf("[cron] task=%v ended with error=%v\n", cronTask.Name, err)
			}
		})
	}
	log.Printf("[cron] Starting..\n")
	c.Start()
	log.Printf("[cron] Cron successfully launched.\n")
}
