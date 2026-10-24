package ledger

import (
	"context"
	"errors"
)

type Posting struct {
	Source      string `json:"source"`
	Destination string `json:"destination"`
	Asset       string `json:"asset"`
	Amount      int64  `json:"amount"`
}

type Command struct {
	Reference string    `json:"reference"`
	Postings  []Posting `json:"postings"`
}

type Transaction struct {
	ID        string
	Reference string
	Postings  []Posting
}

type Ledger interface {
	Post(context.Context, Command) (Transaction, error)
	Find(context.Context, string) (*Transaction, error)
	Balance(context.Context, string, string) (int64, error)
}

var (
	ErrInsufficientFunds = errors.New("ledger: insufficient funds")
	ErrConflict          = errors.New("ledger: conflict")
	ErrUnknown           = errors.New("ledger: outcome unknown")
)
