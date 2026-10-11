package storage

import "testing"

func TestFakeChatRefundKeepsHalfFee(t *testing.T) {
	tests := []struct {
		fee        int
		wantRefund int
	}{
		{fee: 0, wantRefund: 0},
		{fee: 2, wantRefund: 1},
		{fee: 3, wantRefund: 2},
		{fee: 4, wantRefund: 2},
		{fee: 5, wantRefund: 3},
	}
	for _, tt := range tests {
		got := fakeChatRefund(tt.fee)
		if got != tt.wantRefund {
			t.Errorf("fee %d: got refund %d, want %d", tt.fee, got, tt.wantRefund)
		}
		if tt.fee-got != tt.fee/2 {
			t.Errorf("fee %d: retained amount %d, want %d", tt.fee, tt.fee-got, tt.fee/2)
		}
	}
}
