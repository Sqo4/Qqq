package coordinator

import (
	"fmt"
	"net/http"
	"strings"
	"time"
)

// Marking of AI-generated content. Since 243-ФЗ (from 1 March 2027 for its
// labelling part) platforms should let people mark material made with AI.
// Улей marks it everywhere its answers can leave the service: the web app,
// copies and exports, the API and Telegram.

const (
	aiMark       = "Создано ИИ"
	aiMarkHeader = "X-AI-Generated"
)

// aiFooter is appended to AI text that leaves Улей as plain text.
func (s *Server) aiFooter() string {
	return fmt.Sprintf("\n\n— %s · нейросеть «Улей» (модель %s)", aiMark, s.hiveModel())
}

// handleExportChat downloads a chat as Markdown with every AI answer marked.
func (s *Server) handleExportChat(w http.ResponseWriter, r *http.Request) {
	u := currentUser(r)
	c, err := s.store.ChatAccess(r.PathValue("id"), u.ID)
	if err != nil {
		userErr(w, err)
		return
	}
	var all []Message
	before := int64(0)
	for {
		page, err := s.store.Messages(c.ID, u.ID, before, 500)
		if err != nil {
			userErr(w, err)
			return
		}
		all = append(page, all...)
		if len(page) < 500 || len(all) >= 20000 {
			break
		}
		before = page[0].ID
	}
	loc := s.loc()
	var b strings.Builder
	fmt.Fprintf(&b, "# %s\n\nЭкспорт из «Улья», %s.\nСообщения с пометкой «%s» сгенерированы нейросетью «Улей» (модель %s).\n\n---\n\n",
		c.Title, s.store.now().In(loc).Format("02.01.2006 15:04"), aiMark, s.hiveModel())
	for _, m := range all {
		if m.Status == MsgPending {
			continue
		}
		when := time.Unix(m.CreatedAt, 0).In(loc).Format("02.01.2006 15:04")
		if m.UserID == "" {
			fmt.Fprintf(&b, "**🐝 Улей · %s** · %s\n\n%s\n\n", aiMark, when, m.Content)
		} else {
			fmt.Fprintf(&b, "**%s** · %s\n\n%s\n\n", m.AuthorName, when, m.Content)
		}
	}
	w.Header().Set("Content-Type", "text/markdown; charset=utf-8")
	w.Header().Set("Content-Disposition", `attachment; filename="uley-chat.md"`)
	w.Header().Set(aiMarkHeader, "partial") // some messages are made by AI
	w.Write([]byte(b.String()))
}
