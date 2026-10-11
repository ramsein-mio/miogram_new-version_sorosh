package bot

import (
	"strconv"
	"strings"
	"testing"

	"miogram/internal/storage"
)

func TestTicTacToeWinner(t *testing.T) {
	tests := []struct {
		board string
		want  byte
	}{
		{"XXXOO....", 'X'},
		{"XO.XO..O.", 'O'},
		{"XOOOX.X.X", 'X'},
		{"XXXX............", 'X'},
		{"X...X...X...X...", 'X'},
		{"XOXOXOOXO", 0},
		{"bad", 0},
	}
	for _, test := range tests {
		if got := ticTacToeWinner(test.board); got != test.want {
			t.Errorf("ticTacToeWinner(%q) = %q, want %q", test.board, got, test.want)
		}
	}
}

func TestTicTacToeKeyboardEncodesGameAndCell(t *testing.T) {
	keyboard := ticTacToeKeyboard(42, "X.O......", 3)
	if len(keyboard) != 3 || len(keyboard[0]) != 3 {
		t.Fatalf("unexpected board dimensions: %#v", keyboard)
	}
	if got := keyboard[0][1]["callback_data"]; got != "ttt;42;1" {
		t.Fatalf("callback_data = %v, want ttt;42;1", got)
	}
	if got := keyboard[0][0]["text"]; got != "❌" {
		t.Fatalf("first cell = %v, want X marker", got)
	}
}

func TestTicTacToeKeyboardsKeepGamesIndependent(t *testing.T) {
	first := ticTacToeKeyboard(101, strings.Repeat(".", 16), 4)
	second := ticTacToeKeyboard(202, strings.Repeat(".", 25), 5)
	if len(first) != 4 || len(first[0]) != 4 || first[3][3]["callback_data"] != "ttt;101;15" {
		t.Fatalf("unexpected 4x4 keyboard: %#v", first)
	}
	if len(second) != 5 || len(second[0]) != 5 || second[4][4]["callback_data"] != "ttt;202;24" {
		t.Fatalf("unexpected 5x5 keyboard: %#v", second)
	}
}

func TestTicTacToeWinnerSupportsAllBoardSizes(t *testing.T) {
	for size := 3; size <= 5; size++ {
		board := []byte(strings.Repeat(".", size*size))
		for i := 0; i < size; i++ {
			board[i*size+i] = 'O'
		}
		if got := ticTacToeWinnerSize(string(board), size); got != 'O' {
			t.Errorf("%dx%d diagonal winner = %q, want O", size, size, got)
		}
	}
	if got := ticTacToeWinnerSize(strings.Repeat("X", 16), 4); got != 'X' {
		t.Errorf("4x4 row winner = %q, want X", got)
	}
	if got := ticTacToeWinnerSize(strings.Repeat(".", 25), 5); got != 0 {
		t.Errorf("incomplete 5x5 board winner = %q, want no winner", got)
	}
}

func TestGameMenusContainRequestedButtons(t *testing.T) {
	chat := chatKeyboard()
	if len(chat) != 3 || chat[1][0]["text"] != GameListButton {
		t.Fatalf("chat keyboard does not use the game list button: %#v", chat)
	}
	menu := gamesListKeyboard()
	for _, want := range []string{TruthDareButton, DuzGameButton, GamesBackButton} {
		found := false
		for _, row := range menu {
			for _, b := range row {
				if b["text"] == want {
					found = true
				}
			}
		}
		if !found {
			t.Errorf("game menu missing %q", want)
		}
	}
	if got := ticTacToeSizeKeyboard(); len(got) != 2 || got[0][0]["callback_data"] != "tttsize;3" || got[0][1]["callback_data"] != "tttsize;4" || got[1][0]["callback_data"] != "tttsize;5" {
		t.Fatalf("unexpected board-size keyboard: %#v", got)
	}
}

func TestAdminTopicAssignments(t *testing.T) {
	if adminTopicProfile != 50 || adminTopicViolationReport != 45 || adminTopicPaymentReceipt != 84 || adminTopicSupport != 44 || adminTopicUserFiles != 49 {
		t.Fatalf("unexpected admin topic mapping: profile=%d reports=%d payments=%d support=%d files=%d", adminTopicProfile, adminTopicViolationReport, adminTopicPaymentReceipt, adminTopicSupport, adminTopicUserFiles)
	}
}

func TestEndChatTextIncludesDeletionAndRefund(t *testing.T) {
	text := endChatText("abc123", true, 3)
	for _, want := range []string{"/delete_messages_abc123", "توسط کاربر مقابل", "3 سکه"} {
		if !strings.Contains(text, want) {
			t.Errorf("end text does not contain %q: %s", want, text)
		}
	}
}

func TestGenerateRandom5Digit(t *testing.T) {
	for i := 0; i < 100; i++ {
		got := generateRandom5Digit()
		if got < 10000 || got > 99999 {
			t.Fatalf("tracking number %d is outside five-digit range", got)
		}
	}
}

func TestProvinceSearchIsOnlyInAnonymousMenu(t *testing.T) {
	if keyboardHasCallback(searchKeyboard(), "province;select") {
		t.Fatal("advanced user search must not contain province search")
	}
	if !keyboardHasCallback(anonymousSearchKeyboard(), "province;select") {
		t.Fatal("anonymous connection menu must contain province search")
	}
	if keyboardHasCallback(registrationSearchKeyboard(), "province;select") {
		t.Fatal("post-registration quick search must not contain province search")
	}
}

func TestProvinceKeyboardUsesInlineCallbacks(t *testing.T) {
	states := []storage.State{{ID: 1, Name: "تهران"}, {ID: 2, Name: "فارس"}, {ID: 3, Name: "گیلان"}}
	keyboard := provinceInlineKeyboard(states)
	for _, state := range states {
		want := "province;state;" + strconv.Itoa(state.ID)
		if !keyboardHasCallback(keyboard, want) {
			t.Fatalf("missing inline callback %q", want)
		}
	}
	if !keyboardHasCallback(keyboard, "anon") {
		t.Fatal("province keyboard must include an inline back button")
	}
}

func TestStateByID(t *testing.T) {
	states := []storage.State{{ID: 7, Name: "تهران"}, {ID: 19, Name: "گیلان"}}
	state, ok := stateByID(states, 19)
	if !ok || state.Name != "گیلان" {
		t.Fatalf("stateByID returned %#v, %v", state, ok)
	}
	if _, ok := stateByID(states, 99); ok {
		t.Fatal("stateByID accepted an unknown state")
	}
}

func TestADSUsersReceiveFiveCoinsAndNormalRegistrationPrompt(t *testing.T) {
	if adsWelcomeBonus != 5 {
		t.Fatalf("ADS bonus = %d, want 5", adsWelcomeBonus)
	}
	if !strings.Contains(registrationSearchText, "🎲 جستجوی شانسی 🎲") || strings.Contains(registrationSearchText, "💰") {
		t.Fatalf("unexpected post-registration text: %s", registrationSearchText)
	}
}

func TestProfileCompletionAdvancesInOrder(t *testing.T) {
	user := storage.User{}
	if got := nextProfileCompletionField(user); got != "name" {
		t.Fatalf("first field = %q, want name", got)
	}
	user.Name = "علی"
	if got := nextProfileCompletionField(user); got != "city" {
		t.Fatalf("second field = %q, want city", got)
	}
	user.City = "تهران"
	if got := nextProfileCompletionField(user); got != "image" {
		t.Fatalf("third field = %q, want image", got)
	}
	user.Image = "file-id"
	if got := nextProfileCompletionField(user); got != "gps" {
		t.Fatalf("fourth field = %q, want gps", got)
	}
	user.Latitude = 35.7
	if got := nextProfileCompletionField(user); got != "" {
		t.Fatalf("completed profile still requests %q", got)
	}
}

func keyboardHasCallback(keyboard [][]button, callback string) bool {
	for _, row := range keyboard {
		for _, btn := range row {
			if btn["callback_data"] == callback {
				return true
			}
		}
	}
	return false
}

func TestTruthDareRenderShowsActionsForCurrentAsker(t *testing.T) {
	game := truthDareGame{
		ID:             77,
		ChatID:         5,
		RequesterID:    "user-a",
		OtherID:        "user-b",
		Status:         "active",
		State:          "choose_kind",
		TurnAskerID:    "user-a",
		TurnAnswererID: "user-b",
	}
	askerText, askerMarkup := renderTruthDareGame(game, "user-a", "آزاده", "رضا")
	waiterText, waiterMarkup := renderTruthDareGame(game, "user-b", "آزاده", "رضا")
	if !strings.Contains(askerText, "انتخابت کن") || !strings.Contains(askerText, "آزاده") || !strings.Contains(askerText, "رضا") {
		t.Fatalf("asker should see the choice prompt and both names: %q", askerText)
	}
	if !strings.Contains(askerMarkup.(string), "jh;77;kind;dare") || !strings.Contains(askerMarkup.(string), "jh;77;kind;truth") {
		t.Fatalf("asker markup is missing dare/truth actions: %v", askerMarkup)
	}
	if !strings.Contains(waiterText, "صبرکن مخاطب") {
		t.Fatalf("other participant should see the waiting message: %q", waiterText)
	}
	if strings.Contains(waiterMarkup.(string), "jh;77;kind;") {
		t.Fatalf("other participant must not be able to choose the question kind: %v", waiterMarkup)
	}
}

func TestTruthDareQuestionStateShowsConfirmationOnlyToAsker(t *testing.T) {
	game := truthDareGame{
		ID:             88,
		ChatID:         5,
		RequesterID:    "user-a",
		OtherID:        "user-b",
		Status:         "active",
		State:          "waiting_answer",
		TurnAskerID:    "user-a",
		TurnAnswererID: "user-b",
		Kind:           "truth",
		Question:       "نمونه سؤال؟",
	}
	askerText, askerMarkup := renderTruthDareGame(game, "user-a", "آزاده", "رضا")
	answererText, answererMarkup := renderTruthDareGame(game, "user-b", "آزاده", "رضا")
	if !strings.Contains(askerText, "نمونه سؤال؟") || !strings.Contains(askerMarkup.(string), "jh;88;confirm") {
		t.Fatalf("asker should see the question and confirmation button: %q / %v", askerText, askerMarkup)
	}
	if !strings.Contains(answererText, "نمونه سؤال؟") || strings.Contains(answererMarkup.(string), "jh;88;confirm") {
		t.Fatalf("answerer should see the question without confirmation action: %q / %v", answererText, answererMarkup)
	}
}
