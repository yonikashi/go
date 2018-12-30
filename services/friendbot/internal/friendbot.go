package internal

import (
	"fmt"
	"strconv"
	"sync"

	b "github.com/kinecosystem/go/build"
	"github.com/kinecosystem/go/clients/horizon"
	"github.com/kinecosystem/go/keypair"
	"github.com/kinecosystem/go/support/errors"
)

// Bot represents the friendbot subsystem.
type Bot struct {
	Horizon         *horizon.Client
	Secret          string
	Network         string
	StartingBalance string

	// uninitialized
	sequence             uint64
	forceRefreshSequence bool
	lock                 sync.Mutex
}

// Pay funds the account at `destAddress`
func (bot *Bot) Pay(destAddress string, amount string) (*horizon.TransactionSuccess, error) {
	channel := make(chan interface{})
	shouldReadChannel, result, err := bot.lockedPay(channel, destAddress, amount)
	if !shouldReadChannel {
		return result, err
	}

	v := <-channel
	switch tv := v.(type) {
	case horizon.TransactionSuccess:
		return &tv, nil
	case error:
		return nil, tv
	default:
		return nil, fmt.Errorf("failed to submit async txn")
	}
}

func (bot *Bot) lockedPay(channel chan interface{}, destAddress string, amount string) (bool, *horizon.TransactionSuccess, error) {
	bot.lock.Lock()
	defer bot.lock.Unlock()

	err := bot.checkSequenceRefresh()
	if err != nil {
		return false, nil, err
	}

	signed, err := bot.makeTx(destAddress, amount)
	if err != nil {
		return false, nil, err
	}

	go bot.asyncSubmitTransaction(channel, signed)
	return true, nil, nil
}

func (bot *Bot) asyncSubmitTransaction(channel chan interface{}, signed string) {
	result, err := bot.Horizon.SubmitTransaction(signed)
	if err != nil {
		switch e := err.(type) {
		case *horizon.Error:
			bot.checkHandleBadSequence(e)
		}

		channel <- err
	} else {
		channel <- result
	}
}

func (bot *Bot) checkHandleBadSequence(err *horizon.Error) {
	resCode, e := err.ResultCodes()
	isTxBadSeqCode := e == nil && resCode.TransactionCode == "tx_bad_seq"
	if !isTxBadSeqCode {
		return
	}
	bot.forceRefreshSequence = true
}

// establish initial sequence if needed
func (bot *Bot) checkSequenceRefresh() error {
	if bot.sequence != 0 && !bot.forceRefreshSequence {
		return nil
	}
	return bot.refreshSequence()
}

func (bot *Bot) makeTx(destAddress string, amount string) (string, error) {
	txn, err := b.Transaction(
		b.SourceAccount{AddressOrSeed: bot.Secret},
		b.Sequence{Sequence: bot.sequence + 1},
		b.Network{Passphrase: bot.Network},
		b.CreateAccount(
			b.Destination{AddressOrSeed: destAddress},
			b.NativeAmount{Amount: amount},
		),
	)

	if err != nil {
		return "", errors.Wrap(err, "Error building a transaction")
	}

	txs, err := txn.Sign(bot.Secret)
	if err != nil {
		return "", errors.Wrap(err, "Error signing a transaction")
	}

	base64, err := txs.Base64()

	// only increment the in-memory sequence number if we are going to submit the transaction, while we hold the lock
	if err == nil {
		bot.sequence++
	}
	return base64, err
}

// refreshes the sequence from the bot account
func (bot *Bot) refreshSequence() error {
	botAccount, err := bot.Horizon.LoadAccount(bot.address())
	if err != nil {
		bot.sequence = 0
		return err
	}

	seq, err := strconv.ParseInt(botAccount.Sequence, 10, 64)
	if err != nil {
		bot.sequence = 0
		return err
	}

	bot.sequence = uint64(seq)
	bot.forceRefreshSequence = false
	return nil
}

func (bot *Bot) address() string {
	kp := keypair.MustParse(bot.Secret)
	return kp.Address()
}
