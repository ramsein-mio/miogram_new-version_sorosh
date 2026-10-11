package bot

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/url"
	"os"
	"strings"

	"miogram/internal/fleet"
	"miogram/internal/storage"
	"miogram/internal/telegram"
)

func (s *Service) handleProfile(ctx context.Context, c *UpdateContext) (bool, error) {
	// A completed profile must not keep routing every future update through the
	// profile-completion flow. This also repairs users left in that state by an
	// earlier version of the bot.
	if c.User.PrevStep == "complete_profile" {
		missing, _, _ := s.completeProfile(ctx, c.User)
		if missing == 0 {
			if err := s.store.UpdateUserStepPrev(ctx, c.UserID, c.User.Step, "start"); err != nil {
				return true, err
			}
			c.User.PrevStep = "start"
		}
	}

	if c.Text == "/id" {
		_, err := s.send(ctx, "sendMessage", map[string]any{"chat_id": c.UserID, "text": "🆔 ایدی شما در ربات: /user_" + c.User.UniqID})
		return true, err
	}
	if c.Text == "👤پروفایل" || c.Text == "/profile" {
		return true, s.showSelfProfile(ctx, c)
	}
	if part(c.ExData, 0) == "likes" {
		return true, s.handleLikesList(ctx, c)
	}
	if part(c.ExData, 0) == "contacts" {
		return true, s.handleContactsList(ctx, c)
	}
	if part(c.ExData, 0) == "blockeds" {
		return true, s.handleBlockedList(ctx, c)
	}
	if c.Data == "fake_profile" {
		return true, s.answer(ctx, c, "این پروفایل فقط در همین گفتگوی کوتاه قابل مشاهده است.")
	}
	if part(c.ExData, 0) == "profile" && part(c.ExData, 2) == "complete" {
		_, _ = s.store.DB().Exec(ctx, `UPDATE users SET step='start',prev_step='complete_profile' WHERE user_id=$1`, c.UserID)
		c.User.Step = "start"
		c.User.PrevStep = "complete_profile"
		c.refreshStep()
	}
	if part(c.ExStep, 0) == "profile" && c.Data != "start" {
		if handled, err := s.handleProfileEditInput(ctx, c); handled || err != nil {
			return handled, err
		}
	}
	if (part(c.ExData, 0) == "profile" && part(c.ExData, 1) == "silent") || c.Text == "/silent" {
		return true, s.handleSilent(ctx, c)
	}
	if part(c.ExData, 0) == "profile" || c.User.PrevStep == "complete_profile" {
		return true, s.handleProfileCallback(ctx, c)
	}
	if part(c.ExData, 0) == "gib" {
		return true, s.handleGIBCallback(ctx, c)
	}
	if part(c.ExStep, 0) == "gib" {
		return true, s.handleGIBInput(ctx, c)
	}
	return false, nil
}

func (s *Service) showSelfProfile(ctx context.Context, c *UpdateContext) error {
	var count int
	_ = s.store.DB().QueryRow(ctx, `SELECT count(*) FROM likes WHERE target_id=$1`, c.UserID).Scan(&count)
	caption := fmt.Sprintf("• نام: %s\n• جنسیت: %s\n• استان: %s\n• شهر: %s\n• سن: %d\n\n♥️ لایک ها : %d\n\n%s\n\n‏🆔 آیدی : /user_%s\n‏",
		checkInout(c.User.Name), GenderWithEmoji[c.User.Gender], c.User.State, checkInout(c.User.City), c.User.Age, count, lastActivity(c.Now, c.User.LastActivity), c.User.UniqID)
	resp, err := s.send(ctx, "sendPhoto", map[string]any{
		"chat_id":             c.UserID,
		"photo":               s.userProfilePhoto(ctx, c.User),
		"caption":             caption,
		"reply_to_message_id": c.MessageID,
		"reply_markup": telegram.JSON(replyMarkupInline([][]button{
			{callbackButton("📍مشاهده موقعیت GPS ثبت شده من", "profile;gps_show")},
			{callbackButton("🙍‍♂️🙎‍♀️ مخاطبین", "contacts;none"), callbackButton("❤️ لایک های من", "likes;none")},
			{callbackButton("🚫 بلاک شده ها", "blockeds;none"), callbackButton("🔕 سایلنت", "profile;silent;none")},
			{callbackButton("📝 ویرایش اطلاعات پروفایل", "profile;edit")},
		})),
	})
	if err != nil {
		return err
	}
	if !resp.Ok && c.User.Image != "" {
		_, _ = s.send(ctx, "sendPhoto", map[string]any{
			"chat_id":             c.UserID,
			"photo":               s.defaultProfilePhoto(c.User.Gender),
			"caption":             caption,
			"reply_to_message_id": c.MessageID,
		})
	}
	countMissing, info, inline := s.completeProfile(ctx, c.User)
	if countMissing > 0 {
		_, err = s.send(ctx, "sendMessage", map[string]any{
			"chat_id": c.UserID,
			"text": fmt.Sprintf("🔔 فقط %d قدم تا تکمیل پروفایل !\n\nاطلاعات تکمیل نشده ی شما :  %s\n\n<code>پروفایل خود را تکمیل کنید👇 و 5 سکه 💰 دریافت کنید.</code>",
				countMissing, info),
			"parse_mode":          "HTML",
			"reply_to_message_id": c.MessageID,
			"reply_markup":        telegram.JSON(replyMarkupInline(inline)),
		})
	}
	return err
}

func (s *Service) showUserProfile(ctx context.Context, c *UpdateContext, user storage.User, replyTo bool) error {
	statusChat := ""
	if strings.HasPrefix(user.Step, "chatting;") {
		statusChat = " (در حال چت)"
	}
	d := "موقعیت شما ثبت نشده!"
	if c.User.Latitude != 0 && user.Latitude == 0 {
		d = "موقعیت کاربر ثبت نشده!"
	} else if c.User.Latitude != 0 && user.Latitude != 0 {
		d = fmt.Sprintf("%.1fkm", distanceKM(c.User.Latitude, c.User.Longitude, user.Latitude, user.Longitude))
	}
	likes := user.FakeLikes
	if !user.IsFake {
		_ = s.store.DB().QueryRow(ctx, `SELECT count(*) FROM likes WHERE target_id=$1`, user.UserID).Scan(&likes)
	}
	params := map[string]any{
		"chat_id": c.UserID,
		"photo":   s.userProfilePhoto(ctx, user),
		"caption": fmt.Sprintf("• نام: %s\n• جنسیت: %s\n• استان: %s\n• شهر: %s\n• سن: %d\n\n♥️ لایک ها: %d\n\n%s%s\n\n‏🆔 آیدی : /user_%s\n\n\n🏁 فاصله از شما: %s",
			checkInout(user.Name), GenderWithEmoji[user.Gender], user.State, checkInout(user.City), user.Age, likes, lastActivity(c.Now, user.LastActivity), statusChat, user.UniqID, d),
		"reply_markup": telegram.JSON(replyMarkupInline(s.generateInlineButtons(ctx, c, user))),
	}
	if replyTo {
		params["reply_to_message_id"] = c.MessageID
	}
	resp, err := s.send(ctx, "sendPhoto", params)
	if err != nil {
		return err
	}
	if !resp.Ok && user.Image != "" {
		params["photo"] = s.defaultProfilePhoto(user.Gender)
		resp, err = s.send(ctx, "sendPhoto", params)
	}
	if err == nil && resp.Ok {
		s.notifyProfileView(ctx, c, user)
	}
	return err
}

func (s *Service) notifyProfileView(ctx context.Context, c *UpdateContext, target storage.User) {
	if target.IsFake || target.UserID == c.UserID {
		return
	}
	chat, err := s.store.ActiveChat(ctx, c.UserID)
	inConversation := err == nil && ((chat.UserID1 == c.UserID && chat.UserID2 == target.UserID) || (chat.UserID2 == c.UserID && chat.UserID1 == target.UserID))
	text := "کاربر " + GenderEmoji[c.User.Gender] + "/user_" + c.User.UniqID + " «پروفایل میوگرام» شما را #مشاهده کرد."
	if inConversation {
		text = "🤖 پیام سیستم 👇\n\n\nمخاطب شما «پروفایل میوگرام» شما را مشاهده کرد."
	}
	_, _ = s.send(ctx, "sendMessage", map[string]any{"chat_id": target.UserID, "text": text})
}

func (s *Service) generateInlineButtons(ctx context.Context, c *UpdateContext, user storage.User) [][]button {
	var exists bool
	_ = s.store.DB().QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM blocked WHERE user_id=$1 AND target_id=$2)`, c.UserID, user.UserID).Scan(&exists)
	block := "🔒 بلاک کردن کاربر"
	if exists {
		block = "🔐 آنبلاک کردن کاربر"
	}
	_ = s.store.DB().QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM friends WHERE user_id=$1 AND target_id=$2)`, c.UserID, user.UserID).Scan(&exists)
	contact := "➕افزودن به مخاطبین"
	if exists {
		contact = "❌حذف از مخاطبین"
	}
	kb := [][]button{}
	if user.IsLikes {
		count := user.FakeLikes
		if !user.IsFake {
			_ = s.store.DB().QueryRow(ctx, `SELECT count(*) FROM likes WHERE target_id=$1`, user.UserID).Scan(&count)
		}
		kb = append(kb, []button{callbackButton(fmt.Sprintf("Like ❤️ %d", count), "gib;likes;"+user.UniqID+";none")})
	}
	kb = append(kb, []button{
		callbackButton("📨 پیام دایرکت", "gib;directMessage;"+user.UniqID+";none"),
		callbackButton("💬 درخواست چت", "gib;requestChat;"+user.UniqID+";none"),
	})
	kb = append(kb, []button{
		callbackButton(block, "gib;block;"+user.UniqID+";none"),
		callbackButton(contact, "gib;friend;"+user.UniqID+";none"),
	})
	kb = append(kb, []button{callbackButton("🚫 گزارش کاربر", "gib;report;"+user.UniqID+";none")})
	if s.store.IsChatting(ctx, user.UserID) {
		kb = append(kb, []button{callbackButton("🔔 به محض اتمام چت اطلاع بده", "gib;endchatnotif;"+user.UniqID+";none")})
	} else if c.Now-user.LastActivity > 3600 {
		kb = append(kb, []button{callbackButton("🔔 به محض آنلاین شدن اطلاع بده", "gib;onlinenotif;"+user.UniqID+";none")})
	}
	return kb
}

func (s *Service) handleLikesList(ctx context.Context, c *UpdateContext) error {
	switch part(c.ExData, 1) {
	case "unactive":
		_, _ = s.store.DB().Exec(ctx, `UPDATE users SET is_likes=false WHERE user_id=$1`, c.UserID)
		_ = s.answer(ctx, c, "📴 بخش لایک پروفایل شما غیرفعال شد.\n\nدیگر برای کاربران قابل مشاهده نیست!")
		s.deleteMessage(ctx, c.UserID, c.MessageID)
		return nil
	case "active":
		_, _ = s.store.DB().Exec(ctx, `UPDATE users SET is_likes=true WHERE user_id=$1`, c.UserID)
		_ = s.answer(ctx, c, "✅ بخش لایک پروفایل شما فعال شد.")
		s.deleteMessage(ctx, c.UserID, c.MessageID)
		return nil
	}
	if !c.User.IsLikes {
		_, err := s.send(ctx, "sendMessage", map[string]any{
			"chat_id": c.UserID,
			"text": "📴 بخش لایک پروفایل شما غیر فعال است!\n\n" +
				"<code>    - برای کاربران قابل مشاهده نیست.</code>\n\n\n" +
				"برای فعال سازی گزینه (✅ فعال سازی) را لمس کنید 👇",
			"parse_mode":   "HTML",
			"reply_markup": telegram.JSON(replyMarkupInline([][]button{{callbackButton("✅ فعال سازی", "likes;active")}})),
		})
		return err
	}
	page := 1
	if part(c.ExData, 1) != "none" {
		page = parseInt(part(c.ExData, 1))
	}
	step := 10
	offset := (page - 1) * step
	rows, err := s.store.DB().Query(ctx, `SELECT user_id FROM likes WHERE target_id=$1 ORDER BY id DESC LIMIT $2 OFFSET $3`, c.UserID, step, offset)
	if err != nil {
		return err
	}
	defer rows.Close()
	list := []struct{ UserID, Name string }{}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return err
		}
		list = append(list, struct{ UserID, Name string }{id, ""})
	}
	if len(list) == 0 {
		if part(c.ExData, 1) == "none" {
			return s.answer(ctx, c, "⚠️ توجه: تاکنون کاربری پروفایل شما را لایک نکرده است.")
		}
		return s.answer(ctx, c, "⚠️ صفحه دیگری وجود ندارد.")
	}
	var total int
	_ = s.store.DB().QueryRow(ctx, `SELECT count(*) FROM likes WHERE target_id=$1`, c.UserID).Scan(&total)
	txt, _ := s.userInfoList(ctx, c, list, offset+1)
	return s.listShow(ctx, c,
		"👥 لیست کاربرانی که پروفایل شما را لایکـ❤️ کرده اند در زیر آمده است.\n\n"+txt+"\n\n➖ برای حذف دکمه لایک از پروفایلتان میتوانید این بخش را را کلیک روی دکمه غیر فعال سازی بخش لایک غیر فعال کنید. 👇",
		"likes", total, page, step,
		[]button{callbackButton("📴 غیر فعال سازی بخش  لایکـ❤️", "likes;unactive")},
		nil,
	)
}

func (s *Service) handleContactsList(ctx context.Context, c *UpdateContext) error {
	page := 1
	if part(c.ExData, 1) != "none" {
		page = parseInt(part(c.ExData, 1))
	}
	step := 10
	offset := (page - 1) * step
	rows, err := s.store.DB().Query(ctx, `SELECT target_id,coalesce(name,'') FROM friends WHERE user_id=$1 ORDER BY id DESC LIMIT $2 OFFSET $3`, c.UserID, step, offset)
	if err != nil {
		return err
	}
	defer rows.Close()
	list := []struct{ UserID, Name string }{}
	for rows.Next() {
		var row struct{ UserID, Name string }
		if err := rows.Scan(&row.UserID, &row.Name); err != nil {
			return err
		}
		list = append(list, row)
	}
	if len(list) == 0 {
		if part(c.ExData, 1) == "none" {
			return s.answer(ctx, c, "⚠️ توجه: لیست مخاطبین شما خالی میباشد.")
		}
		return s.answer(ctx, c, "⚠️ صفحه دیگری وجود ندارد.")
	}
	var total int
	_ = s.store.DB().QueryRow(ctx, `SELECT count(*) FROM friends WHERE user_id=$1`, c.UserID).Scan(&total)
	txt, _ := s.userInfoList(ctx, c, list, offset+1)
	return s.listShow(ctx, c, "🙎‍♂️🙎‍♀️ لیست مخاطبین شما\n\n"+txt+"\n➖ برای حذف کاربر روی پروفایل کاربر گزینه «حذف از مخاطبین» را بزنید.\n\n🗑 حذف همه مخاطبین : /deleteAllContacts", "contacts", total, page, step, nil, nil)
}

func (s *Service) handleBlockedList(ctx context.Context, c *UpdateContext) error {
	page := 1
	if part(c.ExData, 1) != "none" {
		page = parseInt(part(c.ExData, 1))
	}
	step := 10
	offset := (page - 1) * step
	rows, err := s.store.DB().Query(ctx, `SELECT target_id FROM blocked WHERE user_id=$1 ORDER BY id DESC LIMIT $2 OFFSET $3`, c.UserID, step, offset)
	if err != nil {
		return err
	}
	defer rows.Close()
	list := []struct{ UserID, Name string }{}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return err
		}
		list = append(list, struct{ UserID, Name string }{id, ""})
	}
	if len(list) == 0 {
		if part(c.ExData, 1) == "none" {
			return s.answer(ctx, c, "⚠️ توجه: لیست کاربران مسدود شده شما خالی میباشد.")
		}
		return s.answer(ctx, c, "⚠️ صفحه دیگری وجود ندارد.")
	}
	var total int
	_ = s.store.DB().QueryRow(ctx, `SELECT count(*) FROM blocked WHERE user_id=$1`, c.UserID).Scan(&total)
	txt, _ := s.userInfoList(ctx, c, list, offset+1)
	return s.listShow(ctx, c, "👥 لیست کاربران بلاک شده\n\n"+txt+"\n➖ برای حذف کاربر روی پروفایل کاربر گزینه «آنبلاک کردن کاربر» را بزنید.\n\n🗑 حذف همه بلاک شده ها : /deleteAllBlocks", "blockeds", total, page, step, nil, nil)
}

func (s *Service) handleProfileEditInput(ctx context.Context, c *UpdateContext) (bool, error) {
	// Handle Back button - return to main menu
	if c.Text == BackButton {
		_ = s.store.UpdateUserStep(ctx, c.UserID, "start")
		return true, s.mainMenu(ctx, c, true)
	}

	if part(c.ExStep, 1) != "edit" {
		return false, nil
	}
	switch part(c.ExStep, 2) {
	case "set_name":
		if !validPersianName(c.Text) {
			_, err := s.send(ctx, "sendMessage", map[string]any{
				"chat_id":             c.UserID,
				"text":                "⚠️ خطا: لطفا فقط از حروف فارسی استفاده کنید.",
				"reply_to_message_id": c.MessageID,
				"reply_markup": telegram.JSON(replyMarkupInline([][]button{
					{callbackButton("بیخیال ✏️ تغییر نام", "start")},
				})),
			})
			return true, err
		}
		_, err := s.store.DB().Exec(ctx, `UPDATE users SET name=$2,step='start' WHERE user_id=$1`, c.UserID, c.Text)
		if err != nil {
			return true, err
		}
		if c.User.PrevStep != "complete_profile" {
			_, err = s.send(ctx, "sendMessage", map[string]any{
				"chat_id":             c.UserID,
				"text":                "✏️ تغییر نام با موفقیت انجام شد ✅\n\n" + mainMenuText(),
				"reply_to_message_id": c.MessageID,
				"parse_mode":          "HTML",
				"reply_markup":        telegram.JSON(replyMarkupKeyboard(mainMenuKeyboard(s.isAdmin(c)))),
			})
			if err != nil {
				return true, err
			}
		}
		_, err = s.checkProfileCoin(ctx, c)
		return true, err
	case "set_age":
		age := parseInt(c.Text)
		if age < 9 || age > 99 {
			_, err := s.send(ctx, "sendMessage", map[string]any{
				"chat_id":             c.UserID,
				"text":                "⚠️ خطا : ورودی باید بصورت اعداد باشد.",
				"reply_to_message_id": c.MessageID,
				"reply_markup": telegram.JSON(replyMarkupInline([][]button{
					{callbackButton("بیخیال ✏️ تغییر سن", "start")},
				})),
			})
			return true, err
		}
		_, err := s.store.DB().Exec(ctx, `UPDATE users SET age=$2,step='start' WHERE user_id=$1`, c.UserID, age)
		if err == nil {
			_, err = s.send(ctx, "sendMessage", map[string]any{
				"chat_id":             c.UserID,
				"text":                "✏️ تغییر سن با موفقیت انجام شد ✅\n\n" + mainMenuText(),
				"reply_to_message_id": c.MessageID,
				"parse_mode":          "HTML",
				"reply_markup":        telegram.JSON(replyMarkupKeyboard(mainMenuKeyboard(s.isAdmin(c)))),
			})
		}
		if err == nil {
			_, err = s.checkProfileCoin(ctx, c)
		}
		return true, err
	case "set_gender":
		gender := part(c.ExData, 1)
		if gender != "boy" && gender != "girl" {
			_, err := s.send(ctx, "sendMessage", map[string]any{
				"chat_id":             c.UserID,
				"text":                "⚠️ خطا: فقط یکی از گزینه های زیر را انتخاب کنید 👇",
				"reply_to_message_id": c.MessageID,
				"reply_markup": telegram.JSON(replyMarkupInline([][]button{
					{callbackButton("من🙍‍♂️پسرم", "set_gender;boy"), callbackButton("من🙎‍♀️دخترم", "set_gender;girl")},
					{callbackButton("بیخیال ✏️ تغییر جنسیت", "start")},
				})),
			})
			return true, err
		}
		_, err := s.store.DB().Exec(ctx, `UPDATE users SET gender=$2,step='start' WHERE user_id=$1`, c.UserID, gender)
		if err == nil {
			_, err = s.send(ctx, "sendMessage", map[string]any{
				"chat_id":             c.UserID,
				"text":                "✏️ تغییر جنسیت با موفقیت انجام شد ✅\n\n" + mainMenuText(),
				"reply_to_message_id": c.MessageID,
				"parse_mode":          "HTML",
				"reply_markup":        telegram.JSON(replyMarkupKeyboard(mainMenuKeyboard(s.isAdmin(c)))),
			})
		}
		if err == nil {
			_, err = s.checkProfileCoin(ctx, c)
		}
		return true, err
	case "set_state":
		if !s.store.StateExists(ctx, c.Text, 0) {
			_, err := s.send(ctx, "sendMessage", map[string]any{
				"chat_id":             c.UserID,
				"text":                "⚠️ خطا : ورودی باید بصورت اعداد باشد.",
				"reply_to_message_id": c.MessageID,
				"reply_markup": telegram.JSON(replyMarkupInline([][]button{
					{callbackButton("بیخیال ✏️ تغییر استان", "start")},
				})),
			})
			return true, err
		}
		_, err := s.store.DB().Exec(ctx, `UPDATE users SET state=$2,step='start' WHERE user_id=$1`, c.UserID, c.Text)
		if err == nil {
			_, err = s.send(ctx, "sendMessage", map[string]any{
				"chat_id":             c.UserID,
				"text":                "✏️ تغییر استان با موفقیت انجام شد ✅\n\n" + mainMenuText(),
				"reply_to_message_id": c.MessageID,
				"parse_mode":          "HTML",
				"reply_markup":        telegram.JSON(replyMarkupKeyboard(mainMenuKeyboard(s.isAdmin(c)))),
			})
		}
		if err == nil {
			_, err = s.checkProfileCoin(ctx, c)
		}
		return true, err
	case "set_city":
		parent, _ := s.store.ParentStateID(ctx, c.User.State)
		if !s.store.StateExists(ctx, c.Text, parent) {
			_, err := s.send(ctx, "sendMessage", map[string]any{
				"chat_id":             c.UserID,
				"text":                "⚠️ خطا : ورودی باید بصورت اعداد باشد.",
				"reply_to_message_id": c.MessageID,
				"reply_markup": telegram.JSON(replyMarkupInline([][]button{
					{callbackButton("بیخیال ✏️ تغییر شهر", "start")},
				})),
			})
			return true, err
		}
		_, err := s.store.DB().Exec(ctx, `UPDATE users SET city=$2,step='start' WHERE user_id=$1`, c.UserID, c.Text)
		if err == nil && c.User.PrevStep != "complete_profile" {
			_, err = s.send(ctx, "sendMessage", map[string]any{
				"chat_id":             c.UserID,
				"text":                "✏️ تغییر شهر با موفقیت انجام شد ✅\n\n" + mainMenuText(),
				"reply_to_message_id": c.MessageID,
				"parse_mode":          "HTML",
				"reply_markup":        telegram.JSON(replyMarkupKeyboard(mainMenuKeyboard(s.isAdmin(c)))),
			})
		}
		if err == nil {
			_, err = s.checkProfileCoin(ctx, c)
		}
		return true, err
	case "set_location":
		if c.Message == nil || c.Message.Location == nil {
			_, err := s.send(ctx, "sendMessage", map[string]any{
				"chat_id":             c.UserID,
				"text":                "⚠️ خطا : ورودی باید بصورت لوکیشن GPS باشد.",
				"reply_to_message_id": c.MessageID,
				"reply_markup": telegram.JSON(replyMarkupInline([][]button{
					{callbackButton("بیخیال ✏️ تغییر موقعیت", "start")},
				})),
			})
			return true, err
		}
		_, err := s.store.DB().Exec(ctx, `UPDATE users SET latitude=$2,longitude=$3,step='start' WHERE user_id=$1`, c.UserID, c.Message.Location.Latitude, c.Message.Location.Longitude)
		if err == nil && c.User.PrevStep != "complete_profile" {
			_, err = s.send(ctx, "sendMessage", map[string]any{
				"chat_id":             c.UserID,
				"text":                "✏️ تغییر موقعیت با موفقیت انجام شد ✅\n\n" + mainMenuText(),
				"reply_to_message_id": c.MessageID,
				"parse_mode":          "HTML",
				"reply_markup":        telegram.JSON(replyMarkupKeyboard(mainMenuKeyboard(s.isAdmin(c)))),
			})
		}
		if err == nil {
			_, err = s.checkProfileCoin(ctx, c)
		}
		return true, err

	case "set_image":
		fileID := photoID(c.Message)
		if fileID == "" {
			_, err := s.send(ctx, "sendMessage", map[string]any{
				"chat_id":             c.UserID,
				"text":                "⚠️ خطا: ورودی باید بصورت عکس باشد.",
				"reply_to_message_id": c.MessageID,
				"reply_markup": telegram.JSON(replyMarkupInline([][]button{
					{callbackButton("بیخیال ✏️ تغییر عکس", "start")},
				})),
			})
			return true, err
		}

		// ۱. دانلود و ذخیره تصویر در فایل سیستم محلی
		profilePath := s.downloadAndSaveProfilePhoto(ctx, fileID, c.UserID)
		if profilePath == "" {
			_, err := s.send(ctx, "sendMessage", map[string]any{
				"chat_id":             c.UserID,
				"text":                "⚠️ خطا: در ذخیره تصویر مشکلی پیش آمد. لطفاً دوباره تلاش کنید.",
				"reply_to_message_id": c.MessageID,
				"reply_markup": telegram.JSON(replyMarkupInline([][]button{
					{callbackButton("بیخیال ✏️ تغییر عکس", "start")},
				})),
			})
			return true, err
		}

		// ۲. ذخیره file_id در دیتابیس (برای مرجع)
		_, err := s.store.DB().Exec(ctx, `UPDATE users SET image=$2 WHERE user_id=$1`, c.UserID, fileID)
		if err != nil {
			return true, err
		}

		// ۳. ارسال تصویر به گروه ادمین با دکمه بن (از فایل محلی)
		adminGroupID := s.adminGroupID(ctx)
		if adminGroupID != "" {
			_, sendErr := s.send(ctx, "sendPhoto", map[string]any{
				"chat_id":           adminGroupID,
				"message_thread_id": adminTopicProfile,
				"photo":             telegram.LocalFile{Path: profilePath},
				"caption": fmt.Sprintf("🖼 تصویر پروفایل جدید\n\nکاربر: <a href='tg://user?id=%s'>%s</a>\nشناسه: /user_%s",
					c.UserID, c.UserID, c.User.UniqID),
				"parse_mode": "HTML",
				"reply_markup": telegram.JSON(replyMarkupInline([][]button{
					{callbackButton("🚫 بن کاربر", fmt.Sprintf("profile_ban;%s", c.UserID))},
				})),
			})
			_ = sendErr
		}

		// ۴. به‌روزرسانی مرحله کاربر
		_ = s.store.UpdateUserStepPrev(ctx, c.UserID, "start", "start")
		c.User.Step = "start"
		c.User.PrevStep = "start"
		c.refreshStep()
		_ = s.reloadUser(ctx, c)

		// ۵. پیام موفقیت به کاربر
		_, err = s.send(ctx, "sendMessage", map[string]any{
			"chat_id":      c.UserID,
			"text":         "✅ تصویر شما با موفقیت ثبت شد.",
			"reply_markup": telegram.JSON(replyMarkupKeyboard(mainMenuKeyboard(s.isAdmin(c)))),
		})
		if err != nil {
			return true, err
		}

		// ۶. اهدای سکه در صورت تکمیل پروفایل
		_, err = s.checkProfileCoin(ctx, c)
		return true, err
	}
	return false, nil
}

func (s *Service) handleSilent(ctx context.Context, c *UpdateContext) error {
	method := "sendMessage"
	silent := c.User.Silent
	if part(c.ExData, 2) != "none" && c.Text != "/silent" {
		method = "editMessageText"
		if part(c.ExData, 2) == "0" {
			silent = 0
		} else {
			silent = c.Now + parseInt64(part(c.ExData, 2))
		}
		_, _ = s.store.DB().Exec(ctx, `UPDATE users SET silent=$2 WHERE user_id=$1`, c.UserID, silent)
	}
	kb := [][]button{
		{callbackButton("🔕 سایلنت تا یک ساعت", "profile;silent;3600"), callbackButton("🔕 سایلنت تا 20 دقیقه", "profile;silent;1200")},
		{callbackButton("🔕 همیشه سایلنت", "profile;silent;126144000")},
	}
	status := "🔔 غیر فعال"
	if silent != 0 {
		status = "🔕 فعال تا " + toEnglish(jdate(s.loc, "Y-m-d H:i", silent))
		kb = append(kb, []button{callbackButton("🔔 غیر فعال کردن سایلنت", "profile;silent;0")})
	}
	params := map[string]any{
		"chat_id":      c.UserID,
		"text":         "🔻 حالت سایلنت : " + status + "\n\n_____________________\n<code> 💡با فعال شدن حالت سایلنت ، درخواست چت دریافت نخواهید کرد.</code>",
		"parse_mode":   "HTML",
		"reply_markup": telegram.JSON(replyMarkupInline(kb)),
	}
	if method == "editMessageText" {
		params["message_id"] = c.MessageID
	}
	_, err := s.send(ctx, method, params)
	return err
}

func (s *Service) handleProfileCallback(ctx context.Context, c *UpdateContext) error {
	switch part(c.ExData, 1) {
	case "gps_show":
		if c.User.Latitude == 0 {
			_, err := s.send(ctx, "sendMessage", map[string]any{
				"chat_id":             c.UserID,
				"text":                "⚠️ خطا : شما موقعیت مکانی خود را ثبت نکرده اید.\n\nبا زدن گزینه 📍 ثبت موقعیت GPS  ، موقعیت خود را ثبت کرده و 5 سکه 💰 دریافت کنید.👇",
				"reply_to_message_id": c.MessageID,
				"parse_mode":          "HTML",
				"reply_markup": telegram.JSON(replyMarkupInline([][]button{
					{callbackButton("📍 ثبت مجدد موقعیت GPS", "profile;gps")},
				})),
			})
			return err
		}
		_, err := s.send(ctx, "sendLocation", map[string]any{
			"chat_id":             c.UserID,
			"latitude":            c.User.Latitude,
			"longitude":           c.User.Longitude,
			"reply_to_message_id": c.MessageID,
			"reply_markup": telegram.JSON(replyMarkupInline([][]button{
				{callbackButton("📍 ثبت مجدد موقعیت GPS", "profile;gps")},
			})),
		})
		return err
	case "edit":
		_, err := s.send(ctx, "editMessageReplyMarkup", map[string]any{
			"chat_id":    c.UserID,
			"message_id": c.MessageID,
			"reply_markup": telegram.JSON(replyMarkupInline([][]button{
				{callbackButton("✏️ تغییر جنسیت", "profile;gender"), callbackButton("✏️ تغییر نام", "profile;name")},
				{callbackButton("✏️ تغییر شهر", "profile;city"), callbackButton("✏️ تغییر سن", "profile;age")},
				{callbackButton("✏️ تغییر استان", "profile;state"), callbackButton("✏️ تغییر عکس", "profile;image")},
				{callbackButton("✏️ تغییر موقعیت GPS", "profile;gps")},
			})),
		})
		return err
	case "name":
		return s.askProfileName(ctx, c)
	case "gender":
		_ = s.store.UpdateUserStep(ctx, c.UserID, "profile;edit;set_gender")
		_, err := s.send(ctx, "sendMessage", map[string]any{
			"chat_id": c.UserID,
			"text":    "❓ لطفا جنسیت خود را انتخاب کنید 👇",
			"reply_markup": telegram.JSON(replyMarkupInline([][]button{
				{callbackButton("من🙍‍♂️پسرم", "set_gender;boy"), callbackButton("من🙎‍♀️دخترم", "set_gender;girl")},
			})),
		})
		return err
	case "state":
		states, _ := s.store.StateNames(ctx, 0)
		_ = s.store.UpdateUserStep(ctx, c.UserID, "profile;edit;set_state")
		_, err := s.send(ctx, "sendMessage", map[string]any{
			"chat_id":      c.UserID,
			"text":         "❓ لطفا استان خود را انتخاب کنید 👇",
			"reply_markup": telegram.JSON(replyMarkupKeyboard(statesReplyKeyboard(states, false))),
		})
		return err
	case "city":
		return s.askProfileCity(ctx, c)
	case "age":
		_ = s.store.UpdateUserStep(ctx, c.UserID, "profile;edit;set_age")
		_, err := s.send(ctx, "sendMessage", map[string]any{
			"chat_id":      c.UserID,
			"text":         "❓ لطفا عدد سن خود را انتخاب کنید 👇",
			"reply_markup": telegram.JSON(replyMarkupKeyboard(ageKeyboard())),
		})
		return err
	case "image":
		_ = s.store.UpdateUserStep(ctx, c.UserID, "profile;edit;set_image")
		_, err := s.send(ctx, "sendMessage", map[string]any{
			"chat_id":      c.UserID,
			"text":         "❓ لطفا عکس پروفایل خود را ارسال کنید.",
			"reply_markup": telegram.JSON(removeKeyboard()),
		})
		return err
	case "gps":
		return s.askProfileGPS(ctx, c)
	}
	if c.User.PrevStep == "complete_profile" {
		switch nextProfileCompletionField(c.User) {
		case "name":
			return s.askProfileName(ctx, c)
		case "city":
			return s.askProfileCity(ctx, c)
		case "image":
			_ = s.store.UpdateUserStep(ctx, c.UserID, "profile;edit;set_image")
			_, err := s.send(ctx, "sendMessage", map[string]any{
				"chat_id":      c.UserID,
				"text":         "❓ لطفا عکس پروفایل خود را ارسال کنید.",
				"reply_markup": telegram.JSON(removeKeyboard()),
			})
			return err
		case "gps":
			return s.askProfileGPS(ctx, c)
		}
	}
	return nil
}

func nextProfileCompletionField(user storage.User) string {
	if user.Name == "" {
		return "name"
	}
	if user.City == "" {
		return "city"
	}
	if user.Image == "" {
		return "image"
	}
	if user.Latitude == 0 {
		return "gps"
	}
	return ""
}

func (s *Service) askProfileName(ctx context.Context, c *UpdateContext) error {
	_ = s.store.UpdateUserStep(ctx, c.UserID, "profile;edit;set_name")
	_, err := s.send(ctx, "sendMessage", map[string]any{
		"chat_id":      c.UserID,
		"text":         "❓ لطفا نام خود را به صورت متن ارسال کنید.",
		"reply_markup": telegram.JSON(replyMarkupKeyboard([][]button{{textButton(BackButton)}})),
	})
	return err
}

func (s *Service) askProfileCity(ctx context.Context, c *UpdateContext) error {
	parent, _ := s.store.ParentStateID(ctx, c.User.State)
	states, _ := s.store.StateNames(ctx, parent)
	_ = s.store.UpdateUserStep(ctx, c.UserID, "profile;edit;set_city")
	_, err := s.send(ctx, "sendMessage", map[string]any{
		"chat_id":      c.UserID,
		"text":         "❓ لطفا شهر خود را انتخاب کنید 👇",
		"reply_markup": telegram.JSON(replyMarkupKeyboard(statesReplyKeyboard(states, true))),
	})
	return err
}

func (s *Service) askProfileGPS(ctx context.Context, c *UpdateContext) error {
	s.deleteMessage(ctx, c.UserID, c.MessageID)
	_ = s.store.UpdateUserStep(ctx, c.UserID, "profile;edit;set_location")
	_, _ = s.send(ctx, "sendMessage", map[string]any{
		"chat_id": c.UserID,
		"text": "⚠️ توجه کنید : با توجه به این که پروفایل کاربران به صورت عمومی قابل مشاهده است ، در صورت رعایت نکردن قوانین زیر حساب کاربری شما بصورت دائمی مسدود خواهد شد.\n\n" +
			"1️⃣ هرگونه محتوای غیر اخلاقی یا توهین آمیز در پروفایل ( عکس یا متن )\n\n" +
			"2️⃣ پخش شماره موبایل یا اطلاعات شخصی دیگران\n\n" +
			"3️⃣ تبلیغات کانال ، ربات و یا سایت\n\n👇👇👇",
		"parse_mode": "HTML",
	})
	_, err := s.send(ctx, "sendMessage", map[string]any{
		"chat_id": c.UserID,
		"text": "⚠️ هنگام ارسال موقعیت مکانی مطمعن شوید GPS موبایل شما روشن است.\n\n" +
			"✅ کسی قادر به دیدن موقعیت مکانی شما در ربات نخواهد بود و فقط برای تخمین فاصله و یافتن افراد نزدیک کاربرد خواهد داشت\n\n" +
			"❓موقعیت GPS خود را ارسال کنید👇",
		"parse_mode":               "HTML",
		"disable_web_page_preview": true,
		"reply_markup": telegram.JSON(replyMarkupKeyboard([][]button{
			{{"text": "📍ارسال موقعیت جی پی اس", "request_location": true}},
			{textButton(BackButton)},
		})),
	})
	return err
}

func (s *Service) handleGIBCallback(ctx context.Context, c *UpdateContext) error {
	user, err := s.store.UserByUniqOrID(ctx, part(c.ExData, 2))
	if err != nil {
		s.deleteMessage(ctx, c.UserID, c.MessageID)
		return nil
	}
	switch part(c.ExData, 1) {
	case "likes":
		return s.gibLike(ctx, c, user)
	case "friend":
		return s.gibFriend(ctx, c, user)
	case "requestChat":
		return s.gibRequestChat(ctx, c, user)
	case "accept":
		return s.gibAcceptChat(ctx, c, user)
	case "reject":
		return s.gibRejectChat(ctx, c, user)
	case "directMessage":
		return s.gibDirectCallback(ctx, c, user)
	case "block":
		return s.gibBlock(ctx, c, user)
	case "report":
		return s.gibReportCallback(ctx, c, user)
	case "onlinenotif":
		return s.gibSimpleNotif(ctx, c, user, "onlinenotif")
	case "endchatnotif":
		return s.gibSimpleNotif(ctx, c, user, "endchatnotif")
	}
	return nil
}

func (s *Service) gibLike(ctx context.Context, c *UpdateContext, user storage.User) error {
	var blocked bool
	_ = s.store.DB().QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM blocked WHERE user_id=$1 AND target_id=$2)`, user.UserID, c.UserID).Scan(&blocked)
	if blocked {
		return s.answer(ctx, c, "⚠️ خطا: شما توسط این کاربر بلاک شده اید.")
	}
	var exists bool
	_ = s.store.DB().QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM likes WHERE user_id=$1 AND target_id=$2)`, c.UserID, user.UserID).Scan(&exists)
	if exists {
		// حذف لایک
		_, _ = s.store.DB().Exec(ctx, `DELETE FROM likes WHERE user_id=$1 AND target_id=$2`, c.UserID, user.UserID)
		if user.IsFake {
			_, _ = s.store.DB().Exec(ctx, `UPDATE users SET fake_likes = GREATEST(fake_likes - 1, 0) WHERE user_id=$1`, user.UserID)
			user.FakeLikes--
			if user.FakeLikes < 0 {
				user.FakeLikes = 0
			}
		}
		_ = s.answer(ctx, c, "لایکـ❤️ کاربر حذف شد!")
	} else {
		// افزودن لایک
		_, _ = s.store.DB().Exec(ctx, `INSERT INTO likes (user_id,target_id,created_at) VALUES ($1,$2,$3) ON CONFLICT DO NOTHING`, c.UserID, user.UserID, c.Now)
		if user.IsFake {
			_, _ = s.store.DB().Exec(ctx, `UPDATE users SET fake_likes = fake_likes + 1 WHERE user_id=$1`, user.UserID)
			user.FakeLikes++
		}
		_ = s.answer(ctx, c, "کاربر لایکـ❤️ شد!")
		if !user.IsFake {
			_, _ = s.send(ctx, "sendMessage", map[string]any{
				"chat_id":    user.UserID,
				"text":       "کاربر /user_" + c.User.UniqID + " پروفایل شما رو پسندید ❤️\n\n❗️ اگر کاربر برای شما مزاحمت ایجاد کرده می توانید کاربر را بلاک کنید",
				"parse_mode": "HTML",
			})
		}
	}
	// بازسازی دکمه‌ها با user.FakeLikes به‌روز
	_, err := s.send(ctx, "editMessageReplyMarkup", map[string]any{
		"chat_id":      c.UserID,
		"message_id":   c.MessageID,
		"reply_markup": telegram.JSON(replyMarkupInline(s.generateInlineButtons(ctx, c, user))),
	})
	return err
}

func (s *Service) gibFriend(ctx context.Context, c *UpdateContext, user storage.User) error {
	var exists bool
	_ = s.store.DB().QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM friends WHERE user_id=$1 AND target_id=$2)`, c.UserID, user.UserID).Scan(&exists)
	if exists {
		_, _ = s.store.DB().Exec(ctx, `DELETE FROM friends WHERE user_id=$1 AND target_id=$2`, c.UserID, user.UserID)
		s.deleteMessage(ctx, c.UserID, c.MessageID)
		return nil
	}
	_ = s.store.UpdateUserStepPrev(ctx, c.UserID, c.Data, "start")
	_, err := s.send(ctx, "sendMessage", map[string]any{
		"chat_id":      c.UserID,
		"text":         "👤شما در حال ذخیره کردن کاربر  در لیست مخاطبین خود هستید.\n\n            در صورت تمایل برای اینکار عنوانی که بعدا بتوانید این کاربر را بیاد آورید ارسال کنید یا در صورت عدم تمایل از منوی پایین روی گزینه 《 بازگشت 🔙 》 کلیک کنید.",
		"parse_mode":   "HTML",
		"reply_markup": telegram.JSON(replyMarkupKeyboard([][]button{{textButton(BackButton)}})),
	})
	return err
}

func (s *Service) gibRequestChat(ctx context.Context, c *UpdateContext, user storage.User) error {
	var blocked bool
	_ = s.store.DB().QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM blocked WHERE user_id=$1 AND target_id=$2)`, user.UserID, c.UserID).Scan(&blocked)
	if blocked {
		return s.answer(ctx, c, "⚠️ خطا: شما توسط این کاربر بلاک شده اید.")
	}
	if s.store.IsChatting(ctx, user.UserID) {
		return s.answer(ctx, c, "⚠️ خطا: امکان ارسال درخواست چد وجود ندارد.\n\nکاربر در حال چت است.")
	}
	if s.store.IsChatting(ctx, c.UserID) {
		return s.answer(ctx, c, "⚠️ خطا: امکان ارسال درخواست چد وجود ندارد.\n\nشما هم اکنون در حال چت هستید.")
	}
	if c.User.Balance < 2 {
		_, _ = s.send(ctx, "sendMessage", map[string]any{
			"chat_id": c.UserID,
			"text": fmt.Sprintf("⚠️ توجه : شما سکه کافی ندارید !  (2 سکه مورد نیاز)\n\n<code>💡 برای بدست آوردن سکه میتونی رباتو به دوستات معرفی کنی و به ازای معرفی هر نفر %d .</code>",
				c.Admin.CoinPerInvite+c.Admin.CoinPerInviteProfile+c.Admin.CoinPerInviteInvite),
			"parse_mode": "HTML",
			"reply_markup": telegram.JSON(replyMarkupInline([][]button{
				{callbackButton("🚸 معرفی به دوستان (سکه رایگان)", "invite")},
			})),
		})
		if !c.User.IsCoinComplete {
			count, _, inline := s.completeProfile(ctx, c.User)
			if count > 0 {
				_, _ = s.send(ctx, "sendMessage", map[string]any{
					"chat_id":      c.UserID,
					"text":         "🔔 سکه نداری؟ 😕 میخوای سکه رایگان بهت بدم؟ 😎\n\n    فقط کافیه پروفایلتو تکمیل کنی! تا سکه هدیه بگیری! 😍👇",
					"parse_mode":   "HTML",
					"reply_markup": telegram.JSON(replyMarkupInline(inline)),
				})
			}
		}
		return nil
	}
	if user.LastActivity+3600 < c.Now {
		return s.answer(ctx, c, "⚠️ خطا: فقط امکان ارسال درخواست چت به کاربرانی که در 15 دقیقه اخیر آنلاین بوده اند وجود دارد.\n\nدر حال حاضر میتوانید برای ین کاربر 📨پیام دایرکت ارسال نمایید.")
	}
	if user.Silent > c.Now {
		return s.answer(ctx, c, "⚠️ خطا: حالت سایلنت برای این کاربر فعال است.\nامکان ارسال درخواست چت تا "+toEnglish(jdate(s.loc, "Y:m:d H:i", user.Silent))+" وجود ندارد\n\n💡 شما می توانید برای این کاربر پیام دایرکت ارسال کنید.")
	}
	var notif storage.Notification
	err := s.store.DB().QueryRow(ctx, `SELECT id,date FROM notif WHERE user_id=$1 AND user_id_2=$2 AND reason='request_chat' AND status='doing' AND ($3-date)<120 ORDER BY id DESC LIMIT 1`, c.UserID, user.UserID, c.Now).Scan(&notif.ID, &notif.Date)
	if err == nil {
		return s.answer(ctx, c, "⚠️ خطا: شما در 2 دقیقه اخیر یک درخواست چت به این کاربر ارسال کرده اید.\n\n⏳ لطفا "+waitText(120-(c.Now-notif.Date))+" دیگر صبر کنید.")
	}
	err = s.store.DB().QueryRow(ctx, `INSERT INTO notif (user_id,balance,user_id_2,reason,status,date) VALUES ($1,2,$2,'request_chat','doing',$3) RETURNING id`, c.UserID, user.UserID, c.Now).Scan(&notif.ID)
	if err != nil {
		return err
	}
	_, _ = s.send(ctx, "sendMessage", map[string]any{
		"chat_id": c.UserID,
		"text":    "✅ درخواست چت شما برای /user_" + user.UniqID + " ارسال شد.\n\n🚶منتظر باش و اگه تا دو دقیقه تایید نکرد درخواست چت لغو میشه...",
	})
	if !user.IsFake {
		_, err = s.send(ctx, "sendMessage", map[string]any{
			"chat_id":    user.UserID,
			"text":       "🔔درخواست چت از طرف /user_" + c.User.UniqID + " را میپذیرید؟\n\n<code>- شما تا دو دقیقه پس از ارسال این پیام میتوانید درخواست چت را تایید کنید.</code>\n\n<u>💡با فعال کردن حالت سایلنت ، کسی امکان درخواست چت به شما را نخواهد داشت 👈 /silent</u>",
			"parse_mode": "HTML",
			"reply_markup": telegram.JSON(replyMarkupInline([][]button{
				{callbackButton("✅ قبول درخواست", fmt.Sprintf("gib;accept;%s;%d", c.User.UniqID, notif.ID))},
				{callbackButton("👎 رد درخواست", fmt.Sprintf("gib;reject;%s;%d", c.User.UniqID, notif.ID))},
			})),
		})
	}
	return err
}

func (s *Service) gibAcceptChat(ctx context.Context, c *UpdateContext, requester storage.User) error {
	if s.store.IsChatting(ctx, requester.UserID) {
		return s.answer(ctx, c, "⚠️ خطا: امکان ارسال درخواست چد وجود ندارد.\n\nکاربر در حال چت است.")
	}
	if s.store.IsChatting(ctx, c.UserID) {
		return s.answer(ctx, c, "⚠️ خطا: امکان ارسال درخواست چد وجود ندارد.\n\nشما هم اکنون در حال چت هستید.")
	}
	if requester.Silent > c.Now {
		return s.answer(ctx, c, "⚠️ خطا: حالت سایلنت برای این کاربر فعال است.\nامکان ارسال درخواست چت تا "+toEnglish(jdate(s.loc, "Y:m:d H:i", requester.Silent))+" وجود ندارد\n\n💡 شما می توانید برای این کاربر پیام دایرکت ارسال کنید.")
	}
	notifID := parseInt64(part(c.ExData, 3))
	var balance int
	var date int64
	err := s.store.DB().QueryRow(ctx, `SELECT balance,date FROM notif WHERE id=$1`, notifID).Scan(&balance, &date)
	if err != nil {
		s.deleteMessage(ctx, c.UserID, c.MessageID)
		return nil
	}
	if c.Now-date > 120 {
		_, _ = s.store.DB().Exec(ctx, `UPDATE notif SET status='end' WHERE id=$1`, notifID)
		s.deleteMessage(ctx, c.UserID, c.MessageID)
		return nil
	}
	_, _ = s.store.DB().Exec(ctx, `UPDATE notif SET status='end' WHERE id=$1`, notifID)
	if err := s.store.CreateChat(ctx, c.UserID, requester.UserID, c.Now, 0, balance); err != nil {
		return err
	}
	s.deleteMessage(ctx, c.UserID, c.MessageID)
	_, _ = s.send(ctx, "sendMessage", map[string]any{
		"chat_id":      c.UserID,
		"text":         "👀 درخواست چت وصل شد\n\nبه مخاطبت سلام کن 🗣",
		"reply_markup": telegram.JSON(replyMarkupKeyboard(chatKeyboard())),
	})
	_, err = s.send(ctx, "sendMessage", map[string]any{
		"chat_id":      requester.UserID,
		"text":         "👀 درخواست چت وصل شد\n\nبه مخاطبت سلام کن 🗣",
		"reply_markup": telegram.JSON(replyMarkupKeyboard(chatKeyboard())),
	})
	return err
}

func (s *Service) gibRejectChat(ctx context.Context, c *UpdateContext, requester storage.User) error {
	notifID := parseInt64(part(c.ExData, 3))
	var date int64
	err := s.store.DB().QueryRow(ctx, `SELECT date FROM notif WHERE id=$1`, notifID).Scan(&date)
	if err != nil {
		s.deleteMessage(ctx, c.UserID, c.MessageID)
		return nil
	}
	if c.Now-date > 120 {
		_, _ = s.store.DB().Exec(ctx, `UPDATE notif SET status='end' WHERE id=$1`, notifID)
		s.deleteMessage(ctx, c.UserID, c.MessageID)
		return nil
	}
	_, _ = s.store.DB().Exec(ctx, `UPDATE notif SET status='end' WHERE id=$1`, notifID)
	_ = s.answer(ctx, c, "✅ درخواست چت رد شد.")
	s.deleteMessage(ctx, c.UserID, c.MessageID)
	_, err = s.send(ctx, "sendMessage", map[string]any{
		"chat_id": requester.UserID,
		"text":    "🔔 درخواست چت شما به /user_" + c.User.UniqID + " رد شد.",
	})
	return err
}

func (s *Service) gibDirectCallback(ctx context.Context, c *UpdateContext, user storage.User) error {
	switch part(c.ExData, 3) {
	case "none":
		var blocked bool
		_ = s.store.DB().QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM blocked WHERE user_id=$1 AND target_id=$2)`, user.UserID, c.UserID).Scan(&blocked)
		if blocked {
			return s.answer(ctx, c, "⚠️ خطا: شما توسط این کاربر بلاک شده اید.")
		}
		if c.User.Balance < 1 {
			_ = s.answer(ctx, c, "⚠️ خطا: شما سکه ای برای ارسال پیام دایرکت ندارید!\n\nبرای ارسال پیام دایرکت 1 سکه نیاز است.")
			_, err := s.send(ctx, "sendMessage", map[string]any{
				"chat_id":             c.UserID,
				"text":                "سکه نداری ؟ ولی میخوای برای /user_" + user.UniqID + " پیام دایرکت بفرستی؟ الان بهت یه راهکار نشون میدم\n\nبا قابلیت 《 ارسال 📨 پیام دایرکت و کسر سکه از گیرنده 》  از گیرنده درخواست میشه تا با دادن 💰1 سکه بتونه پیامت رو مشاهده کنه و بجای تو از اون کسر بشه !\n\n<code>برای استفاده از این قابلیت و ارسال پیام دایرکت دکمه زیر رو لمس کن 👇</code>",
				"parse_mode":          "HTML",
				"reply_to_message_id": c.MessageID,
				"reply_markup": telegram.JSON(replyMarkupInline([][]button{
					{callbackButton("ارسال 📨 پیام دایرکت و کسر 💰1 سکه از گیرنده", "gib;directMessage;"+user.UniqID+";wc")},
				})),
			})
			return err
		}
		_ = s.store.UpdateUserStep(ctx, c.UserID, c.Data)
		_, err := s.send(ctx, "sendMessage", map[string]any{
			"chat_id":             c.UserID,
			"text":                "📬 متن پیام دایرکت خود را ارسال کنید (حداکثر 200 حرف)\n\n<code>    برای لغو ارسال پیام دایرکت 《بیخیال》 را لمس کنید👇</code>",
			"parse_mode":          "HTML",
			"reply_to_message_id": c.MessageID,
			"reply_markup": telegram.JSON(replyMarkupInline([][]button{
				{callbackButton("بیخیال", "gib;directMessage;"+user.UniqID+";cancel")},
			})),
		})
		return err
	case "wc":
		var blocked bool
		_ = s.store.DB().QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM blocked WHERE user_id=$1 AND target_id=$2)`, user.UserID, c.UserID).Scan(&blocked)
		if blocked {
			return s.answer(ctx, c, "⚠️ خطا: شما توسط این کاربر بلاک شده اید.")
		}
		_ = s.store.UpdateUserStep(ctx, c.UserID, c.Data)
		_, err := s.send(ctx, "sendMessage", map[string]any{
			"chat_id":             c.UserID,
			"text":                "📬 متن پیام دایرکت خود را ارسال کنید (حداکثر 200 حرف)\n\n<code>    برای لغو ارسال پیام دایرکت 《بیخیال》 را لمس کنید👇</code>",
			"parse_mode":          "HTML",
			"reply_to_message_id": c.MessageID,
			"reply_markup": telegram.JSON(replyMarkupInline([][]button{
				{callbackButton("بیخیال", "gib;directMessage;"+user.UniqID+";cancel")},
			})),
		})
		return err
	case "cancel":
		s.deleteMessage(ctx, c.UserID, c.MessageID)
		_ = s.store.UpdateUserStep(ctx, c.UserID, "start")
		_, err := s.send(ctx, "sendMessage", map[string]any{
			"chat_id":      c.UserID,
			"text":         "✅ ارسال پیام دایرکت لغو شد.",
			"reply_markup": telegram.JSON(replyMarkupKeyboard(mainMenuKeyboard(s.isAdmin(c)))),
		})
		return err
	case "send":
		return s.gibDirectSend(ctx, c, user)
	case "get":
		return s.gibDirectGet(ctx, c, user)
	}
	return nil
}

func checkedTelegramResponse(method string, resp telegram.APIResponse, err error) error {
	if err != nil {
		return err
	}
	if !resp.Ok {
		return fmt.Errorf("telegram %s was not successful: %s", method, resp.Description)
	}
	return nil
}

// deleteMessageOnCurrentBot is used for callback-origin messages. Such a
// message must be edited/deleted with the same bot token that created it,
// even if the user's delivery assignment has since moved to another shard.
func (s *Service) deleteMessageOnCurrentBot(ctx context.Context, chatID string, messageID int) {
	if chatID == "" || messageID == 0 {
		return
	}
	resp, err := s.tg.Call(ctx, "deleteMessage", map[string]any{"chat_id": chatID, "message_id": messageID})
	if err = checkedTelegramResponse("deleteMessage", resp, err); err != nil {
		log.Printf("delete callback message failed: chat=%s message=%d: %v", chatID, messageID, err)
	}
}

func (s *Service) gibDirectSend(ctx context.Context, c *UpdateContext, user storage.User) error {
	notifID := parseInt64(part(c.ExData, 5))
	mode := part(c.ExData, 4)
	receiverPays := mode == "wc"
	if !receiverPays && mode != "none" {
		return s.answer(ctx, c, "⚠️ این درخواست ارسال پیام معتبر نیست؛ لطفاً پیام را دوباره آماده کنید.")
	}
	if receiverPays && user.IsFake {
		return s.answer(ctx, c, "⚠️ ارسال درخواست کسر سکه از پروفایل آزمایشی امکان‌پذیر نیست.")
	}

	notif, err := s.store.PrepareDirectMessage(ctx, notifID, c.UserID, user.UserID, receiverPays)
	if errors.Is(err, storage.ErrInsufficientDirectMessageCoins) {
		return s.answer(ctx, c, "⚠️ موجودی شما کافی نیست. برای ارسال این پیام یک سکه نیاز دارید؛ لطفاً دوباره از گزینه کسر سکه از گیرنده استفاده کنید.")
	}
	if errors.Is(err, storage.ErrDirectMessageNotAvailable) {
		s.deleteMessageOnCurrentBot(ctx, c.UserID, c.MessageID)
		return s.answer(ctx, c, "⚠️ این پیش‌نمایش قبلاً استفاده شده یا دیگر معتبر نیست. لطفاً پیام را دوباره ارسال کنید.")
	}
	if err != nil {
		return err
	}

	// The database has already committed the one-time state transition and any
	// sender-side debit. In the receiver-pays flow, keep the preview intact until
	// the recipient's confirmation prompt is actually delivered.
	if receiverPays {
		recipientResp, recipientCallErr := s.send(ctx, "sendMessage", map[string]any{
			"chat_id": user.UserID,
			"text": "🔔 پیام دایرکت جدید از طرف /user_" + c.User.UniqID + " در " + toEnglish(jdate(s.loc, "Y:m:d H:i", notif.Date)) +
				"\nــــــــــــــــــــــــــــــــــــــــ\nاین پیام به دلیل نداشتن سکه توسط فرستنده، با درخواست کسر ۱ سکه از شما ارسال شده است.\n\n" +
				"اگر با کسر یک سکه موافق هستید، دکمه زیر را بزنید تا متن پیام نمایش داده شود 👇",
			"reply_markup": telegram.JSON(replyMarkupInline([][]button{
				{callbackButton("ارسال 📨 پیام دایرکت و کسر 💰1 سکه از گیرنده", fmt.Sprintf("gib;directMessage;%s;get;%d", c.User.UniqID, notif.ID))},
			})),
		})
		if recipientErr := checkedTelegramResponse("sendMessage", recipientResp, recipientCallErr); recipientErr != nil {
			log.Printf("direct-message approval prompt failed: notif=%d receiver=%s: %v", notif.ID, user.UserID, recipientErr)
			if reopenErr := s.store.ReopenDirectMessagePreview(ctx, notif.ID, c.UserID, user.UserID); reopenErr != nil {
				return fmt.Errorf("direct-message prompt delivery failed: %v; reopening preview failed: %w", recipientErr, reopenErr)
			}
			return s.answer(ctx, c, "⚠️ درخواست به مخاطب تحویل نشد. سکه‌ای کسر نشده؛ می‌توانید دوباره تلاش کنید.")
		}

		senderReceiptResp, senderReceiptCallErr := s.send(ctx, "sendMessage", map[string]any{
			"chat_id":      c.UserID,
			"text":         "✅ درخواست مشاهده پیام دایرکت شما برای /user_" + user.UniqID + " ارسال شد.\n\nتا زمان تأیید مخاطب، از شما سکه‌ای کسر نمی‌شود.\nــــــــــــــــــــــــــــــــــــــــ\n" + notif.Content,
			"reply_markup": telegram.JSON(replyMarkupKeyboard(mainMenuKeyboard(s.isAdmin(c)))),
		})
		s.deleteMessageOnCurrentBot(ctx, c.UserID, c.MessageID)
		return checkedTelegramResponse("sendMessage", senderReceiptResp, senderReceiptCallErr)
	}

	// Sender-pays messages are charged as part of PrepareDirectMessage.
	s.deleteMessageOnCurrentBot(ctx, c.UserID, c.MessageID)
	senderReceiptResp, senderReceiptCallErr := s.send(ctx, "sendMessage", map[string]any{
		"chat_id":      c.UserID,
		"text":         "✅ پیام #دایرکت شما به /user_" + user.UniqID + " در " + toEnglish(jdate(s.loc, "Y:m:d H:i", notif.Date)) + " ارسال شد.\nــــــــــــــــــــــــــــــــــــــــ\n" + notif.Content,
		"reply_markup": telegram.JSON(replyMarkupKeyboard(mainMenuKeyboard(s.isAdmin(c)))),
	})
	senderReceiptErr := checkedTelegramResponse("sendMessage", senderReceiptResp, senderReceiptCallErr)
	if user.IsFake {
		return senderReceiptErr
	}
	recipientResp, recipientCallErr := s.send(ctx, "sendMessage", map[string]any{
		"chat_id": user.UserID,
		"text":    "🔔 پیام #دایرکت جدید از طرف /user_" + c.User.UniqID + " در " + toEnglish(jdate(s.loc, "Y:m:d H:i", notif.Date)) + "\nــــــــــــــــــــــــــــــــــــــــ\n" + notif.Content,
		"reply_markup": telegram.JSON(replyMarkupInline([][]button{
			{callbackButton("📨 ارسال پاسخ", "gib;directMessage;"+c.User.UniqID+";none")},
		})),
	})
	if recipientErr := checkedTelegramResponse("sendMessage", recipientResp, recipientCallErr); recipientErr != nil {
		return recipientErr
	}
	return senderReceiptErr
}

func (s *Service) gibDirectGet(ctx context.Context, c *UpdateContext, sender storage.User) error {
	notifID := parseInt64(part(c.ExData, 4))
	notif, err := s.store.BeginDirectMessageAcceptance(ctx, notifID, sender.UserID, c.UserID)
	if errors.Is(err, storage.ErrInsufficientDirectMessageCoins) {
		return s.answer(ctx, c, "⚠️ موجودی شما کافی نیست؛ برای مشاهده پیام دایرکت یک سکه نیاز دارید. هیچ سکه‌ای کسر نشد.")
	}
	if errors.Is(err, storage.ErrDirectMessageNotAvailable) {
		s.deleteMessageOnCurrentBot(ctx, c.UserID, c.MessageID)
		return s.answer(ctx, c, "⚠️ این درخواست قبلاً تأیید شده یا معتبر نیست.")
	}
	if err != nil {
		return err
	}

	messageText := "🔔 پیام #دایرکت جدید از طرف /user_" + sender.UniqID + " در " + toEnglish(jdate(s.loc, "Y:m:d H:i", notif.Date)) +
		"\nــــــــــــــــــــــــــــــــــــــــ\n" + notif.Content
	markup := telegram.JSON(replyMarkupInline([][]button{
		{callbackButton("📨 ارسال پاسخ", "gib;directMessage;"+sender.UniqID+";none")},
	}))

	// Prefer replacing the request prompt in-place. Some Telegram-compatible
	// API responses reject edits (for example, if the original message vanished),
	// so send the same content as a new message as a reliable fallback.
	// Edit with the bot that received this callback, not the user's possibly
	// newer shard assignment. Telegram only lets the originating bot edit it.
	editResp, editCallErr := s.tg.Call(ctx, "editMessageText", map[string]any{
		"chat_id":      c.UserID,
		"message_id":   c.MessageID,
		"text":         messageText,
		"reply_markup": markup,
	})
	editErr := checkedTelegramResponse("editMessageText", editResp, editCallErr)
	if editErr != nil {
		log.Printf("direct-message edit failed: notif=%d receiver=%s: %v; trying sendMessage fallback", notifID, c.UserID, editErr)
		fallbackResp, fallbackCallErr := s.send(ctx, "sendMessage", map[string]any{
			"chat_id":      c.UserID,
			"text":         messageText,
			"reply_markup": markup,
		})
		fallbackErr := checkedTelegramResponse("sendMessage", fallbackResp, fallbackCallErr)
		if fallbackErr != nil {
			log.Printf("direct-message fallback failed: notif=%d receiver=%s: %v", notifID, c.UserID, fallbackErr)
			if refundErr := s.store.RestoreDirectMessageAfterDeliveryFailure(ctx, notifID, c.UserID); refundErr != nil {
				log.Printf("direct-message refund failed: notif=%d receiver=%s: %v", notifID, c.UserID, refundErr)
				return fmt.Errorf("direct-message delivery failed (edit: %v; fallback: %v) and refund failed: %w", editErr, fallbackErr, refundErr)
			}
			_ = s.answer(ctx, c, "⚠️ نمایش پیام ناموفق بود؛ سکه شما بازگردانده شد. لطفاً دوباره تلاش کنید.")
			return nil
		}
		// The fallback delivered the content, so remove the stale prompt if it
		// could not be edited in place.
		s.deleteMessageOnCurrentBot(ctx, c.UserID, c.MessageID)
	}

	if err := s.store.CompleteDirectMessageAcceptance(ctx, notifID, c.UserID); err != nil {
		return err
	}
	return s.answer(ctx, c, "✅ پیام نمایش داده شد و یک سکه از موجودی شما کسر شد.")
}

func (s *Service) gibBlock(ctx context.Context, c *UpdateContext, user storage.User) error {
	var exists bool
	_ = s.store.DB().QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM blocked WHERE user_id=$1 AND target_id=$2)`, c.UserID, user.UserID).Scan(&exists)
	if exists {
		_, _ = s.store.DB().Exec(ctx, `DELETE FROM blocked WHERE user_id=$1 AND target_id=$2`, c.UserID, user.UserID)
		_ = s.answer(ctx, c, "✅ کاربر آنبلاک شده.\n\nاین کاربر امکان ارسال درخواست چت و پیام دایرکت به شما را خواهد داشت.")
	} else {
		_, _ = s.store.DB().Exec(ctx, `INSERT INTO blocked (user_id,target_id,created_at) VALUES ($1,$2,$3) ON CONFLICT DO NOTHING`, c.UserID, user.UserID, c.Now)
		_ = s.answer(ctx, c, "✅ کاربر بلاک شده.\n\nاین کاربر امکان ارسال درخواست چت و پیام دایرکت به شما را نخواهد داشت.")
	}
	_, err := s.send(ctx, "editMessageReplyMarkup", map[string]any{
		"chat_id":      c.UserID,
		"message_id":   c.MessageID,
		"reply_markup": telegram.JSON(replyMarkupInline(s.generateInlineButtons(ctx, c, user))),
	})
	return err
}

func (s *Service) gibReportCallback(ctx context.Context, c *UpdateContext, user storage.User) error {
	if _, err := s.store.ActiveChat(ctx, c.UserID); err == nil {
		return s.answer(ctx, c, "⚠️ درصورتی که مخاطب شما محتوای نامناسبی برایتان ارسال کرده چت را خاتمه دهید و روی دکمه « 🚫 گزارش کاربر » که در پروفایل مخاطب قرار دارد کلیک کنید تا حساب کاربری او مسدود شود.")
	}
	if part(c.ExData, 3) == "repchat" {
		s.deleteMessage(ctx, c.UserID, c.MessageID)
		return nil
	}
	if part(c.ExData, 3) == "none" {
		kb := [][]button{}
		for _, key := range []string{"ads", "immoral", "disturb", "dissemination", "immoralprofile", "wronggender", "other"} {
			kb = append(kb, []button{callbackButton(ReportOptions[key], "gib;report;"+user.UniqID+";"+key)})
		}
		_, err := s.send(ctx, "sendMessage", map[string]any{
			"chat_id":      c.UserID,
			"text":         "⚠️ فرم ارسال گزارش عدم رعایت قوانین\n\nچرا میخوای /user_" + user.UniqID + " رو گزارش کنی؟\n\n- توجه : تمامی گزارشات بررسی خواهند شد و 🔴 ارسال گزارشات اشتباه موجب مسدود شدن شما خواهد شد.\n\nانتخاب کنید 👇",
			"reply_markup": telegram.JSON(replyMarkupInline(kb)),
		})
		return err
	}
	_ = s.store.UpdateUserStepPrev(ctx, c.UserID, c.Data, "start")
	s.deleteMessage(ctx, c.UserID, c.MessageID)
	_, err := s.send(ctx, "sendMessage", map[string]any{
		"chat_id":      c.UserID,
		"text":         "⚠️ فرم ارسال گزارش عدم رعایت قوانین به دلیل " + ReportOptions[part(c.ExData, 3)] + ".\n\n<code>خب حالا کافیه یه توضیح کوتاه و 《کامل》 درباره گزارشت بفرستی تا ثبتش کنم.</code>\n\n<code>- مثلا : داره تبلیغات فلان کانال رو میکنه.</code>\n\nبرای لغو گزارش 《 بازگشت 🔙 》 را انتخاب کنید 👇",
		"parse_mode":   "HTML",
		"reply_markup": telegram.JSON(replyMarkupKeyboard([][]button{{textButton(BackButton)}})),
	})
	return err
}

func (s *Service) gibSimpleNotif(ctx context.Context, c *UpdateContext, user storage.User, reason string) error {
	var exists bool
	_ = s.store.DB().QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM notif WHERE user_id=$1 AND user_id_2=$2 AND reason=$3 AND status='doing')`, c.UserID, user.UserID, reason).Scan(&exists)
	if exists {
		return s.answer(ctx, c, "⚠️ خطا : این قابلیت قبلا توسط شما برای این کاربر فعال شده است.")
	}
	label := "آنلاین شدن"
	if reason == "endchatnotif" {
		label = "اتمام چت"
	}
	if part(c.ExData, 3) == "none" {
		_, err := s.send(ctx, "sendMessage", map[string]any{
			"chat_id":    c.UserID,
			"text":       "🔔 به محض " + label + " کاربر /user_" + user.UniqID + " به شما اطلاع داده خواهد شد.\n(راهنما : /help_onw)\n\n⚠️ توجه : فعال کردن این قابلیت 1 💰 سکه از شما کم خواهد کرد.\n\n\n<code>فعال سازی 👇</code>",
			"parse_mode": "HTML",
			"reply_markup": telegram.JSON(replyMarkupInline([][]button{
				{callbackButton("کسر 1💰 سکه و فعال سازی", "gib;"+reason+";"+user.UniqID+";active")},
			})),
		})
		return err
	}
	if c.User.Balance < 1 {
		return s.answer(ctx, c, "⚠️ تعداد سکه شما کافی نیست")
	}
	_, _ = s.store.DB().Exec(ctx, `INSERT INTO notif (user_id,user_id_2,reason,date) VALUES ($1,$2,$3,$4)`, c.UserID, user.UserID, reason, c.Now)
	_ = s.store.AddBalance(ctx, c.UserID, -1)
	_, err := s.send(ctx, "sendMessage", map[string]any{
		"chat_id": c.UserID,
		"text":    "✅ با موفقیت ثبت شد. (راهنما : /help_onw)\n\n🔔 به محض " + label + " کاربر /user_" + user.UniqID + " به شما اطلاع داده خواهد شد.",
	})
	return err
}

func (s *Service) handleGIBInput(ctx context.Context, c *UpdateContext) error {
	user, err := s.store.UserByUniqOrID(ctx, part(c.ExStep, 2))
	if err != nil {
		s.deleteMessage(ctx, c.UserID, c.MessageID)
		return nil
	}
	switch part(c.ExStep, 1) {
	case "directMessage":
		return s.gibDirectInput(ctx, c, user)
	case "friend":
		_, _ = s.store.DB().Exec(ctx, `INSERT INTO friends (name,user_id,target_id,created_at) VALUES ($1,$2,$3,$4) ON CONFLICT (user_id,target_id) DO UPDATE SET name=excluded.name,created_at=excluded.created_at`, c.Text, c.UserID, user.UserID, c.Now)
		_ = s.store.UpdateUserStep(ctx, c.UserID, "start")
		_, err := s.send(ctx, "sendMessage", map[string]any{
			"chat_id":             c.UserID,
			"text":                "👥 مخاطب ذخیره شد ✅\n\nتوجه: مخاطبین خود را می توانید از قسمت مخاطبین که در بخش پروفایل قرار دارد مشاهده کنید.\n\n" + mainMenuText(),
			"reply_to_message_id": c.MessageID,
			"parse_mode":          "HTML",
			"reply_markup":        telegram.JSON(replyMarkupKeyboard(mainMenuKeyboard(s.isAdmin(c)))),
		})
		return err
	case "report":
		return s.gibReportInput(ctx, c, user)
	}
	return nil
}

func (s *Service) gibDirectInput(ctx context.Context, c *UpdateContext, user storage.User) error {
	var blocked bool
	_ = s.store.DB().QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM blocked WHERE user_id=$1 AND target_id=$2)`, user.UserID, c.UserID).Scan(&blocked)
	if blocked {
		_ = s.store.UpdateUserStep(ctx, c.UserID, "start")
		_, err := s.send(ctx, "sendMessage", map[string]any{
			"chat_id":             c.UserID,
			"text":                "⚠️ خطا: شما توسط این کاربر بلاک شده اید.",
			"reply_to_message_id": c.MessageID,
		})
		return err
	}
	if part(c.ExStep, 3) == "none" && c.User.Balance < 1 {
		_ = s.answer(ctx, c, "⚠️ خطا: شما سکه ای برای ارسال پیام دایرکت ندارید!\n\nبرای ارسال پیام دایرکت 1 سکه نیاز است.")
		_, err := s.send(ctx, "sendMessage", map[string]any{
			"chat_id":             c.UserID,
			"text":                "سکه نداری ؟ ولی میخوای برای /user_" + user.UniqID + " پیام دایرکت بفرستی؟ الان بهت یه راهکار نشون میدم\n\nبا قابلیت 《 ارسال 📨 پیام دایرکت و کسر سکه از گیرنده 》  از گیرنده درخواست میشه تا با دادن 💰1 سکه بتونه پیامت رو مشاهده کنه و بجای تو از اون کسر بشه !\n\n<code>برای استفاده از این قابلیت و ارسال پیام دایرکت دکمه زیر رو لمس کن 👇</code>",
			"parse_mode":          "HTML",
			"reply_to_message_id": c.MessageID,
			"reply_markup": telegram.JSON(replyMarkupInline([][]button{
				{callbackButton("🚸 معرفی به دوستان (سکه رایگان)", "invite")},
			})),
		})
		return err
	}
	if c.Message != nil && c.Message.Text != "" && len([]rune(c.Text)) <= 200 {
		var notifID int64
		if err := s.store.DB().QueryRow(ctx, `INSERT INTO notif (user_id,user_id_2,reason,content,date) VALUES ($1,$2,'direct_message',$3,$4) RETURNING id`, c.UserID, user.UserID, c.Text, c.Now).Scan(&notifID); err != nil {
			return err
		}
		mode := part(c.ExStep, 3)
		if mode != "wc" {
			mode = "none"
		}
		_ = s.store.UpdateUserStep(ctx, c.UserID, "start")
		_, err := s.send(ctx, "sendMessage", map[string]any{
			"chat_id": c.UserID,
			"text":    "📜 پیش نمایش پیام دایرکت شما به /user_" + user.UniqID + "\n\nــــــــــــــــــــــــــــــــــــــــ\n" + c.Text,
			"reply_markup": telegram.JSON(replyMarkupInline([][]button{
				{callbackButton("📨 ارسال", fmt.Sprintf("gib;directMessage;%s;send;%s;%d", user.UniqID, mode, notifID))},
				{callbackButton("بیخیال", "gib;directMessage;"+user.UniqID+";cancel")},
			})),
		})
		return err
	}
	_, err := s.send(ctx, "sendMessage", map[string]any{
		"chat_id":             c.UserID,
		"text":                "⚠️خطا : پیام دایرکت فقط باید بصورت متن باشد (حداکثر 200 حرف)\n\n- برای لغو ارسال پیام دایرکت به /user_" + user.UniqID + " 《بیخیال》 را لمس کنید",
		"reply_to_message_id": c.MessageID,
		"reply_markup": telegram.JSON(replyMarkupInline([][]button{
			{callbackButton("بیخیال", "gib;directMessage;"+user.UniqID+";cancel")},
		})),
	})
	return err
}

func (s *Service) gibReportInput(ctx context.Context, c *UpdateContext, user storage.User) error {
	_ = s.store.UpdateUserStep(ctx, c.UserID, "start")
	reason := ReportOptions[part(c.ExStep, 3)]

	reporterUsername := getUserUsername(ctx, s, c.UserID)
	targetUsername := getUserUsername(ctx, s, user.UserID)

	reportText := fmt.Sprintf("🚫 ثبت گزارش جدید!\n\n👮‍♂️ گزارش کننده:\n/user_%s\n/user_%s\n%s\n\n🧟‍♂️ گزارش شده:\n/user_%s\n/user_%s\n%s\n\nدلیل:\n%s\n\nمتن:\n",
		c.UserID, c.User.UniqID, reporterUsername,
		user.UserID, user.UniqID, targetUsername,
		reason)

	adminGroupID := s.adminGroupID(ctx)
	var reportResp telegram.APIResponse
	var reportErr error
	if adminGroupID != "" {
		if c.Message != nil && c.Message.Text != "" {
			text := c.Text
			if len([]rune(text)) > 1500 {
				text = string([]rune(text)[:1500])
			}
			reportResp, reportErr = s.send(ctx, "sendMessage", map[string]any{
				"chat_id":           adminGroupID,
				"message_thread_id": adminTopicViolationReport,
				"text":              reportText + text,
			})
		} else if c.Message != nil && len(c.Message.Photo) > 0 {
			caption := c.Message.Caption
			if len([]rune(caption)) > 500 {
				caption = string([]rune(caption)[:500])
			}
			reportResp, reportErr = s.send(ctx, "sendPhoto", map[string]any{
				"chat_id":           adminGroupID,
				"message_thread_id": adminTopicViolationReport,
				"photo":             photoID(c.Message),
				"caption":           reportText + caption,
			})
		} else {
			_, err := s.send(ctx, "sendMessage", map[string]any{
				"chat_id": c.UserID,
				"text":    "❌ لطفا فقط متن یا تصویر ارسال کنید",
			})
			return err
		}
	} else {
		// Fallback to support group or admin
		reportChatID := c.Admin.Support
		if reportChatID == "" {
			reportChatID = s.cfg.AdminID
		}
		if c.Message != nil && c.Message.Text != "" {
			text := c.Text
			if len([]rune(text)) > 1500 {
				text = string([]rune(text)[:1500])
			}
			reportResp, reportErr = s.send(ctx, "sendMessage", map[string]any{
				"chat_id": reportChatID,
				"text":    reportText + text,
			})
		} else if c.Message != nil && len(c.Message.Photo) > 0 {
			caption := c.Message.Caption
			if len([]rune(caption)) > 500 {
				caption = string([]rune(caption)[:500])
			}
			reportResp, reportErr = s.send(ctx, "sendPhoto", map[string]any{
				"chat_id": reportChatID,
				"photo":   photoID(c.Message),
				"caption": reportText + caption,
			})
		} else {
			_, err := s.send(ctx, "sendMessage", map[string]any{
				"chat_id": c.UserID,
				"text":    "❌ لطفا فقط متن یا تصویر ارسال کنید",
			})
			return err
		}
	}
	if reportErr != nil {
		return reportErr
	}
	if !reportResp.Ok {
		return fmt.Errorf("send violation report: %s", reportResp.Description)
	}

	_, err := s.send(ctx, "sendMessage", map[string]any{
		"chat_id":             c.UserID,
		"text":                "✅ با تشکر از همکاری شما، گزارش شما با موفقیت ثبت شد و بزودی بررسی خواهد شد 🌹",
		"reply_to_message_id": c.MessageID,
		"reply_markup":        telegram.JSON(replyMarkupKeyboard(mainMenuKeyboard(s.isAdmin(c)))),
	})
	return err
}

func waitText(seconds int64) string {
	if seconds == 60 {
		return "1 دقیقه"
	}
	if seconds > 60 {
		return fmt.Sprintf("%d دقیقه و %d ثانیه", seconds/60, seconds%60)
	}
	return fmt.Sprintf("%d ثانیه", seconds%60)
}

func searchStateEncoded(name string) string {
	return url.QueryEscape(name)
}

// ============================================================
// تابع نمایش عکس پروفایل - اصلاح‌شده برای حالت PEAK
// ============================================================
func (s *Service) userProfilePhoto(ctx context.Context, user storage.User) any {
	path := s.profilePhotoPath(user.UserID)
	if _, err := os.Stat(path); err == nil {
		return telegram.LocalFile{Path: path}
	}

	if s.fleet != nil && s.fleet.Mode() == fleet.ModePeak {
		return s.defaultProfilePhoto(user.Gender)
	}

	if user.Image != "" && user.IsFake {
		return user.Image
	}

	return s.defaultProfilePhoto(user.Gender)
}
