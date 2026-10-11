package bot

import (
	"context"
	"fmt"
	"strings"

	"miogram/internal/storage"
	"miogram/internal/telegram"
)

func (s *Service) chatPrivacyEnabled(ctx context.Context, chatID int64, userID string) bool {
	var enabled bool
	_ = s.store.DB().QueryRow(ctx, `SELECT enabled FROM chat_privacy WHERE chat_id=$1 AND user_id=$2`, chatID, userID).Scan(&enabled)
	return enabled
}

func (s *Service) toggleChatPrivacy(ctx context.Context, c *UpdateContext, chat storage.Chat) error {
	enabled := !s.chatPrivacyEnabled(ctx, chat.ID, c.UserID)
	_, err := s.store.DB().Exec(ctx, `INSERT INTO chat_privacy (chat_id,user_id,enabled) VALUES ($1,$2,$3) ON CONFLICT (chat_id,user_id) DO UPDATE SET enabled=excluded.enabled`, chat.ID, c.UserID, enabled)
	if err != nil {
		return err
	}
	status := "غیرفعال شد"
	if enabled {
		status = "فعال شد؛ پیام‌های بعدی شما با جلوگیری از فوروارد و ذخیره ارسال می‌شوند"
	}
	_, err = s.send(ctx, "sendMessage", map[string]any{"chat_id": c.UserID, "text": "🔒 حالت خصوصی " + status + ".", "reply_markup": telegram.JSON(replyMarkupKeyboard(chatKeyboard()))})
	if err != nil {
		return err
	}

	// Notify the other user without exposing Telegram IDs or usernames.
	otherID := chat.UserID1
	if otherID == c.UserID {
		otherID = chat.UserID2
	}
	otherUser, err := s.store.UserByID(ctx, otherID)
	if err == nil && !otherUser.IsFake {
		otherStatus := "🔒 فعال"
		if !enabled {
			otherStatus = "🔓 غیرفعال"
		}
		_, _ = s.send(ctx, "sendMessage", map[string]any{
			"chat_id": otherID,
			"text":    "🤖 پیام سیستم 👇\n\n" + displayName(c.User) + " حالت خصوصی خود را " + otherStatus + " کرد.\n\nدر حالت خصوصی، پیام‌های او قابل فوروارد و ذخیره شدن نیست.",
		})
	}

	return nil
}

func displayName(user storage.User) string {
	if strings.TrimSpace(user.Name) != "" {
		return user.Name
	}
	return "مخاطب شما"
}

func (s *Service) startTicTacToe(ctx context.Context, c *UpdateContext, chat storage.Chat, other storage.User, boardSize int) error {
	if boardSize != 3 && boardSize != 4 && boardSize != 5 {
		boardSize = 3
	}
	board := strings.Repeat(".", boardSize*boardSize)
	var gameID int64
	err := s.store.DB().QueryRow(ctx, `INSERT INTO tictactoe_games (chat_id,player_x,player_o,board,board_size,turn,status,created_at,updated_at) VALUES ($1,$2,$3,$4,$5,$2,'active',$6,$6) RETURNING id`, chat.ID, c.UserID, other.UserID, board, boardSize, c.Now).Scan(&gameID)
	if err != nil {
		return err
	}
	text := fmt.Sprintf("🎮 بازی دوز %d×%d شروع شد\n\n❌ شروع‌کننده: شما\n⭕️ حریف\n\nنوبت: ❌", boardSize, boardSize)
	markup := telegram.JSON(replyMarkupInline(ticTacToeKeyboard(gameID, board, boardSize)))
	respX, errX := s.send(ctx, "sendMessage", map[string]any{"chat_id": c.UserID, "text": text, "reply_markup": markup})
	msgX, msgO := 0, 0
	if msg, ok := s.tg.SentMessage(respX); ok {
		msgX = msg.MessageID
	}
	// A synthetic (fake) user has no Telegram account, so never send the board
	// to it; the real user keeps the only playable copy of the game.
	if !other.IsFake {
		respO, errO := s.send(ctx, "sendMessage", map[string]any{"chat_id": other.UserID, "text": "🎮 مخاطب شما بازی دوز را شروع کرد\n\n❌ حریف\n⭕️ شما\n\nنوبت: ❌", "reply_markup": markup})
		if msg, ok := s.tg.SentMessage(respO); ok {
			msgO = msg.MessageID
		}
		if errO != nil || !respO.Ok {
			_, _ = s.store.DB().Exec(ctx, `UPDATE tictactoe_games SET status='send_failed' WHERE id=$1`, gameID)
			return errO
		}
	}
	_, _ = s.store.DB().Exec(ctx, `UPDATE tictactoe_games SET message_x=$2,message_o=$3 WHERE id=$1`, gameID, msgX, msgO)
	if errX != nil || !respX.Ok {
		_, _ = s.store.DB().Exec(ctx, `UPDATE tictactoe_games SET status='send_failed' WHERE id=$1`, gameID)
		return errX
	}
	return nil
}

// listActiveGames returns all active games for a user
func (s *Service) listActiveGames(ctx context.Context, userID string) ([]struct {
	GameID   int64
	PlayerX  string
	PlayerO  string
	Board    string
	Turn     string
	ChatName string
}, error) {
	rows, err := s.store.DB().Query(ctx, `
		SELECT g.id, g.player_x, g.player_o, g.board, g.turn, 'چت خصوصی' as chat_name
		FROM tictactoe_games g
		LEFT JOIN chats c ON c.id = g.chat_id
		WHERE g.status = 'active' AND (g.player_x = $1 OR g.player_o = $1)
		ORDER BY g.updated_at DESC
	`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var games []struct {
		GameID   int64
		PlayerX  string
		PlayerO  string
		Board    string
		Turn     string
		ChatName string
	}
	for rows.Next() {
		var g struct {
			GameID   int64
			PlayerX  string
			PlayerO  string
			Board    string
			Turn     string
			ChatName string
		}
		if err := rows.Scan(&g.GameID, &g.PlayerX, &g.PlayerO, &g.Board, &g.Turn, &g.ChatName); err != nil {
			return nil, err
		}
		games = append(games, g)
	}
	return games, rows.Err()
}

// getGameByPlayers finds an active game between two specific players
func (s *Service) getGameByPlayers(ctx context.Context, chatID int64, player1, player2 string) (int64, error) {
	var gameID int64
	err := s.store.DB().QueryRow(ctx, `SELECT id FROM tictactoe_games WHERE chat_id=$1 AND status='active' AND ((player_x=$2 AND player_o=$3) OR (player_x=$3 AND player_o=$2))`, chatID, player1, player2).Scan(&gameID)
	if err != nil {
		return 0, err
	}
	return gameID, nil
}

// endGame manually ends a game by ID
func (s *Service) endGame(ctx context.Context, gameID int64) error {
	_, err := s.store.DB().Exec(ctx, `UPDATE tictactoe_games SET status='ended' WHERE id=$1`, gameID)
	return err
}

func (s *Service) playTicTacToe(ctx context.Context, c *UpdateContext, chat storage.Chat) error {
	gameID := parseInt64(part(c.ExData, 1))
	if gameID == 0 {
		return s.answer(ctx, c, "حرکت نامعتبر است.")
	}

	// Handle restart by looking up the existing game's players
	if part(c.ExData, 2) == "restart" {
		var gameChatID int64
		var playerX, playerO, status string
		var boardSize int
		if err := s.store.DB().QueryRow(ctx, `SELECT chat_id,player_x,player_o,status,board_size FROM tictactoe_games WHERE id=$1`, gameID).Scan(&gameChatID, &playerX, &playerO, &status, &boardSize); err != nil {
			return s.answer(ctx, c, "بازی یافت نشد.")
		}
		if gameChatID != chat.ID || (c.UserID != playerX && c.UserID != playerO) || (status != "finished" && status != "draw") {
			return s.answer(ctx, c, "امکان شروع مجدد این بازی وجود ندارد.")
		}
		var other storage.User
		otherID := playerO
		if c.UserID == playerO {
			otherID = playerX
		}
		other, err := s.store.UserByID(ctx, otherID)
		if err != nil {
			return s.answer(ctx, c, "خطا در شروع مجدد بازی.")
		}
		_ = s.ack(ctx, c)
		_, _ = s.store.DB().Exec(ctx, `UPDATE tictactoe_games SET status='ended' WHERE id=$1`, gameID)
		return s.startTicTacToe(ctx, c, chat, other, boardSize)
	}

	cell := parseInt(part(c.ExData, 2))
	if cell < 0 {
		return s.answer(ctx, c, "حرکت نامعتبر است.")
	}
	tx, err := s.store.DB().Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var chatID int64
	var playerX, playerO, board, turn, status string
	var messageX, messageO, boardSize int
	err = tx.QueryRow(ctx, `SELECT chat_id,player_x,player_o,board,turn,status,message_x,message_o,board_size FROM tictactoe_games WHERE id=$1 FOR UPDATE`, gameID).Scan(&chatID, &playerX, &playerO, &board, &turn, &status, &messageX, &messageO, &boardSize)
	if err != nil || chatID != chat.ID || status != "active" {
		return s.answer(ctx, c, "این بازی فعال نیست.")
	}
	if c.UserID != turn {
		return s.answer(ctx, c, "هنوز نوبت شما نیست.")
	}
	if boardSize != 3 && boardSize != 4 && boardSize != 5 {
		boardSize = 3
	}
	if cell >= boardSize*boardSize {
		return s.answer(ctx, c, "حرکت نامعتبر است.")
	}
	cells := []byte(board)
	if len(cells) != boardSize*boardSize || cells[cell] != '.' {
		return s.answer(ctx, c, "این خانه قبلاً انتخاب شده است.")
	}
	mark := byte('X')
	nextTurn := playerO
	if c.UserID == playerO {
		mark = 'O'
		nextTurn = playerX
	}
	cells[cell] = mark
	board = string(cells)
	newStatus := "active"
	if ticTacToeWinnerSize(board, boardSize) != 0 {
		newStatus = "finished"
	} else if !strings.Contains(board, ".") {
		newStatus = "draw"
	}
	// If the next turn belongs to a synthetic (fake) user, end the game
	// silently: the bot must never produce distinguishing messages.
	/*if isFakeID(nextTurn) && newStatus == "active" {
		newStatus = "ended"
	}*/

	_, err = tx.Exec(ctx, `UPDATE tictactoe_games SET board=$2,turn=$3,status=$4,updated_at=$5 WHERE id=$1`, gameID, board, nextTurn, newStatus, c.Now)
	if err != nil {
		return err
	}
	if err = tx.Commit(ctx); err != nil {
		return err
	}
	if newStatus == "ended" {
		// Acknowledge the callback so Telegram doesn't show a loading state.
		_ = s.ack(ctx, c)
		return nil
	}
	// Acknowledge the button before queueing two board edits. Under load those
	// edits can legitimately wait behind other Telegram sends; delaying the
	// callback acknowledgement makes Telegram show a misleading client error.
	_ = s.ack(ctx, c)
	text, markup := ticTacToeRender(gameID, board, boardSize, newStatus, nextTurn, playerX, playerO)
	for _, item := range []struct {
		chatID    string
		messageID int
	}{{playerX, messageX}, {playerO, messageO}} {
		if item.messageID != 0 {
			_, _ = s.send(ctx, "editMessageText", map[string]any{"chat_id": item.chatID, "message_id": item.messageID, "text": text, "reply_markup": markup})
		}
	}
	return nil
}

func isFakeID(id string) bool {
	return strings.HasPrefix(id, "fake:")
}

// ticTacToeRender builds the board caption and inline keyboard for a game state.
func ticTacToeRender(gameID int64, board string, boardSize int, status, turn, playerX, playerO string) (string, interface{}) {
	text := fmt.Sprintf("🎮 بازی دوز %d×%d\n\n", boardSize, boardSize)
	markup := telegram.JSON(replyMarkupInline(ticTacToeKeyboard(gameID, board, boardSize)))
	switch {
	case ticTacToeWinnerSize(board, boardSize) == 'X':
		text += "🏆 بازیکن ❌ برنده شد.\n\nبرای شروع دوباره، یکی از شما دکمه «🎮 لیست بازیها» را بزند."
		markup = telegram.JSON(replyMarkupInline([][]button{{callbackButton("🔄 شروع مجدد", fmt.Sprintf("ttt;%d;restart", gameID))}}))
	case ticTacToeWinnerSize(board, boardSize) == 'O':
		text += "🏆 بازیکن ⭕️ برنده شد.\n\nبرای شروع دوباره، یکی از شما دکمه «🎮 لیست بازیها» را بزند."
		markup = telegram.JSON(replyMarkupInline([][]button{{callbackButton("🔄 شروع مجدد", fmt.Sprintf("ttt;%d;restart", gameID))}}))
	case status == "draw":
		text += "🤝 بازی مساوی شد.\n\nبرای شروع دوباره، یکی از شما دکمه «🎮 لیست بازیها» را بزند."
		markup = telegram.JSON(replyMarkupInline([][]button{{callbackButton("🔄 شروع مجدد", fmt.Sprintf("ttt;%d;restart", gameID))}}))
	case turn == playerX:
		text += "نوبت: ❌"
	default:
		text += "نوبت: ⭕️"
	}
	return text, markup
}

func ticTacToeKeyboard(gameID int64, board string, boardSize int) [][]button {
	if boardSize < 3 || boardSize > 5 {
		boardSize = 3
	}
	cells := []byte(board)
	rows := make([][]button, 0, boardSize)
	for row := 0; row < boardSize; row++ {
		buttons := make([]button, 0, boardSize)
		for col := 0; col < boardSize; col++ {
			cell := row*boardSize + col
			label := "▫️"
			if len(cells) == boardSize*boardSize {
				if cells[cell] == 'X' {
					label = "❌"
				} else if cells[cell] == 'O' {
					label = "⭕️"
				}
			}
			buttons = append(buttons, callbackButton(label, fmt.Sprintf("ttt;%d;%d", gameID, cell)))
		}
		rows = append(rows, buttons)
	}
	return rows
}

func ticTacToeWinner(board string) byte {
	size := 0
	for n := 3; n <= 5; n++ {
		if len(board) == n*n {
			size = n
			break
		}
	}
	if size == 0 {
		return 0
	}
	return ticTacToeWinnerSize(board, size)
}

func ticTacToeWinnerSize(board string, size int) byte {
	if size < 3 || size > 5 || len(board) != size*size {
		return 0
	}
	// A win requires a complete row, column, or diagonal of the board size.
	for r := 0; r < size; r++ {
		mark := board[r*size]
		if mark != '.' {
			win := true
			for c := 1; c < size; c++ {
				if board[r*size+c] != mark {
					win = false
					break
				}
			}
			if win {
				return mark
			}
		}
	}
	for c := 0; c < size; c++ {
		mark := board[c]
		if mark != '.' {
			win := true
			for r := 1; r < size; r++ {
				if board[r*size+c] != mark {
					win = false
					break
				}
			}
			if win {
				return mark
			}
		}
	}
	for _, startCol := range []int{0, size - 1} {
		mark := board[startCol]
		if mark == '.' {
			continue
		}
		win := true
		for i := 1; i < size; i++ {
			col := startCol
			if startCol == 0 {
				col = i
			} else {
				col = size - 1 - i
			}
			if board[i*size+col] != mark {
				win = false
				break
			}
		}
		if win {
			return mark
		}
	}
	return 0
}
