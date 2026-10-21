// Package tray shows the Uley icon next to the clock: status, jobs done,
// pause/resume, settings and exit.
package tray

import (
	_ "embed"
	"fmt"
	"runtime"
	"time"

	"fyne.io/systray"
	"github.com/sqo4/uley/client/agent"
	"github.com/sqo4/uley/client/config"
	"github.com/sqo4/uley/internal/protocol"
)

var (
	//go:embed icons/active.png
	activePNG []byte
	//go:embed icons/paused.png
	pausedPNG []byte
	//go:embed icons/active.ico
	activeICO []byte
	//go:embed icons/paused.ico
	pausedICO []byte
)

func icon(active bool) []byte {
	win := runtime.GOOS == "windows"
	switch {
	case active && win:
		return activeICO
	case active:
		return activePNG
	case win:
		return pausedICO
	}
	return pausedPNG
}

type App struct {
	Agent     *agent.Agent
	Config    *config.Store
	OpenPanel func()
	// UpdateAvailable returns a newer version or "".
	UpdateAvailable func() string
	OnQuit          func()
}

// Run blocks until the user chooses "Exit". It must run on the main thread.
func Run(app App) {
	systray.Run(func() { onReady(app) }, app.OnQuit)
}

var stateText = map[string]string{
	protocol.StateWorking:     "работает",
	protocol.StatePaused:      "пауза",
	protocol.StatePreparing:   "готовится",
	protocol.StateUnsupported: "видеокарта не подходит",
}

func onReady(app App) {
	systray.SetIcon(icon(false))
	systray.SetTitle("")
	systray.SetTooltip("Улей")

	mStatus := systray.AddMenuItem("Улей", "")
	mStatus.Disable()
	mJobs := systray.AddMenuItem("Выполнено задач: 0", "")
	mJobs.Disable()
	systray.AddSeparator()
	mToggle := systray.AddMenuItem("Пауза", "Остановить или продолжить работу")
	mPanel := systray.AddMenuItem("Настройки и статистика", "Открыть панель Улья")
	mUpdate := systray.AddMenuItem("", "")
	mUpdate.Hide()
	systray.AddSeparator()
	mQuit := systray.AddMenuItem("Выход", "Закрыть Улей")

	refresh := func() {
		st := app.Agent.Status()
		s := app.Config.Get()
		text := stateText[st.State]
		if st.Busy {
			text = "считает задачу"
		}
		mStatus.SetTitle("Улей: " + text)
		systray.SetTooltip("Улей: " + text + "\n" + st.Reason)
		mJobs.SetTitle(fmt.Sprintf("Выполнено задач: %d", st.JobsDone))
		systray.SetIcon(icon(st.State == protocol.StateWorking))
		switch {
		case s.Consent < config.ConsentVersion:
			mToggle.SetTitle("Подключиться…")
		case s.Paused:
			mToggle.SetTitle("▶ Продолжить")
		default:
			mToggle.SetTitle("⏸ Пауза")
		}
		if v := app.UpdateAvailable(); v != "" {
			mUpdate.SetTitle("Доступно обновление " + v)
			mUpdate.Show()
		}
	}
	refresh()

	go func() {
		t := time.NewTicker(2 * time.Second)
		defer t.Stop()
		for {
			select {
			case <-t.C:
				refresh()
			case <-mToggle.ClickedCh:
				if app.Config.Get().Consent < config.ConsentVersion {
					app.OpenPanel()
					continue
				}
				app.Config.Update(func(s *config.Settings) { s.Paused = !s.Paused })
				app.Agent.Kick()
				refresh()
			case <-mPanel.ClickedCh:
				app.OpenPanel()
			case <-mUpdate.ClickedCh:
				app.OpenPanel()
			case <-mQuit.ClickedCh:
				systray.Quit()
				return
			}
		}
	}()
}
