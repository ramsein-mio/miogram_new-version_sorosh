package bot

import (
	"context"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"

	"miogram/internal/storage"
	"miogram/internal/telegram"
)

type truthDareGame struct {
	ID               int64
	ChatID           int64
	RequesterID      string
	OtherID          string
	Status           string
	State            string
	TurnAskerID      string
	TurnAnswererID   string
	Kind             string
	Question         string
	MessageRequester int
	MessageOther     int
}

func (s *Service) loadTruthDareGame(ctx context.Context, gameID int64) (truthDareGame, error) {
	var g truthDareGame
	err := s.store.DB().QueryRow(ctx, `SELECT id,chat_id,requester_id,other_id,status,state,turn_asker_id,turn_answerer_id,kind,question,message_requester,message_other FROM truth_dare_games WHERE id=$1`, gameID).
		Scan(&g.ID, &g.ChatID, &g.RequesterID, &g.OtherID, &g.Status, &g.State, &g.TurnAskerID, &g.TurnAnswererID, &g.Kind, &g.Question, &g.MessageRequester, &g.MessageOther)
	return g, err
}

func (s *Service) requestTruthDare(ctx context.Context, c *UpdateContext, chat storage.Chat, other storage.User) error {
	if other.IsFake || chat.IsFake {
		_, err := s.send(ctx, "sendMessage", map[string]any{
			"chat_id": c.UserID,
			"text":    "این بازی فقط با مخاطب واقعی قابل اجراست 😅",
		})
		return err
	}

	tx, err := s.store.DB().Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var chatStatus, blockedUser string
	if err = tx.QueryRow(ctx, `SELECT status,coalesce(truth_dare_blocked_user,'') FROM chats WHERE id=$1 FOR UPDATE`, chat.ID).Scan(&chatStatus, &blockedUser); err != nil {
		return err
	}
	if chatStatus != "chatting" {
		_ = tx.Rollback(ctx)
		return s.sendChatNotice(ctx, c.UserID, "این گفتگو دیگر فعال نیست.")
	}
	if blockedUser == c.UserID {
		_ = tx.Rollback(ctx)
		return s.sendChatNotice(ctx, c.UserID, "بیخیالش شو دیگه، گفت که نمیخواد بازی کنه. ایش 😬")
	}

	var existingID int64
	err = tx.QueryRow(ctx, `SELECT id FROM truth_dare_games WHERE chat_id=$1 AND status IN ('pending','starting','active') LIMIT 1`, chat.ID).Scan(&existingID)
	if err == nil {
		_ = tx.Rollback(ctx)
		return s.sendChatNotice(ctx, c.UserID, "یه درخواست یا بازی جرعت حقیقت همین الان برای این گفتگو فعاله 😅")
	}
	if err != nil && err != pgx.ErrNoRows {
		return err
	}

	// If the user who previously rejected a request now initiates one, clear
	// the one-chat restriction: their former requester may ask again later.
	if blockedUser != "" && blockedUser != c.UserID {
		if _, err = tx.Exec(ctx, `UPDATE chats SET truth_dare_blocked_user='' WHERE id=$1`, chat.ID); err != nil {
			return err
		}
	}

	var gameID int64
	err = tx.QueryRow(ctx, `INSERT INTO truth_dare_games (chat_id,requester_id,other_id,status,state,created_at,updated_at) VALUES ($1,$2,$3,'pending','pending',$4,$4) RETURNING id`, chat.ID, c.UserID, other.UserID, c.Now).Scan(&gameID)
	if err != nil {
		return err
	}
	if err = tx.Commit(ctx); err != nil {
		return err
	}

	requestText := "مخاطبت شما درخواست شروع بازی جرعت حقیقت را ارسال کرده. حوصله بازی داری؟ 🤭"
	markup := telegram.JSON(replyMarkupInline([][]button{
		{callbackButton("چرا که نه! بازی میکنم ✅", fmt.Sprintf("jh;%d;accept", gameID))},
		{callbackButton("حوصله ندارم ❌", fmt.Sprintf("jh;%d;decline", gameID))},
	}))
	requestResp, requestErr := s.send(ctx, "sendMessage", map[string]any{
		"chat_id":      other.UserID,
		"text":         requestText,
		"reply_markup": markup,
	})
	if requestErr != nil || !requestResp.Ok {
		_, _ = s.store.DB().Exec(ctx, `UPDATE truth_dare_games SET status='send_failed',state='send_failed',updated_at=$2 WHERE id=$1 AND status='pending'`, gameID, c.Now)
		return s.sendChatNotice(ctx, c.UserID, "ارسال درخواست بازی به مخاطبت ناموفق بود؛ بعداً دوباره امتحان کن. 🙏")
	}

	requestMessageID := 0
	if msg, ok := s.tg.SentMessage(requestResp); ok {
		requestMessageID = msg.MessageID
	}
	_, err = s.store.DB().Exec(ctx, `UPDATE truth_dare_games SET message_other=$2,updated_at=$3 WHERE id=$1 AND status='pending'`, gameID, requestMessageID, c.Now)
	if err != nil {
		return err
	}
	confirmResp, confirmErr := s.send(ctx, "sendMessage", map[string]any{
		"chat_id": c.UserID,
		"text":    "درخواست بازی جرعت حقیقت برای مخاطب ارسال شد. منتظر باشید تا مخاطب به بازی ملحق شود 🤪",
	})
	confirmMessageID := 0
	if confirmErr == nil && confirmResp.Ok {
		if msg, ok := s.tg.SentMessage(confirmResp); ok {
			confirmMessageID = msg.MessageID
		}
	}
	_, err = s.store.DB().Exec(ctx, `UPDATE truth_dare_games SET message_requester=$2,updated_at=$3 WHERE id=$1 AND status='pending'`, gameID, confirmMessageID, c.Now)
	return err
}

func (s *Service) sendChatNotice(ctx context.Context, userID, text string) error {
	_, err := s.send(ctx, "sendMessage", map[string]any{"chat_id": userID, "text": text})
	return err
}

func (s *Service) handleTruthDareCallback(ctx context.Context, c *UpdateContext, chat storage.Chat) error {
	gameID := parseInt64(part(c.ExData, 1))
	action := part(c.ExData, 2)
	if gameID == 0 {
		return s.answer(ctx, c, "درخواست بازی نامعتبر است.")
	}
	g, err := s.loadTruthDareGame(ctx, gameID)
	if err != nil || g.ChatID != chat.ID || (c.UserID != g.RequesterID && c.UserID != g.OtherID) {
		return s.answer(ctx, c, "این درخواست بازی دیگر معتبر نیست.")
	}
	if g.Status == "ended" || g.Status == "declined" || g.Status == "send_failed" {
		return s.answer(ctx, c, "این بازی دیگر فعال نیست.")
	}

	switch action {
	case "accept":
		if c.UserID != g.OtherID || g.Status != "pending" {
			return s.answer(ctx, c, "این درخواست برای شما قابل تأیید نیست یا قبلاً بررسی شده است.")
		}
		return s.acceptTruthDare(ctx, c, g)
	case "decline":
		if c.UserID != g.OtherID || g.Status != "pending" {
			return s.answer(ctx, c, "این درخواست برای شما قابل رد نیست یا قبلاً بررسی شده است.")
		}
		return s.declineTruthDare(ctx, c, g)
	case "end":
		if g.Status != "active" {
			return s.answer(ctx, c, "این بازی فعال نیست.")
		}
		return s.endTruthDare(ctx, c, g)
	case "kind":
		kind := part(c.ExData, 3)
		if kind != "truth" && kind != "dare" {
			return s.answer(ctx, c, "انتخاب نامعتبر است.")
		}
		if g.Status != "active" || g.State != "choose_kind" || g.TurnAskerID != c.UserID {
			return s.answer(ctx, c, "الان نوبت انتخاب جرئت یا حقیقت شما نیست.")
		}
		return s.chooseTruthDareKind(ctx, c, g, kind)
	case "source":
		source := part(c.ExData, 3)
		if g.Status != "active" || g.State != "choose_source" || g.TurnAskerID != c.UserID {
			return s.answer(ctx, c, "الان امکان انتخاب روش پرسیدن سوال وجود ندارد.")
		}
		switch source {
		case "manual":
			return s.chooseManualQuestion(ctx, c, g)
		case "random":
			question, err := s.randomTruthDareQuestion(ctx, g.Kind)
			if err != nil {
				log.Printf("random truth/dare question unavailable: %v", err)
				return s.answer(ctx, c, "فعلاً سوال تصادفی پیدا نشد؛ دوباره امتحان کن یا خودت سوال بنویس.")
			}
			return s.submitTruthDareQuestion(ctx, c, g, question, 0, false)
		default:
			return s.answer(ctx, c, "روش انتخاب سوال نامعتبر است.")
		}
	case "confirm":
		if g.Status != "active" || g.State != "waiting_answer" || g.TurnAskerID != c.UserID {
			return s.answer(ctx, c, "این تأیید الان قابل انجام نیست.")
		}
		return s.confirmTruthDareAnswer(ctx, c, g)
	default:
		return s.answer(ctx, c, "عملیات بازی ناشناخته است.")
	}
}

func (s *Service) acceptTruthDare(ctx context.Context, c *UpdateContext, g truthDareGame) error {
	tx, err := s.store.DB().Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var status string
	err = tx.QueryRow(ctx, `SELECT status FROM truth_dare_games WHERE id=$1 FOR UPDATE`, g.ID).Scan(&status)
	if err != nil {
		return err
	}
	if status != "pending" {
		_ = tx.Rollback(ctx)
		return s.answer(ctx, c, "این درخواست قبلاً بررسی شده است.")
	}
	if _, err = tx.Exec(ctx, `UPDATE truth_dare_games SET status='starting',state='choose_kind',turn_asker_id=requester_id,turn_answerer_id=other_id,updated_at=$2 WHERE id=$1`, g.ID, c.Now); err != nil {
		return err
	}
	if err = tx.Commit(ctx); err != nil {
		return err
	}
	_ = s.ack(ctx, c)
	if g.MessageOther != 0 {
		_, _ = s.send(ctx, "editMessageText", map[string]any{
			"chat_id": g.OtherID, "message_id": g.MessageOther,
			"text":         "درخواست بازی پذیرفته شد ✅\n\nبازی جرعت حقیقت شروع می‌شود 🎀",
			"reply_markup": telegram.JSON(replyMarkupInline([][]button{})),
		})
	}

	requester, err := s.store.UserByID(ctx, g.RequesterID)
	if err != nil {
		_, _ = s.store.DB().Exec(ctx, `UPDATE truth_dare_games SET status='send_failed',state='send_failed' WHERE id=$1`, g.ID)
		return err
	}
	other, err := s.store.UserByID(ctx, g.OtherID)
	if err != nil {
		_, _ = s.store.DB().Exec(ctx, `UPDATE truth_dare_games SET status='send_failed',state='send_failed' WHERE id=$1`, g.ID)
		return err
	}
	g.Status = "starting"
	g.State = "choose_kind"
	g.TurnAskerID = g.RequesterID
	g.TurnAnswererID = g.OtherID
	requesterText, requesterMarkup := renderTruthDareGame(g, g.RequesterID, displayName(requester), displayName(other))
	otherText, otherMarkup := renderTruthDareGame(g, g.OtherID, displayName(requester), displayName(other))
	respRequester, errRequester := s.send(ctx, "sendMessage", map[string]any{"chat_id": g.RequesterID, "text": requesterText, "reply_markup": requesterMarkup})
	respOther, errOther := s.send(ctx, "sendMessage", map[string]any{"chat_id": g.OtherID, "text": otherText, "reply_markup": otherMarkup})
	msgRequester, okRequester := s.tg.SentMessage(respRequester)
	msgOther, okOther := s.tg.SentMessage(respOther)
	if errRequester != nil || errOther != nil || !respRequester.Ok || !respOther.Ok || !okRequester || !okOther || msgRequester.MessageID == 0 || msgOther.MessageID == 0 {
		_, _ = s.store.DB().Exec(ctx, `UPDATE truth_dare_games SET status='send_failed',state='send_failed',updated_at=$2 WHERE id=$1`, g.ID, c.Now)
		if respRequester.Ok && okRequester {
			_, _ = s.send(ctx, "editMessageText", map[string]any{"chat_id": g.RequesterID, "message_id": msgRequester.MessageID, "text": "شروع بازی ناموفق بود؛ لطفاً دوباره درخواست بدهید."})
		}
		if respOther.Ok && okOther {
			_, _ = s.send(ctx, "editMessageText", map[string]any{"chat_id": g.OtherID, "message_id": msgOther.MessageID, "text": "شروع بازی ناموفق بود؛ لطفاً دوباره تلاش کنید."})
		}
		_, _ = s.send(ctx, "sendMessage", map[string]any{"chat_id": g.RequesterID, "text": "شروع بازی جرعت حقیقت ناموفق بود؛ دوباره درخواست بدهید. 🙏"})
		_, _ = s.send(ctx, "sendMessage", map[string]any{"chat_id": g.OtherID, "text": "شروع بازی جرعت حقیقت ناموفق بود؛ دوباره تلاش کنید. 🙏"})
		return nil
	}
	_, err = s.store.DB().Exec(ctx, `UPDATE truth_dare_games SET status='active',state='choose_kind',message_requester=$2,message_other=$3,updated_at=$4 WHERE id=$1 AND status='starting'`, g.ID, msgRequester.MessageID, msgOther.MessageID, c.Now)
	if err != nil {
		return err
	}
	g.Status = "active"
	g.MessageRequester, g.MessageOther = msgRequester.MessageID, msgOther.MessageID
	if err = s.updateTruthDareMessages(ctx, g); err != nil {
		return err
	}
	fresh, loadErr := s.loadTruthDareGame(ctx, g.ID)
	if loadErr == nil {
		g = fresh
	}
	for _, target := range []struct {
		userID string
		msgID  int
	}{{g.RequesterID, g.MessageRequester}, {g.OtherID, g.MessageOther}} {
		if target.msgID == 0 {
			continue
		}
		pinResp, pinErr := s.send(ctx, "pinChatMessage", map[string]any{"chat_id": target.userID, "message_id": target.msgID, "disable_notification": true})
		if pinErr != nil || !pinResp.Ok {
			log.Printf("pin truth/dare game %d for %s failed: %v %s", g.ID, target.userID, pinErr, pinResp.Description)
		}
	}
	return nil
}

func (s *Service) declineTruthDare(ctx context.Context, c *UpdateContext, g truthDareGame) error {
	tx, err := s.store.DB().Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	// Lock chat first, matching EndChat's lock order (chat -> game) so a
	// simultaneous rejection and chat closure cannot deadlock.
	var chatStatus string
	if err = tx.QueryRow(ctx, `SELECT status FROM chats WHERE id=$1 FOR UPDATE`, g.ChatID).Scan(&chatStatus); err != nil {
		return err
	}
	if chatStatus != "chatting" {
		_ = tx.Rollback(ctx)
		return s.answer(ctx, c, "این گفتگو دیگر فعال نیست.")
	}
	var status string
	if err = tx.QueryRow(ctx, `SELECT status FROM truth_dare_games WHERE id=$1 FOR UPDATE`, g.ID).Scan(&status); err != nil {
		return err
	}
	if status != "pending" {
		_ = tx.Rollback(ctx)
		return s.answer(ctx, c, "این درخواست قبلاً بررسی شده است.")
	}
	if _, err = tx.Exec(ctx, `UPDATE truth_dare_games SET status='declined',state='declined',updated_at=$2 WHERE id=$1`, g.ID, c.Now); err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, `UPDATE chats SET truth_dare_blocked_user=$2 WHERE id=$1 AND status='chatting'`, g.ChatID, g.RequesterID); err != nil {
		return err
	}
	if err = tx.Commit(ctx); err != nil {
		return err
	}
	_ = s.ack(ctx, c)
	if g.MessageOther != 0 {
		_, _ = s.send(ctx, "editMessageText", map[string]any{
			"chat_id": g.OtherID, "message_id": g.MessageOther,
			"text":         "حله بازی کنسلش کردم 🫠",
			"reply_markup": telegram.JSON(replyMarkupInline([][]button{})),
		})
	}
	_, err = s.send(ctx, "sendMessage", map[string]any{
		"chat_id": g.RequesterID,
		"text":    "آخ آخ مخاطبت درخواستتو رد کرد و گفت حوصله بازی جرعت حقیقت نداره 🙂‍↕\nمتاسفم!",
	})
	return err
}

func (s *Service) chooseTruthDareKind(ctx context.Context, c *UpdateContext, g truthDareGame, kind string) error {
	result, err := s.store.DB().Exec(ctx, `UPDATE truth_dare_games SET kind=$2,state='choose_source',updated_at=$3 WHERE id=$1 AND status='active' AND state='choose_kind' AND turn_asker_id=$4`, g.ID, kind, c.Now, c.UserID)
	if err != nil {
		return err
	}
	if result.RowsAffected() == 0 {
		return s.answer(ctx, c, "این انتخاب قبلاً انجام شده یا نوبت شما نیست.")
	}
	_ = s.ack(ctx, c)
	g.Kind = kind
	g.State = "choose_source"
	return s.updateTruthDareMessages(ctx, g)
}

func (s *Service) chooseManualQuestion(ctx context.Context, c *UpdateContext, g truthDareGame) error {
	result, err := s.store.DB().Exec(ctx, `UPDATE truth_dare_games SET state='waiting_question',updated_at=$2 WHERE id=$1 AND status='active' AND state='choose_source' AND turn_asker_id=$3`, g.ID, c.Now, c.UserID)
	if err != nil {
		return err
	}
	if result.RowsAffected() == 0 {
		return s.answer(ctx, c, "این مرحله قبلاً انجام شده یا نوبت شما نیست.")
	}
	_ = s.ack(ctx, c)
	g.State = "waiting_question"
	if err = s.updateTruthDareMessages(ctx, g); err != nil {
		return err
	}
	return s.sendChatNotice(ctx, c.UserID, "خب خب سوالت بنویس، نترس من یجوری وانمود میکنم نفهمه سوال رندوم نبوده 🗿👇")
}

func (s *Service) captureTruthDareQuestion(ctx context.Context, c *UpdateContext) (bool, error) {
	if c.Message == nil && c.EditedMessage == nil {
		return false, nil
	}
	var g truthDareGame
	err := s.store.DB().QueryRow(ctx, `SELECT id,chat_id,requester_id,other_id,status,state,turn_asker_id,turn_answerer_id,kind,question,message_requester,message_other FROM truth_dare_games WHERE status='active' AND state='waiting_question' AND turn_asker_id=$1 ORDER BY updated_at DESC LIMIT 1`, c.UserID).
		Scan(&g.ID, &g.ChatID, &g.RequesterID, &g.OtherID, &g.Status, &g.State, &g.TurnAskerID, &g.TurnAnswererID, &g.Kind, &g.Question, &g.MessageRequester, &g.MessageOther)
	if err == pgx.ErrNoRows {
		return false, nil
	}
	if err != nil {
		return true, err
	}
	// Only a new text/caption is accepted as the actual prompt. Even when a
	// photo, sticker, or other non-text message arrives in this state, consume
	// it here instead of letting handleChat relay it to the counterpart.
	if c.EditedMessage != nil {
		return true, s.sendChatNotice(ctx, c.UserID, "لطفاً سوال را با ارسال یک پیام جدید بنویس؛ پیام ویرایش‌شده به مخاطب منتقل نمی‌شود. 📝")
	}
	question := strings.TrimSpace(c.Text)
	if question == "" {
		question = strings.TrimSpace(c.Caption)
	}
	if question == "" {
		return true, s.sendChatNotice(ctx, c.UserID, "لطفاً سوال جرئت یا حقیقت را به‌صورت متن یا کپشن ارسال کن؛ این پیام به مخاطب منتقل نمی‌شود. 📝")
	}
	if utf8.RuneCountInString(question) > 2400 {
		runes := []rune(question)
		question = string(runes[:2400])
	}
	handled := s.submitTruthDareQuestion(ctx, c, g, question, c.MessageID, true)
	return true, handled
}

func (s *Service) submitTruthDareQuestion(ctx context.Context, c *UpdateContext, g truthDareGame, question string, sourceMessageID int, manual bool) error {
	if strings.TrimSpace(question) == "" {
		return s.answer(ctx, c, "سوال خالیه؛ یک سوال کوتاه بنویس.")
	}
	stateCondition := "choose_source"
	if manual {
		stateCondition = "waiting_question"
	}
	result, err := s.store.DB().Exec(ctx, `UPDATE truth_dare_games SET question=$2,state='waiting_answer',updated_at=$3 WHERE id=$1 AND status='active' AND state=$4 AND turn_asker_id=$5`, g.ID, question, c.Now, stateCondition, c.UserID)
	if err != nil {
		return err
	}
	if result.RowsAffected() == 0 {
		if manual {
			return s.sendChatNotice(ctx, c.UserID, "این سوال قبلاً ثبت شده یا نوبت شما نیست.")
		}
		return s.answer(ctx, c, "این سوال قبلاً ثبت شده یا نوبت شما نیست.")
	}
	_ = s.ack(ctx, c)
	g.Question = question
	g.State = "waiting_answer"
	if manual && sourceMessageID != 0 {
		if _, err := s.send(ctx, "sendMessage", map[string]any{
			"chat_id": c.UserID, "text": "حله ازش پرسیدم! 🙂‍↕", "reply_to_message_id": sourceMessageID,
		}); err != nil {
			log.Printf("truth/dare question confirmation for %s: %v", c.UserID, err)
		}
	}
	return s.updateTruthDareMessages(ctx, g)
}

func (s *Service) confirmTruthDareAnswer(ctx context.Context, c *UpdateContext, g truthDareGame) error {
	result, err := s.store.DB().Exec(ctx, `UPDATE truth_dare_games SET turn_asker_id=turn_answerer_id,turn_answerer_id=turn_asker_id,state='choose_kind',kind='',question='',updated_at=$2 WHERE id=$1 AND status='active' AND state='waiting_answer' AND turn_asker_id=$3`, g.ID, c.Now, c.UserID)
	if err != nil {
		return err
	}
	if result.RowsAffected() == 0 {
		return s.answer(ctx, c, "پاسخ قبلاً تأیید شده یا نوبت شما نیست.")
	}
	_ = s.ack(ctx, c)
	g.TurnAskerID, g.TurnAnswererID = g.TurnAnswererID, g.TurnAskerID
	g.State, g.Kind, g.Question = "choose_kind", "", ""
	return s.updateTruthDareMessages(ctx, g)
}

func (s *Service) endTruthDare(ctx context.Context, c *UpdateContext, g truthDareGame) error {
	result, err := s.store.DB().Exec(ctx, `UPDATE truth_dare_games SET status='ended',state='ended',updated_at=$2 WHERE id=$1 AND status='active' AND (requester_id=$3 OR other_id=$3)`, g.ID, c.Now, c.UserID)
	if err != nil {
		return err
	}
	if result.RowsAffected() == 0 {
		return s.answer(ctx, c, "این بازی دیگر فعال نیست.")
	}
	_ = s.ack(ctx, c)
	g.Status, g.State = "ended", "ended"
	return s.updateTruthDareMessages(ctx, g)
}

func (s *Service) updateTruthDareMessages(ctx context.Context, g truthDareGame) error {
	fresh, err := s.loadTruthDareGame(ctx, g.ID)
	if err == nil {
		g = fresh
	}
	requester, err := s.store.UserByID(ctx, g.RequesterID)
	if err != nil {
		return err
	}
	other, err := s.store.UserByID(ctx, g.OtherID)
	if err != nil {
		return err
	}
	for _, target := range []struct {
		userID string
		msgID  int
	}{{g.RequesterID, g.MessageRequester}, {g.OtherID, g.MessageOther}} {
		if target.msgID == 0 {
			continue
		}
		text, markup := renderTruthDareGame(g, target.userID, displayName(requester), displayName(other))
		if err := s.editTruthDarePinnedMessage(ctx, g, target.userID, target.msgID, text, markup); err != nil {
			log.Printf("update truth/dare game %d for %s: %v", g.ID, target.userID, err)
		}
	}
	return nil
}

func (s *Service) editTruthDarePinnedMessage(ctx context.Context, g truthDareGame, userID string, messageID int, text string, markup any) error {
	resp, err := s.send(ctx, "editMessageText", map[string]any{
		"chat_id": userID, "message_id": messageID, "text": text, "reply_markup": markup,
	})
	if err == nil && resp.Ok {
		return nil
	}
	description := strings.ToLower(resp.Description)
	if strings.Contains(description, "message is not modified") {
		return nil
	}
	log.Printf("edit pinned truth/dare message %d for %s failed (ok=%v): %v %s", messageID, userID, resp.Ok, err, resp.Description)

	// Keep the game usable if Telegram refuses to edit the original message.
	// Send a replacement, pin it, and update the stored ID used by later turns.
	replacement, sendErr := s.send(ctx, "sendMessage", map[string]any{
		"chat_id": userID, "text": text, "reply_markup": markup,
	})
	if sendErr != nil || !replacement.Ok {
		return fmt.Errorf("edit failed and replacement could not be sent: edit=%v (%s), send=%v (%s)", err, resp.Description, sendErr, replacement.Description)
	}
	msg, ok := s.tg.SentMessage(replacement)
	if !ok || msg.MessageID == 0 {
		return fmt.Errorf("replacement message was sent without a message id")
	}
	column := "message_other"
	if userID == g.RequesterID {
		column = "message_requester"
	}
	_, updateErr := s.store.DB().Exec(ctx, `UPDATE truth_dare_games SET `+column+`=$2 WHERE id=$1`, g.ID, msg.MessageID)
	if updateErr != nil {
		return updateErr
	}
	_, _ = s.send(ctx, "unpinChatMessage", map[string]any{"chat_id": userID, "message_id": messageID})
	if pinResp, pinErr := s.send(ctx, "pinChatMessage", map[string]any{"chat_id": userID, "message_id": msg.MessageID, "disable_notification": true}); pinErr != nil || !pinResp.Ok {
		log.Printf("pin replacement truth/dare message %d for %s failed: %v %s", msg.MessageID, userID, pinErr, pinResp.Description)
	}
	return nil
}

func renderTruthDareGame(g truthDareGame, viewerID, requesterName, otherName string) (string, any) {
	askerName := requesterName
	answererName := otherName
	if g.TurnAskerID == g.OtherID {
		askerName, answererName = otherName, requesterName
	}
	isAsker := viewerID == g.TurnAskerID
	endRow := []button{callbackButton("پایان بازی جرعت حقیقت ❌", fmt.Sprintf("jh;%d;end", g.ID))}
	markup := telegram.JSON(replyMarkupInline([][]button{endRow}))
	base := "بازی جرعت حقیقت شروع شد 🎀\n\nکسی که میپرسه: " + askerName + "\nکسی که جواب میده: " + answererName + "\n\n"
	if g.State == "ended" {
		return "بازی جرعت حقیقت به درخواست یکی از شما پایان یافت ❌", telegram.JSON(replyMarkupInline([][]button{}))
	}
	if g.Status == "starting" {
		return base + "- بازی در حال آماده‌سازی است؛ یک لحظه صبر کن 🎀", telegram.JSON(replyMarkupInline([][]button{}))
	}
	switch g.State {
	case "choose_kind":
		if isAsker {
			markup = telegram.JSON(replyMarkupInline([][]button{
				{callbackButton("جرعت", fmt.Sprintf("jh;%d;kind;dare", g.ID)), callbackButton("حقیقت", fmt.Sprintf("jh;%d;kind;truth", g.ID))},
				endRow,
			}))
			return base + "- انتخابت کن که میخوای از مخاطبت جرعت بخوای یا سوال حقیقت بپرسی 😈", markup
		}
		return base + "- صبرکن مخاطب داره فکر میکنه جرعت بخواد یا حقیقت بپرسه😶", markup
	case "choose_source":
		kindText := "حقیقت"
		if g.Kind == "dare" {
			kindText = "جرعت"
		}
		if isAsker {
			markup = telegram.JSON(replyMarkupInline([][]button{
				{callbackButton("خودم میپرسم 🧾", fmt.Sprintf("jh;%d;source;manual", g.ID)), callbackButton("رندوم بپرس ♻️", fmt.Sprintf("jh;%d;source;random", g.ID))},
				endRow,
			}))
			return "خب خب وقت انتخاب سواله 😈\n\nکسی که میپرسه: " + askerName + "\nکسی که جواب میده: " + answererName + "\n\n- دوست داری خودت بپرسی یا من یه سوال رندوم ازش بپرسم؟ 🫵", markup
		}
		return base + "- مخاطب " + kindText + " رو انتخاب کرد 🫠\n\n- داره فکر میکنه دقیقا چی بپرسه 🫣", markup
	case "waiting_question":
		if isAsker {
			return base + "- سوالت رو در یک پیام جدید بفرست تا از مخاطبت بپرسم 📝", markup
		}
		return base + "- مخاطبت داره سوال رو آماده میکنه؛ یکم صبر کن 🫣", markup
	case "waiting_answer":
		kindText := "حقیقت"
		if g.Kind == "dare" {
			kindText = "جرعت"
		}
		if isAsker {
			markup = telegram.JSON(replyMarkupInline([][]button{
				{callbackButton("تأیید جواب مخاطب ✅", fmt.Sprintf("jh;%d;confirm", g.ID))},
				endRow,
			}))
			return "پرسیدم صبرکن جواب بده 😇\n\n" + base + "👈 سوال: " + g.Question + "\n\n- هروقت جواب داد بزن رو گزینه «تأیید جواب مخاطب» تا نوبت منتقل بشه 😄", markup
		}
		return "خب مخاطب سوال " + kindText + " رو پرسید 🫢\n\n" + base + "👈 سوال: " + g.Question + "\n\n- خب الان وقتشه جواب سوال بدی یا کاری که ازت خواسته انجام بدی بعدش نوبت منتقل میشه 😎🤘", markup
	default:
		return base + "- بازی جرعت حقیقت آماده شروعه 🎀", markup
	}
}

// randomTruthDareQuestion reads exclusively from data/jh.db. Questions are kept
// in SQLite so the game does not depend on PostgreSQL question tables.
func (s *Service) randomTruthDareQuestion(ctx context.Context, kind string) (string, error) {
	if kind != "truth" && kind != "dare" {
		return "", fmt.Errorf("invalid truth/dare kind %q", kind)
	}
	return randomQuestionFromSQLite(ctx, kind)
}

// randomQuestionFromSQLite expects data/jh.db with a `questions` table shaped as:
// questions(id, kind, question, enabled), where kind is 'truth' or 'dare'.
func randomQuestionFromSQLite(ctx context.Context, kind string) (string, error) {
	if kind != "truth" && kind != "dare" {
		return "", fmt.Errorf("invalid truth/dare kind %q", kind)
	}

	dbPath := filepath.Join("data", "jh.db")
	if _, err := os.Stat(dbPath); err != nil {
		return "", fmt.Errorf("open truth/dare question database %s: %w", dbPath, err)
	}
	binary, err := exec.LookPath("sqlite3")
	if err != nil {
		return "", fmt.Errorf("sqlite3 command is required to read %s: %w", dbPath, err)
	}

	// `kind` is restricted to these two literal values above, so it cannot
	// inject SQL. The command reads only from SQLite; there is no PostgreSQL fallback.
	query := `SELECT question FROM questions WHERE kind='` + kind + `' AND enabled=1 AND trim(question)<>'' ORDER BY RANDOM() LIMIT 1;`
	cmd := exec.CommandContext(ctx, binary, "-batch", "-noheader", dbPath, query)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("query %s questions from %s: %w: %s", kind, dbPath, err, strings.TrimSpace(string(out)))
	}
	question := strings.TrimSpace(string(out))
	if question == "" {
		return "", fmt.Errorf("no enabled %s questions found in %s", kind, dbPath)
	}
	return question, nil
}
